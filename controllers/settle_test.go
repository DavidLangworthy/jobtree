package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// A sweep is a claim about what is dead. Half of these tests exist to pin what is
// ALIVE, because the way a sweep fails is not by missing a corpse — it is by
// closing a healthy run's leases and calling that housekeeping.

func settleState(now time.Time, runs map[string]*v1.Run, leases []v1.GPULease, pods []binder.PodManifest) *ClusterState {
	return &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    runs,
		Leases:  leases,
		Pods:    pods,
	}
}

func terminalRun(name, owner string, phase string, now time.Time) *v1.Run {
	run := nfRun(name, owner, 2, now)
	run.Status.Phase = phase
	return run
}

// The immortal lease, healed. Nothing reconciles a corpse, so no engine path will
// ever come back for this lease: it charges its budget and holds its GPUs until an
// operator finds it by hand.
func TestSweepClosesTheLeasesOfATerminalRun(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	for _, phase := range []string{RunPhaseFailed, RunPhaseComplete} {
		t.Run(phase, func(t *testing.T) {
			state := settleState(now,
				map[string]*v1.Run{"default/dead": terminalRun("dead", "org:ai:team", phase, now)},
				[]v1.GPULease{prodLease("dead-0", "dead", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now)},
				[]binder.PodManifest{tpPod("dead-active-0", "dead", "node-a")})

			sweep := SettleLeases(state, now)
			if len(sweep.Leases) != 1 || sweep.Leases[0].Rule != SweepTerminalRun {
				t.Fatalf("a %s run holding an open lease must be swept, got %+v", phase, sweep.Leases)
			}
			if closed, reason := closureOf(state, "dead-0"); !closed || reason != "SweptTerminalRun" {
				t.Errorf("closed=%v reason=%q; the ledger must record WHY it was swept", closed, reason)
			}
			// Both planes, or the sweep is the same lie told backwards.
			if len(state.Pods) != 0 || sweep.Pods != 1 {
				t.Errorf("the container outlived the lease: pods=%v swept.Pods=%d", podNames(state, "dead"), sweep.Pods)
			}
			if len(sweep.Shirked()) != 1 {
				t.Errorf("a terminal-run closure means a path shirked releaseRun; it must be reported as such")
			}
		})
	}
}

// A Run object that no longer exists. The orphan-run rule is DELETED (R12 step 3):
// the Run finalizer makes an absent-Run-with-open-lease unreachable, and a genuine
// deleted run's leases are closed by cleanupDeletedRun on positive evidence. So the
// sweep now leaves such a lease entirely alone — it must never guess "deleted" from
// a silent load (spec-brief A4) and destroy a job.
func TestSweepIgnoresALeaseWhoseRunIsAbsent(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := settleState(now,
		map[string]*v1.Run{},
		[]v1.GPULease{prodLease("ghost-0", "ghost", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now)},
		[]binder.PodManifest{tpPod("ghost-active-0", "ghost", "node-a")})

	sweep := SettleLeases(state, now)

	if !sweep.Empty() {
		t.Fatalf("the sweep must not touch a lease whose Run is absent; that is cleanupDeletedRun's job on real evidence, got %+v", sweep)
	}
	if closed, _ := closureOf(state, "ghost-0"); closed {
		t.Errorf("an absent-run lease was closed by the sweep; it must be left open")
	}
	if len(state.Pods) != 1 {
		t.Errorf("the sweep dropped an absent-run pod, got %v", state.Pods)
	}
}

// THE REAPER GUARD. A Running run holding open leases is the ordinary state of
// every healthy job in the cluster.
func TestSweepLeavesAHealthyRunAlone(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := settleState(now,
		map[string]*v1.Run{"default/alive": nfRun("alive", "org:ai:team", 2, now)},
		[]v1.GPULease{
			prodLease("alive-0", "alive", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now),
			prodLease("alive-1", "alive", "org:ai:team", "team", []string{"node-a#1"}, binder.RoleActive, now),
		},
		[]binder.PodManifest{tpPod("alive-active-0", "alive", "node-a"), tpPod("alive-active-1", "alive", "node-a")})

	if sweep := SettleLeases(state, now); !sweep.Empty() {
		t.Fatalf("the sweep closed a healthy Running run's leases: %+v", sweep.Leases)
	}
	if len(state.Pods) != 2 {
		t.Fatalf("the sweep deleted a healthy run's containers")
	}
}

