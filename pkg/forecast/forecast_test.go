package forecast

import (
	"reflect"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/pack"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

const testFlavor = "H100-80GB"

func runOf(name, owner string, totalGPUs int32) *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     owner,
			Resources: v1.RunResources{GPUType: testFlavor, TotalGPUs: totalGPUs},
		},
	}
}

func trackedRunOf(name, owner string, created time.Time) *v1.Run {
	run := runOf(name, owner, 8)
	run.CreationTimestamp = v1.NewTime(created)
	return run
}

func leaseOf(name, runName, budget, envelope string, width int, start time.Time) v1.Lease {
	nodes := make([]string, width)
	for i := range nodes {
		nodes[i] = name + "-node-" + string(rune('a'+i))
	}
	return v1.Lease{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.LeaseSpec{
			RunRef:         v1.RunReference{Name: runName, Namespace: "default"},
			Slice:          v1.LeaseSlice{Nodes: nodes, Role: "Active"},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(start)},
			PaidByBudget:   budget,
			PaidByEnvelope: envelope,
			Reason:         "Start",
		},
	}
}

func runsMap(runs ...*v1.Run) map[string]*v1.Run {
	m := make(map[string]*v1.Run, len(runs))
	for _, run := range runs {
		m[keys.NamespacedKey(run.Namespace, run.Name)] = run
	}
	return m
}

