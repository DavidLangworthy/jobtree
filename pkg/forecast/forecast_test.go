package forecast

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/budget"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/pack"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func TestPlanFromCapacityDeficit(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := &v1.Run{Spec: v1.RunSpec{
		Owner:     "org:ai:team",
		Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
	}, ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"}}

	snapshot, err := topology.BuildSnapshotForFlavor([]topology.SourceNode{{
		Name: "node-a",
		Labels: map[string]string{
			topology.LabelRegion:       "us-west",
			topology.LabelCluster:      "cluster-a",
			topology.LabelFabricDomain: "island-a",
			topology.LabelGPUFlavor:    "H100-80GB",
		},
		GPUs: 4,
	}}, nil, "H100-80GB")
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	bud := &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "team"},
		Spec: v1.BudgetSpec{
			Owner: "org:ai:team",
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "west",
				Flavor:      "H100-80GB",
				Selector:    map[string]string{topology.LabelRegion: "us-west"},
				Concurrency: 16,
			}},
		},
	}
	state := budget.BuildBudgetState(bud, nil, now)

	plan, err := Plan(Input{
		Run:          run,
		Now:          now,
		Snapshot:     snapshot,
		PackErr:      &pack.PlanError{Reason: pack.FailureReasonInsufficientCapacity},
		CoverRequest: cover.Request{Owner: run.Spec.Owner},
		BudgetStates: []*budget.BudgetState{state},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if plan.Forecast.DeficitGPUs == 0 {
		t.Fatalf("expected deficit to be non-zero")
	}
	if plan.PayingEnvelope != "west" {
		t.Fatalf("expected paying envelope west, got %s", plan.PayingEnvelope)
	}
	if len(plan.IntendedSlice.Domain) == 0 {
		t.Fatalf("expected domain metadata")
	}
	if plan.EarliestStart.Before(now) {
		t.Fatalf("earliest start should be in future")
	}
}

func TestPlanFutureWindow(t *testing.T) {
	now := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	start := v1.NewTime(now.Add(2 * time.Hour))
	run := &v1.Run{Spec: v1.RunSpec{
		Owner:     "org:ai:team",
		Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
	}, ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"}}

	plan := &pack.Plan{Groups: []pack.GroupPlacement{{
		GroupIndex:     0,
		Size:           4,
		Domain:         topology.DomainKey{Region: "us-west", Cluster: "cluster-a", Fabric: "island-a"},
		NodePlacements: []pack.NodeAllocation{{Node: "node-a", GPUs: 4}},
	}}}

	bud := &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "team"},
		Spec: v1.BudgetSpec{
			Owner: "org:ai:team",
			Envelopes: []v1.BudgetEnvelope{{
				Name:          "west",
				Flavor:        "H100-80GB",
				Selector:      map[string]string{topology.LabelRegion: "us-west"},
				Concurrency:   32,
				Start:         &start,
				PreActivation: &v1.PreActivationPolicy{AllowReservations: true, AllowAdmission: false},
			}},
		},
	}
	state := budget.BuildBudgetState(bud, nil, now)

	result, err := Plan(Input{
		Run:          run,
		Now:          now,
		PackPlan:     plan,
		CoverErr:     &cover.PlanError{Reason: cover.FailureReasonNoMatchingEnvelope},
		CoverRequest: cover.Request{Owner: run.Spec.Owner, Location: map[string]string{topology.LabelRegion: "us-west"}},
		BudgetStates: []*budget.BudgetState{state},
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	if result.PayingEnvelope != "west" {
		t.Fatalf("expected paying envelope west, got %s", result.PayingEnvelope)
	}
	if result.Forecast.Confidence != "window-aligned" {
		t.Fatalf("unexpected confidence %s", result.Forecast.Confidence)
	}
	expectedEarliest := start.Time.Add(WindowActivationOffset)
	if result.EarliestStart.Before(expectedEarliest) {
		t.Fatalf("earliest start %s before expected %s", result.EarliestStart, expectedEarliest)
	}
}
