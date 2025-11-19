package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

func TestReconcileBudgetComputesHeadroomAndMetrics(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	start := v1.NewTime(now.Add(-2 * time.Hour))
	budgetObj := &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "budget-a"},
		Spec: v1.BudgetSpec{
			Owner: "org:a",
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "env-a",
				Flavor:      "H100",
				Selector:    map[string]string{"region": "us-west"},
				Concurrency: 10,
				MaxGPUHours: ptrInt64Test(100),
			}},
			AggregateCaps: []v1.AggregateCap{{
				Name:           "cap",
				Flavor:         "H100",
				Envelopes:      []string{"env-a"},
				MaxConcurrency: ptrInt32Test(8),
				MaxGPUHours:    ptrInt64Test(90),
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
				End:   &v1.Time{Time: now.Add(-time.Hour)},
			},
		},
		Status: v1.LeaseStatus{Closed: true, Ended: &v1.Time{Time: now.Add(-time.Hour)}},
	}}

	metrics := NewBudgetMetrics()
	controller := NewBudgetController(fakeClock{now: now}, metrics)

	status := controller.ReconcileBudget(budgetObj, leases)
	if len(status.Headroom) != 1 {
		t.Fatalf("expected 1 headroom entry, got %d", len(status.Headroom))
	}
	head := status.Headroom[0]
	if head.Concurrency != 7 {
		t.Fatalf("expected concurrency headroom 7, got %d", head.Concurrency)
	}
	if head.GPUHours == nil || *head.GPUHours != 92 {
		t.Fatalf("expected gpu hours headroom 92, got %v", valueOrNil(head.GPUHours))
	}
	if len(status.AggregateHeadroom) != 1 {
		t.Fatalf("expected aggregate headroom entry")
	}
	agg := status.AggregateHeadroom[0]
	if agg.Concurrency == nil || *agg.Concurrency != 5 {
		t.Fatalf("expected aggregate concurrency headroom 5, got %v", agg.Concurrency)
	}

	key := metricKey{Owner: "org:a", Budget: "budget-a", Envelope: "env-a", Flavor: "H100"}
	metricsSnapshot := metrics.Snapshot()
	usage, ok := metricsSnapshot[key]
	if !ok {
		t.Fatalf("expected metrics entry for %v", key)
	}
	if usage.Concurrency != 3 {
		t.Fatalf("expected concurrency metric 3, got %f", usage.Concurrency)
	}
	expectedHours := float64(3*2 + 2*1)
	if mathAbs(usage.GPUHours-expectedHours) > 1e-6 {
		t.Fatalf("expected gpu hours metric %.f, got %f", expectedHours, usage.GPUHours)
	}
}

func ptrInt32Test(v int32) *int32 { return &v }
func ptrInt64Test(v int64) *int64 { return &v }

func mathAbs(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func valueOrNil(v *int64) interface{} {
	if v == nil {
		return nil
	}
	return *v
}
