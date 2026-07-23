package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/resolver"
)

// The 2026-07-10 review (c74e0ef) confirmed by execution: applyResolution's terminal
// `default` branch reaps a run that is inside an UNEXPIRED checkpoint-grace window.
// releaseRun deletes every pod, so the checkpoint writers the grace exists to protect
// die minutes before the deadline the engine itself set. The exemption is not a
// reaper — checkpoint grace is DEFINED (releaseRun's contract) as "leave the
// containers running SO THEY CAN WRITE A CHECKPOINT" until the deadline. Honor the
// deadline, not the phase (adversarial-review playbook, class 9).
func TestResolverCutDuringCheckpointGraceDoesNotReapBeforeTheDeadline(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)

	// A fixed-width 4-GPU run already parked in checkpoint grace by an earlier node
	// failure: Pending, deadline 25 minutes out, both surviving-group leases still
	// open and their containers still running so they can checkpoint.
	run := nfRun("grace", "org:ai:team", 4, now)
	run.Spec.Runtime = &v1.RunRuntime{Checkpoint: metav1.Duration{Duration: 30 * time.Minute}}
	run.Status.Phase = RunPhasePending
	deadline := v1.NewTime(now.Add(25 * time.Minute))
	run.Status.CheckpointDeadline = &deadline

	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/grace": run},
		Leases: []v1.GPULease{
			prodLease("grace-g0", "grace", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			prodLease("grace-g1", "grace", "org:ai:team", "team", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
	}
	mirrorPods(state) // a checkpoint-writing pod behind each open lease
	c := NewRunController(state, runClock{now: now})

	// The resolver cuts one group (a funded lottery cut, NOT a reclaim), dropping the
	// run below its width and into the terminal default branch.
	c.applyResolution(resolver.Result{Seed: "0xseed", Actions: []resolver.Action{
		{Kind: resolver.ActionLottery, Lease: &state.Leases[1], Run: run, GroupIndex: "1", GPUs: 2, Reason: "RandomPreempt(0xseed)"},
	}}, now)

	// Within the unexpired deadline the run must stay parked, not fail: Reconcile
	// fails it when the deadline actually passes.
	if got := run.Status.Phase; got != RunPhasePending {
		t.Fatalf("a resolver cut inside checkpoint grace failed the run early (phase=%s); the checkpoint window was promised until %s", got, deadline.Time.UTC())
	}
	if run.Status.CheckpointDeadline == nil {
		t.Errorf("the checkpoint deadline was cleared; the grace window it promised is gone")
	}
	// The surviving group's lease and its checkpoint-writing pod must both remain:
	// releaseRun (which the reap path calls) would have closed the lease and deleted
	// every pod.
	if closed, _ := closureOf(state, "grace-g0"); closed {
		t.Errorf("the surviving group's lease was released mid-grace")
	}
	if len(state.Pods) == 0 {
		t.Fatalf("every pod was deleted mid-grace: the checkpoint the window exists to save is destroyed")
	}
	// The oracle must also find the post-cut state legal, not just the fields above.
	assertSteady(t, c, "resolver cut inside checkpoint grace")
}
