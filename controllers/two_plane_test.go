package controllers

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/funding"
)

// The ledger and the workload are two different claims on one GPU, and a change that
// moves one without the other is a lie in whichever direction it was told:
//
//	an OPEN LEASE WITH NO POD   bills a budget for nothing, forever
//	a POD WITH NO OPEN LEASE    holds a GPU the ledger has handed back, so the engine
//	                            plans new work onto it and nothing can ever bind
//
// Every test in this file was written from a defect an adversarial panel reproduced
// against a production-shaped fixture. See
// docs/project/reviews/2026-07-09-r27-invariant-oracle-98b602d/adjudication.json.

// tpPod is a pod exactly as emitCohortPods emits one: labelled group "0", whatever
// group the packer actually chose.
func tpPod(name, run, node string) binder.PodManifest {
	return binder.PodManifest{
		Namespace: "default", Name: name, NodeName: node, GPUs: 1,
		Labels: map[string]string{
			binder.LabelRunName:    run,
			binder.LabelGroupIndex: "0",
			binder.LabelRunRole:    binder.RoleActive,
		},
	}
}

func tpNodes() []string { return []string{"node-a", "node-b", "node-c"} }

func tpState(now time.Time, leases []v1.Lease, pods []binder.PodManifest, runs map[string]*v1.Run) *ClusterState {
	nodes := nodeFailureNodes()
	nodes = append(nodes, nodeFailureNodes()[0])
	nodes[2].Name = "node-c"
	return &ClusterState{
		Nodes:   nodes,
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    runs,
		Leases:  leases,
		Pods:    pods,
	}
}

func openLeaseNames(state *ClusterState, runName string) []string {
	var out []string
	for i := range state.Leases {
		l := &state.Leases[i]
		if !l.Status.Closed && l.Spec.RunRef.Name == runName {
			out = append(out, l.Name)
		}
	}
	return out
}

func podNames(state *ClusterState, runName string) []string {
	var out []string
	for _, p := range state.Pods {
		if p.Labels[binder.LabelRunName] == runName {
			out = append(out, p.Name)
		}
	}
	return out
}

// F4. The conflict is found at node#ordinal SLOT granularity. The eviction can only
// act at NODE granularity, because a PodManifest names a machine and not an ordinal.
// The old code closed ONE lease and deleted the victim's WHOLE pod set (every pod is
// labelled group "0"), leaving sibling leases open with no containers behind them —
// billing forever, and invisible to the width invariant, which counts leases.
//
// Both planes must drop together: close exactly the leases whose containers die.
func TestReclaimingASquatterNeverLeavesALeaseWithoutItsContainer(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := tpState(now,
		[]v1.Lease{
			prodLease("active", "run", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now),
			prodLease("spare", "run", "org:ai:team", "team", []string{"node-b#0"}, binder.RoleSpare, now),
			// The squatter, on the spare's exact slot.
			prodLease("squat", "filler", "org:ai:nobody", "", []string{"node-b#0"}, binder.RoleActive, now),
			// A sibling on the SAME machine, a slot the swap never needs.
			prodLease("sibling", "filler", "org:ai:nobody", "", []string{"node-b#2"}, binder.RoleActive, now),
			// ...and one on a machine the swap never touches.
			prodLease("elsewhere", "filler", "org:ai:nobody", "", []string{"node-c#0"}, binder.RoleActive, now),
		},
		[]binder.PodManifest{
			tpPod("filler-squat", "filler", "node-b"),
			tpPod("filler-sibling", "filler", "node-b"),
			tpPod("filler-elsewhere", "filler", "node-c"),
		},
		map[string]*v1.Run{
			"default/run":    nfRun("run", "org:ai:team", 1, now),
			"default/filler": nfRun("filler", "org:ai:nobody", 3, now),
		})
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}

	// Everything on node-b dies in BOTH planes.
	for _, name := range []string{"squat", "sibling"} {
		closed, reason := closureOf(state, name)
		if !closed {
			t.Errorf("lease %s on the reclaimed machine is still open while its container was deleted: "+
				"it bills a budget for a GPU with nothing on it", name)
		} else if reason != "ReclaimedBySpare" {
			t.Errorf("lease %s closed with %q, want ReclaimedBySpare", name, reason)
		}
	}
	// ...and nothing on the machines the swap never touched.
	if closed, _ := closureOf(state, "elsewhere"); closed {
		t.Errorf("a lease on node-c was closed by a swap that only ever needed node-b")
	}

	got := podNames(state, "filler")
	if len(got) != 1 || got[0] != "filler-elsewhere" {
		t.Errorf("the surviving pod set is %v; want exactly [filler-elsewhere] — the containers on the "+
			"reclaimed machine must die, and only those", got)
	}
	if open := openLeaseNames(state, "filler"); len(open) != 1 || open[0] != "elsewhere" {
		t.Errorf("open filler leases = %v, want exactly [elsewhere]", open)
	}

	// And it stays that way: nothing resurrects, nothing leaks.
	for i := 0; i < 20; i++ {
		c.Clock = runClock{now: now.Add(time.Duration(i+1) * time.Hour)}
		_ = c.Reconcile("default", "filler")
	}
	if closed, _ := closureOf(state, "sibling"); !closed {
		t.Errorf("the orphaned sibling lease reopened across 20 reconciles")
	}
}

