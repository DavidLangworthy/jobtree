package pack

import (
	"testing"

	"github.com/davidlangworthy/jobtree/pkg/topology"
)

func TestPlanFillDomainsToEmpty(t *testing.T) {
	snapshot := buildSnapshot(t, []topology.SourceNode{
		fakeNode("a1", "us-west", "gpu-a", "A", 16),
		fakeNode("a2", "us-west", "gpu-a", "A", 16),
		fakeNode("b1", "us-west", "gpu-a", "B", 16),
	}, nil)

	plan, err := Planner(snapshot, Request{Flavor: "H100-80GB", TotalGPUs: 40, AllowCrossGroupSpread: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(plan.Groups))
	}
	if plan.Groups[0].Domain.Fabric != "A" || plan.Groups[0].Size != 32 {
		t.Fatalf("expected first chunk to consume domain A (32 GPUs), got %+v", plan.Groups[0])
	}
	if len(plan.Groups[0].NodePlacements) != 2 {
		t.Fatalf("expected two node allocations for group 0, got %d", len(plan.Groups[0].NodePlacements))
	}
	if plan.Groups[1].Domain.Fabric != "B" || plan.Groups[1].Size != 8 {
		t.Fatalf("expected remainder on domain B (8 GPUs), got %+v", plan.Groups[1])
	}
	if plan.Residual[plan.Groups[1].Domain] != 8 {
		t.Fatalf("expected 8 GPUs residual on domain B, got %d", plan.Residual[plan.Groups[1].Domain])
	}
}

