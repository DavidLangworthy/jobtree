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

// TLA-found (specs/NodeFailure.tla, NodeFailureConsumedCount.cfg →
// ConsumedSpareStaysConsumed): the #91 name-keyed presence fix stopped duplicate
// rebuilds, but the loop still ran `0..(declared-consumed)-1`, truncating the index
// range from the TOP. A swap consumes an ARBITRARY index — findSpareLease matches by
// GROUP and sparePlacements walks groups ascending, so a failure on group 0 consumes
// spare index 0, the LOW one. With one low index consumed, count drops to 1 and the
// loop visits only i==0, whose pod is gone (swap removed it) — so it re-emitted the
// CONSUMED spare, over-provisioning funded capacity the swap already re-used, while a
// genuinely-missing HIGH index past the bound would never be refilled. Fix: retire the
// consumed indices BY NAME (Phase 4 stamps the pod name on every lease) and scan the
// full declared range, capped at `declared - consumed` live spares.
func TestSpareTopUpDoesNotReprovisionSwapConsumedLowIndex(t *testing.T) {
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

	// Hold both spares, then mint their leases the way the plugin does — stamping
	// each lease with its pod name (Phase 4), so a later swap-close names the exact
	// index it retired.
	if got := c.emitSparePods(run, plan, 1, "Start", nil); got != 2 {
		t.Fatalf("initial spare hold created %d pods, want 2", got)
	}

	// Swap consumes GROUP 0's spare — the LOW index 0: close its lease reason "Swap"
	// (carrying the consumed pod's name) and remove its pod, as HandleNodeFailure does.
	consumed := v1.GPULease{
		Spec: v1.GPULeaseSpec{
			RunRef: v1.RunReference{Name: run.Name, Namespace: run.Namespace},
			Slice:  v1.GPULeaseSlice{Nodes: []string{"node-a#0"}, Role: binder.RoleSpare},
		},
		Status: v1.GPULeaseStatus{Closed: true, ClosureReason: "Swap"},
	}
	consumed.Annotations = map[string]string{binder.AnnotationPodName: sparePodName(run, 0)}
	state.Leases = append(state.Leases, consumed)
	dropPodByName(state, sparePodName(run, 0))

	// Top up: nothing is genuinely missing (index 1 survives; index 0 is retired), so
	// the consumed low index must NOT come back.
	if created := c.emitSparePods(run, plan, 1, "Start", nil); created != 0 {
		t.Fatalf("top-up created %d spare pods, want 0 — a swap-consumed low index was re-provisioned", created)
	}
	present := map[string]bool{}
	for i := range state.Pods {
		p := &state.Pods[i]
		if p.Labels[binder.LabelRunRole] == binder.RoleSpare && p.Labels[binder.LabelRunName] == "job" {
			present[p.Name] = true
		}
	}
	if present[sparePodName(run, 0)] {
		t.Fatalf("the swap-consumed spare index 0 was re-provisioned (ConsumedSpareStaysConsumed)")
	}
	if !present[sparePodName(run, 1)] {
		t.Fatalf("the surviving spare index 1 was dropped")
	}
	if len(present) != 1 {
		t.Fatalf("expected exactly 1 live spare after a 2→1 swap consumption, got %d", len(present))
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