// F5. The victim was left Running holding an open lease with ZERO containers, because
// the group-"0" pod removal took its whole pod set while only one lease closed. The
// width invariant counts leases, not pods, so the oracle never saw it.
func TestReclaimingASquatterNeverLeavesARunningRunWithNoContainers(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	victim := nfRun("filler", "org:ai:nobody", 2, now)
	victim.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 1, MaxTotalGPUs: 2, StepGPUs: 1}

	state := tpState(now,
		[]v1.Lease{
			prodLease("active", "run", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now),
			prodLease("spare", "run", "org:ai:team", "team", []string{"node-b#0"}, binder.RoleSpare, now),
			prodLease("squat", "filler", "org:ai:nobody", "", []string{"node-b#0"}, binder.RoleActive, now),
			prodLease("keep", "filler", "org:ai:nobody", "", []string{"node-c#0"}, binder.RoleActive, now),
		},
		[]binder.PodManifest{
			tpPod("filler-squat", "filler", "node-b"),
			tpPod("filler-keep", "filler", "node-c"),
		},
		map[string]*v1.Run{
			"default/run":    nfRun("run", "org:ai:team", 1, now),
			"default/filler": victim,
		})
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}

	// It keeps enough width to run, so it keeps running — but only because the lease
	// it still holds still has its container.
	if victim.Status.Phase != RunPhaseRunning {
		t.Fatalf("a malleable run above its minimum keeps running, got %q (%s)", victim.Status.Phase, victim.Status.Message)
	}
	open := openLeaseNames(state, "filler")
	pods := podNames(state, "filler")
	if len(open) != len(pods) {
		t.Fatalf("the run is Running with %d open lease(s) %v and %d pod(s) %v. "+
			"An open lease with no container bills a budget forever, and no invariant counts pods.",
			len(open), open, len(pods), pods)
	}
	if len(pods) != 1 || pods[0] != "filler-keep" {
		t.Errorf("surviving pods = %v, want [filler-keep]", pods)
	}
}

// The reaper guard. A run's funding class is per-LEASE: one run can hold an Owned
// lease and an Unfunded lease at once. Deleting the squatter's container deletes every
// container it has on that machine — so if one of them backs FUNDED work, we would be
// evicting somebody's paid-for capacity. That decision belongs to pkg/resolver, which
// ranks by class. Decline the swap instead.
//
// Without this the "fix" for the two tests above becomes a reaper, which is exactly
// what a judge's fix-probe demonstrated before it shipped.
func TestSwapDeclinesWhenTheSquattersFundedWorkSharesTheMachine(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := tpState(now,
		[]v1.Lease{
			prodLease("active", "run", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now),
			prodLease("spare", "run", "org:ai:team", "team", []string{"node-b#0"}, binder.RoleSpare, now),
			// UNFUNDED: no envelope pays for it. The squatter, on the spare's slot.
			prodLease("squat", "filler", "org:ai:team", "", []string{"node-b#0"}, binder.RoleActive, now),
			// FUNDED, same run, same machine, a different slot. Its container would die
			// with the squatter's, and it is paid for. A run's class is per-LEASE: this
			// is the ordinary co-funded shape, opportunistic width beside owned width.
			prodLease("paid", "filler", "org:ai:team", "team", []string{"node-b#2"}, binder.RoleActive, now),
		},
		[]binder.PodManifest{
			tpPod("filler-squat", "filler", "node-b"),
			tpPod("filler-paid", "filler", "node-b"),
		},
		map[string]*v1.Run{
			"default/run":    nfRun("run", "org:ai:team", 1, now),
			"default/filler": nfRun("filler", "org:ai:team", 2, now),
		})
	c := NewRunController(state, runClock{now: now})

	ev := c.evaluate(now)
	if class, ok := ev.Class(&state.Leases[3]); !ok || class == funding.ClassUnfunded {
		t.Fatalf("setup: the sibling lease must derive a FUNDED class, got %v ok=%v", class, ok)
	}
	if class, ok := ev.Class(&state.Leases[2]); !ok || class != funding.ClassUnfunded {
		t.Fatalf("setup: the squatter must derive Unfunded, got %v ok=%v", class, ok)
	}

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}

	if closed, _ := closureOf(state, "paid"); closed {
		t.Fatalf("the swap evicted FUNDED work. Choosing between funded runs belongs to the resolver, "+
			"which ranks by class — not to a node-failure swap. It should have declined. (%d pods survive)",
			len(podNames(state, "filler")))
	}
	if closed, _ := closureOf(state, "squat"); closed {
		t.Errorf("the squatter's lease closed even though its machine could not be cleared")
	}
	if n := len(podNames(state, "filler")); n != 2 {
		t.Errorf("the declined swap deleted %d of the victim's containers; it must delete none", 2-n)
	}
	// The swap was declined, so the covered run has no cover: it fails, and releases.
	if run := state.Runs["default/run"]; run.Status.Phase != RunPhaseFailed {
		t.Errorf("declining the swap leaves the group uncovered, so the run is Failed; got %q", run.Status.Phase)
	}
	if closed, reason := closureOf(state, "spare"); !closed || reason != "SwapDeclined" {
		t.Errorf("the spare held for a swap that cannot happen must be released: closed=%v reason=%q", closed, reason)
	}
}

