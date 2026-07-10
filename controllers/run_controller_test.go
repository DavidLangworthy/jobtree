package controllers

import (
	"fmt"
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/forecast"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

type runClock struct{ now time.Time }

func (f runClock) Now() time.Time { return f.now }

func TestRunControllerAdmitsRun(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "rai"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:rai",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west-h100",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 8,
				}},
			},
		}},
		Nodes: []topology.SourceNode{{
			Name: "node-a",
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 4,
		}},
	}
	state.Runs = map[string]*v1.Run{
		"default/train-8": {
			ObjectMeta: v1.ObjectMeta{Name: "train-8", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner: "org:ai:rai",
				Resources: v1.RunResources{
					GPUType:   "H100-80GB",
					TotalGPUs: 4,
				},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "train-8"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Post-cutover the controller no longer mints on the admission path: a
	// placeable + fundable run emits unscheduled Active intent pods (a Roles-less
	// run emits one 1-GPU pod per TotalGPU) and stays Pending for the scheduler
	// plugin to bind and mint. The admission DECISION is covered by pkg/admission.
	run := state.Runs["default/train-8"]
	if run.Status.Phase != RunPhasePending {
		t.Fatalf("expected run phase pending, got %s", run.Status.Phase)
	}
	if got := activeIntentPods(state, "default", "train-8"); got != 4 {
		t.Fatalf("expected 4 active intent pods, got %d", got)
	}
	if len(state.Leases) != 0 {
		t.Fatalf("controller must mint nothing, got %d leases", len(state.Leases))
	}
}

// A run declaring spares emits held RoleSpare intent pods alongside the active
// gang — real, funded standby capacity for the plugin to bind and mint, not the
// PLUGIN-2-era inert declaration. Idempotent across reconciles.
func TestRunControllerEmitsHeldSpares(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	spares := int32(2)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "rai"},
			Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB",
				Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
				Concurrency: 16,
			}}},
		}},
		Nodes: []topology.SourceNode{{
			Name: "node-a",
			Labels: map[string]string{
				topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a",
				topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB",
			},
			GPUs: 8, // 4 active + 2 spare fit with room
		}},
	}
	state.Runs = map[string]*v1.Run{
		"default/train": {
			ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner:     "org:ai:rai",
				Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
				Spares:    &spares,
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "train"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	if got := activeIntentPods(state, "default", "train"); got != 4 {
		t.Errorf("active intent pods = %d, want 4", got)
	}
	if got := spareIntentPods(state, "default", "train"); got != 2 {
		t.Errorf("spare intent pods = %d, want 2 (spares must be held live)", got)
	}
	if len(state.Leases) != 0 {
		t.Errorf("controller mints nothing; the plugin funds spares, got %d leases", len(state.Leases))
	}

	// Idempotent: a second reconcile does not double-emit spares.
	if err := controller.Reconcile("default", "train"); err != nil {
		t.Fatalf("second reconcile failed: %v", err)
	}
	if got := spareIntentPods(state, "default", "train"); got != 2 {
		t.Errorf("spare intent pods after re-reconcile = %d, want 2 (idempotent)", got)
	}
}

func TestRunControllerCoFundedRunUpdatesFundingStatus(t *testing.T) {
	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	lendAllow := true
	limit := int32(32)
	state := &ClusterState{
		Budgets: []v1.Budget{
			{
				ObjectMeta: v1.ObjectMeta{Name: "rai"},
				Spec: v1.BudgetSpec{
					Owner: "org:ai:rai",
					Envelopes: []v1.BudgetEnvelope{{
						Name:        "west-h100",
						Flavor:      "H100-80GB",
						Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
						Concurrency: 96,
					}},
				},
			},
			{
				ObjectMeta: v1.ObjectMeta{Name: "vision"},
				Spec: v1.BudgetSpec{
					Owner: "org:ai:mm:vision",
					Envelopes: []v1.BudgetEnvelope{{
						Name:        "west-h100",
						Flavor:      "H100-80GB",
						Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
						Concurrency: 64,
						Lending: &v1.LendingPolicy{
							Allow:          lendAllow,
							To:             []string{"org:ai:rai", "org:ai:rai:*"},
							MaxConcurrency: &limit,
						},
					}},
				},
			},
		},
	}

	for i := 0; i < 4; i++ {
		state.Nodes = append(state.Nodes, topology.SourceNode{
			Name: fmt.Sprintf("node-%d", i),
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 32,
		})
	}

	maxBorrow := int32(32)
	groupSize := int32(32)
	state.Runs = map[string]*v1.Run{
		"default/train-128": {
			ObjectMeta: v1.ObjectMeta{Name: "train-128", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner: "org:ai:rai",
				Resources: v1.RunResources{
					GPUType:   "H100-80GB",
					TotalGPUs: 128,
				},
				Locality: &v1.RunLocality{GroupGPUs: &groupSize},
				Funding: &v1.RunFunding{
					AllowBorrow:   true,
					MaxBorrowGPUs: &maxBorrow,
					Sponsors:      []string{"org:ai:mm:vision"},
				},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "train-128"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	// Post-cutover the controller mints nothing on the admission path. This run
	// is fundable only by borrowing 32 GPUs from the vision sponsor (96 owned +
	// 32 borrowed = 128); keeping that co-funding SETUP, we now assert the emit
	// contract — a placeable + fundable run emits its full width of unscheduled
	// intent pods and stays Pending for the plugin. The funding-class breakdown
	// (owned/shared/borrowed/lenders) is derived and asserted in pkg/admission,
	// and reads zero here precisely because the controller attributes no leases.
	run := state.Runs["default/train-128"]
	if run.Status.Phase != RunPhasePending {
		t.Fatalf("expected run phase pending, got %s", run.Status.Phase)
	}
	if got := activeIntentPods(state, "default", "train-128"); got != 128 {
		t.Fatalf("expected 128 active intent pods, got %d", got)
	}
	if len(state.Leases) != 0 {
		t.Fatalf("controller must mint nothing, got %d leases", len(state.Leases))
	}
}

func TestRunControllerBorrowLimitCreatesReservation(t *testing.T) {
	now := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	allow := true
	state := &ClusterState{
		Budgets: []v1.Budget{
			{
				ObjectMeta: v1.ObjectMeta{Name: "rai"},
				Spec: v1.BudgetSpec{
					Owner: "org:ai:rai",
					Envelopes: []v1.BudgetEnvelope{{
						Name:        "west-h100",
						Flavor:      "H100-80GB",
						Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
						Concurrency: 64,
					}},
				},
			},
			{
				ObjectMeta: v1.ObjectMeta{Name: "vision"},
				Spec: v1.BudgetSpec{
					Owner: "org:ai:mm:vision",
					Envelopes: []v1.BudgetEnvelope{{
						Name:        "west-h100",
						Flavor:      "H100-80GB",
						Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
						Concurrency: 64,
						Lending: &v1.LendingPolicy{
							Allow: allow,
							To:    []string{"org:ai:rai", "org:ai:rai:*"},
						},
					}},
				},
			},
		},
	}

	for i := 0; i < 4; i++ {
		state.Nodes = append(state.Nodes, topology.SourceNode{
			Name: fmt.Sprintf("node-b-%d", i),
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 32,
		})
	}

	maxBorrow := int32(8)
	groupSize := int32(32)
	state.Runs = map[string]*v1.Run{
		"default/train-128": {
			ObjectMeta: v1.ObjectMeta{Name: "train-128", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner: "org:ai:rai",
				Resources: v1.RunResources{
					GPUType:   "H100-80GB",
					TotalGPUs: 128,
				},
				Locality: &v1.RunLocality{GroupGPUs: &groupSize},
				Funding: &v1.RunFunding{
					AllowBorrow:   true,
					MaxBorrowGPUs: &maxBorrow,
					Sponsors:      []string{"org:ai:mm:vision"},
				},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "train-128"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	run := state.Runs["default/train-128"]
	if run.Status.PendingReservation == nil {
		t.Fatalf("expected reservation due to borrow limit")
	}
	if run.Status.Phase != RunPhasePending {
		t.Fatalf("expected phase pending when borrow limit hit, got %s", run.Status.Phase)
	}
	if len(state.Reservations) != 1 {
		t.Fatalf("expected reservation created, got %d", len(state.Reservations))
	}
}

func TestRunControllerCreatesReservationWhenCapacityMissing(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:team",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 16,
				}},
			},
		}},
		Nodes: []topology.SourceNode{{
			Name: "node-a",
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 4,
		}},
	}
	state.Runs = map[string]*v1.Run{
		"default/train-8": {
			ObjectMeta: v1.ObjectMeta{Name: "train-8", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner: "org:ai:team",
				Resources: v1.RunResources{
					GPUType:   "H100-80GB",
					TotalGPUs: 8,
				},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "train-8"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	run := state.Runs["default/train-8"]
	if run.Status.PendingReservation == nil {
		t.Fatalf("expected pending reservation recorded")
	}
	if run.Status.EarliestStart == nil {
		t.Fatalf("expected earliest start set")
	}
	if len(state.Reservations) != 1 {
		t.Fatalf("expected one reservation, got %d", len(state.Reservations))
	}
	for _, res := range state.Reservations {
		if res.Spec.EarliestStart.Time.Before(now) {
			t.Fatalf("expected reservation earliest start in future")
		}
		if res.Status.Forecast == nil || res.Status.Forecast.DeficitGPUs == 0 {
			t.Fatalf("expected forecast deficit")
		}
	}
}

func TestRunControllerCreatesReservationForFutureWindow(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	start := v1.NewTime(now.Add(2 * time.Hour))
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:team",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 16,
					Start:       &start,
					PreActivation: &v1.PreActivationPolicy{
						AllowReservations: true,
						AllowAdmission:    false,
					},
				}},
			},
		}},
		Nodes: []topology.SourceNode{{
			Name: "node-a",
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 8,
		}},
	}
	state.Runs = map[string]*v1.Run{
		"default/train-8": {
			ObjectMeta: v1.ObjectMeta{Name: "train-8", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner:     "org:ai:team",
				Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "train-8"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if len(state.Reservations) != 1 {
		t.Fatalf("expected reservation created")
	}
	var reservation *v1.Reservation
	for _, res := range state.Reservations {
		reservation = res
	}
	if reservation == nil {
		t.Fatalf("reservation missing")
	}
	expectedEarliest := start.Time.Add(forecast.WindowActivationOffset)
	if reservation.Spec.EarliestStart.Time.Before(expectedEarliest) {
		t.Fatalf("expected earliest start >= %s, got %s", expectedEarliest, reservation.Spec.EarliestStart.Time)
	}
	if reservation.Status.Forecast == nil || reservation.Status.Forecast.Confidence != "window-aligned" {
		t.Fatalf("expected window-aligned forecast")
	}
}

func TestElasticRunGrowsToDesired(t *testing.T) {
	now := time.Date(2024, 2, 2, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:rai",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 256,
				}},
			},
		}},
	}
	for i := 0; i < 5; i++ {
		state.Nodes = append(state.Nodes, topology.SourceNode{
			Name: fmt.Sprintf("node-%d", i),
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 32,
		})
	}
	desired := int32(160)
	group := int32(32)
	state.Runs = map[string]*v1.Run{
		"default/train": {
			ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner: "org:ai:rai",
				Resources: v1.RunResources{
					GPUType:   "H100-80GB",
					TotalGPUs: 96,
				},
				Locality: &v1.RunLocality{GroupGPUs: &group},
				Malleable: &v1.RunMalleability{
					MinTotalGPUs:     96,
					MaxTotalGPUs:     160,
					StepGPUs:         32,
					DesiredTotalGPUs: &desired,
				},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	// Post-cutover Reconcile no longer binds; seed the bound/Running run (96 GPUs
	// allocated) the scheduler plugin would have produced, then drive the
	// still-in-engine elastic grow. The grow-to-128 assertion below implies the
	// 96 baseline (one +32 step).
	seedRunning(t, state, "default/train", now)
	run := state.Runs["default/train"]

	grewAt := now.Add(time.Minute)
	controller.Clock = runClock{now: grewAt}
	if err := controller.Reconcile("default", "train"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	// growRun now emits a +32 grow cohort of unscheduled intent pods; the plugin
	// funds that delta and mints "Grow" leases (seedGrowLeases). The next
	// reconcile reflects the grown width from those leases.
	seedGrowLeases(t, state, "default/train", 32, grewAt)
	if err := controller.Reconcile("default", "train"); err != nil {
		t.Fatalf("reconcile after grow mint failed: %v", err)
	}
	if run.Status.Width == nil || run.Status.Width.Allocated != 128 {
		t.Fatalf("expected allocated width 128 after growth, got %+v", run.Status.Width)
	}
	foundGrow := false
	for _, lease := range state.Leases {
		if lease.Spec.Reason == "Grow" {
			foundGrow = true
			break
		}
	}
	if !foundGrow {
		t.Fatalf("expected at least one grow lease")
	}
}

func TestElasticRunVoluntaryShrink(t *testing.T) {
	now := time.Date(2024, 2, 2, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:rai",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 256,
				}},
			},
		}},
	}
	for i := 0; i < 5; i++ {
		state.Nodes = append(state.Nodes, topology.SourceNode{
			Name: fmt.Sprintf("node-%d", i),
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 32,
		})
	}
	desired := int32(160)
	group := int32(32)
	state.Runs = map[string]*v1.Run{
		"default/train": {
			ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner:     "org:ai:rai",
				Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 96},
				Locality:  &v1.RunLocality{GroupGPUs: &group},
				Malleable: &v1.RunMalleability{
					MinTotalGPUs:     96,
					MaxTotalGPUs:     160,
					StepGPUs:         32,
					DesiredTotalGPUs: &desired,
				},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	// Post-cutover Reconcile no longer binds; seed the bound/Running run (96 GPUs)
	// the scheduler plugin would have produced, then drive the still-in-engine
	// elastic grow (to 128) that sets up the voluntary shrink below.
	seedRunning(t, state, "default/train", now)
	controller.Clock = runClock{now: now.Add(time.Minute)}
	if err := controller.Reconcile("default", "train"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}
	// growRun emitted a +32 grow cohort; the plugin mints it (seedGrowLeases) so
	// the run reaches 128 before we ask it to shrink back to 96.
	seedGrowLeases(t, state, "default/train", 32, now.Add(time.Minute))
	if err := controller.Reconcile("default", "train"); err != nil {
		t.Fatalf("reconcile after grow mint failed: %v", err)
	}

	target := int32(96)
	run := state.Runs["default/train"]
	run.Spec.Malleable.DesiredTotalGPUs = &target
	controller.Clock = runClock{now: now.Add(2 * time.Minute)}
	if err := controller.Reconcile("default", "train"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if run.Status.Width == nil || run.Status.Width.Allocated != 96 {
		t.Fatalf("expected allocated width 96 after shrink, got %+v", run.Status.Width)
	}
	closedShrink := 0
	for i := range state.Leases {
		lease := state.Leases[i]
		if lease.Status.Closed && lease.Status.ClosureReason == "Shrink" {
			closedShrink++
		}
	}
	if closedShrink == 0 {
		t.Fatalf("expected shrink to close leases")
	}
	if len(state.Pods) == 0 {
		t.Fatalf("expected remaining pods after shrink")
	}
}

func TestActivateReservationRunsResolver(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	nodes := []topology.SourceNode{
		{Name: "node-a", GPUs: 8, Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB"}},
		{Name: "node-b", GPUs: 8, Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB"}},
		{Name: "node-c", GPUs: 8, Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB"}},
	}

	budgets := []v1.Budget{
		{ObjectMeta: v1.ObjectMeta{Name: "owner-a"}, Spec: v1.BudgetSpec{Owner: "org:owner:a", Envelopes: []v1.BudgetEnvelope{{
			Name:        "west",
			Flavor:      "H100-80GB",
			Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
			Concurrency: 16,
		}}}},
		{ObjectMeta: v1.ObjectMeta{Name: "owner-b"}, Spec: v1.BudgetSpec{Owner: "org:owner:b", Envelopes: []v1.BudgetEnvelope{{
			Name:        "west",
			Flavor:      "H100-80GB",
			Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
			Concurrency: 16,
		}}}},
		{ObjectMeta: v1.ObjectMeta{Name: "owner-c"}, Spec: v1.BudgetSpec{Owner: "org:owner:c", Envelopes: []v1.BudgetEnvelope{{
			Name:        "west",
			Flavor:      "H100-80GB",
			Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
			Concurrency: 16,
		}}}},
	}

	state := &ClusterState{Nodes: nodes, Budgets: budgets}
	state.Runs = map[string]*v1.Run{
		"default/run-a": {
			ObjectMeta: v1.ObjectMeta{Name: "run-a", Namespace: "default"},
			Spec:       v1.RunSpec{Owner: "org:owner:a", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8}, Locality: &v1.RunLocality{GroupGPUs: int32Ptr(8)}},
		},
		"default/run-b": {
			ObjectMeta: v1.ObjectMeta{Name: "run-b", Namespace: "default"},
			Spec:       v1.RunSpec{Owner: "org:owner:b", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 16}, Locality: &v1.RunLocality{GroupGPUs: int32Ptr(8)}, Malleable: &v1.RunMalleability{MinTotalGPUs: 8, MaxTotalGPUs: 16, StepGPUs: 8}},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	// Post-cutover Reconcile no longer binds; seed run-a and run-b as the
	// bound/Running incumbents the scheduler plugin would have produced, so the
	// activation-time resolver has funded work to preempt (lottery) and shrink.
	seedRunning(t, state, "default/run-a", now)
	seedRunning(t, state, "default/run-b", now)

	// Add the pending run that will trigger a reservation.
	state.Runs["default/run-c"] = &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "run-c", Namespace: "default"},
		Spec:       v1.RunSpec{Owner: "org:owner:c", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 16}, Locality: &v1.RunLocality{GroupGPUs: int32Ptr(8)}},
	}

	if err := controller.Reconcile("default", "run-c"); err != nil {
		t.Fatalf("run-c reconcile failed: %v", err)
	}

	var reservation *v1.Reservation
	for _, res := range state.Reservations {
		reservation = res
	}
	if reservation == nil {
		t.Fatalf("expected reservation for run-c")
	}

	activationTime := now.Add(30 * time.Minute)
	controller.Clock = runClock{now: activationTime}
	if err := controller.ActivateReservations(activationTime); err != nil {
		t.Fatalf("activate reservations failed: %v", err)
	}
	// run-c's funded activation frees capacity and emits intent pods (Pending);
	// the scheduler plugin mints its leases — stood in for by seedRunning — to
	// reach Running.
	seedRunning(t, state, "default/run-c", activationTime)

	runA := state.Runs["default/run-a"]
	runB := state.Runs["default/run-b"]
	runC := state.Runs["default/run-c"]

	if runC.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected run-c running after activation, got %s", runC.Status.Phase)
	}
	if runA.Status.Phase != RunPhaseFailed {
		t.Fatalf("expected run-a failed after lottery, got %s", runA.Status.Phase)
	}
	if runB.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected run-b running after shrink, got %s", runB.Status.Phase)
	}

	// Ensure leases closed with reasons.
	closedLottery := false
	closedShrink := false
	for _, lease := range state.Leases {
		if !lease.Status.Closed {
			continue
		}
		if strings.Contains(lease.Status.ClosureReason, "RandomPreempt") {
			closedLottery = true
		}
		if lease.Status.ClosureReason == "Shrink" {
			closedShrink = true
		}
	}
	if !closedLottery {
		t.Fatalf("expected at least one lottery preempted lease")
	}
	if !closedShrink {
		t.Fatalf("expected shrink closure reason")
	}

	if reservation.Status.State != "Released" {
		t.Fatalf("expected reservation released, got %s", reservation.Status.State)
	}
}

