package v1

import (
	"testing"
	"time"
)

func TestLeaseValidation(t *testing.T) {
	lease := &Lease{
		Spec: LeaseSpec{
			Owner:          "org",
			RunRef:         RunReference{Name: "run"},
			Slice:          LeaseSlice{Nodes: []string{"n1"}, Role: "Active"},
			Interval:       LeaseInterval{Start: NewTime(time.Now())},
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
