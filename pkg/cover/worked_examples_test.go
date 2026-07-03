package cover

import (
	"fmt"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// occupancy is a funded claim already consuming an envelope: it reduces the
// excess available to family. In the derived model there is no "set usage"
// knob — occupancy is leases backed by runs, exactly like real consumption.
type occupancy struct {
	owner    string
	budget   string
	envelope string
	gpus     int
}

// evalWithOccupancy derives the evaluation the Inventory plans against, with
// the given envelopes pre-consumed by live leases. Each occupancy is owned
// by its stated owner (so it classes owned against its own envelope and is
// senior to any new admission), so it subtracts from that envelope's excess.
func evalWithOccupancy(now time.Time, flavor string, budgets []v1.Budget, occ []occupancy) *funding.Evaluation {
	runs := map[string]*v1.Run{}
	var leases []v1.Lease
	for i, o := range occ {
		runName := fmt.Sprintf("occ-%d", i)
		runKey := keys.NamespacedKey(keys.DefaultNamespace, runName)
		runs[runKey] = &v1.Run{
			ObjectMeta: v1.ObjectMeta{
				Name:              runName,
				Namespace:         keys.DefaultNamespace,
				CreationTimestamp: v1.NewTime(now.Add(-time.Hour)),
			},
			Spec: v1.RunSpec{
				Owner:     o.owner,
				Resources: v1.RunResources{GPUType: flavor, TotalGPUs: int32(o.gpus)},
			},
		}
		nodes := make([]string, o.gpus)
		for j := range nodes {
			nodes[j] = fmt.Sprintf("occ-%d-node#%d", i, j)
		}
		leases = append(leases, v1.Lease{
			ObjectMeta: v1.ObjectMeta{Name: fmt.Sprintf("occ-lease-%d", i), Namespace: keys.DefaultNamespace},
			Spec: v1.LeaseSpec{
				Owner:          o.owner,
				RunRef:         v1.RunReference{Name: runName, Namespace: keys.DefaultNamespace},
				Slice:          v1.LeaseSlice{Nodes: nodes, Role: "Active"},
				Interval:       v1.LeaseInterval{Start: v1.NewTime(now.Add(-time.Hour))},
				PaidByBudget:   o.budget,
				PaidByEnvelope: o.envelope,
			},
		})
	}
	return funding.Evaluate(funding.Input{
		Budgets: budgets,
		Leases:  leases,
		Runs:    runs,
		Now:     now,
	})
}

func TestWorkedExampleSingleDomainReservation(t *testing.T) {
	now := time.Date(2025, 11, 1, 10, 0, 0, 0, time.UTC)
	loc := map[string]string{"region": "us-west", "cluster": "gpu-a"}
	budgets := []v1.Budget{
		budgetOf("budget-team-a", "org:team-a", []string{"org:root"}, envSpec{name: "west-h100", flavor: "H100-80GB", concurrency: 48, selector: loc}),
		budgetOf("budget-team-b", "org:team-b", []string{"org:root"}, envSpec{name: "west-h100", flavor: "H100-80GB", concurrency: 24, selector: loc}),
	}
	// team-a has 14 of its own 48 free; its sibling team-b is fully booked.
	inv := NewInventory(evalWithOccupancy(now, "H100-80GB", budgets, []occupancy{
		{owner: "org:team-a", budget: "budget-team-a", envelope: "west-h100", gpus: 34},
		{owner: "org:team-b", budget: "budget-team-b", envelope: "west-h100", gpus: 24},
	}))

	_, err := inv.Plan(Request{
		Owner:    "org:team-a",
		Flavor:   "H100-80GB",
		Quantity: 32,
		Location: loc,
		Now:      now,
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
	domB := map[string]string{"region": "us-west", "fabric.domain": "B"}
	domA := map[string]string{"region": "us-west", "fabric.domain": "A"}
	budgets := []v1.Budget{
		budgetOf("budget-root", "org:ai", nil, envSpec{name: "west-b", flavor: "H100-80GB", concurrency: 16, selector: domB}),
		budgetOf("budget-rai", "org:ai:rai", []string{"org:ai"}, envSpec{name: "west-a", flavor: "H100-80GB", concurrency: 64, selector: domA}),
		budgetOf("budget-rai-al", "org:ai:rai:al", []string{"org:ai:rai"}, envSpec{name: "west-a", flavor: "H100-80GB", concurrency: 48, selector: domA}),
		budgetOf("budget-rai-sys", "org:ai:rai:sys", []string{"org:ai:rai"}, envSpec{name: "west-b", flavor: "H100-80GB", concurrency: 16, selector: domB}),
		budgetOf("budget-mm", "org:ai:mm", []string{"org:ai"}, envSpec{name: "west-b", flavor: "H100-80GB", concurrency: 40, selector: domB}),
		budgetOf("budget-mm-vis", "org:ai:mm:vision", []string{"org:ai:mm"}, envSpec{name: "west-b", flavor: "H100-80GB", concurrency: 28, selector: domB}),
		budgetOf("budget-mm-aud", "org:ai:mm:audio", []string{"org:ai:mm"}, envSpec{name: "west-b", flavor: "H100-80GB", concurrency: 12, selector: domB}),
	}
	// Consumption at T1 of the worked example. vision (the requester) can
	// reach own (full), parent mm (20 free of 40), sibling audio (8 free of
	// 12), cousin rai:sys (full). The grandparent root is out of proximity
	// range, so ~28 of the requested 64 can be funded — a deficit.
	inv := NewInventory(evalWithOccupancy(now, "H100-80GB", budgets, []occupancy{
		{owner: "org:ai:rai:al", budget: "budget-rai-al", envelope: "west-a", gpus: 48},
		{owner: "org:ai:rai:sys", budget: "budget-rai-sys", envelope: "west-b", gpus: 16},
		{owner: "org:ai:mm:vision", budget: "budget-mm-vis", envelope: "west-b", gpus: 28},
		{owner: "org:ai:mm", budget: "budget-mm", envelope: "west-b", gpus: 20},
		{owner: "org:ai:mm:audio", budget: "budget-mm-aud", envelope: "west-b", gpus: 4},
	}))

	_, err := inv.Plan(Request{
		Owner:    "org:ai:mm:vision",
		Flavor:   "H100-80GB",
		Quantity: 64,
		Location: domB,
		Now:      now,
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
	loc := map[string]string{"region": "us-west", "cluster": "gpu-a"}
	// rai and mm:vision are strangers (no family edge): rai reaches vision's
	// envelope only as a sponsor, under the lending contract (max 32).
	inv := NewInventory(evalOf(now,
		budgetOf("budget-rai", "org:ai:rai", nil, envSpec{name: "west-h100", flavor: "H100-80GB", concurrency: 96, selector: loc}),
		budgetOf("budget-mm-vis", "org:ai:mm:vision", nil, envSpec{
			name:        "west-h100",
			flavor:      "H100-80GB",
			concurrency: 64,
			selector:    loc,
			lending: &v1.LendingPolicy{
				Allow:          true,
				To:             []string{"org:ai:rai"},
				MaxConcurrency: ptrInt32(32),
			},
		}),
	))

	plan, err := inv.Plan(Request{
		Owner:       "org:ai:rai",
		Flavor:      "H100-80GB",
		Quantity:    128,
		Location:    loc,
		Now:         now,
		AllowBorrow: true,
		Sponsors:    []string{"org:ai:mm:vision"},
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
	loc := map[string]string{"region": "us-west", "cluster": "gpu-a"}

	inv := NewInventory(evalOf(now,
		budgetOf("budget-future", "org:ai:rai", nil, envSpec{
			name:        "west-h100",
			flavor:      "H100-80GB",
			concurrency: 5000,
			selector:    loc,
			start:       &tomorrow,
		}),
	))

	_, err := inv.Plan(Request{
		Owner:    "org:ai:rai",
		Flavor:   "H100-80GB",
		Quantity: 4096,
		Location: loc,
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
