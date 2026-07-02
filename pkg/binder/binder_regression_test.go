package binder

import (
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/pack"
)

// Reproductions from the July 2026 design review (design-review-2026-07.md):
// one group of 4 GPUs on two 2-GPU nodes with segment boundaries that do not
// align with the node chunks. The broken binder produced a 3-GPU pod on a
// 2-GPU node, and with the segments reversed, two pods with the same name.

func reviewReproRun() *v1.Run {
	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"}}
	run.Spec.Owner = "org:ai:rai"
	return run
}

func reviewReproPackPlan() pack.Plan {
	return pack.Plan{
		Flavor:    "H100-80GB",
		TotalGPUs: 4,
		Groups: []pack.GroupPlacement{
			{
				GroupIndex: 0,
				Size:       4,
				NodePlacements: []pack.NodeAllocation{
					{Node: "node-1", GPUs: 2},
					{Node: "node-2", GPUs: 2},
				},
			},
		},
	}
}

type expectedSlice struct {
	node string
	gpus int
	role string
}

func assertSlices(t *testing.T, res Result, expected []expectedSlice) {
	t.Helper()
	if len(res.Pods) != len(expected) {
		t.Fatalf("expected %d pod slices, got %d: %+v", len(expected), len(res.Pods), res.Pods)
	}
	if len(res.Leases) != len(res.Pods) {
		t.Fatalf("expected %d leases to pair with pods, got %d", len(res.Pods), len(res.Leases))
	}
	for i, want := range expected {
		pod := res.Pods[i]
		if pod.NodeName != want.node || pod.GPUs != want.gpus || pod.Labels[LabelRunRole] != want.role {
			t.Errorf("slice %d: expected %d GPUs on %s as %s, got %d on %s as %s",
				i, want.gpus, want.node, want.role, pod.GPUs, pod.NodeName, pod.Labels[LabelRunRole])
		}
		lease := res.Leases[i]
		if len(lease.Spec.Slice.Nodes) != want.gpus {
			t.Errorf("slice %d: lease %s has %d slots, expected %d", i, lease.Name, len(lease.Spec.Slice.Nodes), want.gpus)
		}
		for _, slot := range lease.Spec.Slice.Nodes {
			if !strings.HasPrefix(slot, want.node+"#") {
				t.Errorf("slice %d: lease slot %s not on node %s", i, slot, want.node)
			}
		}
	}
	seenPods := map[string]struct{}{}
	for _, pod := range res.Pods {
		if _, dup := seenPods[pod.Name]; dup {
			t.Errorf("duplicate pod name %s", pod.Name)
		}
		seenPods[pod.Name] = struct{}{}
	}
	seenLeases := map[string]struct{}{}
	for _, lease := range res.Leases {
		if _, dup := seenLeases[lease.Name]; dup {
			t.Errorf("duplicate lease name %s", lease.Name)
		}
		seenLeases[lease.Name] = struct{}{}
	}
}

func TestMaterializeSegmentBoundaryInsideNodeChunk(t *testing.T) {
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 3},
		{BudgetName: "mm", EnvelopeName: "west", Owner: "org:ai:mm", Quantity: 1, Borrowed: true},
	}}

	res, err := Materialize(Request{Run: reviewReproRun(), PackPlan: reviewReproPackPlan(), CoverPlan: coverPlan, Now: time.Unix(1767225600, 0)})
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	assertSlices(t, res, []expectedSlice{
		{node: "node-1", gpus: 2, role: RoleActive},
		{node: "node-2", gpus: 1, role: RoleActive},
		{node: "node-2", gpus: 1, role: RoleBorrowed},
	})
}

func TestMaterializeSegmentBoundaryInsideNodeChunkReversed(t *testing.T) {
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "mm", EnvelopeName: "west", Owner: "org:ai:mm", Quantity: 1, Borrowed: true},
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 3},
	}}

	res, err := Materialize(Request{Run: reviewReproRun(), PackPlan: reviewReproPackPlan(), CoverPlan: coverPlan, Now: time.Unix(1767225600, 0)})
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	assertSlices(t, res, []expectedSlice{
		{node: "node-1", gpus: 1, role: RoleBorrowed},
		{node: "node-1", gpus: 1, role: RoleActive},
		{node: "node-2", gpus: 2, role: RoleActive},
	})
}

