package lowering

import (
	"errors"
	"testing"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// singleRoleRun returns a Run whose single role is well-formed enough for the
// lowering seam. As JOBSET-5 fills in LowerToJobSet, this fixture is the
// starting point for asserting the real mapping (one ReplicatedJob, parallelism
// == Width, schedulerName=jobtree, nvidia.com/gpu==GPUsPerPod, etc.).
func singleRoleRun() *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "ml"},
		Spec: v1.RunSpec{
			Owner:     "org:test",
			Resources: v1.RunResources{GPUType: "H100", TotalGPUs: 8},
			Roles: []v1.RunRole{{
				Name:       "trainer",
				Width:      2,
				GPUsPerPod: 4,
				Template: corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{Name: v1.GPUTargetContainerName, Image: "ghcr.io/acme/train:latest"},
						},
					},
				},
			}},
		},
	}
}

// TestLowerToJobSetSeam pins the skeleton's current contract: the seam exists,
// rejects a role-less Run, and returns the explicit not-implemented sentinel
// for a real Run so no caller silently ships a half-lowered workload. JOBSET-5
// replaces the ErrNotImplemented assertion with the real JobSet mapping checks.
func TestLowerToJobSetSeam(t *testing.T) {
	if _, err := LowerToJobSet(nil); !errors.Is(err, ErrNoRoles) {
		t.Fatalf("nil run: want ErrNoRoles, got %v", err)
	}

	roleless := singleRoleRun()
	roleless.Spec.Roles = nil
	if _, err := LowerToJobSet(roleless); !errors.Is(err, ErrNoRoles) {
		t.Fatalf("role-less run: want ErrNoRoles, got %v", err)
	}

	obj, err := LowerToJobSet(singleRoleRun())
	if !errors.Is(err, ErrNotImplemented) {
		t.Fatalf("single-role run: want ErrNotImplemented, got %v", err)
	}
	if obj != nil {
		t.Fatalf("single-role run: want nil object until JOBSET-5, got %v", obj)
	}

	// TODO(JOBSET-5): once LowerToJobSet is implemented, replace the assertion
	// above with checks that it returns a *jobsetv1alpha2.JobSet whose single
	// ReplicatedJob has parallelism/completions == role.Width, replicas == 1,
	// schedulerName == "jobtree", no nodeName, restartPolicy == Never, and the
	// nvidia.com/gpu limit == role.GPUsPerPod on the GPU-target container.
}
