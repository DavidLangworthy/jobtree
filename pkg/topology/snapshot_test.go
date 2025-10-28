package topology

import "testing"

func TestBuildSnapshotForFlavor(t *testing.T) {
	nodes := []SourceNode{
		fakeNode("a1", map[string]string{
			LabelRegion:       "us-west",
			LabelCluster:      "gpu-a",
			LabelFabricDomain: "A",
			LabelGPUFlavor:    "H100-80GB",
			LabelRack:         "rack-1",
		}, 8),
		fakeNode("a2", map[string]string{
			LabelRegion:       "us-west",
			LabelCluster:      "gpu-a",
			LabelFabricDomain: "A",
			LabelGPUFlavor:    "H100-80GB",
			LabelRack:         "rack-2",
		}, 8),
		fakeNode("b1", map[string]string{
			LabelRegion:       "us-west",
			LabelCluster:      "gpu-a",
			LabelFabricDomain: "B",
			LabelGPUFlavor:    "H100-80GB",
		}, 8),
		fakeNode("b2", map[string]string{
			LabelRegion:       "us-west",
			LabelCluster:      "gpu-b",
			LabelFabricDomain: "A",
			LabelGPUFlavor:    "A100-80GB",
		}, 8),
	}

	snapshot, err := BuildSnapshotForFlavor(nodes, map[string]int{"a1": 2}, "H100-80GB")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if snapshot.Flavor != "H100-80GB" {
		t.Fatalf("expected flavor H100-80GB, got %s", snapshot.Flavor)
	}

	if len(snapshot.Domains) != 2 {
		t.Fatalf("expected 2 domains, got %d", len(snapshot.Domains))
	}

	domA := snapshot.Domains[0]
	if domA.Key.Fabric != "A" {
		t.Fatalf("expected first domain to be A, got %s", domA.Key.Fabric)
	}
	if domA.FreeGPUs() != 14 { // two nodes 8 each minus usage 2
		t.Fatalf("expected 14 free GPUs, got %d", domA.FreeGPUs())
	}

	domB := snapshot.Domains[1]
	if domB.Key.Fabric != "B" {
		t.Fatalf("expected second domain fabric B, got %s", domB.Key.Fabric)
	}
	if domB.TotalGPUs() != 8 {
		t.Fatalf("expected 8 GPUs in domain B, got %d", domB.TotalGPUs())
	}
}

func TestBuildSnapshotMissingLabels(t *testing.T) {
	nodes := []SourceNode{
		fakeNode("broken", map[string]string{
			LabelRegion:    "us-west",
			LabelGPUFlavor: "H100-80GB",
		}, 8),
	}

	if _, err := BuildSnapshotForFlavor(nodes, nil, "H100-80GB"); err == nil {
		t.Fatalf("expected error due to missing labels")
	}
}

func fakeNode(name string, labels map[string]string, gpus int) SourceNode {
	return SourceNode{
		Name:   name,
		Labels: labels,
		GPUs:   gpus,
	}
}