// The review's lease-collision reproduction: the owner's and the sponsor's
// budgets both contain an envelope named "west"; with names built from the
// envelope name plus a per-materialization timestamp, both leases got the
// same name.
func TestMaterializeLeaseNamesDistinctAcrossBudgets(t *testing.T) {
	packPlan := pack.Plan{
		Flavor:    "H100-80GB",
		TotalGPUs: 4,
		Groups: []pack.GroupPlacement{
			{GroupIndex: 0, Size: 4, NodePlacements: []pack.NodeAllocation{{Node: "node-1", GPUs: 4}}},
		},
	}
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 2},
		{BudgetName: "mm", EnvelopeName: "west", Owner: "org:ai:mm", Quantity: 2, Borrowed: true},
	}}

	res, err := Materialize(Request{Run: reviewReproRun(), PackPlan: packPlan, CoverPlan: coverPlan, Now: time.Unix(1767225600, 0)})
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	if len(res.Leases) != 2 {
		t.Fatalf("expected 2 leases, got %d", len(res.Leases))
	}
	if res.Leases[0].Name == res.Leases[1].Name {
		t.Fatalf("lease names collide: %s", res.Leases[0].Name)
	}
}

// R3: a cover plan that exactly covers the first group but leaves later
// groups unfunded must return an error, not panic on an empty segment slice.
func TestMaterializeErrorsWhenCoverExhaustedBeforeLaterGroups(t *testing.T) {
	packPlan := pack.Plan{
		Flavor:    "H100-80GB",
		TotalGPUs: 6,
		Groups: []pack.GroupPlacement{
			{GroupIndex: 0, Size: 4, NodePlacements: []pack.NodeAllocation{{Node: "node-1", GPUs: 4}}},
			{GroupIndex: 1, Size: 2, NodePlacements: []pack.NodeAllocation{{Node: "node-2", GPUs: 2}}},
		},
	}
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 4},
	}}

	_, err := Materialize(Request{Run: reviewReproRun(), PackPlan: packPlan, CoverPlan: coverPlan, Now: time.Unix(1767225600, 0)})
	if err == nil {
		t.Fatalf("expected an error for an under-provisioned cover plan")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("expected a cover-exhausted error, got: %v", err)
	}
}

// R3 companion: spares funded past the cover plan's end must also error.
func TestMaterializeErrorsWhenCoverExhaustedBeforeSpares(t *testing.T) {
	packPlan := pack.Plan{
		Flavor:    "H100-80GB",
		TotalGPUs: 4,
		Groups: []pack.GroupPlacement{
			{
				GroupIndex:      0,
				Size:            4,
				NodePlacements:  []pack.NodeAllocation{{Node: "node-1", GPUs: 4}},
				Spares:          2,
				SparePlacements: []pack.NodeAllocation{{Node: "node-2", GPUs: 2}},
			},
		},
	}
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 4},
	}}

	_, err := Materialize(Request{Run: reviewReproRun(), PackPlan: packPlan, CoverPlan: coverPlan, Now: time.Unix(1767225600, 0)})
	if err == nil {
		t.Fatalf("expected an error when spares outrun the cover plan")
	}
	if !strings.Contains(err.Error(), "exhausted") {
		t.Fatalf("expected a cover-exhausted error, got: %v", err)
	}
}

// R3 companion: an over-provisioned cover plan against a multi-group pack
// plan must be rejected.
func TestMaterializeErrorsWhenCoverOverProvisioned(t *testing.T) {
	packPlan := pack.Plan{
		Flavor:    "H100-80GB",
		TotalGPUs: 6,
		Groups: []pack.GroupPlacement{
			{GroupIndex: 0, Size: 4, NodePlacements: []pack.NodeAllocation{{Node: "node-1", GPUs: 4}}},
			{GroupIndex: 1, Size: 2, NodePlacements: []pack.NodeAllocation{{Node: "node-2", GPUs: 2}}},
		},
	}
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 8},
	}}

	_, err := Materialize(Request{Run: reviewReproRun(), PackPlan: packPlan, CoverPlan: coverPlan, Now: time.Unix(1767225600, 0)})
	if err == nil {
		t.Fatalf("expected an error for an over-provisioned cover plan")
	}
	if !strings.Contains(err.Error(), "unused cover quantity") {
		t.Fatalf("expected an unused-cover error, got: %v", err)
	}
}
