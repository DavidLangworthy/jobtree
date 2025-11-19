package resolver

import (
	"fmt"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func TestResolveDropsSparesFirst(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := buildRun("team", "default", "run-a", "H100")
	lease := buildLease(run, "0", "Spare", []string{"node-a#0", "node-a#1"}, now)

	input := Input{
		Deficit: 2,
		Flavor:  "H100",
		Scope: map[string]string{
			topology.LabelRegion:       "us-west",
			topology.LabelCluster:      "cluster-a",
			topology.LabelFabricDomain: "island-a",
		},
		SeedSource: "reservation-1",
		Now:        now,
		Nodes: []topology.SourceNode{{
			Name: "node-a",
			Labels: map[string]string{
				topology.LabelRegion:       "us-west",
				topology.LabelCluster:      "cluster-a",
				topology.LabelFabricDomain: "island-a",
				topology.LabelGPUFlavor:    "H100",
			},
			GPUs: 4,
		}},
		Leases: []*v1.Lease{lease},
		Runs: map[string]*v1.Run{
			namespacedKey(run.Namespace, run.Name): run,
		},
	}

	result, err := Resolve(input)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(result.Actions) != 1 {
		t.Fatalf("expected one action, got %d", len(result.Actions))
	}
	action := result.Actions[0]
	if action.Kind != ActionDropSpare {
		t.Fatalf("expected drop spare action, got %s", action.Kind)
	}
	if action.GPUs != 2 {
		t.Fatalf("expected to free 2 GPUs, got %d", action.GPUs)
	}
	if action.Reason != "DropSpare" {
		t.Fatalf("expected DropSpare reason, got %s", action.Reason)
	}
}

func TestResolveShrinksBeforeLottery(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	run := buildRun("team", "default", "elastic", "H100")
	run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 8, MaxTotalGPUs: 16, StepGPUs: 8}

	leaseA := buildLease(run, "0", "Active", []string{"node-a#0", "node-a#1", "node-a#2", "node-a#3", "node-a#4", "node-a#5", "node-a#6", "node-a#7"}, now)
	leaseB := buildLease(run, "1", "Active", []string{"node-b#0", "node-b#1", "node-b#2", "node-b#3", "node-b#4", "node-b#5", "node-b#6", "node-b#7"}, now)

	input := Input{
		Deficit:    8,
		Flavor:     "H100",
		Scope:      map[string]string{},
		SeedSource: "reservation-2",
		Now:        now,
		Nodes: []topology.SourceNode{
			sourceNode("node-a", "us-west", "cluster-a", "island-a", "H100", 8),
			sourceNode("node-b", "us-west", "cluster-a", "island-a", "H100", 8),
		},
		Leases: []*v1.Lease{leaseA, leaseB},
		Runs: map[string]*v1.Run{
			namespacedKey(run.Namespace, run.Name): run,
		},
	}

	result, err := Resolve(input)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	if len(result.Actions) != 1 {
		t.Fatalf("expected one shrink action, got %d", len(result.Actions))
	}
	action := result.Actions[0]
	if action.Kind != ActionShrink {
		t.Fatalf("expected shrink action, got %s", action.Kind)
	}
	if action.Reason != "Shrink" {
		t.Fatalf("expected Shrink reason, got %s", action.Reason)
	}
	if result.Seed != "" {
		t.Fatalf("expected no lottery seed, got %s", result.Seed)
	}
}

func TestResolveLotteryDeterministic(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	runA := buildRun("owner-a", "default", "run-a", "H100")
	runB := buildRun("owner-b", "default", "run-b", "H100")

	leaseA := buildLease(runA, "0", "Active", []string{"node-a#0", "node-a#1", "node-a#2", "node-a#3", "node-a#4", "node-a#5", "node-a#6", "node-a#7"}, now)
	leaseB := buildLease(runB, "0", "Active", []string{"node-b#0", "node-b#1", "node-b#2", "node-b#3", "node-b#4", "node-b#5", "node-b#6", "node-b#7"}, now)

	input := Input{
		Deficit:    8,
		Flavor:     "H100",
		Scope:      map[string]string{},
		SeedSource: "reservation-3",
		Now:        now,
		Nodes: []topology.SourceNode{
			sourceNode("node-a", "us-west", "cluster-a", "island-a", "H100", 8),
			sourceNode("node-b", "us-west", "cluster-a", "island-a", "H100", 8),
		},
		Leases: []*v1.Lease{leaseA, leaseB},
		Runs: map[string]*v1.Run{
			namespacedKey(runA.Namespace, runA.Name): runA,
			namespacedKey(runB.Namespace, runB.Name): runB,
		},
	}

	first, err := Resolve(input)
	if err != nil {
		t.Fatalf("resolve failed: %v", err)
	}
	second, err := Resolve(input)
	if err != nil {
		t.Fatalf("second resolve failed: %v", err)
	}
	if len(first.Actions) == 0 || len(second.Actions) == 0 {
		t.Fatalf("expected actions from lottery")
	}
	if first.Seed == "" || second.Seed == "" {
		t.Fatalf("expected lottery seeds")
	}
	if first.Seed != second.Seed {
		t.Fatalf("expected deterministic seed, got %s and %s", first.Seed, second.Seed)
	}
	if first.Actions[0].Lease.ObjectMeta.Name != second.Actions[0].Lease.ObjectMeta.Name {
		t.Fatalf("expected deterministic winner, got %s and %s", first.Actions[0].Lease.ObjectMeta.Name, second.Actions[0].Lease.ObjectMeta.Name)
	}
}

func buildRun(owner, namespace, name, flavor string) *v1.Run {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: namespace},
		Spec: v1.RunSpec{
			Owner: owner,
			Resources: v1.RunResources{
				GPUType:   flavor,
				TotalGPUs: 8,
			},
		},
	}
	return run
}

func buildLease(run *v1.Run, groupIndex, role string, nodes []string, now time.Time) *v1.Lease {
	lease := &v1.Lease{
		ObjectMeta: v1.ObjectMeta{
			Namespace: run.Namespace,
			Name:      fmt.Sprintf("%s-%s", run.Name, groupIndex),
			Labels: map[string]string{
				binder.LabelRunName:    run.Name,
				binder.LabelGroupIndex: groupIndex,
			},
		},
		Spec: v1.LeaseSpec{
			Owner: run.Spec.Owner,
			RunRef: v1.RunReference{
				Name:      run.Name,
				Namespace: run.Namespace,
			},
			Slice: v1.LeaseSlice{
				Nodes: nodes,
				Role:  role,
			},
			Interval: v1.LeaseInterval{
				Start: v1.NewTime(now),
			},
			PaidByEnvelope: "env",
			Reason:         "Start",
		},
	}
	return lease
}

func sourceNode(name, region, cluster, fabric, flavor string, gpus int) topology.SourceNode {
	return topology.SourceNode{
		Name: name,
		Labels: map[string]string{
			topology.LabelRegion:       region,
			topology.LabelCluster:      cluster,
			topology.LabelFabricDomain: fabric,
			topology.LabelGPUFlavor:    flavor,
		},
		GPUs: gpus,
	}
}
