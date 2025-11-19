package v1

import (
	"testing"
	"time"
)

func TestReservationValidation(t *testing.T) {
	now := NewTime(time.Now())
	res := &Reservation{
		Spec: ReservationSpec{
			RunRef:         RunReference{Name: "run"},
			IntendedSlice:  IntendedSlice{Nodes: []string{"n1"}},
			PayingEnvelope: "env",
			EarliestStart:  now,
		},
	}

	if err := res.ValidateCreate(); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	res.Spec.IntendedSlice = IntendedSlice{}
	if err := res.ValidateCreate(); err == nil {
		t.Fatalf("expected error when slice empty")
	}
}
