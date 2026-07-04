package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

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