// F7. A terminal run releases every lease and used to keep every container. The ledger
// says the GPUs are free; the kubelet says they are occupied. Bridge.apply deletes
// exactly the pods absent from State.Pods, so nothing ever stops them.
func TestATerminalRunStopsItsContainers(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := tpState(now,
		[]v1.Lease{
			prodLease("dead", "run", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now),
			// A healthy rank on a healthy machine. Its lease is swept when the gang dies.
			prodLease("alive", "run", "org:ai:team", "team", []string{"node-b#0"}, binder.RoleActive, now),
		},
		[]binder.PodManifest{
			tpPod("run-pod-a", "run", "node-a"),
			tpPod("run-pod-b", "run", "node-b"),
		},
		map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 2, now)})
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}
	run := state.Runs["default/run"]
	if run.Status.Phase != RunPhaseFailed {
		t.Fatalf("a gang that lost an uncovered rank is Failed, got %q", run.Status.Phase)
	}
	if closed, _ := closureOf(state, "alive"); !closed {
		t.Fatalf("setup: the surviving rank's lease is swept when the gang dies")
	}
	if pods := podNames(state, "run"); len(pods) != 0 {
		t.Fatalf("the failed run's containers are still running: %v. Their leases are closed, so the "+
			"ledger calls those GPUs free while the kubelet keeps them busy — the engine will plan work "+
			"onto them that can never bind.", pods)
	}
}

// ...but the checkpoint-grace window is a DELIBERATE, bounded half-plane state: the
// run is parked Pending with a deadline and its containers keep running precisely so
// they can write a checkpoint. The cull must not reach it.
//
// The exemption keys on the run being non-terminal, which is what the deadline means.
func TestCheckpointGraceKeepsTheContainersRunning(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	run := nfRun("run", "org:ai:team", 2, now)
	run.Spec.Runtime = &v1.RunRuntime{Checkpoint: metav1.Duration{Duration: 30 * time.Minute}}

	state := tpState(now,
		[]v1.Lease{
			prodLease("dead", "run", "org:ai:team", "team", []string{"node-a#0"}, binder.RoleActive, now),
			prodLease("alive", "run", "org:ai:team", "team", []string{"node-b#0"}, binder.RoleActive, now),
		},
		[]binder.PodManifest{
			tpPod("run-pod-a", "run", "node-a"),
			tpPod("run-pod-b", "run", "node-b"),
		},
		map[string]*v1.Run{"default/run": run})
	c := NewRunController(state, runClock{now: now})

	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}
	if run.Status.Phase != RunPhasePending {
		t.Fatalf("a run with a checkpoint grace parks Pending, got %q (%s)", run.Status.Phase, run.Status.Message)
	}
	if run.Status.CheckpointDeadline == nil {
		t.Fatalf("the grace window must carry a deadline, or nothing ever ends it")
	}
	if closed, _ := closureOf(state, "alive"); closed {
		t.Errorf("the surviving rank's lease was swept during checkpoint grace; it is still running")
	}
	if n := len(podNames(state, "run")); n == 0 {
		t.Fatalf("checkpoint grace deleted the containers it exists to let finish writing a checkpoint")
	}
}
