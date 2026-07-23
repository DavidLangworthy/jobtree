package kube

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// R9 9A-1: an emitted pod gets a stable rendezvous identity — hostname + the run's
// headless-Service subdomain — and the bridge creates that Service, owned by the Run
// so kube GC removes it with the Run.
func TestApplyGivesPodsRendezvousIdentityAndAHeadlessService(t *testing.T) {
	_ = captureReport(t)

	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: "train", Namespace: "default", UID: "train-uid-1"},
		Spec:       v1.RunSpec{Owner: "org:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 2}},
		Status:     v1.RunStatus{Phase: controllers.RunPhaseRunning},
	}
	c := fake.NewClientBuilder().WithScheme(testScheme()).
		WithObjects(healthyNode("node-a", 4), run).
		WithStatusSubresource(&v1.Run{}, &v1.GPULease{}).
		Build()
	bridge := &Bridge{Client: c, APIReader: c, Clock: controllers.RealClock{}}

	err := bridge.WithWorld(context.Background(), func(state *controllers.ClusterState, now time.Time) error {
		state.Pods = append(state.Pods, binder.PodManifest{
			Namespace: "default", Name: "train-active-0", NodeName: "node-a", GPUs: 1,
			Labels: map[string]string{
				binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive, binder.LabelGroupIndex: "0",
			},
		})
		return nil
	})
	if err != nil {
		t.Fatalf("WithWorld: %v", err)
	}

	// The pod carries its rendezvous identity: train-active-0.train.default.svc.
	var pod corev1.Pod
	if err := c.Get(context.Background(), types.NamespacedName{Name: "train-active-0", Namespace: "default"}, &pod); err != nil {
		t.Fatalf("get pod: %v", err)
	}
	if pod.Spec.Hostname != "train-active-0" {
		t.Errorf("pod hostname = %q, want train-active-0", pod.Spec.Hostname)
	}
	if pod.Spec.Subdomain != "train" {
		t.Errorf("pod subdomain = %q, want the run's headless service train", pod.Spec.Subdomain)
	}

	// The headless Service exists, publishes not-ready addresses (ranks resolve each
	// other DURING startup rendezvous), and is owned by the Run for GC.
	var svc corev1.Service
	if err := c.Get(context.Background(), types.NamespacedName{Name: "train", Namespace: "default"}, &svc); err != nil {
		t.Fatalf("the run's headless Service was not created: %v", err)
	}
	if svc.Spec.ClusterIP != corev1.ClusterIPNone {
		t.Errorf("service must be headless (ClusterIP=None), got %q", svc.Spec.ClusterIP)
	}
	if !svc.Spec.PublishNotReadyAddresses {
		t.Errorf("service must publish not-ready addresses so ranks rendezvous before Ready")
	}
	if svc.Spec.Selector[binder.LabelRunName] != "train" {
		t.Errorf("service selector must target the run's pods, got %v", svc.Spec.Selector)
	}
	if len(svc.OwnerReferences) != 1 || svc.OwnerReferences[0].UID != "train-uid-1" {
		t.Errorf("service must be owned by its Run for GC, got %+v", svc.OwnerReferences)
	}
}
