package kube

import (
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// R12 verification items 1-3 and 5, against a REAL apiserver.
//
// What envtest can and cannot prove here, stated up front because the difference
// is the whole reason these tests are shaped the way they are:
//
//   - envtest runs an apiserver and etcd, and NO kube-controller-manager. There is
//     therefore **no garbage collector**, and no test in this package can observe a
//     cascade delete. Asserting "delete the Run, the pods disappear" would pass or
//     fail for reasons unrelated to jobtree.
//   - What it CAN prove, and the fake client cannot, is that each ownerReference is
//     one the real GC would act on: the apiserver accepts it, and its
//     apiVersion/kind/name/UID actually resolve to the live Run. A reference with a
//     plausible-looking but wrong apiVersion is silently inert in production and
//     completely invisible to a fake client, which validates nothing. That is the
//     failure mode worth a test.
//   - Finalizer behaviour, by contrast, is REAL here: the apiserver honours
//     finalizers itself, including under `--force --grace-period=0`. So the
//     accounting-cannot-be-escaped guarantee is genuinely exercised, not simulated.
//
// The cascade itself is proven where a garbage collector exists — a live cluster.

// assertOwnedByRun checks that obj carries exactly one ownerReference and that it
// is a controller reference resolving to the named live Run. "Resolving" is the
// operative word: we re-read the Run through the API and compare the UID, and we
// compare the group/version/kind against the scheme's own answer for a Run rather
// than a string literal, because a literal in the test would just repeat whatever
// typo the production code made.
func assertOwnedByRun(t *testing.T, obj client.Object, runName string) {
	t.Helper()

	var run v1.Run
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: runName}, &run); err != nil {
		t.Fatalf("get owning run %s: %v", runName, err)
	}

	gvks, _, err := kubeClient.Scheme().ObjectKinds(&v1.Run{})
	if err != nil || len(gvks) == 0 {
		t.Fatalf("scheme does not know the Run kind: %v", err)
	}
	wantAPIVersion, wantKind := gvks[0].GroupVersion().String(), gvks[0].Kind

	refs := obj.GetOwnerReferences()
	if len(refs) != 1 {
		t.Fatalf("%T %s: want exactly 1 ownerReference (the Run), got %d: %+v",
			obj, obj.GetName(), len(refs), refs)
	}
	ref := refs[0]
	switch {
	case ref.APIVersion != wantAPIVersion:
		t.Errorf("%T %s: ownerReference apiVersion = %q, want %q — the real GC resolves the owner by this string, so a wrong one is a silently dead edge",
			obj, obj.GetName(), ref.APIVersion, wantAPIVersion)
	case ref.Kind != wantKind:
		t.Errorf("%T %s: ownerReference kind = %q, want %q", obj, obj.GetName(), ref.Kind, wantKind)
	case ref.Name != runName:
		t.Errorf("%T %s: ownerReference name = %q, want %q", obj, obj.GetName(), ref.Name, runName)
	case ref.UID != run.UID:
		t.Errorf("%T %s: ownerReference UID = %q, but the live Run's UID is %q — GC treats a UID mismatch as an orphan and would delete the dependent immediately",
			obj, obj.GetName(), ref.UID, run.UID)
	}
	if ref.Controller == nil || !*ref.Controller {
		t.Errorf("%T %s: ownerReference must be a CONTROLLER reference, got controller=%v", obj, obj.GetName(), ref.Controller)
	}
	if ref.BlockOwnerDeletion == nil || !*ref.BlockOwnerDeletion {
		t.Errorf("%T %s: ownerReference must set blockOwnerDeletion so foreground deletion waits for the dependent, got %v",
			obj, obj.GetName(), ref.BlockOwnerDeletion)
	}
}

