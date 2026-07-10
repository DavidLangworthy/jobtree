package controllers

import (
	"fmt"
	"strconv"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/pack"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// R28b. The packer computes placement groups; before this, both other planes threw
// them away. Every pod was stamped "0" and every lease was stamped nothing, so
// pkg/resolver cut whole runs instead of groups, the elastic loop shrank in whole-run
// units, and a reclaim that asked for "the pods of this group" got the pods of the
// entire run — while three separate consumers papered over the missing label with a
// "0" default that made it all look right.

func giNodes(n int, gpus int) []topology.SourceNode {
	var out []topology.SourceNode
	for i := 0; i < n; i++ {
		out = append(out, topology.SourceNode{
			Name: fmt.Sprintf("node-%d", i), GPUs: gpus,
			Labels: map[string]string{
				topology.LabelRegion: "us-west", topology.LabelCluster: "cluster-a",
				topology.LabelFabricDomain: "island-a", topology.LabelGPUFlavor: "H100-80GB",
			},
		})
	}
	return out
}

func giSnapshot(t *testing.T, nodes, gpus int) *topology.Snapshot {
	t.Helper()
	snap, err := topology.BuildSnapshotForFlavor(giNodes(nodes, gpus), nil, "H100-80GB")
	if err != nil {
		t.Fatalf("build snapshot: %v", err)
	}
	return snap
}

func giRun(name string, totalGPUs, groupGPUs int32) *v1.Run {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default"},
		Spec: v1.RunSpec{
			Owner:     "org:ai:team",
			Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: totalGPUs},
		},
	}
	if groupGPUs > 0 {
		run.Spec.Locality = &v1.RunLocality{GroupGPUs: &groupGPUs}
	}
	return run
}

// THE PINNING TEST. groupIndexForPodIndex answers "which group does pod i belong to?"
// from the Run spec alone, because topUpActiveGang re-emits a lost rank long after the
// plan that placed it is gone. That makes it a SECOND implementation of the packer's
// grouping rule, and two implementations drift.
//
// This test is the pin. If pack.deriveGroups ever changes how it lays groups out, this
// fails rather than letting a re-emitted pod join the wrong gang.
func TestPodGroupDerivedFromSpecMatchesThePackersGrouping(t *testing.T) {
	for _, tc := range []struct{ total, groupGPUs, gpusPerPod int32 }{
		{8, 0, 1},   // no locality: one group
		{8, 4, 1},   // two even groups
		{9, 4, 1},   // ragged last group
		{12, 4, 2},  // multi-GPU pods
		{64, 32, 1}, // the shape the docs use
	} {
		t.Run(fmt.Sprintf("total=%d group=%d perPod=%d", tc.total, tc.groupGPUs, tc.gpusPerPod), func(t *testing.T) {
			run := giRun("r", tc.total, tc.groupGPUs)
			snapshot := giSnapshot(t, 8, 16)
			req := pack.Request{Flavor: "H100-80GB", TotalGPUs: int(tc.total)}
			if tc.groupGPUs > 0 {
				g := int(tc.groupGPUs)
				req.GroupGPUs = &g
			}
			plan, err := pack.Planner(snapshot, req)
			if err != nil {
				t.Fatalf("pack: %v", err)
			}

			placements := packPlacements(plan, int(tc.gpusPerPod), 0)
			pods := int(tc.total / tc.gpusPerPod)
			if len(placements) != pods {
				t.Fatalf("packPlacements produced %d placements for %d pods", len(placements), pods)
			}
			for i := 0; i < pods; i++ {
				fromPlan := placements[i].Group
				fromSpec := groupIndexForPodIndex(run, i, int(tc.gpusPerPod))
				if fromPlan != fromSpec {
					t.Fatalf("pod %d: the packer says group %q, the spec-only derivation says %q. "+
						"topUpActiveGang uses the spec-only path, so a re-emitted rank would join the wrong gang.",
						i, fromPlan, fromSpec)
				}
			}
		})
	}
}

