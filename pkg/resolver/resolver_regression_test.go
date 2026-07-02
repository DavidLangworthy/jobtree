package resolver

import (
	"fmt"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// R4: shrink order must be numeric, highest index first. The old string sort
// cut group "9" before "10" and "11".
func TestResolveShrinkCutsHighestIndexFirstPast10(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := buildRun("team", "default", "elastic", "H100")
	// 12 groups of 8 GPUs; the floor allows exactly three cuts.
	run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 72, MaxTotalGPUs: 96, StepGPUs: 8}

	var leases []*v1.Lease
	var nodes []topology.SourceNode
	for g := 0; g < 12; g++ {
		node := fmt.Sprintf("node-%02d", g)
		slots := make([]string, 8)
		for i := range slots {
			slots[i] = fmt.Sprintf("%s#%d", node, i)
		}
		leases = append(leases, buildLease(run, fmt.Sprintf("%d", g), "Active", slots, now))
		nodes = append(nodes, sourceNode(node, "us-west", "cluster-a", "island-a", "H100", 8))
	}

	input := Input{
		Deficit:    24,
		Flavor:     "H100",
		SeedSource: "reservation-r4",
		Now:        now,
		Nodes:      nodes,
		Leases:     leases,
		Runs: map[string]*v1.Run{
			keys.NamespacedKey(run.Namespace, run.Name): run,
		},
	}

	result, err := Resolve(input)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(result.Actions) != 3 {
		t.Fatalf("expected 3 shrink actions, got %d", len(result.Actions))
	}
	wantOrder := []string{"11", "10", "9"}
	for i, action := range result.Actions {
		if action.Kind != ActionShrink {
			t.Fatalf("action %d: expected shrink, got %s", i, action.Kind)
		}
		if action.GroupIndex != wantOrder[i] {
			t.Errorf("cut %d: expected group %s, got %s", i, wantOrder[i], action.GroupIndex)
		}
	}
}

// R5: a lease whose RunRef has no namespace must still resolve to its run
// (state stores key runs under "default/<name>"), so it is considered for
// preemption rather than silently exempted.
func TestResolveConsidersEmptyNamespaceRunRef(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := buildRun("team", "default", "victim", "H100")
	lease := buildLease(run, "0", "Spare", []string{"node-a#0", "node-a#1"}, now)
	lease.Spec.RunRef.Namespace = ""

	input := Input{
		Deficit:    2,
		Flavor:     "H100",
		SeedSource: "reservation-r5",
		Now:        now,
		Nodes:      []topology.SourceNode{sourceNode("node-a", "us-west", "cluster-a", "island-a", "H100", 4)},
		Leases:     []*v1.Lease{lease},
		Runs: map[string]*v1.Run{
			keys.NamespacedKey("default", "victim"): run,
		},
	}

	result, err := Resolve(input)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(result.Actions) != 1 || result.Actions[0].Kind != ActionDropSpare {
		t.Fatalf("expected the empty-namespace lease's spare to be dropped, got %+v", result.Actions)
	}
}

// R6: planning must not touch the resolver-action counters — when the
// lottery fails the whole result is discarded, and previously the spare and
// shrink counts planned along the way leaked into metrics.
func TestResolveFailureLeavesMetricsUnchanged(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := buildRun("team", "default", "run-a", "H100")
	spare := buildLease(run, "0", "Spare", []string{"node-a#0", "node-a#1"}, now)

	input := Input{
		Deficit:    10, // far more than the cluster can free: the lottery must fail
		Flavor:     "H100",
		SeedSource: "reservation-r6",
		Now:        now,
		Nodes:      []topology.SourceNode{sourceNode("node-a", "us-west", "cluster-a", "island-a", "H100", 4)},
		Leases:     []*v1.Lease{spare},
		Runs: map[string]*v1.Run{
			keys.NamespacedKey(run.Namespace, run.Name): run,
		},
	}

	if _, err := Resolve(input); err == nil {
		t.Fatalf("expected resolve to fail on an unclearable deficit")
	}
	if actions := metrics.Snapshot().ResolverActions; len(actions) != 0 {
		t.Fatalf("failed resolve leaked resolver-action metrics: %v", actions)
	}
}

// Resolve must also not count actions on success — counting happens when the
// controller applies the result.
func TestResolveSuccessDoesNotCountMetrics(t *testing.T) {
	metrics.Reset()
	t.Cleanup(metrics.Reset)

	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := buildRun("team", "default", "run-a", "H100")
	spare := buildLease(run, "0", "Spare", []string{"node-a#0", "node-a#1"}, now)

	input := Input{
		Deficit:    2,
		Flavor:     "H100",
		SeedSource: "reservation-r6b",
		Now:        now,
		Nodes:      []topology.SourceNode{sourceNode("node-a", "us-west", "cluster-a", "island-a", "H100", 4)},
		Leases:     []*v1.Lease{spare},
		Runs: map[string]*v1.Run{
			keys.NamespacedKey(run.Namespace, run.Name): run,
		},
	}

	result, err := Resolve(input)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(result.Actions) == 0 {
		t.Fatalf("expected actions")
	}
	if actions := metrics.Snapshot().ResolverActions; len(actions) != 0 {
		t.Fatalf("planning leaked resolver-action metrics: %v", actions)
	}
}
