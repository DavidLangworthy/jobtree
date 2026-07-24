package controllers

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

func TestReconcileBudgetComputesHeadroomAndMetrics(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	start := v1.NewTime(now.Add(-2 * time.Hour))
	budgetObj := &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "budget-a", Namespace: keys.DefaultNamespace},
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

	// Both leases are backed by owner runs so they class owned; the closed
	// one accrued its funded hours before it ended (3*2h + 2*1h = 8), and
	// only the still-open one counts against concurrency at now.
	runs := map[string]*v1.Run{
		keys.NamespacedKey(keys.DefaultNamespace, "run-active"): ownerRun("run-active", "org:a"),
		keys.NamespacedKey(keys.DefaultNamespace, "run-closed"): ownerRun("run-closed", "org:a"),
	}
	leases := []v1.GPULease{{
		Spec: v1.GPULeaseSpec{
			Owner:                 "org:a",
			RunRef:                v1.RunReference{Name: "run-active", Namespace: keys.DefaultNamespace},
			PaidByBudget:          "budget-a",
			PaidByBudgetNamespace: keys.DefaultNamespace,
			PaidByEnvelope:        "env-a",
			Slice:                 v1.GPULeaseSlice{Nodes: []string{"n1", "n2", "n3"}, Role: "Active"},
			Interval:              v1.GPULeaseInterval{Start: start},
		},
	}, {
		Spec: v1.GPULeaseSpec{
			Owner:                 "org:a",
			RunRef:                v1.RunReference{Name: "run-closed", Namespace: keys.DefaultNamespace},
			PaidByBudget:          "budget-a",
			PaidByBudgetNamespace: keys.DefaultNamespace,
			PaidByEnvelope:        "env-a",
			Slice:                 v1.GPULeaseSlice{Nodes: []string{"n4", "n5"}, Role: "Active"},
			Interval:              v1.GPULeaseInterval{Start: start, End: &v1.Time{Time: now.Add(-time.Hour)}},
		},
		Status: v1.GPULeaseStatus{Closed: true, Ended: &v1.Time{Time: now.Add(-time.Hour)}},
	}}

	metrics := NewBudgetMetrics()
	controller := NewBudgetController(fakeClock{now: now}, metrics)

	ev := funding.Evaluate(funding.Input{Budgets: []v1.Budget{*budgetObj}, Leases: leases, Runs: runs, Now: now})
	status := controller.ReconcileBudget(budgetObj, ev)
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

	// The status usage block reports the derived classes: 3 owned, no
	// sharing/borrowing/unfunded.
	if len(status.Usage) != 1 || status.Usage[0].OwnedGPUs != 3 {
		t.Fatalf("expected 3 owned in usage block, got %+v", status.Usage)
	}

	key := metricKey{Owner: "org:a", Budget: "budget-a", Envelope: "env-a", Flavor: "H100"}
	metricsSnapshot := metrics.Snapshot()
	usage, ok := metricsSnapshot[key]
	if !ok {
		t.Fatalf("expected metrics entry for %v", key)
	}
	if usage.Owned != 3 {
		t.Fatalf("expected owned metric 3, got %f", usage.Owned)
	}
	expectedHours := float64(3*2 + 2*1)
	if mathAbs(usage.ConsumedGPUHours-expectedHours) > 1e-6 {
		t.Fatalf("expected consumed gpu hours metric %.f, got %f", expectedHours, usage.ConsumedGPUHours)
	}
}

// ownerRun is a minimal run used to back occupancy leases so they class
// owned instead of orphan-unfunded.
func ownerRun(name, owner string) *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: keys.DefaultNamespace},
		Spec:       v1.RunSpec{Resources: v1.RunResources{GPUType: "H100"}},
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

// TestReconcileBudgetPendingRenewals proves audit finding #22 fixed:
// spec.autoRenew is read by the budget controller and changes the output
// (PendingRenewals) — it is not merely accepted and ignored. The window's
// End is unaffected either way (renewal reports due, it does not itself
// rotate the window).
func TestReconcileBudgetPendingRenewals(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	closingSoon := v1.NewTime(now.Add(2 * time.Hour))
	farOut := v1.NewTime(now.Add(30 * 24 * time.Hour))

	baseSpec := v1.BudgetSpec{
		Owner: "org:a",
		Envelopes: []v1.BudgetEnvelope{
			{Name: "closing-soon", Flavor: "H100", Selector: map[string]string{"region": "us-west"}, Concurrency: 4, End: &closingSoon},
			{Name: "far-out", Flavor: "H100", Selector: map[string]string{"region": "us-west"}, Concurrency: 4, End: &farOut},
		},
	}

	t.Run("no AutoRenew yields no pending renewals", func(t *testing.T) {
		budgetObj := &v1.Budget{ObjectMeta: v1.ObjectMeta{Name: "budget-no-renew"}, Spec: baseSpec}
		bc := NewBudgetController(fakeClock{now: now}, NewBudgetMetrics())
		ev := funding.Evaluate(funding.Input{Budgets: []v1.Budget{*budgetObj}, Now: now})
		status := bc.ReconcileBudget(budgetObj, ev)
		if len(status.PendingRenewals) != 0 {
			t.Fatalf("expected no pending renewals without spec.autoRenew, got %+v", status.PendingRenewals)
		}
	})

	t.Run("AutoRenew flags only the envelope closing within notifyBefore", func(t *testing.T) {
		specWithRenew := baseSpec
		specWithRenew.AutoRenew = &v1.AutoRenewSchedule{
			Period:       metav1.Duration{Duration: 30 * 24 * time.Hour},
			NotifyBefore: metav1.Duration{Duration: 4 * time.Hour},
		}
		budgetObj := &v1.Budget{ObjectMeta: v1.ObjectMeta{Name: "budget-with-renew"}, Spec: specWithRenew}
		bc := NewBudgetController(fakeClock{now: now}, NewBudgetMetrics())
		ev := funding.Evaluate(funding.Input{Budgets: []v1.Budget{*budgetObj}, Now: now})
		status := bc.ReconcileBudget(budgetObj, ev)
		if len(status.PendingRenewals) != 1 {
			t.Fatalf("expected exactly 1 pending renewal, got %+v", status.PendingRenewals)
		}
		if status.PendingRenewals[0].Name != "closing-soon" {
			t.Fatalf("expected closing-soon to be pending renewal, got %+v", status.PendingRenewals[0])
		}
	})
}
