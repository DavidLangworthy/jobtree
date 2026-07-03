package cover

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/funding"
)

func TestPlanPrefersFamilySameLocation(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	// Family excess needs no lending policy (Decision 2): the child draws on
	// its sibling's envelope automatically, in proximity order.
	inv := NewInventory(evalOf(now,
		budgetOf("budget-a", "org:child", []string{"org:parent"}, envSpec{name: "env-a", flavor: "H100", concurrency: 2, selector: map[string]string{"region": "us-west"}}),
		budgetOf("budget-b", "org:sibling", []string{"org:parent"}, envSpec{name: "env-b", flavor: "H100", concurrency: 10, selector: map[string]string{"region": "us-west"}}),
	))

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
	// Family-funded width is never Borrowed — that flag marks sponsor
	// segments only.
	if plan.Segments[1].Owner != "org:sibling" || plan.Segments[1].Quantity != 2 || plan.Segments[1].Borrowed {
		t.Fatalf("unexpected second segment %+v", plan.Segments[1])
	}
}

func TestPlanUsesOtherLocationAfterSame(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	inv := NewInventory(evalOf(now,
		budgetOf("budget-a", "org:child", nil,
			envSpec{name: "env-a", flavor: "H100", concurrency: 2, selector: map[string]string{"region": "us-east"}},
			envSpec{name: "env-b", flavor: "H100", concurrency: 6, selector: map[string]string{"region": "us-west"}}),
	))

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
	b := budgetOf("budget-a", "org:child", nil, envSpec{
		name:        "env-a",
		flavor:      "H100",
		concurrency: 10,
		selector:    map[string]string{"region": "us-west"},
		maxGPUHours: ptrInt64(100),
	})
	b.Spec.AggregateCaps = []v1.AggregateCap{{
		Name:           "cap",
		Flavor:         "H100",
		Envelopes:      []string{"env-a"},
		MaxConcurrency: ptrInt32(3),
		MaxGPUHours:    ptrInt64(30),
	}}
	inv := NewInventory(evalOf(now, b))

	// The aggregate cap bounds the fundable width below the request; the
	// aggregate is conservative (no recall through it), so only 3 fund.
	_, err := inv.Plan(Request{
		Owner:    "org:child",
		Flavor:   "H100",
		Quantity: 5,
		Location: map[string]string{"region": "us-west"},
		Now:      now,
	})
	if err == nil {
		t.Fatalf("expected failure due to aggregate cap")
	}
}

func TestPlanBorrowingHonorsACL(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	// org:lender is a stranger (no family edge): the child reaches it only
	// as a sponsor, gated by the lending policy (Decision 2).
	inv := NewInventory(evalOf(now,
		budgetOf("budget-a", "org:lender", nil, envSpec{
			name:        "env-a",
			flavor:      "H100",
			concurrency: 8,
			selector:    map[string]string{"region": "us-west"},
			lending: &v1.LendingPolicy{
				Allow:          true,
				To:             []string{"org:child"},
				MaxConcurrency: ptrInt32(4),
			},
		}),
		budgetOf("budget-b", "org:child", nil, envSpec{name: "env-b", flavor: "H100", concurrency: 2, selector: map[string]string{"region": "us-west"}}),
	))

	plan, err := inv.Plan(Request{
		Owner:       "org:child",
		Flavor:      "H100",
		Quantity:    6,
		Location:    map[string]string{"region": "us-west"},
		Now:         now,
		AllowBorrow: true,
		Sponsors:    []string{"org:lender"},
	})
	if err != nil {
		t.Fatalf("plan failed: %v", err)
	}
	if len(plan.Segments) != 2 {
		t.Fatalf("expected 2 segments, got %d", len(plan.Segments))
	}
	// Only the sponsor segment is Borrowed, capped by the lending policy's
	// MaxConcurrency=4.
	if !plan.Segments[1].Borrowed || plan.Segments[1].Quantity != 4 {
		t.Fatalf("expected borrowed segment of 4, got %+v", plan.Segments[1])
	}
}

