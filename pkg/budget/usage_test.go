package budget

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

func TestBuildBudgetState(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	start := v1.NewTime(now.Add(-2 * time.Hour))
	end := v1.NewTime(now.Add(-time.Hour))

	budget := &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "budget-a"},
		Spec: v1.BudgetSpec{
			Owner: "org:a",
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "env-a",
				Flavor:      "H100",
				Selector:    map[string]string{"region": "us-west"},
				Concurrency: 10,
				MaxGPUHours: ptrInt64(100),
			}},
			AggregateCaps: []v1.AggregateCap{{
				Name:           "agg",
				Flavor:         "H100",
				Envelopes:      []string{"env-a"},
				MaxConcurrency: ptrInt32(8),
				MaxGPUHours:    ptrInt64(80),
			}},
		},
	}

	leases := []v1.Lease{{
		Spec: v1.LeaseSpec{
			Owner:          "org:a",
			PaidByEnvelope: "env-a",
			Slice:          v1.LeaseSlice{Nodes: []string{"n1", "n2", "n3"}},
			Interval: v1.LeaseInterval{
				Start: start,
			},
		},
	}, {
		Spec: v1.LeaseSpec{
			Owner:          "org:a",
			PaidByEnvelope: "env-a",
			Slice:          v1.LeaseSlice{Nodes: []string{"n4", "n5"}},
			Interval: v1.LeaseInterval{
				Start: start,
				End:   &end,
			},
		},
		Status: v1.LeaseStatus{Closed: true, Ended: &end},
	}, {
		Spec: v1.LeaseSpec{
			Owner:          "org:a",
			PaidByEnvelope: "other",
			Slice:          v1.LeaseSlice{Nodes: []string{"n6"}},
			Interval:       v1.LeaseInterval{Start: start},
		},
	}, {
		Spec: v1.LeaseSpec{
			Owner:          "org:b",
			PaidByEnvelope: "env-a",
			Slice:          v1.LeaseSlice{Nodes: []string{"n7"}},
			Interval:       v1.LeaseInterval{Start: start},
		},
	}, {
		Spec: v1.LeaseSpec{
			Owner:          "org:a",
			PaidByEnvelope: "env-a",
			Slice:          v1.LeaseSlice{Nodes: []string{"n8"}, Role: "Spare"},
			Interval:       v1.LeaseInterval{Start: start},
		},
	}}

	state := BuildBudgetState(budget, leases, now)
	if len(state.Envelopes) != 1 {
		t.Fatalf("expected 1 envelope, got %d", len(state.Envelopes))
	}
	env := state.Envelopes["env-a"]
	if env.Usage.Concurrency != 4 {
		t.Fatalf("expected concurrency 4, got %d", env.Usage.Concurrency)
	}
	expectedHours := float64(3*2 + 2*1 + 1*2)
	if diff := env.Usage.GPUHours - expectedHours; diff > 1e-6 || diff < -1e-6 {
		t.Fatalf("expected gpu hours %.2f, got %.2f", expectedHours, env.Usage.GPUHours)
	}
	if env.Usage.SpareConcurrency != 1 {
		t.Fatalf("expected spare concurrency 1, got %d", env.Usage.SpareConcurrency)
	}
	if env.Usage.SpareGPUHours <= 0 {
		t.Fatalf("expected spare gpu hours to be positive")
	}
	agg := state.Aggregates["agg"]
	if agg.Usage.Concurrency != env.Usage.Concurrency {
		t.Fatalf("aggregate concurrency mismatch")
	}
	if agg.Usage.GPUHours != env.Usage.GPUHours {
		t.Fatalf("aggregate gpu hours mismatch")
	}
}

func ptrInt32(v int32) *int32 { return &v }
func ptrInt64(v int64) *int64 { return &v }

func TestBuildBudgetStateCountsBorrowedUsage(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)

	budget := &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "lender"},
		Spec: v1.BudgetSpec{
			Owner: "org:lender",
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "west-h100",
				Flavor:      "H100-80GB",
				Selector:    map[string]string{"region": "us-west"},
				Concurrency: 10,
			}},
		},
	}

	start := v1.NewTime(now.Add(-30 * time.Minute))
	leases := []v1.Lease{{
		Spec: v1.LeaseSpec{
			Owner:          "org:lender",
			PaidByEnvelope: "west-h100",
			Slice: v1.LeaseSlice{
				Nodes: []string{"n1", "n2", "n3", "n4"},
				Role:  "Borrowed",
			},
			Interval: v1.LeaseInterval{Start: start},
		},
	}}

	state := BuildBudgetState(budget, leases, now)
	env := state.Envelopes["west-h100"]
	if env.Usage.Concurrency != 4 {
		t.Fatalf("expected concurrency 4, got %d", env.Usage.Concurrency)
	}
	if env.Usage.BorrowedConcurrency != 4 {
		t.Fatalf("expected borrowed concurrency 4, got %d", env.Usage.BorrowedConcurrency)
	}
	if env.Usage.BorrowedGPUHours <= 0 {
		t.Fatalf("expected borrowed gpu hours to be positive")
	}
	if env.Usage.SpareConcurrency != 0 {
		t.Fatalf("expected no spare concurrency for borrowed lease")
	}
}
