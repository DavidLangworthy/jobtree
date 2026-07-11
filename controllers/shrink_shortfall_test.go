package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// #59: shrinkRun's shortfall path — it cannot land EXACTLY on the target without
// dropping below it, so it stops at the nearest feasible width above target and
// returns the "insufficient groups" error. That error used to `return` BEFORE
// removePodsForGroups, stranding the pods of the groups whose leases it had
// already closed: the ledger freed the GPUs (leases closed) while the containers
// kept holding them. Same half-plane class as the SwapDeclined fix, different
// door. Both planes must drop together, error or not.
func TestShrinkShortfallDoesNotStrandClosedGroupsPods(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	runKey := "default/run"
	mkPod := func(group string) binder.PodManifest {
		return binder.PodManifest{
			Namespace: "default", Name: "run-active-" + group, GPUs: 2, Phase: "Running",
			Labels: map[string]string{binder.LabelRunName: "run", binder.LabelGroupIndex: group, binder.LabelRunRole: binder.RoleActive},
		}
	}
	// Three 2-GPU groups (6 GPUs) across node-a (4) + node-b (2).
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{runKey: nfRun("run", "org:ai:team", 6, now)},
		Leases: []v1.Lease{
			nfLeaseGroup("g0", "run", "org:ai:team", "team", "0", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLeaseGroup("g1", "run", "org:ai:team", "team", "1", []string{"node-a#2", "node-a#3"}, binder.RoleActive, now),
			nfLeaseGroup("g2", "run", "org:ai:team", "team", "2", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
		Pods: []binder.PodManifest{mkPod("0"), mkPod("1"), mkPod("2")},
	}
	c := NewRunController(state, runClock{now: now})

	// Target 1 is unreachable with 2-GPU groups (the nearest feasible >= 1 is 2):
	// shrinkRun closes two groups, then reports the shortfall.
	err := c.shrinkRun(state.Runs[runKey], 1, c.evaluate(now), now)
	if err == nil {
		t.Fatalf("expected the insufficient-groups shortfall error (target 1 is below a single 2-GPU group)")
	}

	// Half-plane invariant: no group may have a CLOSED lease but a surviving pod,
	// and no group with an OPEN lease may have lost its pod.
	livePods := map[string]bool{}
	for _, p := range state.Pods {
		if keys.NamespacedKey(p.Namespace, p.Labels[binder.LabelRunName]) == runKey {
			livePods[p.Labels[binder.LabelGroupIndex]] = true
		}
	}
	closedGroups := 0
	for i := range state.Leases {
		l := &state.Leases[i]
		grp := l.Labels[binder.LabelGroupIndex]
		if l.Status.Closed {
			closedGroups++
			if livePods[grp] {
				t.Errorf("group %s: lease closed (%s) but its pod still holds GPUs — the ledger freed capacity the workload kept",
					grp, l.Status.ClosureReason)
			}
		} else if !livePods[grp] {
			t.Errorf("group %s: lease still open but its pod was removed — a live lease with no container", grp)
		}
	}
	if closedGroups == 0 {
		t.Fatalf("built no shortfall: shrinkRun closed no groups, so the stranding path was never exercised")
	}
}
