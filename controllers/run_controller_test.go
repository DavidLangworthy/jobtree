package controllers

import (
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
