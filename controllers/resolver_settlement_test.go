package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/resolver"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// Regressions for the three defects pkg/invariant's oracle and Fable's reading of
// applyResolution turned up together. None of them had a test. Two of them had no
// symptom either: the run reported a healthy phase while the ledger quietly went
// on charging a budget for GPUs nobody was using.
//
// Every test here drives the REAL resolver rather than a hand-built
// resolver.Result, because the load-bearing question in each case is whether the
// resolver can actually produce the input that breaks applyResolution.

// rsNode builds a node in a named fabric domain, so a run's active gang and its
// spare can be made to sit in DIFFERENT scoping domains.
func rsNode(name, fabric string, gpus int) topology.SourceNode {
	return topology.SourceNode{
		Name: name,
		Labels: map[string]string{
			topology.LabelRegion:       "us-west",
			topology.LabelCluster:      "cluster-a",
			topology.LabelFabricDomain: fabric,
			topology.LabelGPUFlavor:    "H100-80GB",
		},
		GPUs: gpus,
	}
}

// A resolver cut that ends a run must release every lease that run still holds.
//
// applyResolution's terminal branch set Phase=Failed and returned. failRun and
// HandleNodeFailure both sweep; this one did not. The resolver only sees leases
// inside the reservation's scope (resolver.leaseInScope), so a run whose spare
// lies in a different fabric domain has that spare filtered out of the cut — and
// the run was ended while the spare stayed open forever, charging its budget and
// holding healthy GPUs. The immortal-lease class, reached by a third door.
func TestResolverEndingARunReleasesTheLeasesItStillHolds(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	runKey := keys.NamespacedKey("default", "victim")

	state := &ClusterState{
		Nodes: []topology.SourceNode{
			rsNode("node-a", "island-a", 4), // the active gang
			rsNode("node-b", "island-b", 4), // the spare, in another domain
		},
		Budgets: []v1.Budget{nfBudget("team-budget", "org:ai:team")},
		Runs:    map[string]*v1.Run{runKey: nfRun("victim", "org:ai:team", 1, now)},
		Leases: []v1.Lease{
			nfLeaseGroup("victim-active", "victim", "org:ai:team", "team-budget", "0",
				[]string{"node-a#0"}, binder.RoleActive, now),
			nfLeaseGroup("victim-spare", "victim", "org:ai:team", "team-budget", "0",
				[]string{"node-b#0"}, binder.RoleSpare, now),
		},
	}
	c := NewRunController(state, runClock{now: now})

	// The active must derive a FUNDED class, or reclaimUnfunded takes it and the
	// run lands in the Pending "reclaimed" branch instead of the terminal one.
	ev := c.evaluate(now)
	if class, ok := ev.Class(&state.Leases[0]); !ok || class == funding.ClassUnfunded {
		t.Fatalf("setup: the active lease must be funded, got class=%v ok=%v", class, ok)
	}

	// Scope pinned to the active gang's domain — exactly what activateReservation
	// passes from reservation.Spec.IntendedSlice.Domain.
	resolution, err := resolver.Resolve(resolver.Input{
		Deficit:    1,
		Flavor:     "H100-80GB",
		Scope:      map[string]string{topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a", topology.LabelFabricDomain: "island-a"},
		SeedSource: "resolver-settlement",
		Now:        now,
		Nodes:      state.Nodes,
		Leases:     activeLeasePointers(state.Leases),
		Runs:       state.Runs,
		Evaluation: ev,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	for _, a := range resolution.Actions {
		if a.Lease.Name == "victim-spare" {
			t.Fatalf("setup broken: the resolver saw the out-of-scope spare; the scope filter no longer holds")
		}
	}
	if len(resolution.Actions) == 0 {
		t.Fatalf("setup broken: the resolver cut nothing, so the terminal branch is never reached")
	}

	c.applyResolution(resolution, now)

	run := state.Runs[runKey]
	if run.Status.Phase != RunPhaseFailed {
		t.Fatalf("a run whose whole gang was cut is Failed, got %q (%s)", run.Status.Phase, run.Status.Message)
	}
	if closed, _ := closureOf(state, "victim-active"); !closed {
		t.Errorf("the lottery's own victim must be closed")
	}
	// The point of the test.
	closed, reason := closureOf(state, "victim-spare")
	if !closed {
		t.Fatalf("the spare of a run the resolver ENDED is still open: it charges the budget " +
			"and holds healthy GPUs forever, and nothing reconciles a terminal run")
	}
	if reason != "RunFailed" {
		t.Errorf("spare closed with %q, want RunFailed", reason)
	}

	// And it stays closed: no later reconcile resurrects it.
	for i := 0; i < 4; i++ {
		c.Clock = runClock{now: now.Add(time.Duration(i+1) * time.Hour)}
		_ = c.Reconcile("default", "victim")
		if closed, _ := closureOf(state, "victim-spare"); !closed {
			t.Fatalf("reconcile #%d reopened the spare", i+1)
		}
	}
}

// fixedWidthTwoGroups: 4 GPUs, no Malleable, two groups of 2.
// minRunnableGPUs == 4: "start together or not at all".
func fixedWidthTwoGroups(now time.Time) *ClusterState {
	return &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 4, now)},
		Leases: []v1.Lease{
			nfLeaseGroup("g0", "run", "org:ai:team", "team", "0", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLeaseGroup("g1", "run", "org:ai:team", "team", "1", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
	}
}

// The lottery cuts group-by-group, and its all-or-nothing guard protects only
// MALLEABLE runs. So a deficit smaller than a fixed-width run's width cuts a
// STRICT SUBSET of its groups, and applyResolution used to stamp the survivor
// "Running" on the strength of `active > 0`.
//
// A distributed training job missing half its ranks makes no progress. Nothing
// repairs it either: reconcileElasticRun returns immediately for a fixed-width
// run, and topUpActiveGang is only reachable before Running. It was wedged
// forever, reporting healthy, charging a budget for its surviving ranks.
func TestResolverNeverReportsAFixedWidthGangRunningBelowItsWidth(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := fixedWidthTwoGroups(now)
	run := state.Runs["default/run"]
	c := NewRunController(state, runClock{now: now})

	if got := minRunnableGPUs(run); got != 4 {
		t.Fatalf("setup: a fixed-width 4-GPU run must require all 4, got %d", got)
	}

	// Deficit of exactly one group's worth.
	resolution, err := resolver.Resolve(resolver.Input{
		Deficit:    2,
		Flavor:     "H100-80GB",
		Now:        now,
		SeedSource: "resolver-settlement",
		Nodes:      state.Nodes,
		Leases:     activeLeasePointers(state.Leases),
		Runs:       state.Runs,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	if len(resolution.Actions) != 1 {
		t.Fatalf("setup: expected the lottery to cut exactly one group, got %d actions", len(resolution.Actions))
	}

	c.applyResolution(resolution, now)

	if got := baseGangGPUsForRun("default/run", state.Leases); got >= 4 {
		t.Fatalf("setup: the run should be below its width after the cut, holds %d", got)
	}
	if run.Status.Phase == RunPhaseRunning {
		t.Fatalf("a fixed-width gang holding %d of 4 GPUs reported Running (%q): "+
			"start together or not at all", baseGangGPUsForRun("default/run", state.Leases), run.Status.Message)
	}
	if run.Status.Phase != RunPhaseFailed {
		t.Fatalf("a fixed-width gang cut below its width is Failed, got %q", run.Status.Phase)
	}
	// Failing it must also return the capacity the deficit was chasing.
	if closed, reason := closureOf(state, "g0"); !closed || reason != "RunFailed" {
		if closed, reason = closureOf(state, "g1"); !closed || reason != "RunFailed" {
			t.Errorf("the surviving group's lease outlived the failed run: closed=%v reason=%q", closed, reason)
		}
	}
}

// The converse, and the reason the gate is minRunnableGPUs rather than the run's
// full width: a MALLEABLE run shrunk to its declared minimum is running, not
// broken. Demote-not-kill. Getting this wrong would turn every elastic shrink
// into a terminal failure.
func TestResolverKeepsAMalleableRunRunningAtItsMinimum(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := fixedWidthTwoGroups(now)
	run := state.Runs["default/run"]
	run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 2, MaxTotalGPUs: 4, StepGPUs: 2}
	c := NewRunController(state, runClock{now: now})

	if got := minRunnableGPUs(run); got != 2 {
		t.Fatalf("setup: a malleable run's minimum is MinTotalGPUs, got %d", got)
	}

	resolution, err := resolver.Resolve(resolver.Input{
		Deficit:    2,
		Flavor:     "H100-80GB",
		Now:        now,
		SeedSource: "resolver-settlement",
		Nodes:      state.Nodes,
		Leases:     activeLeasePointers(state.Leases),
		Runs:       state.Runs,
	})
	if err != nil {
		t.Fatalf("resolve: %v", err)
	}
	c.applyResolution(resolution, now)

	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("a malleable run shrunk to its minimum keeps running, got %q (%s)", run.Status.Phase, run.Status.Message)
	}
	if got := baseGangGPUsForRun("default/run", state.Leases); got != 2 {
		t.Errorf("expected the run to hold its 2-GPU minimum, holds %d", got)
	}
}

// Reclaiming an unfunded squatter is an eviction, and an eviction has to happen
// in BOTH planes. Closing its lease alone left the victim's container running on
// the exact node#ordinal the swap then targeted — the ledger said the slot was
// free, the kubelet disagreed — and left the victim reporting Running forever,
// holding nothing, a zombie no reconcile would ever visit.
func TestReclaimedSquatterIsEvictedInBothPlanes(t *testing.T) {
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
		// The squatter's container. This is the claim the ledger cannot see.
		Pods: []binder.PodManifest{{
			Namespace: "default", Name: "filler-g0-0", NodeName: "node-b", GPUs: 2,
			Labels: map[string]string{
				binder.LabelRunName:    "filler",
				binder.LabelGroupIndex: "0",
				binder.LabelRunRole:    binder.RoleActive,
			},
		}},
	}
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}

	if closed, reason := closureOf(state, "filler"); !closed || reason != "ReclaimedBySpare" {
		t.Fatalf("the unfunded squatter's lease must close: closed=%v reason=%q", closed, reason)
	}
	for _, pod := range state.Pods {
		if pod.Labels[binder.LabelRunName] == "filler" {
			t.Errorf("the squatter's pod %s still occupies node-b: the swap is about to place a rank "+
				"on the same node#ordinal slots the ledger now calls free", pod.Name)
		}
	}
	filler := state.Runs["default/filler"]
	if filler.Status.Phase == RunPhaseRunning {
		t.Errorf("the reclaimed squatter still reports Running while holding no lease: a zombie")
	}
	if filler.Status.Phase != RunPhasePending {
		t.Errorf("unfunded work is demoted, not killed (quota-semantics R14): want Pending, got %q", filler.Status.Phase)
	}
	// The swap itself must still have happened.
	if closed, reason := closureOf(state, "spare"); !closed || reason != "Swap" {
		t.Errorf("the swap proceeds: spare closed=%v reason=%q", closed, reason)
	}
	if run := state.Runs["default/run"]; run.Status.Phase != RunPhaseRunning {
		t.Errorf("the swap keeps the covered run Running, got %s (%s)", run.Status.Phase, run.Status.Message)
	}
}
