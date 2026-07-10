package kube

import (
	"context"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
)

func req(name string) ctrl.Request {
	return ctrl.Request{NamespacedName: types.NamespacedName{Name: name, Namespace: "default"}}
}

// R12 step 2. These pin the finalizer with a fake client that honors finalizers
// exactly as the apiserver does — a Delete on a finalized object sets a
// DeletionTimestamp and keeps the object until the last finalizer is removed. That
// is the whole guarantee: a deleted Run's leases are CLOSED before the object can
// vanish, so no delete path — graceful or --force — leaves capacity funded.

func liveRun(name string, finalizers ...string) *v1.Run {
	return &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default", Finalizers: finalizers},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1}},
		Status:     v1.RunStatus{Phase: controllers.RunPhaseRunning},
	}
}

// A run the controller sees for the first time is given the finalizer before it is
// admitted, so a delete that races admission still finds a hold to run under.
func TestReconcileInstallsTheFundingClosureFinalizer(t *testing.T) {
	_ = captureReport(t) // the engine runs on a live run; do not let a stray violation panic this test

	run := liveRun("fresh") // no finalizer yet
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(healthyNode("node-a", 4), run).
		WithStatusSubresource(&v1.Run{}, &v1.Lease{}).
		Build()
	r := &RunReconciler{Bridge: &Bridge{Client: c, APIReader: c, Clock: controllers.RealClock{}}}

	if _, err := r.Reconcile(context.Background(), req("fresh")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got v1.Run
	if err := c.Get(context.Background(), types.NamespacedName{Name: "fresh", Namespace: "default"}, &got); err != nil {
		t.Fatalf("get run: %v", err)
	}
	if !controllerutil.ContainsFinalizer(&got, FundingClosureFinalizer) {
		t.Fatalf("a live run must carry the funding-closure finalizer, got %v", got.Finalizers)
	}
}

// The load-bearing guarantee: deleting a Run closes its open lease (RunDeleted) and
// only then lets the object go. Accounting cannot be escaped.
func TestDeletingARunClosesItsLeasesBeforeTheObjectIsGone(t *testing.T) {
	_ = captureReport(t)

	run := liveRun("dead", FundingClosureFinalizer) // already adopted
	lease := openLeaseOn("dead-0", "dead", "node-a")
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(healthyNode("node-a", 4), run, lease).
		WithStatusSubresource(&v1.Run{}, &v1.Lease{}).
		Build()
	r := &RunReconciler{Bridge: &Bridge{Client: c, APIReader: c, Clock: controllers.RealClock{}}}

	// Delete it. The finalizer holds the object: DeletionTimestamp is set, the Run
	// still exists, and List still returns it — so "an open lease whose Run is
	// absent" never arises.
	if err := c.Delete(context.Background(), run); err != nil {
		t.Fatalf("delete run: %v", err)
	}
	var held v1.Run
	if err := c.Get(context.Background(), types.NamespacedName{Name: "dead", Namespace: "default"}, &held); err != nil {
		t.Fatalf("the finalizer must hold the Run in the API, but it is already gone: %v", err)
	}
	if held.DeletionTimestamp == nil {
		t.Fatalf("setup: the Run should be marked for deletion")
	}

	if _, err := r.Reconcile(context.Background(), req("dead")); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	// The lease is closed on the accounting-safe reason, before the object goes.
	var gotLease v1.Lease
	if err := c.Get(context.Background(), types.NamespacedName{Name: "dead-0", Namespace: "default"}, &gotLease); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if !gotLease.Status.Closed || gotLease.Status.ClosureReason != "RunDeleted" {
		t.Fatalf("a deleted run's lease must close as RunDeleted before the run goes: closed=%v reason=%q",
			gotLease.Status.Closed, gotLease.Status.ClosureReason)
	}
	// And only now is the Run actually gone: the finalizer was removed, so the
	// apiserver completes the delete.
	var gone v1.Run
	err := c.Get(context.Background(), types.NamespacedName{Name: "dead", Namespace: "default"}, &gone)
	if err == nil {
		t.Fatalf("the finalizer must be removed and the Run deleted once its accounting is closed; it is still present with finalizers %v", gone.Finalizers)
	}
}
