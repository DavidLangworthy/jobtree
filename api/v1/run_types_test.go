package v1

import "testing"

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
