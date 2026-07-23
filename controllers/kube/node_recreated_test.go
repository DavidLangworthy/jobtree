package kube

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// A node reconcile decides "this node is fenced" and then waits for the bridge
// mutex, which serializes every engine decision in the process. It can wait for
// seconds behind a slow admission pass. In that window the world moves: a Node
// deleted a moment ago is recreated under the same name — a kubelet
// re-registering, an operator swapping a machine, a test rebuilding its fixture —
// and the plugin mints fresh leases on it.
//
// If the verdict is not re-taken under the lock, the stale verdict closes the
// leases of a live node carrying work. The gang loses a rank it never lost, and
// if its spare has not been minted yet the run is "without spare coverage".
//
// That is task #36. It was declared closed by moving the re-read ABOVE the
// WithWorld call, with a comment saying so. The re-read was outside the critical
// section, so it narrowed the window rather than closing it — and the window is
// exactly wide enough to reach in CI.
//
// racyReader models that window with no timing at all: the first Get (the cheap
// filter, outside the lock) reports the node gone; every Get after it (the verdict,
// under the lock) reports it alive. A test that needs a sleep to see a race is a
// test that will one day not see it.
type racyReader struct {
	client.Reader
	node  *corev1.Node
	gets  int
	watch string
}

func (r *racyReader) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	if key.Name != r.watch {
		return r.Reader.Get(ctx, key, obj, opts...)
	}
	r.gets++
	if r.gets == 1 {
		return apierrors.NewNotFound(schema.GroupResource{Resource: "nodes"}, key.Name)
	}
	node, ok := obj.(*corev1.Node)
	if !ok {
		return r.Reader.Get(ctx, key, obj, opts...)
	}
	r.node.DeepCopyInto(node)
	return nil
}

// testScheme is the suite's scheme, built standalone: these tests need no
// envtest, no API server, and no KUBEBUILDER_ASSETS. They run in `go test ./...`.
func testScheme() *runtime.Scheme {
	s := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		panic(err)
	}
	if err := v1.AddToScheme(s); err != nil {
		panic(err)
	}
	return s
}

func healthyNode(name string, gpus int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name},
		Status: corev1.NodeStatus{
			Capacity:    corev1.ResourceList{GPUCapacityResource: *resource.NewQuantity(gpus, resource.DecimalSI)},
			Allocatable: corev1.ResourceList{GPUCapacityResource: *resource.NewQuantity(gpus, resource.DecimalSI)},
			Conditions:  []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func openLeaseOn(name, runName, node string) *v1.GPULease {
	return &v1.GPULease{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: "default",
			Labels: map[string]string{binder.LabelRunName: runName, binder.LabelRunRole: binder.RoleActive},
		},
		Spec: v1.GPULeaseSpec{
			Owner:          "org:team",
			RunRef:         v1.RunReference{Name: runName, Namespace: "default"},
			Slice:          v1.GPULeaseSlice{Nodes: []string{node + "#0"}, Role: binder.RoleActive},
			Interval:       v1.GPULeaseInterval{Start: metav1.NewTime(time.Now().Add(-time.Minute))},
			PaidByBudget:   "team",
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}
}

func TestStaleFencingVerdictDoesNotCloseARecreatedNodesLeases(t *testing.T) {
	node := healthyNode("node-a", 4)
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1}},
		Status:     v1.RunStatus{Phase: controllers.RunPhaseRunning},
	}
	lease := openLeaseOn("train-lease", "train", "node-a")

	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(node, run, lease).
		WithStatusSubresource(&v1.Run{}, &v1.GPULease{}).
		Build()

	reader := &racyReader{Reader: c, node: node, watch: "node-a"}
	bridge := &Bridge{Client: c, APIReader: reader, Clock: controllers.RealClock{}}
	r := &NodeReconciler{Bridge: bridge}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "node-a"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	if reader.gets < 2 {
		t.Fatalf("the fencing verdict was taken %d time(s); it must be re-taken under the bridge lock, "+
			"or a node recreated while the reconcile waited for the mutex has its live leases closed", reader.gets)
	}

	var got v1.GPULease
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "train-lease"}, &got); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if got.Status.Closed {
		t.Fatalf("a stale fencing verdict closed the lease of a node that is alive and carrying work "+
			"(closureReason=%q). The rank was never lost; the reconcile merely waited on the bridge mutex "+
			"while the node was recreated.", got.Status.ClosureReason)
	}
}

// ...and a node that is genuinely still gone when the verdict is re-taken must
// still have its leases closed. A guard that never lets anything through is not a
// guard either.
func TestAGenuinelyDeletedNodeStillClosesItsLeases(t *testing.T) {
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1}},
		Status:     v1.RunStatus{Phase: controllers.RunPhaseRunning},
	}
	lease := openLeaseOn("train-lease", "train", "node-a")

	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(run, lease). // no node: it is really gone
		WithStatusSubresource(&v1.Run{}, &v1.GPULease{}).
		Build()

	bridge := &Bridge{Client: c, APIReader: c, Clock: controllers.RealClock{}}
	r := &NodeReconciler{Bridge: bridge}

	if _, err := r.Reconcile(context.Background(), ctrl.Request{
		NamespacedName: types.NamespacedName{Name: "node-a"},
	}); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	var got v1.GPULease
	if err := c.Get(context.Background(), client.ObjectKey{Namespace: "default", Name: "train-lease"}, &got); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if !got.Status.Closed {
		t.Fatalf("a deleted node is a fenced node: its leases must close, or the ledger charges a budget " +
			"for a machine that no longer exists")
	}
	if got.Status.ClosureReason != "NodeFailure" {
		t.Errorf("closureReason = %q, want NodeFailure", got.Status.ClosureReason)
	}
}