// A run parked Pending by the checkpoint grace still holds the leases of its
// surviving groups, and its containers are alive ON PURPOSE so they can write a
// checkpoint. Sweeping it would destroy exactly the state R14's demote-not-kill
// exists to preserve.
func TestSweepLeavesARunInCheckpointGraceAlone(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	run := nfRun("recovering", "org:ai:team", 2, now)
	run.Status.Phase = RunPhasePending
	deadline := v1.NewTime(now.Add(10 * time.Minute))
	run.Status.CheckpointDeadline = &deadline

	state := settleState(now,
		map[string]*v1.Run{"default/recovering": run},
		[]v1.GPULease{prodLease("survivor", "recovering", "org:ai:team", "team", []string{"node-b#0"}, binder.RoleActive, now)},
		[]binder.PodManifest{tpPod("recovering-active-0", "recovering", "node-b")})

	if sweep := SettleLeases(state, now); !sweep.Empty() {
		t.Fatalf("the sweep reaped a run that is deliberately checkpointing: %+v", sweep.Leases)
	}
}

// THE RULE THAT IS NOT HERE. Fable's second sweep rule — "an open Spare lease with
// no open Active lease of its group is an orphan" — is refuted: a leftover
// spare-only run is a legal state, and the plugin mints per pod, so a spare
// legitimately exists before its actives do. This test is the refutation, kept
// executable so nobody adds the rule back.
func TestSweepLeavesASpareOnlyRunAlone(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := settleState(now,
		map[string]*v1.Run{"default/spare-only": nfRun("spare-only", "org:ai:team", 2, now)},
		[]v1.GPULease{prodLease("standby", "spare-only", "org:ai:team", "team", []string{"node-b#0"}, binder.RoleSpare, now)},
		nil)
	state.Runs["default/spare-only"].Status.Phase = RunPhasePending

	if sweep := SettleLeases(state, now); !sweep.Empty() {
		t.Fatalf("a spare with no active of its group is a LEGAL state; sweeping it closes healthy "+
			"standby capacity during every ordinary admission: %+v", sweep.Leases)
	}
}

// Closed leases are settled facts. Re-closing one would move its Ended timestamp,
// which INV-CLOSED-MONOTONE calls a lie, and funding.Evaluate would charge it twice.
func TestSweepDoesNotTouchAlreadyClosedLeases(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	settled := prodLease("settled", "dead", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now)
	CloseLease(&settled, "Completed", now.Add(-time.Hour))

	state := settleState(now,
		map[string]*v1.Run{"default/dead": terminalRun("dead", "org:ai:team", RunPhaseComplete, now)},
		[]v1.GPULease{settled}, nil)

	if sweep := SettleLeases(state, now); !sweep.Empty() {
		t.Fatalf("a closed lease is a settled fact and must not be swept again: %+v", sweep.Leases)
	}
	if got := state.Leases[0].Status.Ended.Time; !got.Equal(now.Add(-time.Hour)) {
		t.Errorf("the recorded ending moved to %v; a closure timestamp is a fact, not a variable", got)
	}
	if reason := state.Leases[0].Status.ClosureReason; reason != "Completed" {
		t.Errorf("the closure reason was rewritten to %q", reason)
	}
}

// The sweep is idempotent: running it twice must find nothing the second time.
// Bridge.WithWorld calls it on every single pass, so a sweep that kept finding
// work would emit a Warning event per reconcile, forever.
func TestSweepIsIdempotent(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := settleState(now,
		map[string]*v1.Run{"default/dead": terminalRun("dead", "org:ai:team", RunPhaseFailed, now)},
		[]v1.GPULease{prodLease("dead-0", "dead", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now)},
		[]binder.PodManifest{tpPod("dead-active-0", "dead", "node-a")})

	if first := SettleLeases(state, now); first.Empty() {
		t.Fatalf("the first pass must repair something")
	}
	if second := SettleLeases(state, now.Add(time.Minute)); !second.Empty() {
		t.Fatalf("the second pass found work again: %+v (+%d pods)", second.Leases, second.Pods)
	}
}
