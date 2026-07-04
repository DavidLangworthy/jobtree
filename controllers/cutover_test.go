package controllers

import (
	"testing"
	"time"

	"github.com/davidlangworthy/jobtree/pkg/admission"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// seedRunning simulates the scheduler plugin having already scheduled and
// funded a run: it mints the exact leases admission.Plan produces (the old
// in-controller commit) plus their pods and marks the run Running. Lifecycle
// tests (completion, elasticity, node-failure, reservation activation) start
// from a bound run this way, now that Reconcile itself emits unscheduled intent
// pods instead of minting (borrow-vs-build.md §9). It is the test-side stand-in
// for the plugin's PreBind mint + the controller's adoption flip.
func seedRunning(t *testing.T, state *ClusterState, runKey string, now time.Time) {
	t.Helper()
	run := state.Runs[runKey]
	if run == nil {
		t.Fatalf("seedRunning: run %s not found", runKey)
	}
	res, err := admission.Plan(admission.Input{
		Run:     run,
		Budgets: state.Budgets,
		Runs:    state.Runs,
		Leases:  state.Leases,
		Nodes:   state.Nodes,
		Now:     now,
	})
	if err != nil {
		t.Fatalf("seedRunning %s: %v", runKey, err)
	}
	state.Leases = append(state.Leases, res.Leases...)
	state.Pods = append(state.Pods, res.Pods...)
	run.Status.Phase = RunPhaseRunning
}

// activeIntentPods counts the unscheduled Active workload pods Reconcile emitted
// for a run (the width it is requesting from the scheduler).
func activeIntentPods(state *ClusterState, namespace, runName string) int {
	n := 0
	for i := range state.Pods {
		p := &state.Pods[i]
		if p.Namespace == namespace && p.Labels[binder.LabelRunName] == runName && p.Labels[binder.LabelRunRole] == binder.RoleActive {
			n++
		}
	}
	return n
}
