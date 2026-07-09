package controllers

import (
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/resolver"
)

// The metamorphic rail for playbook class 3, LAST-WRITER-WINS.
//
// A run's phase was once assigned by whichever lease the loop happened to visit
// last, so a gang with a dead, uncovered rank could report Running. The fix was
// not to sort the leases — a deterministic order that happens to give the right
// answer is still a coincidence. The fix was to make the fold COMMUTATIVE.
//
// This file is how we know it stayed that way. It does not check one alternative
// ordering; it checks EVERY ordering, and compares the whole outcome rather than
// the one field the last bug happened to corrupt. That is the difference between
// testing an instance and retiring a class.
//
// It also covers Go's randomized map iteration, which no permutation of a slice
// can reach: applyResolution folds over `affectedRuns`, a map.

// outcome is a total, order-insensitive fingerprint of everything an engine call
// is allowed to decide: every run's phase and message, and every lease's closure.
func outcome(state *ClusterState) string {
	var lines []string
	for key, run := range state.Runs {
		lines = append(lines, fmt.Sprintf("run %s phase=%s msg=%q", key, run.Status.Phase, run.Status.Message))
	}
	for i := range state.Leases {
		l := &state.Leases[i]
		lines = append(lines, fmt.Sprintf("lease %s closed=%t reason=%s", l.Name, l.Status.Closed, l.Status.ClosureReason))
	}
	for _, pod := range state.Pods {
		lines = append(lines, "pod "+pod.Namespace+"/"+pod.Name)
	}
	sort.Strings(lines)
	return strings.Join(lines, "\n")
}

// permutations enumerates every ordering of idx. n is small on purpose: the
// point is exhaustiveness, not scale.
func permutations(idx []int) [][]int {
	if len(idx) <= 1 {
		return [][]int{append([]int(nil), idx...)}
	}
	var out [][]int
	for i := range idx {
		rest := make([]int, 0, len(idx)-1)
		rest = append(rest, idx[:i]...)
		rest = append(rest, idx[i+1:]...)
		for _, p := range permutations(rest) {
			out = append(out, append([]int{idx[i]}, p...))
		}
	}
	return out
}

// A node failure's outcome must not depend on the order the leases happen to sit
// in state.Leases. Four leases, twenty-four orderings, one answer.
func TestNodeFailureOutcomeIsInvariantUnderLeaseOrder(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	// The scenario must make the two groups DISAGREE about the run's phase, or the
	// test cannot see an order-dependent fold at all: group 0 holds a spare and
	// swaps (Running), group 1 has no cover and dies (Failed). Under a
	// last-writer-wins fold the answer is whichever group the loop reached last.
	//
	// An earlier version of this fixture put a FUNDED squatter on the spare's
	// slots. That declined the swap, so both groups wrote Failed, no conflict ever
	// arose, and the test passed against a deliberately reintroduced
	// last-writer-wins bug. It was decorative. The squatter is unfunded now, so
	// the swap proceeds and the disagreement is real.
	build := func(order []int) *ClusterState {
		all := []v1.Lease{
			nfLeaseGroup("active-0", "run", "org:ai:team", "team", "0", []string{"node-a#0"}, binder.RoleActive, now),
			nfLeaseGroup("spare-0", "run", "org:ai:team", "team", "0", []string{"node-b#0"}, binder.RoleSpare, now),
			nfLeaseGroup("active-1", "run", "org:ai:team", "team", "1", []string{"node-a#1"}, binder.RoleActive, now),
			// No budget of its own -> derives Unfunded -> reclaimable.
			nfLeaseGroup("squatter", "filler", "org:ai:nobody", "", "0", []string{"node-b#0"}, binder.RoleActive, now),
		}
		leases := make([]v1.Lease, 0, len(all))
		for _, i := range order {
			leases = append(leases, all[i])
		}
		return &ClusterState{
			Nodes:   nodeFailureNodes(),
			Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
			Runs: map[string]*v1.Run{
				"default/run":    nfRun("run", "org:ai:team", 2, now),
				"default/filler": nfRun("filler", "org:ai:nobody", 1, now),
			},
			Leases: leases,
		}
	}

	var canonical string
	var canonicalOrder []int
	for _, order := range permutations([]int{0, 1, 2, 3}) {
		state := build(order)
		c := NewRunController(state, runClock{now: now})
		_ = c.HandleNodeFailure("node-a", now)

		got := outcome(state)
		if canonical == "" {
			canonical, canonicalOrder = got, order
			continue
		}
		if got != canonical {
			t.Fatalf("the outcome depends on the order of state.Leases, which is not part of the "+
				"specification.\norder %v:\n%s\n\norder %v:\n%s", canonicalOrder, canonical, order, got)
		}
	}
	if !strings.Contains(canonical, "phase=Failed") {
		t.Fatalf("setup: group 1 lost its rank with no cover, so the run is Failed in every order:\n%s", canonical)
	}
}

// applyResolution folds over `affectedRuns`, a Go map. Map iteration order is
// randomized per process, so an order-dependent fold here would not merely be
// wrong — it would be wrong intermittently, which is worse. No slice permutation
// can reach this; only repetition can.
func TestApplyResolutionOutcomeIsInvariantUnderMapIteration(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	build := func() (*ClusterState, resolver.Result) {
		state := &ClusterState{
			Nodes:   nodeFailureNodes(),
			Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
			Runs: map[string]*v1.Run{
				"default/a": nfRun("a", "org:ai:team", 2, now),
				"default/b": nfRun("b", "org:ai:team", 2, now),
				"default/c": nfRun("c", "org:ai:team", 2, now),
			},
			Leases: []v1.Lease{
				nfLeaseGroup("a0", "a", "org:ai:team", "team", "0", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
				nfLeaseGroup("b0", "b", "org:ai:team", "team", "0", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
				nfLeaseGroup("c0", "c", "org:ai:team", "team", "0", []string{"node-a#2", "node-a#3"}, binder.RoleActive, now),
			},
		}
		// Cut a and c entirely; leave b. Both cut runs go terminal and must
		// release everything, whatever order the map hands them to us in.
		res := resolver.Result{Seed: "0xdeadbeef", Actions: []resolver.Action{
			{Kind: resolver.ActionLottery, Lease: &state.Leases[0], Run: state.Runs["default/a"], GroupIndex: "0", GPUs: 2, Reason: "RandomPreempt(0xdeadbeef)"},
			{Kind: resolver.ActionLottery, Lease: &state.Leases[2], Run: state.Runs["default/c"], GroupIndex: "0", GPUs: 2, Reason: "RandomPreempt(0xdeadbeef)"},
		}}
		return state, res
	}

	var canonical string
	for i := 0; i < 64; i++ {
		state, res := build()
		c := NewRunController(state, runClock{now: now})
		c.applyResolution(res, now)

		got := outcome(state)
		if canonical == "" {
			canonical = got
			continue
		}
		if got != canonical {
			t.Fatalf("applyResolution's outcome varies with Go's randomized map iteration "+
				"(iteration %d).\nfirst:\n%s\n\nnow:\n%s", i, canonical, got)
		}
	}
	if !strings.Contains(canonical, "run default/b phase=") || strings.Contains(canonical, "lease b0 closed=true") {
		t.Fatalf("setup: run b was not cut and must keep its lease:\n%s", canonical)
	}
}