// R12 verification item 1. Every object the bridge creates on a Run's behalf and
// intends the apiserver to garbage-collect — the workload pods and the rendezvous
// Service — is anchored to the Run by a reference the apiserver accepted and that
// resolves back to the live object.
//
// Deliberately NOT asserted: that the Lease is owned. A Lease is a funding fact
// that must be CLOSED and audited, never cascade-deleted; owning it would let a
// force-deleted Run erase its own accounting. The finalizer tests below are that
// half of R12, and this test pins the negative so a future "make GC uniform"
// cleanup has something to trip over.
func TestRunOwnedObjectsCarryAnOwnerReferenceTheRealGCWouldResolve(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "owned", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	pods := waitForRunPods(t, "owned", 4)
	for i := range pods {
		assertOwnedByRun(t, &pods[i], "owned")
	}

	// The headless rendezvous Service (R9 9A-1) is created on the same edge.
	eventually(t, 15*time.Second, func() error {
		var svc corev1.Service
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "owned"}, &svc); err != nil {
			return err
		}
		return nil
	})
	var svc corev1.Service
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "owned"}, &svc); err != nil {
		t.Fatalf("get run service: %v", err)
	}
	assertOwnedByRun(t, &svc, "owned")

	// A Lease must NOT be owned: it is closed, not collected.
	seedPluginLeases(t, "owned")
	waitForRunPhase(t, "owned", "Running")
	for _, lease := range waitForRunLeases(t, "owned", 1) {
		if refs := lease.GetOwnerReferences(); len(refs) != 0 {
			t.Errorf("lease %s must have NO ownerReference: cascade-deleting a funding fact erases accounting history and lets a force-deleted Run escape its charge (R12 design decision 1c). Got %+v",
				lease.Name, refs)
		}
	}
}

// R12 verification item 1, the Reservation edge. A Reservation is a planning
// artifact with no accounting to audit, so unlike a Lease it SHOULD cascade.
func TestAReservationIsOwnedByItsRun(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	// Too small a cluster to place the run, so planning parks it behind a
	// Reservation instead of admitting it.
	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 16)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "reserved", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}

	var resName string
	eventually(t, 30*time.Second, func() error {
		var parked v1.Run
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "reserved"}, &parked); err != nil {
			return err
		}
		if parked.Status.PendingReservation == nil {
			return fmt.Errorf("run status = %+v, want a pending reservation", parked.Status)
		}
		resName = *parked.Status.PendingReservation
		return nil
	})

	var res v1.Reservation
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: resName}, &res); err != nil {
		t.Fatalf("get reservation %s: %v", resName, err)
	}
	assertOwnedByRun(t, &res, "reserved")
}

// R12 verification item 2, against a real apiserver: deleting a Running Run closes
// its open leases with reason RunDeleted, and the Run object does not go away until
// they are closed.
//
// The fake-client twin of this (finalizer_test.go) drives one Reconcile by hand.
// This one deletes through a real apiserver and lets the real watch-driven manager
// notice — which is what exercises the widened For() predicate that has to fire on
// "a Run entered deletion", without which the run strands Terminating forever.
func TestDeletingARunClosesItsLeasesOnARealAPIServer(t *testing.T) {
	requireEnv(t)
	resetWorld(t)
	assertDeleteClosesLeases(t, "graceful", func(run *v1.Run) error {
		return kubeClient.Delete(suiteCtx, run)
	})
}

// R12 verification item 3: `kubectl delete --force --grace-period=0`. A grace period
// of zero bypasses graceful termination; it does NOT bypass finalizers. Accounting
// therefore cannot be escaped by the impatient operator, which is the entire point
// of closing leases from a finalizer rather than from a best-effort cleanup path.
func TestForceDeletingARunStillClosesItsLeases(t *testing.T) {
	requireEnv(t)
	resetWorld(t)
	assertDeleteClosesLeases(t, "forced", func(run *v1.Run) error {
		return kubeClient.Delete(suiteCtx, run, client.GracePeriodSeconds(0))
	})
}