// The base gang's pods carry their real group, and a spare carries the group it covers
// — which is how findSpareLease pairs a dead rank with its own group's standby.
func TestEmittedPodsCarryTheirRealPlacementGroup(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	run := giRun("train", 8, 4)
	spares := int32(2)
	run.Spec.Spares = &spares

	snapshot := giSnapshot(t, 4, 8)
	g := 4
	plan, err := pack.Planner(snapshot, pack.Request{Flavor: "H100-80GB", TotalGPUs: 8, GroupGPUs: &g, SparesPerGroup: 1})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	state := &ClusterState{Runs: map[string]*v1.Run{"default/train": run}}
	c := NewRunController(state, runClock{now: now})
	c.emitIntentPods(run, plan)

	byGroup := map[string]int{}
	spareGroups := map[string]int{}
	for _, p := range state.Pods {
		g := p.Labels[binder.LabelGroupIndex]
		if g == "" {
			t.Fatalf("pod %s carries no group index; the plugin will refuse to mint its lease", p.Name)
		}
		if p.Labels[binder.LabelRunRole] == binder.RoleSpare {
			spareGroups[g]++
			continue
		}
		byGroup[g]++
	}
	if len(byGroup) != 2 || byGroup["0"] != 4 || byGroup["1"] != 4 {
		t.Errorf("active pods per group = %v, want 4 in group 0 and 4 in group 1", byGroup)
	}
	if len(spareGroups) != 2 {
		t.Errorf("spares per group = %v; each group's spare must name the group it covers", spareGroups)
	}
}

// A grow appends NEW groups above the base gang's. It must never renumber the groups
// the gang is already running on: a later node-failure swap addresses ranks by index,
// and a renumbering would pair a dead rank with another group's spare.
func TestGrowAppendsGroupsAboveTheBaseGang(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	run := giRun("train", 8, 4)
	state := &ClusterState{
		Runs: map[string]*v1.Run{"default/train": run},
		Pods: []binder.PodManifest{
			{Namespace: "default", Name: "train-active-0", GPUs: 1,
				Labels: map[string]string{binder.LabelRunName: "train", binder.LabelGroupIndex: "0", binder.LabelRunRole: binder.RoleActive}},
			{Namespace: "default", Name: "train-active-4", GPUs: 1,
				Labels: map[string]string{binder.LabelRunName: "train", binder.LabelGroupIndex: "1", binder.LabelRunRole: binder.RoleActive}},
		},
	}
	c := NewRunController(state, runClock{now: now})
	if got := c.nextGroupIndex(run); got != 2 {
		t.Fatalf("nextGroupIndex = %d, want 2 (the base gang occupies 0 and 1)", got)
	}

	snapshot := giSnapshot(t, 4, 8)
	g := 4
	plan, err := pack.Planner(snapshot, pack.Request{Flavor: "H100-80GB", TotalGPUs: 4, GroupGPUs: &g})
	if err != nil {
		t.Fatalf("pack: %v", err)
	}
	placements := packPlacements(plan, 1, c.nextGroupIndex(run))
	for _, p := range placements {
		idx, err := strconv.Atoi(p.Group)
		if err != nil || idx < 2 {
			t.Fatalf("a grow placed a rank in group %q, colliding with the base gang's 0 and 1", p.Group)
		}
	}
}

// The resolver buckets a run's leases by group. With real groups it can cut ONE group;
// with the old "0"-for-everything it could only cut the whole run.
func TestTheResolverSeesOneTokenPerGroupNotPerRun(t *testing.T) {
	now := time.Date(2024, 1, 1, 10, 0, 0, 0, time.UTC)
	state := &ClusterState{
		Nodes:   nodeFailureNodes(),
		Budgets: []v1.Budget{nfBudget("team", "org:ai:team")},
		Runs:    map[string]*v1.Run{"default/run": nfRun("run", "org:ai:team", 4, now)},
		Leases: []v1.Lease{
			prodLeaseGroup("g0", "run", "org:ai:team", "team", "0", []string{"node-a#0", "node-a#1"}, binder.RoleActive, now),
			prodLeaseGroup("g1", "run", "org:ai:team", "team", "1", []string{"node-b#0", "node-b#1"}, binder.RoleActive, now),
		},
	}
	groups := collectElasticGroups("default/run", state.Leases, nil)
	if len(groups) != 2 {
		t.Fatalf("the elastic loop sees %d group(s); a two-group run has two. "+
			"With every lease stamped the same group it shrank in whole-run units.", len(groups))
	}
	if _, ok := groups[1]; !ok {
		t.Errorf("group 1 is missing: %v", groups)
	}
}
