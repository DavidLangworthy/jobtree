package plugin

import (
	"context"
	"reflect"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// TestPreBindFailureUnreserveRetryKeepsOneLease calibrates the
// PhysicalCapacity PreBind -> BindFailure -> Unreserve/retry seam against the
// compiled plugin and auditor cores.
//
// Kubernetes invokes Unreserve after a binding-cycle error. jobtree must retain
// the already-minted lease and payer claim so a retry converges on that same
// open lease. The Pending pod is also positive evidence of an in-flight retry,
// so the ledger auditor must not close the lease as an orphan. This is retry
// safety, not a bounded-recovery claim: neither component establishes a
// deadline by which a permanently unbindable pod's charging lease is closed.
func TestPreBindFailureUnreserveRetryKeepsOneLease(t *testing.T) {
	ctx := context.Background()
	run := trainRun()
	pod := gangPod()
	pod.Labels[binder.LabelGroupIndex] = "0"
	pod.Annotations[binder.AnnotationRunNonce] = "incarnation-a"

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(run, teamBudget(8), gpuNode("node-a", 4)).Build()
	m := newGangManager(c, func() time.Time {
		return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	})
	if fundable, reason := m.decide(ctx, pod); !fundable {
		t.Fatalf("funding decision failed: %s", reason)
	}
	j := &JobTree{client: c, gm: m}

	if status := j.PreBind(ctx, nil, pod, "node-a"); status != nil && !status.IsSuccess() {
		t.Fatalf("first PreBind failed: %s", status.Message())
	}
	// Stand in for the framework's Bind error callback. Unreserve releases the
	// scheduler-cache assumption outside this plugin; here it preserves the
	// claimed payer because PreBind already minted.
	j.Unreserve(ctx, nil, pod, "node-a")

	if status := j.PreBind(ctx, nil, pod, "node-a"); status != nil && !status.IsSuccess() {
		t.Fatalf("retry PreBind failed: %s", status.Message())
	}

	var leases v1.GPULeaseList
	if err := c.List(ctx, &leases, client.InNamespace("default")); err != nil {
		t.Fatalf("list leases: %v", err)
	}
	if len(leases.Items) != 1 {
		t.Fatalf("PreBind retry minted %d leases, want exactly one", len(leases.Items))
	}
	lease := leases.Items[0]
	if lease.Status.Closed {
		t.Fatal("PreBind/Unreserve retry closed the lease")
	}
	if got, want := lease.Name, pod.Name+"-incarnation-a-lease"; got != want {
		t.Fatalf("lease name = %q, want nonce-stable %q", got, want)
	}
	if got := binder.LeasePodName(&lease); got != pod.Name {
		t.Fatalf("lease pod identity = %q, want %q", got, pod.Name)
	}

	run.Status.Phase = v1.RunPhaseRunning
	violations := controllers.AuditLedger(controllers.LedgerWorld{
		Runs:   map[string]*v1.Run{"default/train": run},
		Leases: leases.Items,
		Pods: []controllers.LedgerPod{{
			Namespace: "default",
			Name:      pod.Name,
			RunName:   run.Name,
			Phase:     string(corev1.PodPending),
		}},
		NodeNames: map[string]bool{"node-a": true},
	})
	if len(violations) != 0 {
		t.Fatalf("auditor would reap a retrying Pending pod's lease: %+v", violations)
	}
}

// TestPreBindCrossNodeRetryLeavesStaleLeaseAndAuditorWouldCloseIt is a
// known-bad production specimen. PreBind's AlreadyExists branch authenticates
// the run and open bit but not the placement. A retry on another scheduler
// node therefore succeeds against the first attempt's immutable lease.
//
// Once the retry binds on node-b, the pod is healthy there while its lease
// still names node-a. If node-a disappears, AuditLedger sees positive
// dead-node evidence and returns a repairable violation for that stale lease.
// The real auditor would close it after grace, leaving the healthy node-b work
// without its charging/capacity record.
func TestPreBindCrossNodeRetryLeavesStaleLeaseAndAuditorWouldCloseIt(t *testing.T) {
	ctx := context.Background()
	run := trainRun()
	pod := gangPod()
	pod.Labels[binder.LabelGroupIndex] = "0"
	pod.Annotations[binder.AnnotationRunNonce] = "incarnation-a"

	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(run, teamBudget(8), gpuNode("node-a", 4), gpuNode("node-b", 4)).Build()
	m := newGangManager(c, func() time.Time {
		return time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	})
	if fundable, reason := m.decide(ctx, pod); !fundable {
		t.Fatalf("funding decision failed: %s", reason)
	}
	j := &JobTree{client: c, gm: m}

	if status := j.PreBind(ctx, nil, pod, "node-a"); status != nil && !status.IsSuccess() {
		t.Fatalf("node-a PreBind failed: %s", status.Message())
	}
	j.Unreserve(ctx, nil, pod, "node-a")
	if status := j.PreBind(ctx, nil, pod, "node-b"); status != nil && !status.IsSuccess() {
		t.Fatalf("cross-node retry PreBind failed: %s", status.Message())
	}
	// PreBind succeeded for node-b, so model the framework's subsequent bind.
	pod.Spec.NodeName = "node-b"
	pod.Status.Phase = corev1.PodRunning

	var leases v1.GPULeaseList
	if err := c.List(ctx, &leases, client.InNamespace("default")); err != nil {
		t.Fatalf("list leases: %v", err)
	}
	if len(leases.Items) != 1 {
		t.Fatalf("cross-node retry left %d leases, want exactly one", len(leases.Items))
	}
	lease := leases.Items[0]
	wantSlots := []string{"node-a#0", "node-a#1", "node-a#2", "node-a#3"}
	if !reflect.DeepEqual(lease.Spec.Slice.Nodes, wantSlots) {
		t.Fatalf("lease slots = %v, want immutable first-PreBind slots %v", lease.Spec.Slice.Nodes, wantSlots)
	}
	if pod.Spec.NodeName != "node-b" {
		t.Fatalf("bound pod node = %q, want node-b", pod.Spec.NodeName)
	}

	run.Status.Phase = v1.RunPhaseRunning
	violations := controllers.AuditLedger(controllers.LedgerWorld{
		Runs:   map[string]*v1.Run{"default/train": run},
		Leases: leases.Items,
		Pods: []controllers.LedgerPod{{
			Namespace: pod.Namespace,
			Name:      pod.Name,
			RunName:   run.Name,
			Phase:     string(pod.Status.Phase),
		}},
		// node-b and the healthy pod still exist; only stale lease node-a is gone.
		NodeNames: map[string]bool{"node-b": true},
	})
	if len(violations) != 1 {
		t.Fatalf("AuditLedger violations = %+v, want one stale-node violation", violations)
	}
	if got := violations[0]; got.Kind != controllers.ViolationLeaseDeadNode ||
		!got.Repairable || got.LeaseName != lease.Name {
		t.Fatalf("AuditLedger violation = %+v, want repairable lease_dead_node for %s", got, lease.Name)
	}
}
