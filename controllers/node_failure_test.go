package controllers

import (
	"errors"
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

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
	return nfLeaseGroup(name, run, owner, budget, "0", slots, role, now)
}

func nfLeaseGroup(name, run, owner, budget, group string, slots []string, role string, now time.Time) v1.Lease {
	return v1.Lease{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default",
			Labels: map[string]string{binder.LabelRunName: run, binder.LabelGroupIndex: group, binder.LabelRunRole: role}},
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
		// 2 GPUs, and the active lease holds exactly 2. A fixture whose Run asks
		// for more width than its leases hold is a Running run below its minimum:
		// an illegal state pkg/invariant rejects, and one the engine never builds.
		Runs: map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 2, now)},
		Leases: []v1.Lease{
			nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLease("spare", "run", "org:ai:team", "team", []string{"node-b#0", "node-b#1"}, binder.RoleSpare, now),
		},
	}
	mirrorPods(state)
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
		// 1 GPU, held by the one 1-slot active lease: see the note in
		// TestNodeFailureClosesASpareOnlyNode on why the widths must agree.
		Runs:   map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 1, now)},
		Leases: []v1.Lease{nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now)},
	}
	mirrorPods(state)
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
	mirrorPods(state)
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
	mirrorPods(state)
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}
	if closed, reason := closureOf(state, "squatter"); closed {
		t.Fatalf("a FUNDED run must never be evicted by a swap; it closed with %q", reason)
	}
	if closed, reason := closureOf(state, "active"); !closed || reason != "NodeFailure" {
		t.Errorf("the failed slice still closes: closed=%v reason=%q", closed, reason)
	}
	// The declined spare must be RELEASED, not stranded. Leaving it open charges the
	// run's budget forever for GPUs it can never use, and keeps the ledger marking
	// node-b occupied — the immortal-lease class R25 exists to kill. Nothing
	// downstream closes a terminal run's leases, so if this one survives, it
	// survives until someone deletes the Run object.
	//
	// An earlier version of this very test asserted the opposite ("the spare must
	// not be consumed"), which is how the leak shipped past a green suite.
	if closed, _ := closureOf(state, "spare"); !closed {
		t.Errorf("the declined spare must be released, not left open charging the budget")
	}
	// No spare could be used, so the run takes the no-spare path.
	if run := state.Runs["default/run"]; run.Status.Phase != RunPhaseFailed {
		t.Errorf("with no usable spare the run fails (no checkpoint set), got %s", run.Status.Phase)
	}
	// ...and a failed run holds no open leases at all.
	for _, lease := range state.Leases {
		if lease.Spec.RunRef.Name != "run" {
			continue
		}
		if !lease.Status.Closed {
			t.Errorf("failed run still holds open lease %s (%v)", lease.Name, lease.Spec.Slice.Nodes)
		}
	}
}

// A run may hold several active leases on the failed node. Each group reaches its
// own verdict, but they share one Status.Phase, and it used to be written per group
// — so the LAST group in c.State.Leases won. A run with one group swapping to a
// spare and another group dead without coverage reported whichever came last: a run
// with a dead, uncovered rank could report Running.
//
// The phase must be the worst outcome, whatever the slice order.
func TestRunPhaseDoesNotDependOnLeaseOrder(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	// group 0 can swap (it holds a spare on node-b); group 1 cannot. The gang is
	// dead either way.
	build := func(order string) *ClusterState {
		active0 := nfLeaseGroup("active-0", "run", "org:ai:team", "team", "0", []string{"node-a#0"}, binder.RoleActive, now)
		spare0 := nfLeaseGroup("spare-0", "run", "org:ai:team", "team", "0", []string{"node-b#0"}, binder.RoleSpare, now)
		active1 := nfLeaseGroup("active-1", "run", "org:ai:team", "team", "1", []string{"node-a#1"}, binder.RoleActive, now)

		leases := []v1.Lease{active0, spare0, active1}
		if order == "reversed" {
			leases = []v1.Lease{active1, spare0, active0}
		}
		return &ClusterState{
			Nodes:   nodeFailureNodes(),
			Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
			Runs:    map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 2, now)},
			Leases:  leases,
		}
	}

	for _, order := range []string{"natural", "reversed"} {
		state := build(order)
		c := NewRunController(state, runClock{now: now})
		if err := c.HandleNodeFailure("node-a", now); err != nil {
			t.Fatalf("%s: handle node failure: %v", order, err)
		}
		run := state.Runs["default/run"]
		if run.Status.Phase != RunPhaseFailed {
			t.Errorf("%s order: phase = %s (%q); group 1 lost its rank with no spare, so the run is Failed regardless of lease order",
				order, run.Status.Phase, run.Status.Message)
		}
		// ...and a Failed gang releases everything, including the spare on the
		// healthy node that group 0 was about to swap onto.
		for _, lease := range state.Leases {
			if !lease.Status.Closed {
				t.Errorf("%s order: failed run still holds open lease %s on %v", order, lease.Name, lease.Spec.Slice.Nodes)
			}
		}
	}
}

