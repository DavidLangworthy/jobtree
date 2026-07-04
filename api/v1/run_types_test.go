package v1

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestRunValidation(t *testing.T) {
	run := &Run{}
	if err := run.ValidateCreate(); err == nil {
		t.Fatalf("expected error for missing owner")
	}

	run.Spec.Owner = "org:test"
	run.Spec.Resources.GPUType = "H100"
	run.Spec.Resources.TotalGPUs = 128
	if err := run.ValidateCreate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	val := int32(-1)
	run.Spec.Locality = &RunLocality{GroupGPUs: &val}
	if err := run.ValidateCreate(); err == nil {
		t.Fatalf("expected validation error for negative groupGPUs")
	}

	run.Spec.Locality = nil
	run.Spec.Spares = &val
	if err := run.ValidateCreate(); err == nil {
		t.Fatalf("expected validation error for negative spares")
	}

	run.Spec.Spares = nil
	run.Spec.Malleable = &RunMalleability{MinTotalGPUs: 64, MaxTotalGPUs: 160, StepGPUs: 16}
	run.Spec.Resources.TotalGPUs = 32
	if err := run.ValidateCreate(); err == nil {
		t.Fatalf("expected error for initial total outside range")
	}

	run.Spec.Resources.TotalGPUs = 90
	if err := run.ValidateCreate(); err == nil {
		t.Fatalf("expected error for step misalignment")
	}

	run.Spec.Resources.TotalGPUs = 96
	desired := int32(150)
	run.Spec.Malleable.DesiredTotalGPUs = &desired
	if err := run.ValidateCreate(); err == nil {
		t.Fatalf("expected error for desired not aligned")
	}

	desired = 64
	run.Spec.Malleable.DesiredTotalGPUs = &desired
	if err := run.ValidateCreate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunFollowValidation(t *testing.T) {
	base := func() *Run {
		return &Run{
			ObjectMeta: ObjectMeta{Name: "train"},
			Spec: RunSpec{
				Owner:     "org:test",
				Resources: RunResources{GPUType: "H100", TotalGPUs: 8},
			},
		}
	}

	negGrace := metav1.Duration{Duration: -time.Hour}
	cases := []struct {
		name    string
		follow  *RunFollow
		wantErr bool
	}{
		{"valid single", &RunFollow{After: []string{"data-prep"}}, false},
		{"valid multi + policy", &RunFollow{After: []string{"a", "b"}, OnUpstreamFailure: "fail"}, false},
		{"empty after", &RunFollow{After: []string{}}, true},
		{"empty name", &RunFollow{After: []string{""}}, true},
		{"self follow", &RunFollow{After: []string{"train"}}, true},
		{"duplicate", &RunFollow{After: []string{"a", "a"}}, true},
		{"bad policy", &RunFollow{After: []string{"a"}, OnUpstreamFailure: "nuke"}, true},
		{"negative grace", &RunFollow{After: []string{"a"}, UpstreamFailureGrace: &negGrace}, true},
	}
	for _, tc := range cases {
		run := base()
		run.Spec.Follow = tc.follow
		err := run.ValidateCreate()
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected an error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}
}

func TestRunRoleValidation(t *testing.T) {
	// A well-formed single-role Run: width*gpusPerPod == totalGPUs, one
	// container with an image, no jobtree-owned fields set.
	validRole := func() RunRole {
		return RunRole{
			Name:       "trainer",
			Width:      2,
			GPUsPerPod: 4,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{Name: GPUTargetContainerName, Image: "ghcr.io/acme/train:latest"},
					},
				},
			},
		}
	}
	base := func() *Run {
		return &Run{
			ObjectMeta: ObjectMeta{Name: "train"},
			Spec: RunSpec{
				Owner:     "org:test",
				Resources: RunResources{GPUType: "H100", TotalGPUs: 8},
			},
		}
	}

	// Baseline: no roles at all is still valid (legacy pause-pod path).
	if err := base().ValidateCreate(); err != nil {
		t.Fatalf("role-less run should validate: %v", err)
	}

	posGroup := int32(4)
	zeroGroup := int32(0)
	negSpares := int32(-1)
	okSpares := int32(1)

	cases := []struct {
		name    string
		mutate  func(*Run)
		wantErr bool
	}{
		{"valid single role", func(r *Run) { r.Spec.Roles = []RunRole{validRole()} }, false},
		{"valid with group+spares override", func(r *Run) {
			role := validRole()
			role.GroupGPUs = &posGroup
			role.Spares = &okSpares
			r.Spec.Roles = []RunRole{role}
		}, false},
		{"valid target container fallback to first", func(r *Run) {
			role := validRole()
			role.Template.Spec.Containers = []corev1.Container{{Name: "notworkload", Image: "img"}}
			r.Spec.Roles = []RunRole{role}
		}, false},
		{"two roles rejected in v1", func(r *Run) { r.Spec.Roles = []RunRole{validRole(), validRole()} }, true},
		{"missing name", func(r *Run) {
			role := validRole()
			role.Name = ""
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"non-positive width", func(r *Run) {
			role := validRole()
			role.Width = 0
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"non-positive gpusPerPod", func(r *Run) {
			role := validRole()
			role.GPUsPerPod = 0
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"width*gpusPerPod mismatch", func(r *Run) {
			role := validRole()
			role.Width = 3 // 3*4=12 != 8
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"no containers", func(r *Run) {
			role := validRole()
			role.Template.Spec.Containers = nil
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"empty image on target", func(r *Run) {
			role := validRole()
			role.Template.Spec.Containers = []corev1.Container{{Name: GPUTargetContainerName}}
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"template sets nodeName", func(r *Run) {
			role := validRole()
			role.Template.Spec.NodeName = "node-1"
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"template sets schedulerName", func(r *Run) {
			role := validRole()
			role.Template.Spec.SchedulerName = "jobtree"
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"template sets restartPolicy", func(r *Run) {
			role := validRole()
			role.Template.Spec.RestartPolicy = corev1.RestartPolicyOnFailure
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"non-positive groupGPUs override", func(r *Run) {
			role := validRole()
			role.GroupGPUs = &zeroGroup
			r.Spec.Roles = []RunRole{role}
		}, true},
		{"negative spares override", func(r *Run) {
			role := validRole()
			role.Spares = &negSpares
			r.Spec.Roles = []RunRole{role}
		}, true},
	}
	for _, tc := range cases {
		run := base()
		tc.mutate(run)
		err := run.ValidateCreate()
		if tc.wantErr && err == nil {
			t.Errorf("%s: expected an error, got nil", tc.name)
		}
		if !tc.wantErr && err != nil {
			t.Errorf("%s: unexpected error: %v", tc.name, err)
		}
	}
}

func TestRunRoleGPUTargetContainerIndex(t *testing.T) {
	cases := []struct {
		name       string
		containers []corev1.Container
		want       int
	}{
		{"no containers", nil, -1},
		{"single unnamed falls back to first", []corev1.Container{{Name: "app", Image: "x"}}, 0},
		{"named workload wins", []corev1.Container{
			{Name: "sidecar", Image: "x"},
			{Name: GPUTargetContainerName, Image: "y"},
		}, 1},
	}
	for _, tc := range cases {
		role := RunRole{Template: corev1.PodTemplateSpec{Spec: corev1.PodSpec{Containers: tc.containers}}}
		if got := role.GPUTargetContainerIndex(); got != tc.want {
			t.Errorf("%s: got %d, want %d", tc.name, got, tc.want)
		}
	}
}

func TestRunDefaultSetsDesired(t *testing.T) {
	run := &Run{
		Spec: RunSpec{
			Owner: "org:test",
			Resources: RunResources{
				GPUType:   "H100",
				TotalGPUs: 96,
			},
			Malleable: &RunMalleability{
				MinTotalGPUs: 64,
				MaxTotalGPUs: 160,
				StepGPUs:     16,
			},
		},
	}
	run.Default()
	if run.Spec.Malleable.DesiredTotalGPUs == nil {
		t.Fatalf("expected desired GPUs defaulted")
	}
	if *run.Spec.Malleable.DesiredTotalGPUs != run.Spec.Malleable.MaxTotalGPUs {
		t.Fatalf("expected desired to equal max when defaulted, got %d", *run.Spec.Malleable.DesiredTotalGPUs)
	}
}