func TestPlanFromCapacityDeficit(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := runOf("train", "org:ai:team", 8)

	snapshot, err := topology.BuildSnapshotForFlavor([]topology.SourceNode{{
		Name: "node-a",
		Labels: map[string]string{
			topology.LabelRegion:       "us-west",
			topology.LabelCluster:      "cluster-a",
			topology.LabelFabricDomain: "island-a",
			topology.LabelGPUFlavor:    testFlavor,
		},
		GPUs: 4,
	}}, nil, testFlavor)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}

	budgets := []v1.Budget{{
		ObjectMeta: v1.ObjectMeta{Name: "team"},
		Spec: v1.BudgetSpec{
			Owner: "org:ai:team",
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "west",
				Flavor:      testFlavor,
				Selector:    map[string]string{topology.LabelRegion: "us-west"},
				Concurrency: 16,
			}},
		},
	}}
	ev := funding.Evaluate(funding.Input{Budgets: budgets, Now: now})

	plan, err := Plan(Input{
		Run:          run,
		Now:          now,
		Snapshot:     snapshot,
		PackErr:      &pack.PlanError{Reason: pack.FailureReasonInsufficientCapacity},
		CoverRequest: cover.Request{Owner: run.Spec.Owner},
		Evaluation:   ev,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}

	// 8 requested against 4 free GPUs in scope.
	if plan.Forecast.DeficitGPUs != 4 {
		t.Fatalf("expected deficit of 4 GPUs, got %d", plan.Forecast.DeficitGPUs)
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
	// The consolidated reclaim order from quota-semantics.md: unfunded
	// capacity is the first cut, ahead of spares, shrink, and the lottery.
	wantRemedies := []string{
		"Reclaim unfunded capacity in scope",
		"Drop spares in scope",
		"Shrink elastic runs by step size",
		"Run fair lottery if deficit remains",
	}
	if !reflect.DeepEqual(plan.Forecast.Remedies, wantRemedies) {
		t.Fatalf("unexpected remedies: got %v, want %v", plan.Forecast.Remedies, wantRemedies)
	}
}

func TestPlanFutureWindow(t *testing.T) {
	now := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	start := v1.NewTime(now.Add(2 * time.Hour))
	run := runOf("train", "org:ai:team", 4)

	plan := &pack.Plan{Groups: []pack.GroupPlacement{{
		GroupIndex:     0,
		Size:           4,
		Domain:         topology.DomainKey{Region: "us-west", Cluster: "cluster-a", Fabric: "island-a"},
		NodePlacements: []pack.NodeAllocation{{Node: "node-a", GPUs: 4}},
	}}}

	budgets := []v1.Budget{{
		ObjectMeta: v1.ObjectMeta{Name: "team"},
		Spec: v1.BudgetSpec{
			Owner: "org:ai:team",
			Envelopes: []v1.BudgetEnvelope{{
				Name:          "west",
				Flavor:        testFlavor,
				Selector:      map[string]string{topology.LabelRegion: "us-west"},
				Concurrency:   32,
				Start:         &start,
				PreActivation: &v1.PreActivationPolicy{AllowReservations: true, AllowAdmission: false},
			}},
		},
	}}
	ev := funding.Evaluate(funding.Input{Budgets: budgets, Now: now})

	result, err := Plan(Input{
		Run:          run,
		Now:          now,
		PackPlan:     plan,
		CoverErr:     &cover.PlanError{Reason: cover.FailureReasonNoMatchingEnvelope},
		CoverRequest: cover.Request{Owner: run.Spec.Owner, Location: map[string]string{topology.LabelRegion: "us-west"}},
		Evaluation:   ev,
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

func TestPlanBorrowLimitReason(t *testing.T) {
	now := time.Date(2024, 3, 1, 0, 0, 0, 0, time.UTC)
	run := runOf("train", "org:ai:team", 16)

	budgets := []v1.Budget{{
		ObjectMeta: v1.ObjectMeta{Name: "team"},
		Spec: v1.BudgetSpec{
			Owner: "org:ai:team",
			Envelopes: []v1.BudgetEnvelope{{
				Name:        "west",
				Flavor:      testFlavor,
				Selector:    map[string]string{topology.LabelRegion: "us-west"},
				Concurrency: 8,
			}},
		},
	}}
	ev := funding.Evaluate(funding.Input{Budgets: budgets, Now: now})

	borrowCap := int32(4)
	result, err := Plan(Input{
		Run:          run,
		Now:          now,
		CoverErr:     &cover.PlanError{Reason: cover.FailureReasonBorrowLimit},
		CoverRequest: cover.Request{Owner: run.Spec.Owner, MaxBorrowGPUs: &borrowCap},
		Evaluation:   ev,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	if result.Forecast.DeficitGPUs == 0 {
		t.Fatalf("expected deficit due to borrow cap")
	}
	// 16 requested against 8 own-envelope headroom plus the 4-GPU borrow cap.
	if result.Forecast.DeficitGPUs != 4 {
		t.Fatalf("expected deficit of 4 GPUs, got %d", result.Forecast.DeficitGPUs)
	}
	if result.Reason != "borrow limit of 4 GPUs exhausted for requested width" {
		t.Fatalf("unexpected reason: %s", result.Reason)
	}
}

// Headroom counts only the run owner's own envelopes: the cover planner
// already failed to fill from family and sponsor tiers, so counting the
// parent's family-sharable excess here would understate the deficit.
func TestPlanHeadroomExcludesFamilyEnvelopes(t *testing.T) {
	now := time.Date(2024, 4, 1, 0, 0, 0, 0, time.UTC)
	run := runOf("train", "org:ai:team", 16)

	budgets := []v1.Budget{
		{
			ObjectMeta: v1.ObjectMeta{Name: "ai-budget"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "shared-pool",
					Flavor:      testFlavor,
					Selector:    map[string]string{topology.LabelRegion: "us-west"},
					Concurrency: 32,
				}},
			},
		},
		{
			ObjectMeta: v1.ObjectMeta{Name: "team-budget"},
			Spec: v1.BudgetSpec{
				Owner:   "org:ai:team",
				Parents: []string{"org:ai"},
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      testFlavor,
					Selector:    map[string]string{topology.LabelRegion: "us-west"},
					Concurrency: 4,
				}},
			},
		},
	}
	ev := funding.Evaluate(funding.Input{Budgets: budgets, Now: now})

	result, err := Plan(Input{
		Run:          run,
		Now:          now,
		CoverErr:     &cover.PlanError{Reason: cover.FailureReasonInsufficientCapacity},
		CoverRequest: cover.Request{Owner: run.Spec.Owner},
		Evaluation:   ev,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// 16 requested; only the owner's 4-wide envelope counts as headroom,
	// despite 32 idle family GPUs one tier up.
	if result.Forecast.DeficitGPUs != 12 {
		t.Fatalf("expected deficit of 12 GPUs, got %d", result.Forecast.DeficitGPUs)
	}
	if result.PayingEnvelope != "west" {
		t.Fatalf("expected paying envelope west, got %s", result.PayingEnvelope)
	}
}

// Headroom is ranked, not summed: claims below the run's admission rank
// would recall (quota-semantics.md Decision 3), so a sibling's shared width
// and a later-admitted owner run do not consume headroom, while the owner's
// earlier claim does.
func TestPlanDeficitRespectsClaimRanking(t *testing.T) {
	now := time.Date(2024, 5, 1, 12, 0, 0, 0, time.UTC)
	run := runOf("train", "org:ai:team", 8)

	budgets := []v1.Budget{
		{
			ObjectMeta: v1.ObjectMeta{Name: "team-budget"},
			Spec: v1.BudgetSpec{
				Owner:   "org:ai:team",
				Parents: []string{"org:ai"},
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      testFlavor,
					Selector:    map[string]string{topology.LabelRegion: "us-west"},
					Concurrency: 8,
				}},
			},
		},
		// The sibling needs a budget only to appear in the family graph.
		{
			ObjectMeta: v1.ObjectMeta{Name: "peer-budget"},
			Spec:       v1.BudgetSpec{Owner: "org:ai:peer", Parents: []string{"org:ai"}},
		},
	}
	runs := runsMap(
		trackedRunOf("steady", "org:ai:team", now.Add(-3*time.Hour)),
		trackedRunOf("latecomer", "org:ai:team", now.Add(-30*time.Minute)),
		trackedRunOf("guest", "org:ai:peer", now.Add(-2*time.Hour)),
	)
	leases := []v1.Lease{
		leaseOf("l-steady", "steady", "team-budget", "west", 2, now.Add(-3*time.Hour)),
		leaseOf("l-late", "latecomer", "team-budget", "west", 2, now.Add(-30*time.Minute)),
		// Family excess needs no lending policy (Decision 2); this funds as
		// shared and stays recallable by the owner.
		leaseOf("l-guest", "guest", "team-budget", "west", 2, now.Add(-2*time.Hour)),
	}
	ev := funding.Evaluate(funding.Input{Budgets: budgets, Leases: leases, Runs: runs, Now: now})

	result, err := Plan(Input{
		Run:      run,
		Now:      now,
		CoverErr: &cover.PlanError{Reason: cover.FailureReasonInsufficientCapacity},
		// The run keeps the rank of its original admission when it grows.
		CoverRequest: cover.Request{Owner: run.Spec.Owner, Admitted: now.Add(-time.Hour)},
		Evaluation:   ev,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// Of the 8-wide envelope only steady's 2 GPUs rank above the claim;
	// latecomer (same tier, admitted later) and guest (sibling tier) would
	// recall. Headroom 6 against 8 requested leaves a 2-GPU deficit.
	if result.Forecast.DeficitGPUs != 2 {
		t.Fatalf("expected deficit of 2 GPUs, got %d", result.Forecast.DeficitGPUs)
	}
	if result.Reason != "budget headroom short by 2 GPUs" {
		t.Fatalf("unexpected reason: %s", result.Reason)
	}
}

// Admission is metered: funding a claim requires width x period of remaining
// envelope GPU-hours (Decision 1 — nothing is admitted born-opportunistic),
// and an envelope whose window has not opened admits nothing without
// preActivation.allowAdmission.
func TestPlanHeadroomMeteredAndWindowGated(t *testing.T) {
	now := time.Date(2024, 6, 1, 0, 0, 0, 0, time.UTC)
	run := runOf("train", "org:ai:team", 8)

	maxHours := int64(48) // two GPUs' worth of the default 24h period
	start := v1.NewTime(now.Add(2 * time.Hour))
	budgets := []v1.Budget{{
		ObjectMeta: v1.ObjectMeta{Name: "team-budget"},
		Spec: v1.BudgetSpec{
			Owner: "org:ai:team",
			Envelopes: []v1.BudgetEnvelope{
				{
					Name:        "east",
					Flavor:      testFlavor,
					Selector:    map[string]string{topology.LabelRegion: "us-east"},
					Concurrency: 8,
					MaxGPUHours: &maxHours,
				},
				{
					Name:        "later",
					Flavor:      testFlavor,
					Selector:    map[string]string{topology.LabelRegion: "us-east"},
					Concurrency: 8,
					Start:       &start,
				},
			},
		},
	}}
	ev := funding.Evaluate(funding.Input{Budgets: budgets, Now: now})

	result, err := Plan(Input{
		Run:          run,
		Now:          now,
		CoverErr:     &cover.PlanError{Reason: cover.FailureReasonInsufficientCapacity},
		CoverRequest: cover.Request{Owner: run.Spec.Owner},
		Evaluation:   ev,
	})
	if err != nil {
		t.Fatalf("plan: %v", err)
	}
	// east's integral funds floor(48h / 24h) = 2 GPUs despite concurrency 8;
	// later's window is closed to admission. 8 requested - 2 = 6.
	if result.Forecast.DeficitGPUs != 6 {
		t.Fatalf("expected deficit of 6 GPUs, got %d", result.Forecast.DeficitGPUs)
	}
	if result.PayingEnvelope != "east" {
		t.Fatalf("expected paying envelope east, got %s", result.PayingEnvelope)
	}
}
