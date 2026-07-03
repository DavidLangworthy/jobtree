package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// completionWorld builds a Running run with two active leases+pods and one
// spare, so completion tests can vary only the pod phases.
func completionWorld(activePhases []string, sparePhase string) (*ClusterState, string) {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "job", Namespace: keys.DefaultNamespace},
		Spec:       v1.RunSpec{Owner: "team", Resources: v1.RunResources{GPUType: "H100", TotalGPUs: int32(len(activePhases))}},
		Status:     v1.RunStatus{Phase: RunPhaseRunning},
	}
	key := keys.NamespacedKey(run.Namespace, run.Name)
	state := &ClusterState{Runs: map[string]*v1.Run{key: run}}

	mkLease := func(name, role string) v1.Lease {
		return v1.Lease{
			ObjectMeta: v1.ObjectMeta{Namespace: keys.DefaultNamespace, Name: name},
			Spec: v1.LeaseSpec{
				Owner:  "team",
				RunRef: v1.RunReference{Name: "job", Namespace: keys.DefaultNamespace},
				Slice:  v1.LeaseSlice{Nodes: []string{"n1#0"}, Role: role},
			},
		}
	}
	mkPod := func(name, role, phase string) binder.PodManifest {
		return binder.PodManifest{
			Namespace: keys.DefaultNamespace, Name: name, Phase: phase,
			Labels: map[string]string{binder.LabelRunName: "job", binder.LabelRunRole: role},
		}
	}
	for i, phase := range activePhases {
		state.Leases = append(state.Leases, mkLease(nameN("a", i), binder.RoleActive))
		state.Pods = append(state.Pods, mkPod(nameN("a", i), binder.RoleActive, phase))
	}
	if sparePhase != "" {
		state.Leases = append(state.Leases, mkLease("spare", binder.RoleSpare))
		state.Pods = append(state.Pods, mkPod("spare", binder.RoleSpare, sparePhase))
	}
	return state, key
}

func nameN(prefix string, i int) string { return prefix + string(rune('0'+i)) }

func TestRunCompletesWhenAllActivePodsSucceed(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	// Two active pods Succeeded; a spare still Running must not block completion.
	state, key := completionWorld([]string{"Succeeded", "Succeeded"}, "Running")

	c := NewRunController(state, &qsClock{now: now})
	if err := c.Reconcile(keys.DefaultNamespace, "job"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}

	run := state.Runs[key]
	if run.Status.Phase != RunPhaseComplete {
		t.Fatalf("expected Completed, got %s", run.Status.Phase)
	}
	for i := range state.Leases {
		if !state.Leases[i].Status.Closed {
			t.Errorf("lease %s left open on completion", state.Leases[i].Name)
		}
		if state.Leases[i].Status.Closed && state.Leases[i].Status.ClosureReason != "Completed" {
			t.Errorf("lease %s closed with reason %q, want Completed", state.Leases[i].Name, state.Leases[i].Status.ClosureReason)
		}
	}
	if len(state.Pods) != 0 {
		t.Errorf("expected the run's pods to be dropped, %d remain", len(state.Pods))
	}
}

func TestRunStaysRunningUntilAllPodsSucceed(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	// One active pod still Running: not complete.
	state, key := completionWorld([]string{"Succeeded", "Running"}, "")

	c := NewRunController(state, &qsClock{now: now})
	if err := c.Reconcile(keys.DefaultNamespace, "job"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := state.Runs[key].Status.Phase; got != RunPhaseRunning {
		t.Fatalf("expected still Running, got %s", got)
	}
	for i := range state.Leases {
		if state.Leases[i].Status.Closed {
			t.Errorf("lease %s closed prematurely", state.Leases[i].Name)
		}
	}
}

// A single pod failure must neither complete nor fail the run (owner decision).
func TestSinglePodFailureDoesNotFailOrCompleteRun(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	state, key := completionWorld([]string{"Succeeded", "Failed"}, "")

	c := NewRunController(state, &qsClock{now: now})
	if err := c.Reconcile(keys.DefaultNamespace, "job"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := state.Runs[key].Status.Phase; got != RunPhaseRunning {
		t.Fatalf("a pod Failure must leave the run Running, got %s", got)
	}
}
