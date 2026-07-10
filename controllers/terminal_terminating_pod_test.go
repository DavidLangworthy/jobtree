package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/invariant"
)

// Regression for the reaper the 2026-07-10 adversarial review (c74e0ef) confirmed
// against a live envtest apiserver: INV-TERMINAL-NO-PODS fired on the ORDINARY
// graceful-deletion window.
//
// completeRun/failRun close a run's leases and delete its pods in the same pass.
// But Bridge.apply issues a plain graceful Delete, so a bound pod lingers with a
// DeletionTimestamp until the kubelet finalizes it — and Bridge.load re-lists it
// every pass. checkInvariants is deferred on EVERY Reconcile over ALL loaded runs,
// so the pass after any completion would panic INV-TERMINAL-NO-PODS on the dying
// run — while reconciling some entirely healthy neighbour. A reaper: an invariant
// firing on a state the engine legally produces after every ordinary completion.
//
// The fix (PodManifest.Terminating, set from DeletionTimestamp at load, excluded
// from the oracle's pod count): a pod on its way out is not a pod the run "still
// holds". apply keeps seeing it (so it neither re-creates its name nor re-deletes
// it); only the projection stops counting it.

// terminalWithTerminatingPod builds exactly the pass-N+1 state Bridge.load would
// produce: a terminal run whose lease is already closed, its pod still Terminating,
// beside a healthy unrelated run. terminating toggles the one bit under test.
func terminalWithTerminatingPod(now time.Time, terminating bool) *ClusterState {
	deadPod := tpPod("dead-active-0", "dead", "node-a")
	deadPod.Terminating = terminating

	closed := prodLease("dead-0", "dead", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now)
	CloseLease(&closed, "Completed", now) // releaseRun already closed it last pass

	return settleState(now,
		map[string]*v1.Run{
			"default/dead": terminalRun("dead", "org:ai:team", RunPhaseComplete, now),
			"default/live": nfRun("live", "org:ai:team", 2, now),
		},
		[]v1.Lease{closed},
		[]binder.PodManifest{deadPod},
	)
}

func TestTerminatingPodDoesNotTripTerminalNoPods(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	// The fix: a Terminating pod is not counted, so the terminal run reads as
	// having released both planes. The oracle is silent — as it must be on a
	// world produced by an ordinary, correct completion.
	c := NewRunController(terminalWithTerminatingPod(now, true), runClock{now: now})
	if vs := invariant.CheckSteady(c.snapshotWorld()); len(vs) != 0 {
		t.Fatalf("a pod under graceful deletion is not a pod the run holds; the oracle must stay silent, got %+v", vs)
	}
}

// The other direction pins the fix as load-bearing: with Terminating cleared, the
// SAME state is the exact reaper the review reproduced. If this ever stops firing,
// the projection has stopped counting pods at all and INV-TERMINAL-NO-PODS is dead.
func TestNonTerminatingLingeringPodStillTripsTerminalNoPods(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	c := NewRunController(terminalWithTerminatingPod(now, false), runClock{now: now})
	vs := invariant.CheckSteady(c.snapshotWorld())
	found := false
	for _, v := range vs {
		if v.ID == invariant.TerminalNoPods {
			found = true
		}
	}
	if !found {
		t.Fatalf("a terminal run with a genuinely present (non-Terminating) pod must trip INV-TERMINAL-NO-PODS, got %+v", vs)
	}
}
