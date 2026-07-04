package plugin

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apiruntime "k8s.io/apimachinery/pkg/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func testScheme(t *testing.T) *apiruntime.Scheme {
	t.Helper()
	s := apiruntime.NewScheme()
	if err := clientgoscheme.AddToScheme(s); err != nil {
		t.Fatalf("core scheme: %v", err)
	}
	if err := v1.AddToScheme(s); err != nil {
		t.Fatalf("v1 scheme: %v", err)
	}
	return s
}

func gpuNode(name string, gpus int64) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: name, Labels: map[string]string{
			topology.LabelRegion:       "us-west",
			topology.LabelCluster:      "cluster-a",
			topology.LabelFabricDomain: "island-a",
			topology.LabelGPUFlavor:    "H100-80GB",
		}},
		Status: corev1.NodeStatus{
			Capacity:   corev1.ResourceList{gpuResource: *resource.NewQuantity(gpus, resource.DecimalSI)},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
}

func teamBudget(concurrency int32) *v1.Budget {
	return &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "team"},
		Spec: v1.BudgetSpec{Owner: "org:ai:team", Envelopes: []v1.BudgetEnvelope{{
			Name: "west", Flavor: "H100-80GB", Concurrency: concurrency,
			Selector: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
		}}},
	}
}

func trainRun() *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4}},
	}
}

func gangPod() *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace:   "default",
			Name:        "train-pod-0",
			Labels:      map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive},
			Annotations: map[string]string{binder.AnnotationGPUs: "4", binder.AnnotationExpectedWidth: "1", binder.AnnotationFlavor: "H100-80GB"},
		},
	}
}

func newManager(t *testing.T, objs ...client.Object) *gangManager {
	t.Helper()
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).WithObjects(objs...).Build()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	return newGangManager(c, func() time.Time { return now })
}

// A gang that fits and funds: decide returns fundable and claimPayer hands out
// the paying envelope for a pod's mint.
func TestGangDecideFundable(t *testing.T) {
	m := newManager(t, trainRun(), teamBudget(8), gpuNode("node-a", 4))
	pod := gangPod()

	fundable, reason := m.decide(context.Background(), pod)
	if !fundable {
		t.Fatalf("expected fundable, got not-fundable: %s", reason)
	}
	seg, gpusPerPod, ok := m.claimPayer(pod)
	if !ok {
		t.Fatalf("claimPayer returned !ok for a fundable gang")
	}
	if seg.Owner != "org:ai:team" || seg.EnvelopeName != "west" {
		t.Errorf("payer = %s/%s, want org:ai:team/west", seg.Owner, seg.EnvelopeName)
	}
	if gpusPerPod != 4 {
		t.Errorf("gpusPerPod = %d, want 4", gpusPerPod)
	}
	// Idempotent per pod: re-claiming for the same pod returns the same payer
	// (a PreBind retry must not consume another).
	if seg2, _, ok := m.claimPayer(pod); !ok || seg2.EnvelopeName != seg.EnvelopeName {
		t.Errorf("expected idempotent re-claim for the same pod, got ok=%v seg=%s", ok, seg2.EnvelopeName)
	}
	// Only one pod's worth of funding: a different pod overflows the 1-pod gang.
	other := gangPod()
	other.Name = "train-pod-1"
	if _, _, ok := m.claimPayer(other); ok {
		t.Errorf("expected the 1-pod gang's funding to be exhausted for a second distinct pod")
	}
}

// A gang whose flavor has no capacity anywhere cannot place: decide is not
// fundable (the plugin will reject → controller reserves).
func TestGangDecideUnfundableNoCapacity(t *testing.T) {
	m := newManager(t, trainRun(), teamBudget(8), gpuNode("node-a", 1)) // only 1 GPU, run needs 4
	pod := gangPod()

	if fundable, _ := m.decide(context.Background(), pod); fundable {
		t.Fatalf("expected not-fundable (insufficient capacity)")
	}
	if _, _, ok := m.claimPayer(pod); ok {
		t.Errorf("claimPayer should return !ok for an unfundable gang")
	}
}

// A budget with zero concurrency funds nothing: decide is not fundable even
// though the topology fits.
func TestGangDecideUnfundableNoBudget(t *testing.T) {
	m := newManager(t, trainRun(), teamBudget(0), gpuNode("node-a", 4))
	if fundable, _ := m.decide(context.Background(), gangPod()); fundable {
		t.Fatalf("expected not-fundable (zero-concurrency envelope)")
	}
}

// forget clears an undecided/unclaimed gang so a retry re-derives; it must not
// drop a gang that has already handed out a payer (its lease is being minted).
func TestGangForget(t *testing.T) {
	m := newManager(t, trainRun(), teamBudget(8), gpuNode("node-a", 4))
	pod := gangPod()

	m.decide(context.Background(), pod)
	m.claimPayer(pod) // gang now has claimed==1
	m.forget(pod)
	if _, ok := m.gangs[gangKey(pod)]; !ok {
		t.Errorf("forget dropped a gang with an outstanding claim")
	}
}
