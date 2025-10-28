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
}
