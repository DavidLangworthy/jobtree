package plugin

import (
	"context"
	"reflect"
	"strings"
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

// TestPreBindCrossNodeRetryRefusesLeaseOnAnotherNode is the L19 regression
// specimen. PreBind's AlreadyExists branch authenticates the run and the open
// bit; until the placement guard it did not authenticate the NODE, so a retry on
// another scheduler node succeeded against the first attempt's immutable lease.
// The pod then bound on node-b while its lease still held GPUs on node-a, and
// losing node-a made AuditLedger return a repairable lease_dead_node against a
// perfectly healthy run — matured repair closes it, and the node-b work loses
// its charging and capacity record.
//
// A lease spec is immutable and this plugin is not the closer, so the guard
// refuses the mint instead of repairing it. The lease stays on node-a, alone and
// open, and the divergent state is unreachable rather than merely audited.
func TestPreBindCrossNodeRetryRefusesLeaseOnAnotherNode(t *testing.T) {
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

	// The load-bearing assertion: the retry is on a different node than the lease
	// this pod already minted, so PreBind must refuse rather than report a mint
	// that would place the pod away from the GPUs it charges for.
	status := j.PreBind(ctx, nil, pod, "node-b")
	if status == nil || status.IsSuccess() {
		t.Fatal("cross-node retry PreBind succeeded; the pod would bind on node-b holding a node-a lease")
	}
	if msg := status.Message(); !strings.Contains(msg, "node-a") || !strings.Contains(msg, "node-b") {
		t.Errorf("refusal message %q should name both the lease's node and the attempted node", msg)
	}

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
	if lease.Status.Closed {
		t.Fatal("refused cross-node retry closed the lease; PreBind is not a closer")
	}

	// The guard must not break the same-node retry it shares a branch with: the
	// pod converges on the node its lease already froze.
	if status := j.PreBind(ctx, nil, pod, "node-a"); status != nil && !status.IsSuccess() {
		t.Fatalf("same-node retry after a refused cross-node retry failed: %s", status.Message())
	}
	if err := c.List(ctx, &leases, client.InNamespace("default")); err != nil {
		t.Fatalf("re-list leases: %v", err)
	}
	if len(leases.Items) != 1 {
		t.Fatalf("same-node retry minted a second lease (%d total)", len(leases.Items))
	}

	// Bound where its lease charges, the run is healthy and the auditor is quiet.
	pod.Spec.NodeName = "node-a"
	pod.Status.Phase = corev1.PodRunning
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
		NodeNames: map[string]bool{"node-a": true, "node-b": true},
	})
	if len(violations) != 0 {
		t.Fatalf("auditor flagged a healthy run bound on its lease's own node: %+v", violations)
	}
}