func TestHandleNodeFailureSwapsToSpare(t *testing.T) {
	now := time.Date(2024, 2, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:team",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 8,
				}},
			},
		}},
		Nodes: []topology.SourceNode{
			{Name: "node-a", GPUs: 4, Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB"}},
			{Name: "node-b", GPUs: 4, Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB"}},
		},
	}
	state.Runs = map[string]*v1.Run{
		"default/run": {
			ObjectMeta: v1.ObjectMeta{Name: "run", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner:     "org:ai:team",
				Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
				Locality:  &v1.RunLocality{GroupGPUs: int32Ptr(4)},
				Spares:    int32Ptr(2),
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	// Post-cutover Reconcile no longer binds; seed the bound/Running run (4 active
	// GPUs + 2 spares) the scheduler plugin would have produced, then exercise the
	// still-in-engine node-failure spare swap.
	seedRunning(t, state, "default/run", now)

	var spareLease *v1.Lease
	for i := range state.Leases {
		if state.Leases[i].Spec.Slice.Role == binder.RoleSpare {
			spareLease = &state.Leases[i]
			break
		}
	}
	if spareLease == nil {
		t.Fatalf("expected spare lease to be created")
	}

	// Filler work squatting on the spare's node: role is Active (roles are
	// Active|Spare only now); its unfunded nature would be derived, but all
	// that matters here is that a swap reclaims it.
	fillerLease := v1.Lease{
		ObjectMeta: v1.ObjectMeta{Name: "filler"},
		Spec: v1.LeaseSpec{
			Owner:          "org:ai:other",
			RunRef:         v1.RunReference{Name: "filler", Namespace: "default"},
			Slice:          v1.LeaseSlice{Nodes: append([]string{}, spareLease.Spec.Slice.Nodes...), Role: binder.RoleActive},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(now)},
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}
	state.Leases = append(state.Leases, fillerLease)
	state.Pods = append(state.Pods, binder.PodManifest{
		Namespace: "default",
		Name:      "filler",
		NodeName:  nodeFromSlot(spareLease.Spec.Slice.Nodes[0]),
		GPUs:      len(spareLease.Spec.Slice.Nodes),
		Labels: map[string]string{
			binder.LabelRunName:    "filler",
			binder.LabelGroupIndex: "0",
			binder.LabelRunRole:    binder.RoleActive,
		},
	})

	failTime := now.Add(5 * time.Minute)
	controller.Clock = runClock{now: failTime}
	if err := controller.HandleNodeFailure("node-a", failTime); err != nil {
		t.Fatalf("handle node failure failed: %v", err)
	}

	run := state.Runs["default/run"]
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected run to remain running, got %s", run.Status.Phase)
	}
	if !strings.Contains(run.Status.Message, "swapping") {
		t.Fatalf("expected swap message, got %s", run.Status.Message)
	}
	// HandleNodeFailure now emits a swap pod (stamped with the spare's provenance
	// + hard-targeted at the spare node) instead of minting; the plugin mints the
	// Swap lease from that provenance — stood in for by seedSwapLease.
	seedSwapLease(t, state, "run", failTime)

	var activeOnSpare *v1.Lease
	fillerClosed := false
	for i := range state.Leases {
		lease := state.Leases[i]
		if lease.Spec.RunRef.Name == "run" && lease.Spec.Slice.Role == binder.RoleActive && !lease.Status.Closed && lease.Spec.Interval.Start.Time.Equal(failTime) {
			activeOnSpare = &lease
		}
		if lease.Spec.RunRef.Name == "filler" && lease.Status.ClosureReason == "ReclaimedBySpare" {
			fillerClosed = true
		}
	}
	if activeOnSpare == nil {
		t.Fatalf("expected new active lease on spare nodes")
	}
	if !fillerClosed {
		t.Fatalf("expected filler lease reclaimed by spare")
	}
	if len(state.Pods) == 0 || state.Pods[len(state.Pods)-1].NodeName != nodeFromSlot(spareLease.Spec.Slice.Nodes[0]) {
		t.Fatalf("expected new pod on spare node, got %+v", state.Pods)
	}
	// The held spare's pod on the reclaimed node was removed so the bridge frees
	// its GPU for the swap pod (which hard-targets that node). One spare remains.
	spareNode := nodeFromSlot(spareLease.Spec.Slice.Nodes[0])
	for i := range state.Pods {
		p := &state.Pods[i]
		if p.Labels[binder.LabelRunName] == "run" && p.Labels[binder.LabelRunRole] == binder.RoleSpare && p.NodeName == spareNode {
			t.Fatalf("spare pod on reclaimed node %s was not removed: %+v", spareNode, *p)
		}
	}
	// A consumed spare (its lease closed with reason Swap) is not re-provisioned:
	// re-running admission-side spare emission tops up to declared-minus-consumed.
	if got := controller.consumedSpareCount(state.Runs["default/run"]); got != 1 {
		t.Fatalf("consumedSpareCount = %d, want 1 after one swap", got)
	}
}

func int32Ptr(v int32) *int32 { return &v }

// A run whose flavor has no matching nodes anywhere must park as plain
// Pending: a reservation with neither nodes nor a domain scope would be
// rejected by its own validating webhook once one is installed (R18),
// permanently wedging the run behind a failing bridge apply.
func TestRunControllerParksRunWhenNoMatchingDomain(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
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
		}},
		// No nodes of the flavor at all.
		Runs: map[string]*v1.Run{
			"default/stranded": {
				ObjectMeta: v1.ObjectMeta{Name: "stranded", Namespace: "default"},
				Spec: v1.RunSpec{
					Owner:     "org:ai:team",
					Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
				},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "stranded"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	if len(state.Reservations) != 0 {
		t.Fatalf("expected no reservation for a domainless forecast, got %d", len(state.Reservations))
	}
	run := state.Runs["default/stranded"]
	if run.Status.Phase != RunPhasePending {
		t.Fatalf("expected Pending, got %s", run.Status.Phase)
	}
	if !strings.Contains(run.Status.Message, "no capacity in any matching domain") {
		t.Fatalf("unexpected message: %s", run.Status.Message)
	}
}

// A run below Running that already holds open leases is a half-applied
// admission (the bridge's apply lost the status write — R28): reconcile
// must finish the transition, not double-bind against the run's own leases.
func TestReconcileAdoptsHalfAppliedAdmission(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:team",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 16,
				}},
			},
		}},
		Nodes: []topology.SourceNode{{
			Name: "node-a",
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 4,
		}},
		Leases: []v1.Lease{{
			ObjectMeta: v1.ObjectMeta{Name: "half-g00-team-west-1-0", Namespace: "default",
				Labels: map[string]string{binder.LabelRunName: "half", binder.LabelGroupIndex: "0", binder.LabelRunRole: binder.RoleActive}},
			Spec: v1.LeaseSpec{
				Owner:          "org:ai:team",
				RunRef:         v1.RunReference{Name: "half", Namespace: "default"},
				Slice:          v1.LeaseSlice{Nodes: []string{"node-a#0", "node-a#1", "node-a#2", "node-a#3"}, Role: binder.RoleActive},
				Interval:       v1.LeaseInterval{Start: v1.NewTime(now.Add(-time.Minute))},
				PaidByBudget:   "team",
				PaidByEnvelope: "west",
				Reason:         "Start",
			},
		}},
		Runs: map[string]*v1.Run{
			"default/half": {
				ObjectMeta: v1.ObjectMeta{Name: "half", Namespace: "default"},
				Spec: v1.RunSpec{
					Owner:     "org:ai:team",
					Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
				},
				Status: v1.RunStatus{Phase: RunPhasePending},
			},
		},
	}

	mirrorPods(state)
	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "half"); err != nil {
		t.Fatalf("reconcile failed: %v", err)
	}

	run := state.Runs["default/half"]
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected adoption to Running, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
	// The seeded lease is a single object covering the run's full 4-GPU width, so
	// adoption reports the adopted GPU width (R2 adopts at width, not at lease
	// count — one lease object may cover many GPUs).
	if !strings.Contains(run.Status.Message, "adopted 4 GPUs") {
		t.Errorf("unexpected message: %s", run.Status.Message)
	}
	if len(state.Leases) != 1 {
		t.Fatalf("adoption must not create leases, got %d", len(state.Leases))
	}
}

