package kube

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// h100Envelope is a Budget whose one envelope covers the labelled node below.
func h100Envelope(name, owner string) *v1.Budget {
	return &v1.Budget{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.BudgetSpec{Owner: owner, Envelopes: []v1.BudgetEnvelope{{
			Name: "west", Flavor: "H100-80GB", Concurrency: 16,
			Selector: map[string]string{
				topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a",
				topology.LabelFabricDomain: "island-a",
			}, Start: &testWindowStart, End: &testWindowEnd,
		}}},
	}
}

func labelledH100Node(name string, gpus int64) *corev1.Node {
	n := healthyNode(name, gpus)
	n.Labels = map[string]string{
		topology.LabelRegion:       "us-west",
		topology.LabelCluster:      "cluster-a",
		topology.LabelFabricDomain: "island-a",
		topology.LabelGPUFlavor:    "H100-80GB",
	}
	return n
}

// ReservationReconciler is the SOLE caller of RunController.ActivateReservations
// on a real cluster, and its state gate mirrors the engine's. So a state the
// engine re-considers and this gate drops is a state the engine never sees
// again — the change is correct under `go test` and inert in production, which is
// a finding in its own right (AGENTS.md:148-150).
//
// BlockedFunding is exactly that state, and nothing else would wake it. This
// reconciler watches Reservations only, so repairing the namespace→owner binding
// — a write to a *Budget* — produces no event that reaches here; and a reconciler
// that returns without a RequeueAfter schedules no poll either. Both halves are
// pinned below, because either one alone leaves the reservation asleep forever:
// "Recovery is automatic ... Nothing to resubmit" (quota-semantics.md:38-39)
// would have been true in the engine and false on a cluster.
func TestBlockedReservationIsStillDrivenByItsReconciler(t *testing.T) {
	_ = captureReport(t)
	ctx := context.Background()

	newWorld := func(budgets ...*v1.Budget) (*ReservationReconciler, func() *v1.Reservation) {
		run := &v1.Run{
			ObjectMeta: metav1.ObjectMeta{Name: "train", Namespace: "default", UID: "run-uid"},
			Spec:       v1.RunSpec{Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4}},
		}
		res := &v1.Reservation{
			ObjectMeta: metav1.ObjectMeta{Name: "res", Namespace: "default"},
			Spec: v1.ReservationSpec{
				RunRef:        v1.RunReference{Name: "train", Namespace: "default"},
				EarliestStart: metav1.NewTime(time.Now().Add(-time.Hour)),
			},
			// The state the previous pass left behind.
			Status: v1.ReservationStatus{
				State:        "BlockedFunding",
				Reason:       `namespace "default" has no funding principal`,
				BlockedSince: &metav1.Time{Time: time.Now().Add(-time.Hour)},
			},
		}
		builder := fake.NewClientBuilder().WithScheme(testScheme()).
			WithObjects(labelledH100Node("node-a", 8), run, res).
			WithStatusSubresource(&v1.Run{}, &v1.GPULease{}, &v1.Reservation{}, &v1.Budget{})
		for _, b := range budgets {
			builder = builder.WithObjects(b)
		}
		c := builder.Build()
		r := &ReservationReconciler{Bridge: &Bridge{Client: c, APIReader: c, Clock: controllers.RealClock{}}}
		read := func() *v1.Reservation {
			var got v1.Reservation
			if err := c.Get(ctx, types.NamespacedName{Name: "res", Namespace: "default"}, &got); err != nil {
				t.Fatalf("read back the reservation: %v", err)
			}
			return &got
		}
		return r, read
	}

	request := ctrl.Request{NamespacedName: types.NamespacedName{Name: "res", Namespace: "default"}}

	t.Run("still blocked: it must keep polling", func(t *testing.T) {
		// Two owners in one namespace: OwnerOf derives the empty fail-safe.
		r, read := newWorld(h100Envelope("team", "org:team"), h100Envelope("rival", "org:rival"))

		out, err := r.Reconcile(ctx, request)
		if err != nil {
			t.Fatalf("reconciling a blocked reservation must not error: %v", err)
		}
		if out.RequeueAfter <= 0 {
			t.Fatalf("a blocked reservation was dropped with RequeueAfter=%v: the repair is a write to a "+
				"BUDGET, which this reconciler does not watch, so the poll is the only way it ever "+
				"learns the binding was fixed — without it the reservation sleeps forever",
				out.RequeueAfter)
		}
		if got := read().Status.State; got != "BlockedFunding" {
			t.Errorf("the binding is still broken, so the reservation must stay blocked, got %q", got)
		}
	})

	t.Run("binding repaired: it must activate", func(t *testing.T) {
		// One owner: the namespace binds. This is the admin having removed the
		// rival Budget between passes.
		r, read := newWorld(h100Envelope("team", "org:team"))

		if _, err := r.Reconcile(ctx, request); err != nil {
			t.Fatalf("reconciling after the repair must not error: %v", err)
		}
		if got := read().Status.State; got == "BlockedFunding" {
			t.Fatalf("the reservation is still BlockedFunding after the binding was repaired: the "+
				"reconciler's state gate dropped it before ActivateReservations ever ran, so the "+
				"engine's widened gate is unreachable in production (reason %q)", read().Status.Reason)
		}
	})
}

