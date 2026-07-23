package cmd

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

func labeledPod(name, run, role, group, node string, phase corev1.PodPhase) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      name,
			Labels: map[string]string{
				binder.LabelRunName:    run,
				binder.LabelRunRole:    role,
				binder.LabelGroupIndex: group,
			},
		},
		Spec:   corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{Phase: phase},
	}
}

// liveRunPods must find pods by the run-name label and MUST NOT return another
// run's pods — the selector is the whole point (a researcher should not have to
// know it), so a leak here hands them someone else's containers.
func TestLiveRunPodsSelectsOnlyTheRunsPods(t *testing.T) {
	c := fake.NewClientBuilder().WithScheme(liveScheme).WithObjects(
		labeledPod("train-g0-active-0", "train", binder.RoleActive, "0", "node-a", corev1.PodRunning),
		labeledPod("train-g0-active-1", "train", binder.RoleActive, "0", "node-b", corev1.PodRunning),
		labeledPod("other-g0-active-0", "other", binder.RoleActive, "0", "node-c", corev1.PodRunning),
	).Build()

	pods, err := liveRunPods(context.Background(), c, "default", "train")
	if err != nil {
		t.Fatalf("liveRunPods: %v", err)
	}
	if len(pods) != 2 {
		t.Fatalf("expected 2 pods for run train, got %d: %+v", len(pods), pods)
	}
	for _, p := range pods {
		if p.Name == "other-g0-active-0" {
			t.Fatalf("liveRunPods returned another run's pod: %s", p.Name)
		}
	}
}

// The listing order is load-bearing: logs.go's --rank counts pods in exactly this
// order, so "rank 0" must be a stable, meaningful pod (the first active member),
// never whatever order the API server happened to return.
func TestRunPodsSortActiveBeforeSpareThenGroup(t *testing.T) {
	pods := []runPod{
		{Name: "z-active-g1", Role: binder.RoleActive, Group: "1"},
		{Name: "spare-g0", Role: binder.RoleSpare, Group: "0"},
		{Name: "a-active-g0", Role: binder.RoleActive, Group: "0"},
		{Name: "b-active-g0", Role: binder.RoleActive, Group: "0"},
	}
	sortRunPods(pods)
	want := []string{"a-active-g0", "b-active-g0", "z-active-g1", "spare-g0"}
	for i, w := range want {
		if pods[i].Name != w {
			t.Fatalf("rank %d = %s, want %s (order: %+v)", i, pods[i].Name, w, names(pods))
		}
	}
}

func names(pods []runPod) []string {
	out := make([]string, len(pods))
	for i, p := range pods {
		out[i] = p.Name
	}
	return out
}

// The per-pod envelope is read off the OPEN lease's pod-name annotation. A closed
// lease must not contribute — a pod whose lease closed is no longer charging that
// envelope, and reporting it as if it were would misstate who is paying.
func TestPayerByPodUsesOpenLeasesOnly(t *testing.T) {
	leases := []v1.GPULease{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "train-g0-active-0-lease",
				Annotations: map[string]string{binder.AnnotationPodName: "train-g0-active-0"},
			},
			Spec:   v1.GPULeaseSpec{PaidByEnvelope: "team-a/pool"},
			Status: v1.GPULeaseStatus{Closed: false},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name:        "train-g0-active-1-lease",
				Annotations: map[string]string{binder.AnnotationPodName: "train-g0-active-1"},
			},
			Spec:   v1.GPULeaseSpec{PaidByEnvelope: "team-a/pool"},
			Status: v1.GPULeaseStatus{Closed: true},
		},
	}
	byPod := payerByPod(leases)
	if byPod["train-g0-active-0"] != "team-a/pool" {
		t.Errorf("open lease's pod = %q, want team-a/pool", byPod["train-g0-active-0"])
	}
	if _, ok := byPod["train-g0-active-1"]; ok {
		t.Errorf("a closed lease's pod appeared in the payer map: %v", byPod)
	}
}

// buildPodsPayload joins each pod to its paying envelope and renders "-" when
// there is none (a pod that has not minted yet), so the table never shows a blank
// column that reads as missing data.
func TestBuildPodsPayloadJoinsEnvelopeAndFillsGaps(t *testing.T) {
	pods := []runPod{
		{Name: "train-g0-active-0", Role: binder.RoleActive, Group: "0", Node: "node-a", Phase: "Running"},
		{Name: "train-g0-active-1", Role: binder.RoleActive, Group: "0", Node: "", Phase: "Pending"},
	}
	leases := []v1.GPULease{{
		ObjectMeta: metav1.ObjectMeta{Annotations: map[string]string{binder.AnnotationPodName: "train-g0-active-0"}},
		Spec:       v1.GPULeaseSpec{PaidByEnvelope: "team-a/pool"},
	}}
	payload := buildPodsPayload("default", "train", pods, leases)
	if got := payload.Rows[0][5]; got != "team-a/pool" {
		t.Errorf("pod 0 envelope column = %q, want team-a/pool", got)
	}
	if got := payload.Rows[1][5]; got != "-" {
		t.Errorf("unminted pod envelope column = %q, want -", got)
	}
	if got := payload.Rows[1][3]; got != "-" {
		t.Errorf("unscheduled pod node column = %q, want -", got)
	}
}