// The activation path must adopt too: without it, every activation tick
// re-plans against the run's own orphaned leases, reports them as a
// deficit, and reschedules forever.
func TestActivateReservationAdoptsHalfAppliedActivation(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	earliest := v1.NewTime(now.Add(-time.Minute))
	state := &ClusterState{
		Nodes: []topology.SourceNode{{
			Name: "node-a",
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 4,
		}},
		Leases: []v1.Lease{{
			ObjectMeta: v1.ObjectMeta{Name: "half-g00-team-west-1-0", Namespace: "default",
				Labels: map[string]string{binder.LabelRunName: "half", binder.LabelGroupIndex: "0", binder.LabelRunRole: binder.RoleActive}},
			Spec: v1.LeaseSpec{
				Owner:          "org:ai:team",
				RunRef:         v1.RunReference{Name: "half", Namespace: "default"},
				Slice:          v1.LeaseSlice{Nodes: []string{"node-a#0", "node-a#1", "node-a#2", "node-a#3"}, Role: binder.RoleActive},
				Interval:       v1.LeaseInterval{Start: v1.NewTime(now.Add(-time.Minute))},
				PaidByBudget:   "team",
				PaidByEnvelope: "west",
				Reason:         "Start",
			},
		}},
		Runs: map[string]*v1.Run{
			"default/half": {
				ObjectMeta: v1.ObjectMeta{Name: "half", Namespace: "default"},
				Spec: v1.RunSpec{
					Owner:     "org:ai:team",
					Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
				},
				Status: v1.RunStatus{Phase: RunPhasePending, PendingReservation: ptrString("half-res-1")},
			},
		},
		Reservations: map[string]*v1.Reservation{
			"default/half-res-1": {
				ObjectMeta: v1.ObjectMeta{Name: "half-res-1", Namespace: "default"},
				Spec: v1.ReservationSpec{
					RunRef:         v1.RunReference{Name: "half", Namespace: "default"},
					IntendedSlice:  v1.IntendedSlice{Domain: map[string]string{topology.LabelRegion: "us-west"}},
					PayingEnvelope: "west",
					EarliestStart:  earliest,
				},
				Status: v1.ReservationStatus{State: "Pending"},
			},
		},
	}

	mirrorPods(state)
	controller := NewRunController(state, runClock{now: now})
	if err := controller.ActivateReservations(now); err != nil {
		t.Fatalf("activate failed: %v", err)
	}

	run := state.Runs["default/half"]
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected adoption to Running, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
	if run.Status.PendingReservation != nil {
		t.Errorf("pendingReservation should clear on adoption")
	}
	res := state.Reservations["default/half-res-1"]
	if res.Status.State != "Released" || res.Status.Reason != "Activated" {
		t.Errorf("reservation = %s/%s, want Released/Activated", res.Status.State, res.Status.Reason)
	}
	if len(state.Leases) != 1 {
		t.Fatalf("adoption must not create leases, got %d", len(state.Leases))
	}
}