func assertDeleteClosesLeases(t *testing.T, name string, del func(*v1.Run) error) {
	t.Helper()

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitForRunPods(t, name, 4)
	seedPluginLeases(t, name)
	waitForRunPhase(t, name, "Running")
	leases := waitForRunLeases(t, name, 1)

	// Precondition: the reconciler installed the finalizer on the live Run. Without
	// it the deletion is unheld and everything below would pass vacuously.
	var live v1.Run
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: name}, &live); err != nil {
		t.Fatalf("get run: %v", err)
	}
	found := false
	for _, f := range live.Finalizers {
		if f == FundingClosureFinalizer {
			found = true
		}
	}
	if !found {
		t.Fatalf("a live Run must carry %s before deletion is meaningful; finalizers = %v", FundingClosureFinalizer, live.Finalizers)
	}

	if err := del(&live); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	// The Run goes only after the accounting is closed.
	eventually(t, 30*time.Second, func() error {
		var gone v1.Run
		err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: name}, &gone)
		if err == nil {
			return fmt.Errorf("run still present (finalizers %v, deletionTimestamp %v)", gone.Finalizers, gone.DeletionTimestamp)
		}
		if !apierrors.IsNotFound(err) {
			return err
		}
		return nil
	})

	// And every lease it held is CLOSED — present in the API, auditable, not
	// cascade-deleted with the Run.
	for _, seeded := range leases {
		var got v1.GPULease
		if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: seeded.Name}, &got); err != nil {
			t.Fatalf("lease %s must survive its Run's deletion as a closed funding record: %v", seeded.Name, err)
		}
		if !got.Status.Closed || got.Status.ClosureReason != "RunDeleted" {
			t.Errorf("lease %s: closed=%v reason=%q, want closed with RunDeleted", got.Name, got.Status.Closed, got.Status.ClosureReason)
		}
	}
}

// R12 verification item 5 — the licence to have deleted R27c's `orphan-run` sweep
// rule. That rule inferred a Run's deletion from the SILENCE of a List and then
// destroyed work. The finalizer makes its premise unreachable, and this is that
// claim stated as an executable property against a real apiserver:
//
//	at no world load does an OPEN lease exist whose Run is absent.
//
// It is a sampling argument, and deliberately so — the property is about every
// load, and the only honest way to check "every" from outside is to look often,
// through the whole deletion window, on the real object graph. It is sound because
// the finalizer is removed only AFTER WithWorld has applied the closures: once the
// Run can vanish, its leases are already closed, so the sampled invariant has no
// window to miss.
func TestNoOpenLeaseIsEverLoadedWithoutItsRun(t *testing.T) {
	requireEnv(t)
	resetWorld(t)

	createH100Node(t, "node-a", 4)
	createBudget(t, "team", "org:team", 8)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "vanishing", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if err := kubeClient.Create(suiteCtx, run); err != nil {
		t.Fatalf("create run: %v", err)
	}
	waitForRunPods(t, "vanishing", 4)
	seedPluginLeases(t, "vanishing")
	waitForRunPhase(t, "vanishing", "Running")
	waitForRunLeases(t, "vanishing", 1)

	var live v1.Run
	if err := kubeClient.Get(suiteCtx, types.NamespacedName{Namespace: "default", Name: "vanishing"}, &live); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if err := kubeClient.Delete(suiteCtx, &live); err != nil {
		t.Fatalf("delete run: %v", err)
	}

	key := keys.NamespacedKey("default", "vanishing")
	deadline := time.Now().Add(30 * time.Second)
	loads, sawOpenLease, runIsGone := 0, false, false
	for time.Now().Before(deadline) && !runIsGone {
		err := suiteBridge.WithWorld(suiteCtx, func(state *controllers.ClusterState, now time.Time) error {
			loads++
			open := 0
			for _, lease := range state.Leases {
				if lease.Spec.RunRef.Name == "vanishing" && lease.Spec.RunRef.Namespace == "default" && !lease.Status.Closed {
					open++
				}
			}
			_, present := state.Runs[key]
			if open > 0 {
				sawOpenLease = true
				if !present {
					return fmt.Errorf("load observed %d OPEN lease(s) for a Run that is absent from the world — the exact orphan state the deleted sweep rule used to act on", open)
				}
			}
			if !present {
				runIsGone = true
			}
			// SettleLeases must also find nothing to do: the Run is present and
			// not terminal, so its open lease is not an orphan by any rule.
			if sweep := controllers.SettleLeases(state, now); !sweep.Empty() {
				return fmt.Errorf("the sweep acted during a deletion window: %+v", sweep)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WithWorld during the deletion window: %v", err)
		}
	}

	if !sawOpenLease {
		t.Fatalf("the window closed before any load saw an open lease (%d loads); this test would pass vacuously", loads)
	}
	if !runIsGone {
		t.Fatalf("the Run was still present after 30s (%d loads); the finalizer never drained", loads)
	}
}