func TestPlanBorrowingDeniedByACL(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	inv := NewInventory(evalOf(now,
		budgetOf("budget-a", "org:lender", nil, envSpec{
			name:        "env-a",
			flavor:      "H100",
			concurrency: 8,
			selector:    map[string]string{"region": "us-west"},
			lending: &v1.LendingPolicy{
				Allow: true,
				To:    []string{"org:someone-else"},
			},
		}),
		budgetOf("budget-b", "org:child", nil, envSpec{name: "env-b", flavor: "H100", concurrency: 2, selector: map[string]string{"region": "us-west"}}),
	))

	// The ACL excludes org:child, so the sponsor grants nothing and the
	// remaining width simply cannot be funded.
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

func TestPlanRespectsBorrowLimit(t *testing.T) {
	now := time.Date(2025, 11, 1, 12, 0, 0, 0, time.UTC)
	inv := NewInventory(evalOf(now,
		budgetOf("budget-owner", "org:child", nil, envSpec{
			name:        "env-owner",
			flavor:      "H100",
			concurrency: 2,
			selector:    map[string]string{"region": "us-west"},
		}),
		budgetOf("budget-sponsor", "org:lender", nil, envSpec{
			name:        "env-sponsor",
			flavor:      "H100",
			concurrency: 8,
			selector:    map[string]string{"region": "us-west"},
			lending: &v1.LendingPolicy{
				Allow:          true,
				MaxConcurrency: ptrInt32(4),
			},
		}),
	))

	limit := int32(2)
	_, err := inv.Plan(Request{
		Owner:         "org:child",
		Flavor:        "H100",
		Quantity:      6,
		Location:      map[string]string{"region": "us-west"},
		Now:           now,
		AllowBorrow:   true,
		Sponsors:      []string{"org:lender"},
		MaxBorrowGPUs: &limit,
	})
	if err == nil {
		t.Fatalf("expected failure due to borrow limit")
	}
	planErr, ok := err.(*PlanError)
	if !ok {
		t.Fatalf("expected PlanError, got %v", err)
	}
	if planErr.Reason != FailureReasonBorrowLimit {
		t.Fatalf("expected borrow limit failure, got %s", planErr.Reason)
	}
}

type envSpec struct {
	name        string
	flavor      string
	concurrency int32
	selector    map[string]string
	maxGPUHours *int64
	lending     *v1.LendingPolicy
	sharing     string
	start       *time.Time
	end         *time.Time
	preActAdmit bool
}

func budgetOf(name, owner string, parents []string, envelopes ...envSpec) v1.Budget {
	specEnvelopes := make([]v1.BudgetEnvelope, len(envelopes))
	for i, env := range envelopes {
		spec := v1.BudgetEnvelope{
			Name:        env.name,
			Flavor:      env.flavor,
			Selector:    env.selector,
			Concurrency: env.concurrency,
			MaxGPUHours: env.maxGPUHours,
			Lending:     env.lending,
			Sharing:     env.sharing,
		}
		if env.start != nil {
			t := v1.NewTime(*env.start)
			spec.Start = &t
		}
		if env.end != nil {
			t := v1.NewTime(*env.end)
			spec.End = &t
		}
		if env.preActAdmit {
			spec.PreActivation = &v1.PreActivationPolicy{AllowAdmission: true}
		}
		specEnvelopes[i] = spec
	}
	return v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: name},
		Spec: v1.BudgetSpec{
			Owner:     owner,
			Parents:   parents,
			Envelopes: specEnvelopes,
		},
	}
}

// evalOf derives the funding evaluation the Inventory plans against. No
// leases means every envelope starts empty.
func evalOf(now time.Time, budgets ...v1.Budget) *funding.Evaluation {
	return funding.Evaluate(funding.Input{
		Budgets: budgets,
		Now:     now,
	})
}

func ptrInt32(v int32) *int32 { return &v }
func ptrInt64(v int64) *int64 { return &v }
