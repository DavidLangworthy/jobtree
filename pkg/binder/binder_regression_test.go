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

// expectedSlice identifies a slice by placement, role, and payer. The payer
// budget stands in for the old role-based borrowed check: funding class is
// derived downstream by pkg/funding from PaidByBudget/PaidByEnvelope
// (Decision 3), so segment identity must survive on the lease's payer fields.
type expectedSlice struct {
	node   string
	gpus   int
	role   string
	paidBy string
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
		if lease.Spec.Slice.Role != want.role {
			t.Errorf("slice %d: lease %s has role %s, expected %s", i, lease.Name, lease.Spec.Slice.Role, want.role)
		}
		if want.paidBy != "" && lease.Spec.PaidByBudget != want.paidBy {
			t.Errorf("slice %d: lease %s paid by budget %s, expected %s", i, lease.Name, lease.Spec.PaidByBudget, want.paidBy)
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
	// The sponsor segment keeps Borrowed: true — the binder must ignore it
	// for roles (RoleBorrowed is gone; class is derived by pkg/funding) but
	// still split the slice at the segment boundary and record the payer.
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 3},
		{BudgetName: "mm", EnvelopeName: "west", Owner: "org:ai:mm", Quantity: 1, Borrowed: true},
	}}

	res, err := Materialize(Request{Run: reviewReproRun(), PackPlan: reviewReproPackPlan(), CoverPlan: coverPlan, Now: time.Unix(1767225600, 0)})
	if err != nil {
		t.Fatalf("materialize failed: %v", err)
	}
	assertSlices(t, res, []expectedSlice{
		{node: "node-1", gpus: 2, role: RoleActive, paidBy: "rai"},
		{node: "node-2", gpus: 1, role: RoleActive, paidBy: "rai"},
		{node: "node-2", gpus: 1, role: RoleActive, paidBy: "mm"},
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
		{node: "node-1", gpus: 1, role: RoleActive, paidBy: "mm"},
		{node: "node-1", gpus: 1, role: RoleActive, paidBy: "rai"},
		{node: "node-2", gpus: 2, role: RoleActive, paidBy: "rai"},
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

// Review finding (PR #11): two materializations for the same run in the
// same wall-clock instant must not collide when the caller seeds the name
// sequence with the existing lease count (shrink-then-grow can reuse group
// indices, so gNN does not disambiguate).
func TestMaterializeNamesUniqueAcrossSameInstantMaterializations(t *testing.T) {
	now := time.Unix(1767225600, 0)
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 3},
		{BudgetName: "mm", EnvelopeName: "west", Owner: "org:ai:mm", Quantity: 1, Borrowed: true},
	}}

	first, err := Materialize(Request{Run: reviewReproRun(), PackPlan: reviewReproPackPlan(), CoverPlan: coverPlan, Now: now})
	if err != nil {
		t.Fatalf("first materialize failed: %v", err)
	}
	second, err := Materialize(Request{
		Run:       reviewReproRun(),
		PackPlan:  reviewReproPackPlan(),
		CoverPlan: coverPlan,
		Now:       now,
		NameSeed:  len(first.Leases),
	})
	if err != nil {
		t.Fatalf("second materialize failed: %v", err)
	}

	leaseNames := map[string]struct{}{}
	for _, lease := range first.Leases {
		leaseNames[lease.Name] = struct{}{}
	}
	for _, lease := range second.Leases {
		if _, dup := leaseNames[lease.Name]; dup {
			t.Errorf("lease name %s collides across materializations", lease.Name)
		}
	}
	podNames := map[string]struct{}{}
	for _, pod := range first.Pods {
		podNames[pod.Name] = struct{}{}
	}
	for _, pod := range second.Pods {
		if _, dup := podNames[pod.Name]; dup {
			t.Errorf("pod name %s collides across materializations", pod.Name)
		}
	}
}

// Review finding (PR #11): a spare shortfall (placements cover fewer GPUs
// than the group requested) must error, symmetric to the active path.
func TestMaterializeErrorsOnSpareShortfall(t *testing.T) {
	packPlan := pack.Plan{
		Flavor:    "H100-80GB",
		TotalGPUs: 4,
		Groups: []pack.GroupPlacement{{
			GroupIndex:      0,
			Size:            4,
			NodePlacements:  []pack.NodeAllocation{{Node: "node-1", GPUs: 4}},
			Spares:          4,
			SparePlacements: []pack.NodeAllocation{{Node: "node-2", GPUs: 2}},
		}},
	}
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 8},
	}}

	_, err := Materialize(Request{Run: reviewReproRun(), PackPlan: packPlan, CoverPlan: coverPlan, Now: time.Unix(1767225600, 0)})
	if err == nil || !strings.Contains(err.Error(), "spare allocation mismatch") {
		t.Fatalf("expected spare allocation mismatch, got: %v", err)
	}
}

// Review finding (PR #11): a negative chunk must not cancel out against the
// group-size check (size-4 group with chunks 6 and -2 silently materialized
// 6 GPUs).
func TestMaterializeErrorsOnNonPositiveChunk(t *testing.T) {
	packPlan := pack.Plan{
		Flavor:    "H100-80GB",
		TotalGPUs: 4,
		Groups: []pack.GroupPlacement{{
			GroupIndex: 0,
			Size:       4,
			NodePlacements: []pack.NodeAllocation{
				{Node: "node-1", GPUs: 6},
				{Node: "node-2", GPUs: -2},
			},
		}},
	}
	coverPlan := cover.Plan{Segments: []cover.Segment{
		{BudgetName: "rai", EnvelopeName: "west", Owner: "org:ai:rai", Quantity: 4},
	}}

	_, err := Materialize(Request{Run: reviewReproRun(), PackPlan: packPlan, CoverPlan: coverPlan, Now: time.Unix(1767225600, 0)})
	if err == nil || !strings.Contains(err.Error(), "non-positive placement chunk") {
		t.Fatalf("expected non-positive chunk rejection, got: %v", err)
	}
}
