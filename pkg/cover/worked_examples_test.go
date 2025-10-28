package cover

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/budget"
)

func TestWorkedExampleSingleDomainReservation(t *testing.T) {
	now := time.Date(2025, 11, 1, 10, 0, 0, 0, time.UTC)

	teamA := budgetState("budget-team-a", "org:team-a", []string{"org:root"}, []envSpec{{
		name:        "west-h100",
		flavor:      "H100-80GB",
		concurrency: 48,
		selector: map[string]string{
			"region":  "us-west",
			"cluster": "gpu-a",
		},
	}})
	teamB := budgetState("budget-team-b", "org:team-b", []string{"org:root"}, []envSpec{{
		name:        "west-h100",
		flavor:      "H100-80GB",
		concurrency: 24,
		selector: map[string]string{
			"region":  "us-west",
			"cluster": "gpu-a",
		},
	}})

	setEnvelopeUsage(teamA, "west-h100", budget.Usage{Concurrency: 34})
	setEnvelopeUsage(teamB, "west-h100", budget.Usage{Concurrency: 24})

	inv := NewInventory([]*budget.BudgetState{teamA, teamB})

	_, err := inv.Plan(Request{
		Owner:    "org:team-a",
		Flavor:   "H100-80GB",
		Quantity: 32,
		Location: map[string]string{
			"region":  "us-west",
			"cluster": "gpu-a",
		},
		Now: now,
	})
	if err == nil {
		t.Fatalf("expected planning to fail due to insufficient capacity")
	}
	perr, ok := err.(*PlanError)
	if !ok || perr.Reason != FailureReasonInsufficientCapacity {
		t.Fatalf("expected insufficient capacity error, got %v", err)
	}
}

func TestWorkedExampleFamilySharingDeficit(t *testing.T) {
	now := time.Date(2025, 11, 1, 10, 0, 0, 0, time.UTC)

	// Root org budget (location B headroom)
	root := budgetState("budget-root", "org:ai", nil, []envSpec{{
		name:        "west-b",
		flavor:      "H100-80GB",
		concurrency: 16,
		selector: map[string]string{
			"region":        "us-west",
			"fabric.domain": "B",
		},
	}})

	rai := budgetState("budget-rai", "org:ai:rai", []string{"org:ai"}, []envSpec{{
		name:        "west-a",
		flavor:      "H100-80GB",
		concurrency: 64,
		selector: map[string]string{
			"region":        "us-west",
			"fabric.domain": "A",
		},
	}})
	raiAL := budgetState("budget-rai-al", "org:ai:rai:al", []string{"org:ai:rai"}, []envSpec{{
		name:        "west-a",
		flavor:      "H100-80GB",
		concurrency: 48,
		selector: map[string]string{
			"region":        "us-west",
			"fabric.domain": "A",
		},
	}})
	raiSYS := budgetState("budget-rai-sys", "org:ai:rai:sys", []string{"org:ai:rai"}, []envSpec{{
		name:        "west-b",
		flavor:      "H100-80GB",
		concurrency: 16,
		selector: map[string]string{
			"region":        "us-west",
			"fabric.domain": "B",
		},
	}})

	mm := budgetState("budget-mm", "org:ai:mm", []string{"org:ai"}, []envSpec{{
		name:        "west-b",
		flavor:      "H100-80GB",
		concurrency: 40,
		selector: map[string]string{
			"region":        "us-west",
			"fabric.domain": "B",
		},
	}})
	mmVis := budgetState("budget-mm-vis", "org:ai:mm:vision", []string{"org:ai:mm"}, []envSpec{{
		name:        "west-b",
		flavor:      "H100-80GB",
		concurrency: 28,
		selector: map[string]string{
			"region":        "us-west",
			"fabric.domain": "B",
		},
	}})
	mmAud := budgetState("budget-mm-aud", "org:ai:mm:audio", []string{"org:ai:mm"}, []envSpec{{
		name:        "west-b",
		flavor:      "H100-80GB",
		concurrency: 12,
		selector: map[string]string{
			"region":        "us-west",
			"fabric.domain": "B",
		},
	}})

	// Existing consumption at T1 of the worked example
	setEnvelopeUsage(raiAL, "west-a", budget.Usage{Concurrency: 64})
	setEnvelopeUsage(raiSYS, "west-b", budget.Usage{Concurrency: 16})
	setEnvelopeUsage(mmVis, "west-b", budget.Usage{Concurrency: 28})
	setEnvelopeUsage(mm, "west-b", budget.Usage{Concurrency: 20})
	setEnvelopeUsage(mmAud, "west-b", budget.Usage{Concurrency: 4})

	inv := NewInventory([]*budget.BudgetState{root, rai, raiAL, raiSYS, mm, mmVis, mmAud})

	_, err := inv.Plan(Request{
		Owner:    "org:ai:mm:vision",
		Flavor:   "H100-80GB",
		Quantity: 64,
		Location: map[string]string{
			"region":        "us-west",
			"fabric.domain": "B",
		},
		Now: now,
	})
	if err == nil {
		t.Fatalf("expected plan failure due to deficit")
	}
	perr, ok := err.(*PlanError)
	if !ok || perr.Reason != FailureReasonInsufficientCapacity {
		t.Fatalf("expected insufficient capacity error, got %v", err)
	}
}

