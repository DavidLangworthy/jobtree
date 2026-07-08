package kube

import (
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// R2: buildPod stamps a per-incarnation run nonce (a 12-char prefix of the Run
// UID) so the plugin's minted lease name is unique per incarnation — a
// delete+resubmit of a same-named Run cannot alias the prior incarnation's
// closed lease (the ABA hazard). A run with no UID (pure-engine tests) stamps
// nothing, keeping the legacy lease name.
func TestBuildPodStampsRunNonce(t *testing.T) {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default", UID: types.UID("0123456789abcdef-aaaa-bbbb")},
		Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1}},
	}
	manifest := binder.PodManifest{
		Namespace: "default", Name: "train-active-0", GPUs: 1,
		Labels: map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive},
	}
	pod := buildPod(manifest, run)
	if got := pod.Annotations[binder.AnnotationRunNonce]; got != "0123456789ab" {
		t.Errorf("run-nonce = %q, want the 12-char UID prefix %q", got, "0123456789ab")
	}

	// No UID → no nonce annotation (backward compatible).
	run.UID = ""
	if pod := buildPod(manifest, run); pod.Annotations[binder.AnnotationRunNonce] != "" {
		t.Errorf("run-nonce = %q, want empty when the Run has no UID", pod.Annotations[binder.AnnotationRunNonce])
	}
}

// buildPod must render a real, UNSCHEDULED pod: the researcher's container is
// preserved, only scheduling-owned fields are overlaid, and nodeName is never
// set (the plugin places it).
func TestBuildPodFromRoleTemplate(t *testing.T) {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:ai:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
			Roles: []v1.RunRole{{
				Name: "trainer", Width: 2, GPUsPerPod: 4,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:    v1.GPUTargetContainerName,
						Image:   "ghcr.io/acme/train:latest",
						Command: []string{"python", "train.py"},
					}},
				}},
			}},
		},
	}
	manifest := binder.PodManifest{
		Namespace: "default", Name: "train-pod-0", NodeName: "node-a", GPUs: 4,
		Labels:      map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive},
		Annotations: map[string]string{binder.AnnotationExpectedWidth: "2"},
	}

	pod := buildPod(manifest, run)

	if pod.Spec.SchedulerName != schedulerName {
		t.Errorf("schedulerName = %q, want %q", pod.Spec.SchedulerName, schedulerName)
	}
	if pod.Spec.NodeName != "" {
		t.Errorf("nodeName = %q, want empty (plugin places it)", pod.Spec.NodeName)
	}
	if pod.Spec.RestartPolicy != corev1.RestartPolicyNever {
		t.Errorf("restartPolicy = %q, want Never", pod.Spec.RestartPolicy)
	}
	c := pod.Spec.Containers[0]
	if c.Image != "ghcr.io/acme/train:latest" || len(c.Command) != 2 || c.Command[0] != "python" {
		t.Errorf("researcher container not preserved: image=%q cmd=%v", c.Image, c.Command)
	}
	if got := c.Resources.Requests[GPUCapacityResource]; got.Value() != 4 {
		t.Errorf("gpu request = %s, want 4", got.String())
	}
	if got := c.Resources.Limits[GPUCapacityResource]; got.Value() != 4 {
		t.Errorf("gpu limit = %s, want 4", got.String())
	}
	if pod.Annotations[binder.AnnotationFlavor] != "H100-80GB" {
		t.Errorf("flavor annotation = %q, want H100-80GB", pod.Annotations[binder.AnnotationFlavor])
	}
	if pod.Annotations[PodGPUAnnotation] != "4" {
		t.Errorf("gpus annotation = %q, want 4", pod.Annotations[PodGPUAnnotation])
	}
	if pod.Annotations[binder.AnnotationExpectedWidth] != "2" {
		t.Errorf("expected-width annotation = %q, want 2", pod.Annotations[binder.AnnotationExpectedWidth])
	}
	if pod.Labels[binder.LabelRunName] != "train" {
		t.Errorf("run label not carried: %v", pod.Labels)
	}
	na := pod.Spec.Affinity.NodeAffinity
	if na == nil || len(na.PreferredDuringSchedulingIgnoredDuringExecution) != 1 {
		t.Fatalf("expected one advisory node preference, got %+v", pod.Spec.Affinity)
	}
	if got := na.PreferredDuringSchedulingIgnoredDuringExecution[0].Preference.MatchExpressions[0].Values[0]; got != "node-a" {
		t.Errorf("advisory node = %q, want node-a", got)
	}
}

// A Roles-less legacy Run gets a real terminating default container, not a
// pause mannequin — so its completion is real.
func TestBuildPodLegacyRolelessUsesRealDefault(t *testing.T) {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "legacy", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1}},
	}
	manifest := binder.PodManifest{
		Namespace: "default", Name: "legacy-pod-0", GPUs: 1,
		Labels: map[string]string{binder.LabelRunName: "legacy", binder.LabelRunRole: binder.RoleActive},
	}

	pod := buildPod(manifest, run)

	if pod.Spec.Containers[0].Image != defaultWorkloadImage {
		t.Errorf("default image = %q, want %q", pod.Spec.Containers[0].Image, defaultWorkloadImage)
	}
	if len(pod.Spec.Containers[0].Command) == 0 {
		t.Errorf("default container must run a real terminating command, got none")
	}
	if pod.Spec.SchedulerName != schedulerName || pod.Spec.NodeName != "" {
		t.Errorf("legacy pod scheduling overlay wrong: scheduler=%q node=%q", pod.Spec.SchedulerName, pod.Spec.NodeName)
	}
}

