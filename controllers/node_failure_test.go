package controllers

import (
	"errors"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// R21 + R22 + R25 all live in HandleNodeFailure. These pin each one.

func nodeFailureNodes() []topology.SourceNode {
	mk := func(name string) topology.SourceNode {
		return topology.SourceNode{Name: name, GPUs: 4, Labels: map[string]string{
			topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a",
			topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB",
		}}
	}
	return []topology.SourceNode{mk("node-a"), mk("node-b")}
}

func nfLease(name, run, owner, budget string, slots []string, role string, now time.Time) v1.Lease {
	return v1.Lease{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default",
			Labels: map[string]string{binder.LabelRunName: run, binder.LabelGroupIndex: "0", binder.LabelRunRole: role}},
		Spec: v1.LeaseSpec{
			Owner:          owner,
			RunRef:         v1.RunReference{Name: run, Namespace: "default"},
			Slice:          v1.LeaseSlice{Nodes: slots, Role: role},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(now.Add(-time.Minute))},
			PaidByBudget:   budget,
			PaidByEnvelope: "west",
			Reason:         "Start",
		},
	}
}

func nfRun(name, owner string, gpus int32, now time.Time) *v1.Run {
	return &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: v1.NewTime(now.Add(-time.Hour))},
		Spec:       v1.RunSpec{Owner: owner, Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: gpus}},
		Status:     v1.RunStatus{Phase: RunPhaseRunning},
	}
}

func nfBudget(name, owner string) v1.Budget {
	return v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: name},
		Spec: v1.BudgetSpec{Owner: owner, Envelopes: []v1.BudgetEnvelope{{
			Name: "west", Flavor: "H100-80GB", Concurrency: 16,
			Selector: map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
		}}},
	}
}

func closureOf(state *ClusterState, name string) (bool, string) {
	for i := range state.Leases {
		if state.Leases[i].Name == name {
			return state.Leases[i].Status.Closed, state.Leases[i].Status.ClosureReason
		}
	}
	return false, "<missing>"
}

// R25 — a node holding nothing but a SPARE used to match no lease at all: the
// loop skipped spares before it tested the node, so it returned "no active lease
// found", the caller swallowed that by string-match, and the spare's lease stayed
// open forever, charging a budget for a node that no longer exists.
func TestNodeFailureClosesASpareOnlyNode(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 4, now)},
		Leases: []v1.Lease{
			nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLease("spare", "run", "org:ai:team", "team", []string{"node-b#0", "node-b#1"}, binder.RoleSpare, now),
		},
	}
	c := NewRunController(state, runClock{now: now})

	// node-b holds only the spare.
	if err := c.HandleNodeFailure("node-b", now); err != nil {
		t.Fatalf("a spare-only node is a lease the engine must handle, got error: %v", err)
	}
	if closed, reason := closureOf(state, "spare"); !closed || reason != "NodeFailure" {
		t.Errorf("spare must close with NodeFailure, got closed=%v reason=%q", closed, reason)
	}
	if closed, _ := closureOf(state, "active"); closed {
		t.Errorf("the active lease on a healthy node must not be touched")
	}
	if run := state.Runs["default/run"]; run.Status.Phase != RunPhaseRunning {
		t.Errorf("losing a spare must not fail the run, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
}

// R25 — and when NO lease of any role names the node, say so with a typed
// sentinel. The caller used to string-match the message, which is exactly how the
// leaked spare stayed invisible.
func TestNodeFailureReturnsTypedSentinelWhenNoLeaseNamesTheNode(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 4, now)},
		Leases:  []v1.Lease{nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now)},
	}
	c := NewRunController(state, runClock{now: now})

	err := c.HandleNodeFailure("node-b", now)
	if err == nil {
		t.Fatalf("expected an error for a node no lease names")
	}
	if !errors.Is(err, ErrNoLeaseOnNode) {
		t.Errorf("callers must be able to use errors.Is; got %#v", err)
	}
}

