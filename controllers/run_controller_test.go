package controllers

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/forecast"
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

	run := state.Runs["default/train-8"]
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("expected run phase running, got %s", run.Status.Phase)
	}
	if len(state.Pods) != 1 {
		t.Fatalf("expected 1 pod manifest, got %d", len(state.Pods))
	}
	if state.Pods[0].NodeName != "node-a" {
		t.Fatalf("expected pod bound to node-a, got %s", state.Pods[0].NodeName)
	}
	if len(state.Leases) != 1 {
		t.Fatalf("expected 1 lease, got %d", len(state.Leases))
	}
	if state.Leases[0].Spec.PaidByEnvelope != "west-h100" {
		t.Fatalf("expected lease paid by west-h100, got %s", state.Leases[0].Spec.PaidByEnvelope)
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
	if err := controller.Reconcile("default", "run-a"); err != nil {
		t.Fatalf("run-a reconcile failed: %v", err)
	}
	if err := controller.Reconcile("default", "run-b"); err != nil {
		t.Fatalf("run-b reconcile failed: %v", err)
	}

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

func int32Ptr(v int32) *int32 { return &v }