// The confirmed defect from the R21/R22/R25 adversarial review, as a standing test:
// a run that declines the swap must not strand its own spare. The reviewer proved
// that 20 reconciles over 20 hours never closed it, and `funding.Evaluate` kept
// deriving it as Owned — a budget charged forever for GPUs nobody used.
func TestDecliningTheSwapNeverStrandsTheRunsOwnSpare(t *testing.T) {
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
			nfLease("squatter", "squatter", "org:ai:other", "other", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
	}
	mirrorPods(state)
	c := NewRunController(state, runClock{now: now})
	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}

	// The lease is closed...
	closed, reason := closureOf(state, "spare")
	if !closed {
		t.Fatalf("the declined spare is still open")
	}
	if reason != "SwapDeclined" && reason != "RunFailed" {
		t.Errorf("spare closed with reason %q; want it attributed to the declined swap", reason)
	}

	// ...and the funding derivation, which is what actually charges the budget,
	// agrees. `ev.Class` is open-leases-only, so a closed lease has no class at all.
	ev := c.evaluate(now.Add(10 * time.Hour))
	for i := range state.Leases {
		if state.Leases[i].Name != "spare" {
			continue
		}
		if _, classified := ev.Class(&state.Leases[i]); classified {
			t.Errorf("the released spare still derives a funding class ten hours later: it is charging the budget")
		}
	}
}

// The same leak, on the path where nothing else can catch it.
//
// A run with spec.runtime.checkpoint parks Pending rather than failing, so the
// terminal "release everything a failed run holds" sweep never runs. If the decline
// path does not release the spare itself, the lease survives: the reviewer drove 20
// reconciles over 20 hours and it stayed open and Owned the whole time.
func TestDecliningTheSwapReleasesTheSpareEvenWhenTheRunParksInCheckpointGrace(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	run := nfRun("run", "org:ai:team", 2, now)
	// spec.runtime.checkpoint says the workload can be safely requeued, so the run
	// parks Pending instead of failing.
	run.Spec.Runtime = &v1.RunRuntime{Checkpoint: metav1.Duration{Duration: 30 * time.Minute}}

	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team"), nfBudget("other", "org:ai:other")},
		Runs: map[string]*v1.Run{
			"default/run":      run,
			"default/squatter": nfRun("squatter", "org:ai:other", 2, now),
		},
		Leases: []v1.Lease{
			nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLease("spare", "run", "org:ai:team", "team", []string{"node-b#0", "node-b#1"}, binder.RoleSpare, now),
			nfLease("squatter", "squatter", "org:ai:other", "other", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
	}
	mirrorPods(state)
	c := NewRunController(state, runClock{now: now})
	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}

	if got := state.Runs["default/run"].Status.Phase; got != RunPhasePending {
		t.Fatalf("setup: a checkpointable run parks Pending, got %s", got)
	}
	closed, reason := closureOf(state, "spare")
	if !closed {
		t.Fatalf("the declined spare leaked: a Pending run is never swept, so nothing else will ever close it")
	}
	if reason != "SwapDeclined" {
		t.Errorf("spare closed with %q, want SwapDeclined (the decline path must release it directly)", reason)
	}
	if closed, _ := closureOf(state, "squatter"); closed {
		t.Errorf("the funded squatter must not be evicted")
	}
}

// failRun used to *assert* that a failing run holds no leases ("there is nothing to
// close") and close nothing. Nothing enforced it. A run parked in checkpoint grace
// after one group lost its node still holds its OTHER groups' leases on healthy
// nodes; when the grace expires, failRun is what runs. If it closes nothing, those
// leases charge the run's budget until someone deletes the Run object.
func TestFailingARunReleasesEveryLeaseItStillHolds(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	run := nfRun("run", "org:ai:team", 2, now)
	run.Status.Phase = RunPhasePending

	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/run": run},
		Leases: []v1.Lease{
			// group 0 already lost node-a and was closed by HandleNodeFailure.
			func() v1.Lease {
				l := nfLeaseGroup("active-0", "run", "org:ai:team", "team", "0", []string{"node-a#0"}, binder.RoleActive, now)
				CloseLease(&l, "NodeFailure", now)
				return l
			}(),
			// group 1 is still running on a healthy node, holding a live lease.
			nfLeaseGroup("active-1", "run", "org:ai:team", "team", "1", []string{"node-b#0"}, binder.RoleActive, now),
		},
	}
	c := NewRunController(state, runClock{now: now})

	c.failRun(run, "checkpoint grace expired without recovering capacity")

	if run.Status.Phase != RunPhaseFailed {
		t.Fatalf("setup: failRun must fail the run, got %s", run.Status.Phase)
	}
	closed, reason := closureOf(state, "active-1")
	if !closed {
		t.Errorf("the surviving group's lease on a healthy node outlived the failed run: it charges the budget forever")
	} else if reason != "RunFailed" {
		t.Errorf("lease closed with %q, want RunFailed", reason)
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
