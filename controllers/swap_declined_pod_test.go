package controllers

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/davidlangworthy/jobtree/pkg/binder"
)

// The 2026-07-10 review (c74e0ef) confirmed a half-plane action: the decline-the-
// swap path closed the spare's LEASE ("SwapDeclined") but never dropped its POD.
// The ledger freed the GPUs while the placeholder container kept holding them; if
// the run re-admitted inside checkpoint grace the pod lingered forever, and no
// invariant saw it (INV-LEASE-HAS-POD fires only at zero pods). The existing test
// TestDecliningTheSwapReleasesTheSpare... checks only the ledger plane, which is
// why the bug shipped green. This checks the plane that was leaking.
func TestDecliningTheSwapDropsTheSparePodNotJustItsLease(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	run := nfRun("run", "org:ai:team", 2, now)
	run.Spec.Runtime = &v1.RunRuntime{Checkpoint: metav1.Duration{Duration: 30 * time.Minute}}

	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team"), nfBudget("other", "org:ai:other")},
		Runs: map[string]*v1.Run{
			"default/run":           run,
			"org-ai-other/squatter": nfRun("squatter", "org:ai:other", 2, now),
		},
		Leases: []v1.GPULease{
			nfLease("active", "run", "org:ai:team", "team", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			nfLease("spare", "run", "org:ai:team", "team", []string{"node-b#0", "node-b#1"}, binder.RoleSpare, now),
			nfLease("squatter", "squatter", "org:ai:other", "other", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
	}
	mirrorPods(state) // one pod per open lease, exactly as the sole committer produces
	c := NewRunController(state, runClock{now: now})
	if err := c.HandleNodeFailure("node-a", now); err != nil {
		t.Fatalf("handle node failure: %v", err)
	}

	// Ledger plane, as the old test already guaranteed.
	if closed, reason := closureOf(state, "spare"); !closed || reason != "SwapDeclined" {
		t.Fatalf("setup: the spare lease must close SwapDeclined, got closed=%v reason=%q", closed, reason)
	}
	// Pod plane — the half that leaked. The spare's placeholder must be gone, or it
	// squats 2 GPUs the ledger just handed back.
	for _, p := range state.Pods {
		if p.Labels[binder.LabelRunName] == "run" && p.Labels[binder.LabelRunRole] == binder.RoleSpare {
			t.Fatalf("declined spare pod %s still holds %d GPUs on %s after its lease was released",
				p.Name, p.GPUs, p.NodeName)
		}
	}
}

// removeSparePodOnNodes keyed on node alone and returned the first match, so when
// two groups' spares legally co-locate (pkg/pack assigns spare domains per group
// with no cross-group exclusion) it deleted whichever the slice happened to list
// first — stranding the retiring group's own pod and freeing a sibling's. This
// pins the group-aware match directly.
func TestRemoveSparePodOnNodesHonorsTheGroup(t *testing.T) {
	run := nfRun("run", "org:ai:team", 4, time.Now())
	sparePod := func(name, group string) binder.PodManifest {
		return binder.PodManifest{
			Namespace: "default", Name: name, NodeName: "node-s", GPUs: 2,
			Labels: map[string]string{
				binder.LabelRunName:    "run",
				binder.LabelRunRole:    binder.RoleSpare,
				binder.LabelGroupIndex: group,
			},
		}
	}
	// Group 0's spare is listed FIRST — the pre-fix node-only match would have
	// deleted it even though we are retiring group 1.
	state := &ClusterState{Pods: []binder.PodManifest{sparePod("spare-g0", "0"), sparePod("spare-g1", "1")}}
	c := NewRunController(state, runClock{now: time.Now()})

	c.removeSparePodOnNodes(run, "1", []string{"node-s"})

	var remaining []string
	for _, p := range state.Pods {
		remaining = append(remaining, p.Name)
	}
	if len(remaining) != 1 || remaining[0] != "spare-g0" {
		t.Fatalf("retiring group 1 must remove exactly spare-g1 and keep spare-g0, remaining=%v", remaining)
	}
}