// A grow-cohort pod must carry the cohort annotation through to the real pod, or
// the scheduler plugin folds it into the base gang and its funding claim fails
// (regression: a live grow proof caught buildPod dropping the cohort).
func TestBuildPodCarriesCohortAnnotation(t *testing.T) {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 2}},
	}
	manifest := binder.PodManifest{
		Namespace: "default", Name: "train-c1-active-0", GPUs: 1,
		Labels: map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive},
		Annotations: map[string]string{
			binder.AnnotationExpectedWidth: "2",
			binder.AnnotationLeaseReason:   "Grow",
			binder.AnnotationCohort:        "1",
		},
	}
	pod := buildPod(manifest, run)
	if pod.Annotations[binder.AnnotationCohort] != "1" {
		t.Errorf("cohort annotation = %q, want 1 (dropped → plugin folds it into the base gang)", pod.Annotations[binder.AnnotationCohort])
	}
	if pod.Annotations[binder.AnnotationLeaseReason] != "Grow" {
		t.Errorf("lease-reason = %q, want Grow", pod.Annotations[binder.AnnotationLeaseReason])
	}
}

// A hot-spare pod reserves its GPU but runs a long-lived holder, NOT the
// researcher's (terminating) workload — it holds the slice until a swap promotes
// it. If it ran the terminating default it would exit and release the GPU.
func TestBuildPodSpareIsLongLivedGPUHolder(t *testing.T) {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:ai:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
			Roles: []v1.RunRole{{
				Name: "trainer", Width: 4, GPUsPerPod: 1,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: []corev1.Container{{
					Name: v1.GPUTargetContainerName, Image: "ghcr.io/acme/train:latest", Command: []string{"python", "train.py"},
				}}}},
			}},
		},
	}
	manifest := binder.PodManifest{
		Namespace: "default", Name: "train-spare-0", NodeName: "node-b", GPUs: 1,
		Labels:      map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleSpare},
		Annotations: map[string]string{binder.AnnotationExpectedWidth: "4"},
	}

	pod := buildPod(manifest, run)

	c := pod.Spec.Containers[0]
	if c.Image == "ghcr.io/acme/train:latest" {
		t.Errorf("spare must not run the researcher's workload, got image %q", c.Image)
	}
	joined := strings.Join(c.Command, " ")
	if !strings.Contains(joined, "sleep") {
		t.Errorf("spare container must be long-lived (a GPU holder), got command %v", c.Command)
	}
	if got := c.Resources.Requests[GPUCapacityResource]; got.Value() != 1 {
		t.Errorf("spare gpu request = %s, want 1 (it reserves the slice)", got.String())
	}
	if pod.Spec.NodeName != "" || pod.Spec.SchedulerName != schedulerName {
		t.Errorf("spare scheduling overlay wrong: node=%q scheduler=%q", pod.Spec.NodeName, pod.Spec.SchedulerName)
	}
}

// A node-failure swap pod must carry its funding provenance AND be HARD-targeted
// (required node affinity) at the reclaimed spare node.
func TestBuildPodSwapHardTargetsAndCarriesProvenance(t *testing.T) {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 1}},
	}
	manifest := binder.PodManifest{
		Namespace: "default", Name: "train-g0-swap-1", GPUs: 1,
		Labels: map[string]string{binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive},
		Annotations: map[string]string{
			binder.AnnotationLeaseReason:   "Swap",
			binder.AnnotationSwapNode:      "node-b",
			binder.AnnotationPayerOwner:    "org:sponsor",
			binder.AnnotationPayerBudget:   "sponsor",
			binder.AnnotationPayerEnvelope: "west",
		},
	}
	pod := buildPod(manifest, run)

	for k, want := range map[string]string{
		binder.AnnotationSwapNode:      "node-b",
		binder.AnnotationPayerOwner:    "org:sponsor",
		binder.AnnotationPayerBudget:   "sponsor",
		binder.AnnotationPayerEnvelope: "west",
		binder.AnnotationLeaseReason:   "Swap",
	} {
		if pod.Annotations[k] != want {
			t.Errorf("annotation %s = %q, want %q", k, pod.Annotations[k], want)
		}
	}
	na := pod.Spec.Affinity.NodeAffinity
	if na == nil || na.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		t.Fatalf("swap pod must have a REQUIRED node affinity, got %+v", pod.Spec.Affinity)
	}
	terms := na.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms
	if len(terms) != 1 || len(terms[0].MatchExpressions) != 1 || terms[0].MatchExpressions[0].Values[0] != "node-b" {
		t.Errorf("required affinity = %+v, want a single hostname==node-b term", terms)
	}
}