// TestElasticGrowShrinkEmitMetrics proves TRUTH-10/audit finding #19 fixed:
// growRun/shrinkRun actually emit jobtree_elastic_grows_total,
// jobtree_elastic_shrinks_total, and jobtree_elastic_width_current — not
// just bookkeeping leases with no observability, and M9 is genuinely done on
// this front rather than the elastic-runs.md "will follow in M9" hedge.
func TestElasticGrowShrinkEmitMetrics(t *testing.T) {
	metrics.Reset()
	now := time.Date(2024, 2, 2, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:rai",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 256,
				}},
			},
		}},
	}
	for i := 0; i < 5; i++ {
		state.Nodes = append(state.Nodes, topology.SourceNode{
			Name: fmt.Sprintf("node-%d", i),
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 32,
		})
	}
	// Desired one step above the 96 baseline: a single grow step reaches it, so
	// exactly one IncElasticGrow fires (async grow needs a re-reconcile to
	// advance the width gauge, which must not trigger a second grow step).
	desired := int32(128)
	group := int32(32)
	runKey := "default/train-metrics"
	state.Runs = map[string]*v1.Run{
		runKey: {
			ObjectMeta: v1.ObjectMeta{Name: "train-metrics", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner:     "org:ai:rai",
				Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 96},
				Locality:  &v1.RunLocality{GroupGPUs: &group},
				Malleable: &v1.RunMalleability{MinTotalGPUs: 96, MaxTotalGPUs: 160, StepGPUs: 32, DesiredTotalGPUs: &desired},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	// Post-cutover Reconcile no longer binds; seed the bound/Running run (96 GPUs)
	// the scheduler plugin would have produced, then drive the still-in-engine
	// elastic grow/shrink so their metrics fire.
	seedRunning(t, state, "default/train-metrics", now)
	controller.Clock = runClock{now: now.Add(time.Minute)}
	if err := controller.Reconcile("default", "train-metrics"); err != nil {
		t.Fatalf("reconcile (grow): %v", err)
	}
	// The grow was requested (IncElasticGrow fired); the plugin mints the +32
	// cohort (seedGrowLeases) and the next reconcile advances the width gauge.
	seedGrowLeases(t, state, "default/train-metrics", 32, now.Add(time.Minute))
	if err := controller.Reconcile("default", "train-metrics"); err != nil {
		t.Fatalf("reconcile (grow mint): %v", err)
	}

	snap := metrics.Snapshot()
	if snap.ElasticGrows["H100-80GB"] != 1 {
		t.Fatalf("expected 1 elastic grow, got %+v", snap.ElasticGrows)
	}
	if snap.ElasticWidth[runKey] != 128 {
		t.Fatalf("expected elastic width 128, got %+v", snap.ElasticWidth)
	}

	// Now shrink back down and verify the shrink counter and updated width.
	shrinkDesired := int32(96)
	state.Runs[runKey].Spec.Malleable.DesiredTotalGPUs = &shrinkDesired
	controller.Clock = runClock{now: now.Add(2 * time.Minute)}
	if err := controller.Reconcile("default", "train-metrics"); err != nil {
		t.Fatalf("reconcile (shrink): %v", err)
	}
	snap = metrics.Snapshot()
	if snap.ElasticShrinks["H100-80GB"] != 1 {
		t.Fatalf("expected 1 elastic shrink, got %+v", snap.ElasticShrinks)
	}
	if snap.ElasticWidth[runKey] != 96 {
		t.Fatalf("expected elastic width 96 after shrink, got %+v", snap.ElasticWidth)
	}
}

// TestReservationBacklogMetricLifecycle proves TRUTH-13/audit finding #21
// fixed: the backlog gauge is (a) keyed per reservation, not collapsed by
// flavor, (b) refreshed on later reconciles instead of frozen at creation
// time, and (c) cleared once the reservation activates.
func TestReservationBacklogMetricLifecycle(t *testing.T) {
	metrics.Reset()
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{
				Owner: "org:ai:team",
				Envelopes: []v1.BudgetEnvelope{{
					Name:        "west",
					Flavor:      "H100-80GB",
					Selector:    map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
					Concurrency: 16,
				}},
			},
		}},
		Nodes: []topology.SourceNode{{
			Name: "node-a",
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100-80GB",
			},
			GPUs: 4,
		}},
	}
	state.Runs = map[string]*v1.Run{
		"default/train-backlog": {
			ObjectMeta: v1.ObjectMeta{Name: "train-backlog", Namespace: "default"},
			Spec: v1.RunSpec{
				Owner:     "org:ai:team",
				Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8},
			},
		},
	}

	controller := NewRunController(state, runClock{now: now})
	if err := controller.Reconcile("default", "train-backlog"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	run := state.Runs["default/train-backlog"]
	if run.Status.PendingReservation == nil {
		t.Fatalf("expected a pending reservation")
	}
	resKey := keysNamespacedKeyTest("default", *run.Status.PendingReservation)

	snap := metrics.Snapshot()
	initial, ok := snap.ReservationBacklog[resKey]
	if !ok {
		t.Fatalf("expected a backlog entry keyed by reservation %q, got %+v", resKey, snap.ReservationBacklog)
	}
	if initial.Flavor != "H100-80GB" || initial.Seconds <= 0 {
		t.Fatalf("unexpected initial backlog entry: %+v", initial)
	}

	// A later tick, still before EarliestStart, must refresh (shrink) the
	// countdown rather than leaving it frozen at the creation-time value.
	laterNotDue := now.Add(30 * time.Second)
	controller.Clock = runClock{now: laterNotDue}
	if err := controller.ActivateReservations(laterNotDue); err != nil {
		t.Fatalf("activate reservations (not due): %v", err)
	}
	snap = metrics.Snapshot()
	refreshed, ok := snap.ReservationBacklog[resKey]
	if !ok {
		t.Fatalf("expected the backlog entry to still exist while pending")
	}
	if refreshed.Seconds >= initial.Seconds {
		t.Fatalf("expected backlog to shrink from %v to something smaller, got %v", initial.Seconds, refreshed.Seconds)
	}

	// Capacity arrives and the clock reaches EarliestStart: activation must
	// clear the backlog entry entirely rather than leaving a stale value.
	state.Nodes = append(state.Nodes, topology.SourceNode{
		Name: "node-b",
		Labels: map[string]string{
			topology.LabelRegion:       "us-west",
			topology.LabelCluster:      "cluster-a",
			topology.LabelFabricDomain: "island-a",
			topology.LabelGPUFlavor:    "H100-80GB",
		},
		GPUs: 4,
	})
	var reservation *v1.Reservation
	for _, res := range state.Reservations {
		reservation = res
	}
	activationTime := reservation.Spec.EarliestStart.Time.Add(time.Second)
	controller.Clock = runClock{now: activationTime}
	if err := controller.ActivateReservations(activationTime); err != nil {
		t.Fatalf("activate reservations: %v", err)
	}
	// Funded activation emits intent pods (Pending); the plugin mints — stood in
	// for by seedRunning — to reach Running.
	seedRunning(t, state, "default/train-backlog", activationTime)
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected run to activate to Running, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
	snap = metrics.Snapshot()
	if _, ok := snap.ReservationBacklog[resKey]; ok {
		t.Fatalf("expected backlog entry cleared after activation, got %+v", snap.ReservationBacklog)
	}
}

func keysNamespacedKeyTest(namespace, name string) string {
	if namespace == "" {
		return name
	}
	return namespace + "/" + name
}