// The generated CRD must actually carry `status.blockedSince`, and only a real
// apiserver can say so. `9afbe19` added the Go field without running
// `make generate`, and a structural schema PRUNES what it does not declare — so
// with the manifests stale the field would round-trip fine in every in-process
// test, compile everywhere, and silently vanish the moment it was written to a
// cluster. The reservation would then read BlockedFunding with no onset at all,
// which is the durable-onset half of MERGE-127.md item 1 quietly not shipping.
//
// This is the same class as TestRunSpecOwnerIsPrunedByTheAPIServer, pointed the
// other way: there, pruning is the security property being asserted; here, it is
// the failure mode being ruled out.
func TestBlockedSinceSurvivesTheAPIServer(t *testing.T) {
	requireEnv(t)

	c := directClient(t)
	onset := metav1.NewTime(time.Now().Truncate(time.Second).UTC())
	res := &v1.Reservation{
		ObjectMeta: metav1.ObjectMeta{Name: "blocked-since-roundtrip", Namespace: "default"},
		Spec: v1.ReservationSpec{
			RunRef:         v1.RunReference{Name: "train", Namespace: "default"},
			EarliestStart:  metav1.NewTime(time.Now()),
			PayingEnvelope: "west",
			IntendedSlice:  v1.IntendedSlice{Domain: map[string]string{topology.LabelFabricDomain: "island-a"}},
		},
	}
	if err := c.Create(suiteCtx, res); err != nil {
		t.Fatalf("create reservation: %v", err)
	}
	t.Cleanup(func() { _ = c.Delete(suiteCtx, res) })

	res.Status.State = "BlockedFunding"
	res.Status.Reason = `namespace "default" has no funding principal`
	res.Status.BlockedSince = &onset
	if err := c.Status().Update(suiteCtx, res); err != nil {
		t.Fatalf("write the blocked status: %v", err)
	}

	var stored v1.Reservation
	if err := c.Get(suiteCtx, client.ObjectKeyFromObject(res), &stored); err != nil {
		t.Fatalf("read back the reservation: %v", err)
	}
	if stored.Status.State != "BlockedFunding" {
		t.Errorf("state did not persist: %q", stored.Status.State)
	}
	if stored.Status.BlockedSince == nil {
		t.Fatalf("status.blockedSince was PRUNED by the apiserver: the CRD manifests do not declare "+
			"it, so a blocked reservation would carry no onset on a real cluster (state=%q reason=%q)",
			stored.Status.State, stored.Status.Reason)
	}
	if !stored.Status.BlockedSince.Time.Equal(onset.Time) {
		t.Errorf("blockedSince = %v, want %v", stored.Status.BlockedSince.Time, onset.Time)
	}
}
