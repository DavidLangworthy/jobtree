package binder

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/pack"
)

func TestMaterializeSplitsCoverAcrossAllocations(t *testing.T) {
	run := &v1.Run{}
	run.Name = "train"
	run.Namespace = "default"
	run.Spec.Owner = "org:ai:rai"

	packPlan := pack.Plan{
		Flavor:    "H100-80GB",
		TotalGPUs: 6,
		Groups: []pack.GroupPlacement{
			{
				GroupIndex: 0,
				Size:       6,
				NodePlacements: []pack.NodeAllocation{
					{Node: "node-a", GPUs: 4},
					{Node: "node-b", GPUs: 2},
				},
			},
		},
	}

	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "core", Owner: "org:ai:rai", Quantity: 4},
		{BudgetName: "mm", EnvelopeName: "vision", Owner: "org:ai:mm", Quantity: 2, Borrowed: true},
	}}

	res, err := Materialize(Request{Run: run, PackPlan: packPlan, CoverPlan: coverPlan, Now: time.Unix(0, 0)})
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	if len(res.Pods) != 2 {
		t.Fatalf("expected 2 pods, got %d", len(res.Pods))
	}
	if res.Pods[0].NodeName != "node-a" {
		t.Fatalf("expected first pod on node-a, got %s", res.Pods[0].NodeName)
	}
	if len(res.Leases) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(res.Leases))
	}
	if res.Leases[0].Spec.PaidByEnvelope != "core" {
		t.Fatalf("expected first lease paid by core, got %s", res.Leases[0].Spec.PaidByEnvelope)
	}
	if res.Leases[1].Spec.Slice.Role != "Borrowed" {
		t.Fatalf("expected borrowed lease role, got %s", res.Leases[1].Spec.Slice.Role)
	}
	if len(res.Leases[0].Spec.Slice.Nodes) != 4 {
		t.Fatalf("expected 4 gpu slots in first lease, got %d", len(res.Leases[0].Spec.Slice.Nodes))
	}
	if len(res.Leases[1].Spec.Slice.Nodes) != 2 {
		t.Fatalf("expected 2 gpu slots in second lease, got %d", len(res.Leases[1].Spec.Slice.Nodes))
	}
}

func TestMaterializeErrorsWhenSegmentsInsufficient(t *testing.T) {
	run := &v1.Run{}
	run.Name = "train"
	run.Namespace = "default"
	run.Spec.Owner = "org:ai:rai"

	packPlan := pack.Plan{
		Flavor:    "H100-80GB",
		TotalGPUs: 4,
		Groups: []pack.GroupPlacement{
			{
				GroupIndex:     0,
				Size:           4,
				NodePlacements: []pack.NodeAllocation{{Node: "node-a", GPUs: 4}},
			},
		},
	}

	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "core", Owner: "org:ai:rai", Quantity: 2},
	}}

	_, err := Materialize(Request{Run: run, PackPlan: packPlan, CoverPlan: coverPlan, Now: time.Unix(0, 0)})
	if err == nil {
		t.Fatalf("expected error due to insufficient cover quantity")
	}
}
