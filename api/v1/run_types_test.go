package v1

import (
	"testing"
	"time"

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
