package plugin

import (
	"context"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// The mint must stamp durable gang identity — the cohort and the pod name — onto the
// lease, so a scheduler restart (R2 pt3) and a staleness-robust cache fold (R4 pt1b)
// can rebuild gang membership from the leases alone rather than string-parsing the
// lease name. This is the shared foundation (correctness closeout Phase 4).
func TestMintStampsGangIdentityOntoTheLease(t *testing.T) {
	ctx := context.Background()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	c := fake.NewClientBuilder().WithScheme(testScheme(t)).
		WithObjects(trainRun(), teamBudget(8), gpuNode("node-a", 4)).Build()
	m := newGangManager(c, func() time.Time { return now })

	pod := gangPod()
	pod.Labels[binder.LabelGroupIndex] = "0"
	if fundable, reason := m.decide(ctx, pod); !fundable {
		t.Fatalf("gang should fund: %s", reason)
	}
	mintPodLease(t, ctx, c, m, pod, "node-a")

	var lease v1.GPULease
	if err := c.Get(ctx, client.ObjectKey{Namespace: "default", Name: pod.Name + "-lease"}, &lease); err != nil {
		t.Fatalf("get lease: %v", err)
	}
	if got := binder.LeaseCohort(&lease); got != "0" {
		t.Errorf("lease cohort = %q, want %q (base gang)", got, "0")
	}
	if got := binder.LeasePodName(&lease); got != pod.Name {
		t.Errorf("lease pod-name = %q, want %q — a lease must name the pod it funds", got, pod.Name)
	}
}

// cohortOf normalizes the base gang to the literal "0" (absent annotation == "0")
// and passes an elastic-grow cohort through unchanged.
func TestCohortOfNormalizesTheBaseGang(t *testing.T) {
	base := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p"}}
	if got := cohortOf(base); got != "0" {
		t.Errorf("cohortOf(no annotation) = %q, want %q", got, "0")
	}
	zero := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Annotations: map[string]string{binder.AnnotationCohort: "0"}}}
	if got := cohortOf(zero); got != "0" {
		t.Errorf("cohortOf(\"0\") = %q, want %q", got, "0")
	}
	grow := &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: "p", Annotations: map[string]string{binder.AnnotationCohort: "2"}}}
	if got := cohortOf(grow); got != "2" {
		t.Errorf("cohortOf(\"2\") = %q, want %q", got, "2")
	}
}

// The readers default an unstamped legacy lease safely: cohort "0", pod name "".
func TestLeaseIdentityReadersDefaultForLegacyLeases(t *testing.T) {
	legacy := &v1.GPULease{ObjectMeta: v1.ObjectMeta{Name: "old"}}
	if got := binder.LeaseCohort(legacy); got != "0" {
		t.Errorf("LeaseCohort(legacy) = %q, want %q", got, "0")
	}
	if got := binder.LeasePodName(legacy); got != "" {
		t.Errorf("LeasePodName(legacy) = %q, want \"\"", got)
	}
}
