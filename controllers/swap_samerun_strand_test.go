package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// #60: HandleNodeFailure pass 2 finds a same-run lease occupying the spare's exact
// slots and clears it "ReclaimedBySpare". It used to close the lease and drop NO
// pod — a ledger-only eviction that stranded the pod on the swap target: the pod
// kept its real nvidia.com/gpu claim on the spare's node, so the swap pod (hard-
// pinned there) could never bind, and INV-LEASE-HAS-POD stayed green because it is
// coarse. Both planes must drop together (fail-closed, like reclaimSquatter).
//
// Reachable because slot strings use chunk-local ordinals (binder.assign /
// admission.PodLeaseWithRole both reset to #0 per chunk), so a second same-run
// lease landing a chunk on the spare's node aliases node-b#0/#1.
func TestSameRunReclaimDropsTheStrandedPod(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 2, now)},
		Leases: []v1.Lease{
			// The base gang: active on node-a (about to fail), spare on node-b.
			nfLeaseGroup("active", "run", "org:ai:team", "team", "0", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLeaseGroup("spare", "run", "org:ai:team", "team", "0", []string{"node-b#0", "node-b#1"}, binder.RoleSpare, now),
			// A leftover open same-run lease on the spare's EXACT slots (slot-string
			// aliased). Pass 2 will clear it — and must take its pod with it.
			nfLeaseGroup("stale", "run", "org:ai:team", "team", "1", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
	}
	mirrorPods(state) // one pod per open lease: active-pod, spare-pod, stale-pod
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}

	// The stale lease is cleared in the ledger...
	if closed, reason := closureOf(state, "stale"); !closed || reason != "ReclaimedBySpare" {
		t.Fatalf("the stale same-run lease on the spare's slots must be reclaimed: closed=%v reason=%q", closed, reason)
	}
	// ...and its pod must be gone with it. Before the fix it was stranded on node-b,
	// holding the GPUs the swap pod needs.
	for _, p := range state.Pods {
		if p.Name == "stale-pod" {
			t.Fatalf("the reclaimed lease's pod (%s on %s) was stranded — the ledger freed the slot the container still holds",
				p.Name, p.NodeName)
		}
	}
	// The swap still proceeds: the spare is consumed and the run stays Running.
	if closed, reason := closureOf(state, "spare"); !closed || reason != "Swap" {
		t.Errorf("the spare must be promoted for the swap: closed=%v reason=%q", closed, reason)
	}
	if run := state.Runs["default/run"]; run.Status.Phase != RunPhaseRunning {
		t.Errorf("the swap must proceed, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
}
