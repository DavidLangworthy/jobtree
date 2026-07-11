package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// #90: a Running gang whose active pod is externally deleted (drain/eviction) — GONE,
// not Failed — must be REPAIRED (the rank re-emitted in place from its still-open
// lease), not left below width billing for a pod that no longer exists, and not
// mistaken for a finished member by the completion gate. Detection is GPU-sum,
// lease-relative (open active lease GPUs > active pod GPUs); the lease stays OPEN —
// it is the durable record the re-emit recovers provenance from.

func TestEvictedActiveMemberIsReEmittedNotReaped(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	state, run, key := roledFailureWorld(v1.FailurePolicyFail, 0, []string{"Running", "Running"})

	// Evict member 1: its pod vanishes (its GPU leaves the pod plane), its lease stays open.
	dropPodByName(state, cohortPodName(run, "0", 1))

	c := NewRunController(state, runClock{now: now})
	if err := c.Reconcile(keys.DefaultNamespace, "job"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	run = state.Runs[key]
	if run.Status.Phase != RunPhaseRunning {
		t.Fatalf("an eviction is a displacement, not a failure: the run stays Running, got %s", run.Status.Phase)
	}
	// The evicted member's lease stays OPEN (reused, not closed).
	if closed, _ := closureOf(state, cohortPodName(run, "0", 1)); closed {
		t.Fatalf("the evicted member's open lease must be reused, not closed — it is what the re-emit recovers from")
	}
	// The pod plane is back to covering the open leases' GPUs: the rank was re-emitted.
	if pods, leases := c.activePodGPUs(run), runnableGPUsForRun(key, state.Leases); pods != leases {
		t.Fatalf("active pod GPUs (%d) != open active lease GPUs (%d): the evicted rank was not re-emitted", pods, leases)
	}
}

// The completion-gate guard: an evicted member plus all-others-Succeeded must NOT
// complete the run — that would finish a gang with a rank that never ran.
func TestEvictedMemberDoesNotLetTheGangCompletePrematurely(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	state, run, key := roledFailureWorld(v1.FailurePolicyFail, 0, []string{"Succeeded", "Running"})

	// Member 0 succeeded; member 1 is evicted (pod gone, lease open).
	dropPodByName(state, cohortPodName(run, "0", 1))

	c := NewRunController(state, runClock{now: now})
	if err := c.Reconcile(keys.DefaultNamespace, "job"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if got := state.Runs[key].Status.Phase; got == RunPhaseComplete {
		t.Fatalf("the gang completed while a member was evicted, not finished — a rank that never ran was counted done")
	}
}

// A healthy full gang (pods cover their leases) must NOT trip the eviction detector.
func TestHealthyGangIsNotSeenAsEvicted(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	state, run, _ := roledFailureWorld(v1.FailurePolicyFail, 0, []string{"Running", "Running"})
	c := NewRunController(state, runClock{now: now})
	if detected, _ := c.recoverEvictedRanks(run); detected {
		t.Fatalf("a full gang (pod GPUs == lease GPUs) was misread as having an evicted member")
	}
}

// #90 tail: a Running gang that dropped below its minimum runnable width with NO pod
// awaiting a mint — a rank fully lost (no lease to re-emit from, no pod in flight),
// e.g. a node-failure swap that only partly covered the gang — is the
// INV-WIDTH-ASSEMBLED reaper (Running, missing a rank, every survivor billing). It
// must demote to Pending so the assembly path re-provisions, not sit Running below
// width.
func TestRunningGangBelowMinWithNoMintInFlightReAssembles(t *testing.T) {
	now := time.Date(2026, 7, 3, 12, 0, 0, 0, time.UTC)
	run := nfRun("job", "org:ai:team", 2, now) // min 2
	run.Status.Phase = RunPhaseRunning
	key := keys.NamespacedKey(run.Namespace, run.Name)
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{key: run},
		Leases: []v1.Lease{ // only ONE of the two ranks survives
			nfLeaseGroup("m0", "job", "org:ai:team", "team", "0", []string{"node-a#0"}, binder.RoleActive, now),
		},
	}
	mirrorPods(state) // one pod for the one open lease: runnable 1, no pod in flight
	c := NewRunController(state, runClock{now: now})
	if got := runnableGPUsForRun(key, state.Leases); got != 1 {
		t.Fatalf("setup: want runnable 1 of min 2, got %d", got)
	}
	if err := c.Reconcile(keys.DefaultNamespace, "job"); err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	run = state.Runs[key]
	if run.Status.Phase == RunPhaseRunning &&
		runnableGPUsForRun(key, state.Leases) < minRunnableGPUs(run) && !c.awaitingMint(run) {
		t.Fatalf("a Running gang below its minimum with no pod awaiting a mint is the "+
			"INV-WIDTH-ASSEMBLED reaper; it must demote/re-assemble, phase=%s runnable=%d",
			run.Status.Phase, runnableGPUsForRun(key, state.Leases))
	}
}
