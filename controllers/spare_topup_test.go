package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/pack"
)

// #91: emitSparePods topped spares up by a raw COUNT of survivors and re-emitted
// indices existing..count. When a spare goes missing OUT OF ORDER — a low index
// gone while a higher sibling survives — that rebuilds the survivor's index instead
// of the missing one: two spare pods (then two leases) of ONE name, which
// CheckTransition reads as a closure-reason rewrite (INV-CLOSED-MONOTONE). Presence
// must be keyed by NAME, so only the genuinely-missing index is refilled — the same
// fix emitCohortPods already carries for actives.
func TestSpareTopUpRefillsMissingIndexNotADuplicate(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "job", Namespace: keys.DefaultNamespace}}
	plan := pack.Plan{
		TotalSpares: 2,
		Groups: []pack.GroupPlacement{
			{GroupIndex: 0, SparePlacements: []pack.NodeAllocation{{Node: "node-a", GPUs: 1}}},
			{GroupIndex: 1, SparePlacements: []pack.NodeAllocation{{Node: "node-b", GPUs: 1}}},
		},
	}
	state := &ClusterState{Runs: map[string]*v1.Run{keys.NamespacedKey(run.Namespace, run.Name): run}}
	c := NewRunController(state, runClock{now: now})

	// Hold both spares: job-spare-0 and job-spare-1.
	if got := c.emitSparePods(run, plan, 1, "Start", nil); got != 2 {
		t.Fatalf("initial spare hold created %d pods, want 2", got)
	}
	// Lose the LOW-index spare out of order (an eviction / wrong-sibling removal).
	dropPodByName(state, sparePodName(run, 0))

	// Top up again: it must refill the MISSING index 0, not duplicate the survivor 1.
	created := c.emitSparePods(run, plan, 1, "Start", nil)
	if created != 1 {
		t.Fatalf("top-up created %d spare pods, want 1 (only the missing index 0)", created)
	}

	names := map[string]int{}
	spares := 0
	for i := range state.Pods {
		p := &state.Pods[i]
		if p.Labels[binder.LabelRunRole] == binder.RoleSpare && p.Labels[binder.LabelRunName] == "job" {
			names[p.Name]++
			spares++
		}
	}
	if spares != 2 {
		t.Fatalf("expected exactly 2 spare pods after refill, got %d", spares)
	}
	for name, n := range names {
		if n != 1 {
			t.Fatalf("spare pod %q exists %d times — a duplicate-named spare, the #91 reaper (two leases will collide on one name)", name, n)
		}
	}
	if names[sparePodName(run, 0)] != 1 {
		t.Fatalf("the missing spare index 0 was not refilled; names=%v", names)
	}
}

func dropPodByName(state *ClusterState, name string) {
	kept := state.Pods[:0]
	for _, p := range state.Pods {
		if p.Name != name {
			kept = append(kept, p)
		}
	}
	state.Pods = kept
}