func TestWorkedExampleCoFundedRun(t *testing.T) {
	now := time.Date(2025, 11, 1, 10, 0, 0, 0, time.UTC)

	rai := budgetState("budget-rai", "org:ai:rai", nil, []envSpec{{
		name:        "west-h100",
		flavor:      "H100-80GB",
		concurrency: 96,
		selector: map[string]string{
			"region":  "us-west",
			"cluster": "gpu-a",
		},
	}})

	mmVision := budgetState("budget-mm-vis", "org:ai:mm:vision", nil, []envSpec{{
		name:        "west-h100",
		flavor:      "H100-80GB",
		concurrency: 64,
		selector: map[string]string{
			"region":  "us-west",
			"cluster": "gpu-a",
		},
		lending: &v1.LendingPolicy{
			Allow:          true,
			To:             []string{"org:ai:rai"},
			MaxConcurrency: ptrInt32(32),
		},
	}})

	inv := NewInventory([]*budget.BudgetState{rai, mmVision})

	plan, err := inv.Plan(Request{
		Owner:            "org:ai:rai",
		Flavor:           "H100-80GB",
		Quantity:         128,
		Location:         map[string]string{"region": "us-west", "cluster": "gpu-a"},
		Now:              now,
		AllowBorrow:      true,
		Sponsors:         []string{"org:ai:mm:vision"},
		ExpectedDuration: 2 * time.Hour,
	})
	if err != nil {
		t.Fatalf("unexpected plan error: %v", err)
	}

	if len(plan.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(plan.Segments))
	}

	var raiQty, mmQty int32
	for _, seg := range plan.Segments {
		if seg.Owner == "org:ai:rai" {
			raiQty += seg.Quantity
		}
		if seg.Owner == "org:ai:mm:vision" {
			if !seg.Borrowed {
				t.Fatalf("expected sponsor allocation to be marked borrowed: %+v", seg)
			}
			mmQty += seg.Quantity
		}
	}

	if raiQty != 96 || mmQty != 32 {
		t.Fatalf("expected 96/32 split, got %d/%d", raiQty, mmQty)
	}
}

func TestWorkedExampleFutureDatedBudget(t *testing.T) {
	now := time.Date(2025, 11, 1, 10, 0, 0, 0, time.UTC)
	tomorrow := now.Add(24 * time.Hour)

	future := budgetState("budget-future", "org:ai:rai", nil, []envSpec{{
		name:        "west-h100",
		flavor:      "H100-80GB",
		concurrency: 5000,
		selector: map[string]string{
			"region":  "us-west",
			"cluster": "gpu-a",
		},
	}})
	future.Budget.Spec.Envelopes[0].Start = &v1.Time{Time: tomorrow}
	future.Envelopes["west-h100"].Spec.Start = &v1.Time{Time: tomorrow}

	inv := NewInventory([]*budget.BudgetState{future})

	_, err := inv.Plan(Request{
		Owner:    "org:ai:rai",
		Flavor:   "H100-80GB",
		Quantity: 4096,
		Location: map[string]string{"region": "us-west", "cluster": "gpu-a"},
		Now:      now,
	})
	if err == nil {
		t.Fatalf("expected failure because window has not opened")
	}
	perr, ok := err.(*PlanError)
	if !ok || perr.Reason != FailureReasonNoMatchingEnvelope {
		t.Fatalf("expected no matching envelope error, got %v", err)
	}
}

func setEnvelopeUsage(state *budget.BudgetState, envelope string, usage budget.Usage) {
	env, ok := state.Envelopes[envelope]
	if !ok {
		panic("envelope not found")
	}
	env.Usage = usage
}
