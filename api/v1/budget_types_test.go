package v1

import (
	"testing"
	"time"
)

func TestBudgetEnvelopeValidate(t *testing.T) {
	now := NewTime(time.Unix(0, 0))
	later := NewTime(time.Unix(3600, 0))
	tests := []struct {
		name       string
		env        BudgetEnvelope
		expectsErr bool
	}{
		{
			name: "valid window",
			env: BudgetEnvelope{
				Name:        "a",
				Flavor:      "H100",
				Selector:    map[string]string{"region": "us"},
				Concurrency: 10,
				Start:       &now,
				End:         &later,
				MaxGPUHours: ptrInt64(10),
			},
		},
		{
			name: "invalid hours",
			env: BudgetEnvelope{
				Name:        "a",
				Flavor:      "H100",
				Selector:    map[string]string{"region": "us"},
				Concurrency: 1,
				Start:       &now,
				End:         &later,
				MaxGPUHours: ptrInt64(10000),
			},
			expectsErr: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.env.Validate()
			if tc.expectsErr && err == nil {
				t.Fatalf("expected error")
			}
			if !tc.expectsErr && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
		})
	}
}

func ptrInt64(v int64) *int64 {
	return &v
}
