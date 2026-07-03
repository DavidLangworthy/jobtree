package budget

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// R10: a lease is attributed to the (budget, envelope) pair that funded it.
// Before PaidByBudget existed, a same-named envelope in another budget of
// the same owner double-counted the lease.
func TestBuildBudgetStateScopesLeasesToPayingBudget(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	envelope := v1.BudgetEnvelope{
		Name:        "west",
		Flavor:      "H100-80GB",
		Selector:    map[string]string{"region": "us-west"},
		Concurrency: 16,
	}
	budgetA := &v1.Budget{ObjectMeta: v1.ObjectMeta{Name: "budget-a"}, Spec: v1.BudgetSpec{Owner: "org:ai", Envelopes: []v1.BudgetEnvelope{envelope}}}
	budgetB := &v1.Budget{ObjectMeta: v1.ObjectMeta{Name: "budget-b"}, Spec: v1.BudgetSpec{Owner: "org:ai", Envelopes: []v1.BudgetEnvelope{envelope}}}

	lease := v1.Lease{
		ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "lease-1"},
		Spec: v1.LeaseSpec{
			Owner:          "org:ai",
			RunRef:         v1.RunReference{Namespace: "default", Name: "train"},
			Slice:          v1.LeaseSlice{Nodes: []string{"node-a#0", "node-a#1"}, Role: "Active"},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(now.Add(-time.Hour))},
			PaidByBudget:   "budget-a",
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}
	leases := []v1.Lease{lease}

	stateA := BuildBudgetState(budgetA, leases, now)
	stateB := BuildBudgetState(budgetB, leases, now)

	if got := stateA.Envelopes["west"].Usage.Concurrency; got != 2 {
		t.Errorf("paying budget should count the lease: got concurrency %d, want 2", got)
	}
	if got := stateB.Envelopes["west"].Usage.Concurrency; got != 0 {
		t.Errorf("non-paying budget double-counted the lease: got concurrency %d, want 0", got)
	}
}

// Leases persisted before PaidByBudget existed keep the owner+envelope
// attribution so existing state files stay accounted.
func TestBuildBudgetStateLegacyLeaseFallback(t *testing.T) {
	now := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	budget := &v1.Budget{ObjectMeta: v1.ObjectMeta{Name: "budget-a"}, Spec: v1.BudgetSpec{Owner: "org:ai", Envelopes: []v1.BudgetEnvelope{{
		Name:        "west",
		Flavor:      "H100-80GB",
		Selector:    map[string]string{"region": "us-west"},
		Concurrency: 16,
	}}}}

	legacy := v1.Lease{
		ObjectMeta: v1.ObjectMeta{Namespace: "default", Name: "lease-legacy"},
		Spec: v1.LeaseSpec{
			Owner:          "org:ai",
			RunRef:         v1.RunReference{Namespace: "default", Name: "train"},
			Slice:          v1.LeaseSlice{Nodes: []string{"node-a#0"}, Role: "Active"},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(now.Add(-time.Hour))},
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}

	state := BuildBudgetState(budget, []v1.Lease{legacy}, now)
	if got := state.Envelopes["west"].Usage.Concurrency; got != 1 {
		t.Errorf("legacy lease without PaidByBudget should still count: got %d, want 1", got)
	}
}
