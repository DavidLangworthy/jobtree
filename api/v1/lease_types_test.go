package v1

import (
	"testing"
	"time"
)

func TestLeaseValidation(t *testing.T) {
	lease := &GPULease{
		Spec: GPULeaseSpec{
			Owner:          "org",
			RunRef:         RunReference{Name: "run"},
			Slice:          GPULeaseSlice{Nodes: []string{"n1"}, Role: "Active"},
			Interval:       GPULeaseInterval{Start: NewTime(time.Now())},
			PaidByEnvelope: "env",
			Reason:         "Start",
		},
	}

	if err := lease.ValidateCreate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	lease.Spec.Slice.Nodes = nil
	if err := lease.ValidateCreate(); err == nil {
		t.Fatalf("expected error for empty nodes")
	}
}
