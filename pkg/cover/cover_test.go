package cover

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/budget"
)

func TestPlanPrefersFamilySameLocation(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	budgetA := budgetState("budget-a", "org:child", []string{"org:parent"}, []envSpec{{name: "env-a", flavor: "H100", concurrency: 2, selector: map[string]string{"region": "us-west"}}})
	budgetB := budgetState("budget-b", "org:sibling", []string{"org:parent"}, []envSpec{{name: "env-b", flavor: "H100", concurrency: 10, selector: map[string]string{"region": "us-west"}}})
	inv := NewInventory([]*budget.BudgetState{budgetA, budgetB})

	plan, err := inv.Plan(Request{
		Owner:    "org:child",
		Flavor:   "H100",
		Quantity: 4,
		Location: map[string]string{"region": "us-west"},
		Now:      now,
	})
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if len(plan.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(plan.Segments))
	}
	if plan.Segments[0].Owner != "org:child" || plan.Segments[0].Quantity != 2 {
		t.Fatalf("unexpected first segment %+v", plan.Segments[0])
	}
	if plan.Segments[1].Owner != "org:sibling" || plan.Segments[1].Quantity != 2 {
		t.Fatalf("unexpected second segment %+v", plan.Segments[1])
	}
}

func TestPlanUsesOtherLocationAfterSame(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	budgetA := budgetState("budget-a", "org:child", nil, []envSpec{{name: "env-a", flavor: "H100", concurrency: 2, selector: map[string]string{"region": "us-east"}}})
	budgetB := budgetState("budget-b", "org:child", nil, []envSpec{{name: "env-b", flavor: "H100", concurrency: 6, selector: map[string]string{"region": "us-west"}}})
	inv := NewInventory([]*budget.BudgetState{budgetA, budgetB})

	plan, err := inv.Plan(Request{
		Owner:    "org:child",
		Flavor:   "H100",
		Quantity: 4,
		Location: map[string]string{"region": "us-west"},
		Now:      now,
	})
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if len(plan.Segments) != 1 || plan.Segments[0].EnvelopeName != "env-b" || plan.Segments[0].Quantity != 4 {
		t.Fatalf("expected allocation from env-b, got %+v", plan.Segments)
	}
}

func TestPlanRespectsAggregateCap(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	budgetA := budgetState("budget-a", "org:child", nil, []envSpec{{
		name:        "env-a",
		flavor:      "H100",
		concurrency: 10,
		selector:    map[string]string{"region": "us-west"},
		maxGPUHours: ptrInt64(100),
	}})
	budgetA.Aggregates["cap"] = &budget.AggregateState{
		Spec: v1.AggregateCap{
			Name:           "cap",
			Flavor:         "H100",
			Envelopes:      []string{"env-a"},
			MaxConcurrency: ptrInt32(3),
			MaxGPUHours:    ptrInt64(30),
		},
	}
	budgetA.Envelopes["env-a"].Aggregates = []*budget.AggregateState{budgetA.Aggregates["cap"]}
	inv := NewInventory([]*budget.BudgetState{budgetA})

	_, err := inv.Plan(Request{
		Owner:            "org:child",
		Flavor:           "H100",
		Quantity:         5,
		Location:         map[string]string{"region": "us-west"},
		Now:              now,
		ExpectedDuration: 10 * time.Hour,
	})
	if err == nil {
		t.Fatalf("expected failure due to aggregate cap")
	}
}

func TestPlanBorrowingHonorsACL(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	budgetA := budgetState("budget-a", "org:lender", nil, []envSpec{{
		name:        "env-a",
		flavor:      "H100",
		concurrency: 8,
		selector:    map[string]string{"region": "us-west"},
		lending: &v1.LendingPolicy{
			Allow:          true,
			To:             []string{"org:child"},
			MaxConcurrency: ptrInt32(4),
		},
	}})
	budgetB := budgetState("budget-b", "org:child", nil, []envSpec{{name: "env-b", flavor: "H100", concurrency: 2, selector: map[string]string{"region": "us-west"}}})
	inv := NewInventory([]*budget.BudgetState{budgetA, budgetB})

	plan, err := inv.Plan(Request{
		Owner:            "org:child",
		Flavor:           "H100",
		Quantity:         6,
		Location:         map[string]string{"region": "us-west"},
		Now:              now,
		AllowBorrow:      true,
		Sponsors:         []string{"org:lender"},
		ExpectedDuration: time.Hour,
	})
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if len(plan.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(plan.Segments))
	}
	if !plan.Segments[1].Borrowed || plan.Segments[1].Quantity != 4 {
		t.Fatalf("expected borrowed segment of 4, got %+v", plan.Segments[1])
	}
}

func TestPlanBorrowingDeniedByACL(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	budgetA := budgetState("budget-a", "org:lender", nil, []envSpec{{
		name:        "env-a",
		flavor:      "H100",
		concurrency: 8,
		selector:    map[string]string{"region": "us-west"},
		lending: &v1.LendingPolicy{
			Allow: true,
			To:    []string{"org:someone-else"},
		},
	}})
	budgetB := budgetState("budget-b", "org:child", nil, []envSpec{{name: "env-b", flavor: "H100", concurrency: 2, selector: map[string]string{"region": "us-west"}}})
	inv := NewInventory([]*budget.BudgetState{budgetA, budgetB})

	_, err := inv.Plan(Request{
		Owner:       "org:child",
		Flavor:      "H100",
		Quantity:    4,
		Location:    map[string]string{"region": "us-west"},
		Now:         now,
		AllowBorrow: true,
		Sponsors:    []string{"org:lender"},
	})
	if err == nil {
		t.Fatalf("expected borrowing failure")
	}
	if pe, ok := err.(*PlanError); !ok || pe.Reason != FailureReasonInsufficientCapacity {
		t.Fatalf("expected insufficient capacity, got %v", err)
	}
}

type envSpec struct {
	name        string
	flavor      string
	concurrency int32
	selector    map[string]string
	maxGPUHours *int64
	lending     *v1.LendingPolicy
}

func budgetState(name, owner string, parents []string, envelopes []envSpec) *budget.BudgetState {
	specEnvelopes := make([]v1.BudgetEnvelope, len(envelopes))
	for i, env := range envelopes {
		specEnvelopes[i] = v1.BudgetEnvelope{
			Name:        env.name,
			Flavor:      env.flavor,
			Selector:    env.selector,
			Concurrency: env.concurrency,
			MaxGPUHours: env.maxGPUHours,
			Lending:     env.lending,
		}
	}
	budgetObj := &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: name},
		Spec: v1.BudgetSpec{
			Owner:     owner,
			Parents:   parents,
			Envelopes: specEnvelopes,
		},
	}
	state := budget.BuildBudgetState(budgetObj, nil, time.Now())
	return state
}

func ptrInt32(v int32) *int32 { return &v }
func ptrInt64(v int64) *int64 { return &v }
