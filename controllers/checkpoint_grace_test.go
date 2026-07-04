package controllers

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func checkpointFixtureState(checkpoint time.Duration) (*ClusterState, *v1.Run) {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "run", Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:ai:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4},
		},
	}
	if checkpoint > 0 {
		run.Spec.Runtime = &v1.RunRuntime{Checkpoint: metav1.Duration{Duration: checkpoint}}
	}
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
		},
		Runs: map[string]*v1.Run{"default/run": run},
	}
	return state, run
}

// TestHandleNodeFailureWithoutSpareRespectsCheckpoint proves finding #10/#15
// fixed: RunSpec.Runtime.Checkpoint is read by HandleNodeFailure and
// actually changes the outcome. Zero (or unset) keeps the old behavior —
// immediate terminal failure; a positive duration parks the run Pending
// with a bounded grace window instead.
func TestHandleNodeFailureWithoutSpareRespectsCheckpoint(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)

	t.Run("no checkpoint fails immediately", func(t *testing.T) {
		state, _ := checkpointFixtureState(0)
		controller := NewRunController(state, runClock{now: now})
		if err := controller.Reconcile("default", "run"); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if err := controller.HandleNodeFailure("node-a", now); err != nil {
			t.Fatalf("handle node failure: %v", err)
		}
		run := state.Runs["default/run"]
		if run.Status.Phase != RunPhaseFailed {
			t.Fatalf("expected Failed with no checkpoint, got %s (%s)", run.Status.Phase, run.Status.Message)
		}
		if run.Status.CheckpointDeadline != nil {
			t.Fatalf("expected no checkpoint deadline, got %v", run.Status.CheckpointDeadline)
		}
	})

	t.Run("checkpoint grants a grace window", func(t *testing.T) {
		state, _ := checkpointFixtureState(10 * time.Minute)
		controller := NewRunController(state, runClock{now: now})
		if err := controller.Reconcile("default", "run"); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if err := controller.HandleNodeFailure("node-a", now); err != nil {
			t.Fatalf("handle node failure: %v", err)
		}
		run := state.Runs["default/run"]
		if run.Status.Phase != RunPhasePending {
			t.Fatalf("expected Pending during checkpoint grace, got %s (%s)", run.Status.Phase, run.Status.Message)
		}
		if run.Status.CheckpointDeadline == nil {
			t.Fatalf("expected a checkpoint deadline to be set")
		}
		wantDeadline := now.Add(10 * time.Minute)
		if !run.Status.CheckpointDeadline.Time.Equal(wantDeadline) {
			t.Fatalf("expected deadline %s, got %s", wantDeadline, run.Status.CheckpointDeadline.Time)
		}

		// A second node appears before the deadline: the next reconcile
		// re-admits the run and clears the deadline.
		state.Nodes = append(state.Nodes, topology.SourceNode{
			Name: "node-b", GPUs: 4,
			Labels: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB"},
		})
		recoverTime := now.Add(2 * time.Minute)
		controller.Clock = runClock{now: recoverTime}
		if err := controller.Reconcile("default", "run"); err != nil {
			t.Fatalf("reconcile after recovery: %v", err)
		}
		if run.Status.Phase != RunPhaseRunning {
			t.Fatalf("expected the run to re-admit as Running, got %s (%s)", run.Status.Phase, run.Status.Message)
		}
		if run.Status.CheckpointDeadline != nil {
			t.Fatalf("expected checkpoint deadline cleared after recovery, got %v", run.Status.CheckpointDeadline)
		}
	})

	t.Run("checkpoint grace expires without recovery", func(t *testing.T) {
		state, _ := checkpointFixtureState(10 * time.Minute)
		controller := NewRunController(state, runClock{now: now})
		if err := controller.Reconcile("default", "run"); err != nil {
			t.Fatalf("reconcile: %v", err)
		}
		if err := controller.HandleNodeFailure("node-a", now); err != nil {
			t.Fatalf("handle node failure: %v", err)
		}
		// No replacement node ever appears; once the clock passes the
		// deadline the run must fail rather than retry forever.
		lateTime := now.Add(11 * time.Minute)
		controller.Clock = runClock{now: lateTime}
		if err := controller.Reconcile("default", "run"); err != nil {
			t.Fatalf("reconcile after expiry: %v", err)
		}
		run := state.Runs["default/run"]
		if run.Status.Phase != RunPhaseFailed {
			t.Fatalf("expected Failed after checkpoint grace expired, got %s (%s)", run.Status.Phase, run.Status.Message)
		}
		if run.Status.CheckpointDeadline != nil {
			t.Fatalf("expected checkpoint deadline cleared on terminal failure, got %v", run.Status.CheckpointDeadline)
		}
	})
}
