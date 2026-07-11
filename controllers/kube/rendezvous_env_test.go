package kube

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/controllers"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

func roledRun(name string, width, gpusPerPod int32, malleable bool) *v1.Run {
	run := &v1.Run{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: width * gpusPerPod},
			Roles: []v1.RunRole{{
				Name: "worker", Width: width, GPUsPerPod: gpusPerPod,
				Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: v1.GPUTargetContainerName, Image: "trainer:1"}},
				}},
			}},
		},
		Status: v1.RunStatus{Phase: controllers.RunPhaseRunning},
	}
	if malleable {
		run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 1, MaxTotalGPUs: 64, StepGPUs: 1}
	}
	return run
}

func activeManifest(name string) binder.PodManifest {
	return binder.PodManifest{
		Namespace: "default", Name: name, GPUs: 1,
		Labels: map[string]string{
			binder.LabelRunName: "train", binder.LabelRunRole: binder.RoleActive, binder.LabelGroupIndex: "0",
		},
	}
}

func envOf(pod *corev1.Pod) map[string]string {
	m := map[string]string{}
	for _, e := range pod.Spec.Containers[0].Env {
		m[e.Name] = e.Value
	}
	return m
}

// R9 9A-2: a fixed-width gang's Active pod carries torch rendezvous env derived from
// its ordinal and the run's shape.
func TestRendezvousEnvForFixedWidthGang(t *testing.T) {
	pod := buildPod(activeManifest("train-active-2"), roledRun("train", 4, 1, false))
	env := envOf(pod)
	if env["MASTER_ADDR"] != "train-active-0.train.default.svc" {
		t.Errorf("MASTER_ADDR = %q, want train-active-0.train.default.svc", env["MASTER_ADDR"])
	}
	if env["MASTER_PORT"] != "29500" {
		t.Errorf("MASTER_PORT = %q, want 29500", env["MASTER_PORT"])
	}
	if env["WORLD_SIZE"] != "4" || env["NNODES"] != "4" {
		t.Errorf("WORLD_SIZE/NNODES = %q/%q, want 4/4", env["WORLD_SIZE"], env["NNODES"])
	}
	if env["NODE_RANK"] != "2" {
		t.Errorf("NODE_RANK = %q, want 2 (this pod's ordinal)", env["NODE_RANK"])
	}
	// Per-process env is torchrun's job, never ours.
	if _, ok := env["RANK"]; ok {
		t.Errorf("RANK must not be injected — it is per-process (torchrun's job)")
	}
}

// A swap pod inherits its replaced member's ordinal via hostname, so its NODE_RANK is
// that member's rank — the whole point of the 9A-1 identity carrying into 9A-2.
func TestRendezvousEnvSwapPodInheritsRankFromHostname(t *testing.T) {
	m := activeManifest("train-g0-swap-1782900000000000000") // unique object name
	m.Hostname = "train-active-1"                            // inherited from the dead member
	env := envOf(buildPod(m, roledRun("train", 4, 1, false)))
	if env["NODE_RANK"] != "1" {
		t.Errorf("swap NODE_RANK = %q, want 1 (from the inherited hostname), not the object name", env["NODE_RANK"])
	}
}

func TestNoRendezvousEnvForWidthOne(t *testing.T) {
	env := envOf(buildPod(activeManifest("solo-active-0"), roledRun("train", 1, 1, false)))
	if _, ok := env["MASTER_ADDR"]; ok {
		t.Errorf("a width-1 run needs no rendezvous, got env %v", env)
	}
}

func TestNoRendezvousEnvForMalleableRun(t *testing.T) {
	// A malleable run resizes; a static WORLD_SIZE would be wrong — it needs elastic
	// rendezvous, which is not this.
	env := envOf(buildPod(activeManifest("train-active-2"), roledRun("train", 4, 1, true)))
	if _, ok := env["WORLD_SIZE"]; ok {
		t.Errorf("a malleable run must not get static rendezvous env, got %v", env)
	}
}

func TestRendezvousEnvOverridesAResearcherSetName(t *testing.T) {
	run := roledRun("train", 4, 1, false)
	run.Spec.Roles[0].Template.Spec.Containers[0].Env = []corev1.EnvVar{{Name: "WORLD_SIZE", Value: "999"}}
	env := envOf(buildPod(activeManifest("train-active-2"), run))
	if env["WORLD_SIZE"] != "4" {
		t.Errorf("jobtree's injected WORLD_SIZE must win over a researcher's, got %q", env["WORLD_SIZE"])
	}
	// And exactly once — no duplicate env entries.
	count := 0
	for _, e := range buildPod(activeManifest("train-active-2"), run).Spec.Containers[0].Env {
		if e.Name == "WORLD_SIZE" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("WORLD_SIZE must appear exactly once, got %d", count)
	}
}
