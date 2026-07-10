package kube

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/invariant"
)

// controllers.SettleLeases has its own unit tests. These pin the thing those
// cannot see: that it is actually WIRED, that its closures reach the API, and
// that a shirked duty is a test failure rather than a log line.
//
// A sweep nobody calls, or one whose writes the bridge drops, is dead code with a
// green suite — which is the exact shape of the defects this whole effort exists
// to end.

func runPod(name, runName, node string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			Labels: map[string]string{
				binder.LabelRunName:    runName,
				binder.LabelRunRole:    binder.RoleActive,
				binder.LabelGroupIndex: "0",
			},
		},
		Spec: corev1.PodSpec{NodeName: node, Containers: []corev1.Container{{Name: "main", Image: "x"}}},
	}
}

// captureReport swaps the oracle's reporter for the duration of a test. The sweep
// PANICS under `go test` by design, and a test that means to exercise the reporting
// path must therefore stand in for the reporter — not disable it.
func captureReport(t *testing.T) *[]invariant.Violation {
	t.Helper()
	prior := invariant.Report
	var seen []invariant.Violation
	invariant.Report = func(_ string, vs []invariant.Violation) { seen = append(seen, vs...) }
	t.Cleanup(func() { invariant.Report = prior })
	return &seen
}

// The immortal lease reaches the API, and the sweep closes it there. Without the
// bridge writing the closure back, SettleLeases would mutate a snapshot that is
// thrown away at the end of the pass.
func TestWithWorldSweepsATerminalRunsLeaseAllTheWayToTheAPI(t *testing.T) {
	seen := captureReport(t)

	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "dead", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1}},
		Status:     v1.RunStatus{Phase: controllers.RunPhaseFailed},
	}
	lease := openLeaseOn("dead-lease", "dead", "node-a")
	pod := runPod("dead-active-0", "dead", "node-a")

	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(healthyNode("node-a", 4), run, lease, pod).
		WithStatusSubresource(&v1.Run{}, &v1.Lease{}).
		Build()
	bridge := &Bridge{Client: c, APIReader: c, Clock: controllers.RealClock{}}

	// A reconcile that does nothing at all: the sweep is not something the engine
	// asks for, it is something the bridge guarantees.
	if err := bridge.WithWorld(context.Background(), func(*controllers.ClusterState, time.Time) error { return nil }); err != nil {
		t.Fatalf("WithWorld: %v", err)
	}

	var got v1.Lease
	if err := c.Get(context.Background(), types.NamespacedName{Name: "dead-lease", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if !got.Status.Closed {
		t.Fatalf("the lease of a Failed run is still open in the API: it charges its budget forever, " +
			"and nothing reconciles a corpse to come back for it")
	}
	if got.Status.ClosureReason != "SweptTerminalRun" {
		t.Errorf("closure reason %q; the ledger must record that a sweep did this", got.Status.ClosureReason)
	}
	if got.Status.Ended == nil || got.Status.Ended.IsZero() {
		t.Errorf("a closed lease with no Ended timestamp bills to its START instant and accrues nothing")
	}

	var pods corev1.PodList
	if err := c.List(context.Background(), &pods, client.InNamespace("default")); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 0 {
		t.Errorf("the container outlived the lease: %d pod(s) still hold GPUs the ledger just handed back", len(pods.Items))
	}

	// ...and it is reported, because a terminal-run sweep means a path shirked.
	if len(*seen) != 1 || (*seen)[0].ID != invariant.TerminalPresent {
		t.Fatalf("a shirked duty must be reported to the oracle, got %v", *seen)
	}
}

// The healthy case, which is every pass in a working cluster: the sweep must find
// nothing, write nothing, and accuse nobody.
func TestWithWorldSweepIsSilentOnAHealthyWorld(t *testing.T) {
	seen := captureReport(t)

	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1}},
		Status:     v1.RunStatus{Phase: controllers.RunPhaseRunning},
	}
	lease := openLeaseOn("train-lease", "train", "node-a")
	pod := runPod("train-active-0", "train", "node-a")

	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(healthyNode("node-a", 4), run, lease, pod).
		WithStatusSubresource(&v1.Run{}, &v1.Lease{}).
		Build()
	bridge := &Bridge{Client: c, APIReader: c, Clock: controllers.RealClock{}}

	if err := bridge.WithWorld(context.Background(), func(*controllers.ClusterState, time.Time) error { return nil }); err != nil {
		t.Fatalf("WithWorld: %v", err)
	}

	var got v1.Lease
	if err := c.Get(context.Background(), types.NamespacedName{Name: "train-lease", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if got.Status.Closed {
		t.Fatalf("the sweep closed a running job's lease")
	}
	var pods corev1.PodList
	if err := c.List(context.Background(), &pods, client.InNamespace("default")); err != nil {
		t.Fatalf("list pods: %v", err)
	}
	if len(pods.Items) != 1 {
		t.Fatalf("the sweep deleted a running job's container")
	}
	if len(*seen) != 0 {
		t.Fatalf("a healthy world must accuse nobody, got %v", *seen)
	}
}

// A Run deleted out from under its lease. The sweep repairs it — and does NOT
// report it, because a Run delete and a reconcile of one of its pods race by
// construction. Reporting it would make cleanupDeletedRun's ordinary operation
// fail every envtest that deletes a Run.
func TestWithWorldSweepsAnOrphanedLeaseWithoutAccusingAnyone(t *testing.T) {
	seen := captureReport(t)

	lease := openLeaseOn("ghost-lease", "ghost", "node-a")
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(healthyNode("node-a", 4), lease). // no Run: it is really gone
		WithStatusSubresource(&v1.Run{}, &v1.Lease{}).
		Build()
	bridge := &Bridge{Client: c, APIReader: c, Clock: controllers.RealClock{}}

	if err := bridge.WithWorld(context.Background(), func(*controllers.ClusterState, time.Time) error { return nil }); err != nil {
		t.Fatalf("WithWorld: %v", err)
	}

	var got v1.Lease
	if err := c.Get(context.Background(), types.NamespacedName{Name: "ghost-lease", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if !got.Status.Closed || got.Status.ClosureReason != "SweptOrphanRun" {
		t.Fatalf("an orphaned lease must be closed as one: closed=%v reason=%q", got.Status.Closed, got.Status.ClosureReason)
	}
	if len(*seen) != 0 {
		t.Fatalf("an orphan closure races a Run deletion; it accuses nobody, got %v", *seen)
	}
}