// R22 — a run merely SHARING the failed-over node, on different GPUs, is not a
// conflict. The old sweep compared node names (nodeFromSlot discards the ordinal)
// and closed it anyway: a swap for run A silently killed run B's funded work.
func TestSwapLeavesACoLocatedRunOnOtherSlotsAlone(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team"), nfBudget("other", "org:ai:other")},
		Runs: map[string]*v1.Run{
			"default/run":       nfRun("run", "org:ai:team", 2, now),
			"default/neighbour": nfRun("neighbour", "org:ai:other", 2, now),
		},
		Leases: []v1.Lease{
			nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLease("spare", "run", "org:ai:team", "team", []string{"node-b#0", "node-b#1"}, binder.RoleSpare, now),
			// Same NODE as the spare, different GPUs. Not a conflict.
			nfLease("neighbour", "neighbour", "org:ai:other", "other", []string{"node-b#2", "node-b#3"}, binder.RoleActive, now),
		},
	}
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}
	if closed, reason := closureOf(state, "neighbour"); closed {
		t.Errorf("a run sharing the node on DIFFERENT slots must never be reclaimed; it closed with %q", reason)
	}
	if closed, reason := closureOf(state, "spare"); !closed || reason != "Swap" {
		t.Errorf("the spare must be promoted: closed=%v reason=%q", closed, reason)
	}
	if run := state.Runs["default/run"]; run.Status.Phase != RunPhaseRunning {
		t.Errorf("the swap must proceed, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
}

// R22 — an exact-slot conflict with a FUNDED run is not ours to resolve. Evicting
// it is the resolver's call, which ranks by funding class. Decline the swap
// instead; the run re-admits through the normal, funding-aware route.
func TestSwapDeclinesRatherThanEvictAFundedRunOnTheSpareSlots(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team"), nfBudget("other", "org:ai:other")},
		Runs: map[string]*v1.Run{
			"default/run":      nfRun("run", "org:ai:team", 2, now),
			"default/squatter": nfRun("squatter", "org:ai:other", 2, now),
		},
		Leases: []v1.Lease{
			nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLease("spare", "run", "org:ai:team", "team", []string{"node-b#0", "node-b#1"}, binder.RoleSpare, now),
			// Exactly the spare's slots, and FUNDED (owns budget "other").
			nfLease("squatter", "squatter", "org:ai:other", "other", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
	}
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}
	if closed, reason := closureOf(state, "squatter"); closed {
		t.Fatalf("a FUNDED run must never be evicted by a swap; it closed with %q", reason)
	}
	if closed, _ := closureOf(state, "spare"); closed {
		t.Errorf("the spare must not be consumed when its slots are unavailable")
	}
	if closed, reason := closureOf(state, "active"); !closed || reason != "NodeFailure" {
		t.Errorf("the failed slice still closes: closed=%v reason=%q", closed, reason)
	}
	// No spare could be used, so the run takes the no-spare path.
	if run := state.Runs["default/run"]; run.Status.Phase != RunPhaseFailed {
		t.Errorf("with no usable spare the run fails (no checkpoint set), got %s", run.Status.Phase)
	}
}

// ...but an UNFUNDED squatter on the exact slots is opportunistic by definition:
// it runs on capacity nobody paid for, and the swap reclaims it.
func TestSwapReclaimsAnUnfundedSquatterOnTheSpareSlots(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs: map[string]*v1.Run{
			"default/run":    nfRun("run", "org:ai:team", 2, now),
			"default/filler": nfRun("filler", "org:ai:nobody", 2, now),
		},
		Leases: []v1.Lease{
			nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLease("spare", "run", "org:ai:team", "team", []string{"node-b#0", "node-b#1"}, binder.RoleSpare, now),
			// Exact slots, no budget of its own -> derives Unfunded.
			nfLease("filler", "filler", "org:ai:nobody", "", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
	}
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}
	if closed, reason := closureOf(state, "filler"); !closed || reason != "ReclaimedBySpare" {
		t.Errorf("an unfunded squatter on the exact slots is reclaimable: closed=%v reason=%q", closed, reason)
	}
	if closed, reason := closureOf(state, "spare"); !closed || reason != "Swap" {
		t.Errorf("the swap proceeds: spare closed=%v reason=%q", closed, reason)
	}
	if run := state.Runs["default/run"]; run.Status.Phase != RunPhaseRunning {
		t.Errorf("the swap must keep the run Running, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
}

// A spare that sits ON the failed node is a casualty, not a swap target:
// emitSwapPod hard-targets the spare's own node, so promoting it would place the
// replacement rank on the corpse.
func TestSwapNeverTargetsASpareOnTheFailedNode(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 2, now)},
		Leases: []v1.Lease{
			nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			// The only spare is on the very node that failed.
			nfLease("spare", "run", "org:ai:team", "team", []string{"node-a#2", "node-a#3"}, binder.RoleSpare, now),
		},
	}
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}
	if closed, reason := closureOf(state, "spare"); !closed || reason != "NodeFailure" {
		t.Errorf("a spare on the failed node dies with it: closed=%v reason=%q", closed, reason)
	}
	for i := range state.Pods {
		if state.Pods[i].Annotations[binder.AnnotationSwapNode] == "node-a" {
			t.Fatalf("a swap pod was targeted at the failed node %q", "node-a")
		}
	}
	if run := state.Runs["default/run"]; run.Status.Phase != RunPhaseFailed {
		t.Errorf("no usable spare remains, so the run fails, got %s", run.Status.Phase)
	}
}