func TestPlanWithGroupsPrefersUsedDomain(t *testing.T) {
	snapshot := buildSnapshot(t, []topology.SourceNode{
		fakeNode("a1", "us-west", "gpu-a", "A", 32),
		fakeNode("a2", "us-west", "gpu-a", "A", 16),
		fakeNode("b1", "us-west", "gpu-a", "B", 32),
		fakeNode("b2", "us-west", "gpu-a", "B", 32),
	}, nil)

	plan, err := Planner(snapshot, Request{Flavor: "H100-80GB", TotalGPUs: 96, GroupGPUs: intPtr(32), AllowCrossGroupSpread: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Groups) != 3 {
		t.Fatalf("expected 3 groups, got %d", len(plan.Groups))
	}
	if plan.Groups[0].Domain.Fabric != "B" {
		t.Fatalf("expected domain B first due to higher capacity, got %s", plan.Groups[0].Domain.Fabric)
	}
	if plan.Groups[1].Domain != plan.Groups[0].Domain {
		t.Fatalf("expected second group to stay on domain B")
	}
	if plan.Groups[2].Domain.Fabric != "A" {
		t.Fatalf("expected final group to land on domain A, got %s", plan.Groups[2].Domain.Fabric)
	}
}

func TestPlanSingleDomainRequirement(t *testing.T) {
	snapshot := buildSnapshot(t, []topology.SourceNode{
		fakeNode("a1", "us-west", "gpu-a", "A", 16),
		fakeNode("b1", "us-west", "gpu-a", "B", 16),
	}, nil)

	_, err := Planner(snapshot, Request{Flavor: "H100-80GB", TotalGPUs: 24, AllowCrossGroupSpread: false})
	if err == nil {
		t.Fatalf("expected error when no single domain can host run")
	}
}

func TestPlanRespectsExistingUsage(t *testing.T) {
	usage := map[string]int{"a1": 12}
	snapshot := buildSnapshot(t, []topology.SourceNode{
		fakeNode("a1", "us-west", "gpu-a", "A", 16),
		fakeNode("b1", "us-west", "gpu-a", "B", 16),
	}, usage)

	plan, err := Planner(snapshot, Request{Flavor: "H100-80GB", TotalGPUs: 12, AllowCrossGroupSpread: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("expected single allocation from domain B, got %d", len(plan.Groups))
	}
	if plan.Groups[0].Domain.Fabric != "B" || plan.Groups[0].Size != 12 {
		t.Fatalf("expected allocation from domain B for 12 GPUs, got %+v", plan.Groups[0])
	}
	if plan.Residual[topology.DomainKey{Region: "us-west", Cluster: "gpu-a", Fabric: "A"}] != 4 {
		t.Fatalf("expected 4 GPUs remaining on domain A")
	}
}

func TestPlanWorkedExampleShard(t *testing.T) {
	snapshot := buildSnapshot(t, []topology.SourceNode{
		fakeNode("a1", "us-west", "gpu-a", "A", 32),
		fakeNode("a2", "us-west", "gpu-a", "A", 32),
		fakeNode("b1", "us-west", "gpu-a", "B", 32),
		fakeNode("b2", "us-west", "gpu-a", "B", 32),
	}, nil)

	plan, err := Planner(snapshot, Request{Flavor: "H100-80GB", TotalGPUs: 64, GroupGPUs: intPtr(32), AllowCrossGroupSpread: true})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(plan.Groups) != 2 {
		t.Fatalf("expected 2 groups, got %d", len(plan.Groups))
	}
	if plan.Groups[0].Domain.Fabric != "A" || plan.Groups[1].Domain.Fabric != "A" {
		t.Fatalf("expected both groups to land on domain A before using B")
	}
	if plan.Residual[topology.DomainKey{Region: "us-west", Cluster: "gpu-a", Fabric: "A"}] != 0 {
		t.Fatalf("expected domain A to be full")
	}
}

func TestPlanAllocatesSparesInSameDomain(t *testing.T) {
	snapshot := buildSnapshot(t, []topology.SourceNode{
		fakeNode("a1", "us-west", "gpu-a", "A", 32),
		fakeNode("a2", "us-west", "gpu-a", "A", 8),
	}, nil)

	plan, err := Planner(snapshot, Request{Flavor: "H100-80GB", TotalGPUs: 32, GroupGPUs: intPtr(32), AllowCrossGroupSpread: true, SparesPerGroup: 4})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.TotalSpares != 4 {
		t.Fatalf("expected 4 total spares, got %d", plan.TotalSpares)
	}
	if len(plan.Groups) != 1 {
		t.Fatalf("expected single group, got %d", len(plan.Groups))
	}
	if len(plan.Groups[0].SparePlacements) != 1 || plan.Groups[0].SparePlacements[0].Node != "a2" {
		t.Fatalf("expected spare on node a2, got %+v", plan.Groups[0].SparePlacements)
	}
	if plan.Groups[0].SparePlacements[0].GPUs != 4 {
		t.Fatalf("expected 4 GPUs allocated as spare, got %d", plan.Groups[0].SparePlacements[0].GPUs)
	}
}

func TestPlanAllocatesSparesFallback(t *testing.T) {
	snapshot := buildSnapshot(t, []topology.SourceNode{
		fakeNode("a1", "us-west", "gpu-a", "A", 32),
		fakeNode("a2", "us-west", "gpu-a", "A", 2),
		fakeNode("b1", "us-west", "gpu-a", "B", 8),
	}, nil)

	plan, err := Planner(snapshot, Request{Flavor: "H100-80GB", TotalGPUs: 32, GroupGPUs: intPtr(32), AllowCrossGroupSpread: true, SparesPerGroup: 4})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if plan.TotalSpares != 4 {
		t.Fatalf("expected 4 total spares, got %d", plan.TotalSpares)
	}
	if len(plan.Groups[0].SparePlacements) != 2 {
		t.Fatalf("expected spare placements across domains, got %d", len(plan.Groups[0].SparePlacements))
	}
	fallbackFound := false
	for _, alloc := range plan.Groups[0].SparePlacements {
		if alloc.Node == "b1" {
			fallbackFound = true
		}
	}
	if !fallbackFound {
		t.Fatalf("expected spare fallback onto domain B, got %+v", plan.Groups[0].SparePlacements)
	}
}

func buildSnapshot(t *testing.T, nodes []topology.SourceNode, usage map[string]int) *topology.Snapshot {
	t.Helper()
	snapshot, err := topology.BuildSnapshotForFlavor(nodes, usage, "H100-80GB")
	if err != nil {
		t.Fatalf("failed to build snapshot: %v", err)
	}
	return snapshot
}

func fakeNode(name, region, cluster, fabric string, gpus int) topology.SourceNode {
	return topology.SourceNode{
		Name: name,
		Labels: map[string]string{
			topology.LabelRegion:       region,
			topology.LabelCluster:      cluster,
			topology.LabelFabricDomain: fabric,
			topology.LabelGPUFlavor:    "H100-80GB",
		},
		GPUs: gpus,
	}
}

func intPtr(v int) *int {
	return &v
}

// DeriveGroups is the single grouping rule the controller's re-emit path shares (it
// is exported for exactly that — see groupSizesFor). This exhaustive contract check
// pins its invariants so a change that would drift the two callers apart fails here:
// the sizes sum to total, none exceeds groupSize, only the last may be short, and a
// nil groupSize means one group. (R27 #61 pt2.)
func TestDeriveGroupsContract(t *testing.T) {
	if got := DeriveGroups(10, nil); len(got) != 1 || got[0] != 10 {
		t.Fatalf("DeriveGroups(10, nil) = %v, want [10] (one group)", got)
	}
	for total := 1; total <= 64; total++ {
		for size := 1; size <= 64; size++ {
			groups := DeriveGroups(total, &size)
			sum := 0
			for i, g := range groups {
				if g <= 0 || g > size {
					t.Fatalf("DeriveGroups(%d,%d): group %d = %d, want (0,%d]", total, size, i, g, size)
				}
				if g < size && i != len(groups)-1 {
					t.Fatalf("DeriveGroups(%d,%d): only the LAST group may be short, group %d = %d", total, size, i, g)
				}
				sum += g
			}
			if sum != total {
				t.Fatalf("DeriveGroups(%d,%d) sums to %d, want %d", total, size, sum, total)
			}
		}
	}
}
