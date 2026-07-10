package admission

import (
	"fmt"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/binder"
	"github.com/davidlangworthy/jobtree/pkg/cover"
	"github.com/davidlangworthy/jobtree/pkg/topology"
)

// These worlds mirror controllers/golden_test.go's scenarios. The point is to
// prove the admission moat (pack + funding/cover + binder.Materialize) produces
// the same commit here, in the plugin's new home, as it did inside the
// controller's Reconcile — before any scheduler-framework plumbing goes near it.

func sel() map[string]string {
	return map[string]string{
		topology.LabelRegion:       "us-west",
		topology.LabelCluster:      "cluster-a",
		topology.LabelFabricDomain: "island-a",
	}
}

func node(name string, gpus int) topology.SourceNode {
	labels := sel()
	labels[topology.LabelGPUFlavor] = "H100-80GB"
	return topology.SourceNode{Name: name, Labels: labels, GPUs: gpus}
}

func i32(v int32) *int32 { return &v }

func totalGPUs(leases []v1.Lease) int {
	n := 0
	for _, l := range leases {
		n += len(l.Spec.Slice.Nodes)
	}
	return n
}

// simple-fit: 4-GPU run on a 4-GPU node, one envelope. Admits; one Start lease
// paid by rai/west-h100 for 4 GPUs on node-a.
func TestPlanSimpleFit(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	in := Input{
		Now: now,
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "rai"},
			Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: sel(), Concurrency: 8,
			}}},
		}},
		Nodes: []topology.SourceNode{node("node-a", 4)},
		Run: &v1.Run{
			ObjectMeta: v1.ObjectMeta{Name: "train-8", Namespace: "default"},
			Spec:       v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 4}},
		},
	}
	in.Runs = map[string]*v1.Run{"default/train-8": in.Run}

	res, err := Plan(in)
	if err != nil {
		t.Fatalf("expected admission, got error: %v", err)
	}
	if len(res.Leases) != 1 {
		t.Fatalf("expected 1 lease, got %d", len(res.Leases))
	}
	l := res.Leases[0]
	if l.Spec.PaidByEnvelope != "west-h100" || l.Spec.PaidByBudget != "rai" {
		t.Errorf("payer = %s/%s, want rai/west-h100", l.Spec.PaidByBudget, l.Spec.PaidByEnvelope)
	}
	if l.Spec.Owner != "org:ai:rai" {
		t.Errorf("owner = %s, want org:ai:rai", l.Spec.Owner)
	}
	if l.Spec.Reason != "Start" {
		t.Errorf("reason = %s, want Start (binder default)", l.Spec.Reason)
	}
	if got := totalGPUs(res.Leases); got != 4 {
		t.Errorf("total GPUs = %d, want 4", got)
	}
	if len(res.Pods) != 1 || res.Pods[0].NodeName != "node-a" {
		t.Errorf("intended placement = %+v, want 1 pod on node-a", res.Pods)
	}
}

// borrow-sponsor-runs: family out of quota, sponsor lends the shortfall; admits
// as 96 owned (rai) + 32 borrowed (vision, capped at 32).
func TestPlanBorrowSponsorRuns(t *testing.T) {
	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	in := Input{
		Now: now,
		Budgets: []v1.Budget{
			{ObjectMeta: v1.ObjectMeta{Name: "rai"}, Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: sel(), Concurrency: 96,
			}}}},
			{ObjectMeta: v1.ObjectMeta{Name: "vision"}, Spec: v1.BudgetSpec{Owner: "org:ai:mm:vision", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: sel(), Concurrency: 64,
				Lending: &v1.LendingPolicy{Allow: true, To: []string{"org:ai:rai", "org:ai:rai:*"}, MaxConcurrency: i32(32)},
			}}}},
		},
	}
	for i := 0; i < 4; i++ {
		in.Nodes = append(in.Nodes, node(fmt.Sprintf("node-%d", i), 32))
	}
	in.Run = &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train-128", Namespace: "default"},
		Spec: v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 128},
			Locality: &v1.RunLocality{GroupGPUs: i32(32)},
			Funding:  &v1.RunFunding{AllowBorrow: true, MaxBorrowGPUs: i32(64), Sponsors: []string{"org:ai:mm:vision"}}},
	}
	in.Runs = map[string]*v1.Run{"default/train-128": in.Run}

	res, err := Plan(in)
	if err != nil {
		t.Fatalf("expected admission, got error: %v", err)
	}
	if got := totalGPUs(res.Leases); got != 128 {
		t.Errorf("total GPUs = %d, want 128", got)
	}
	var owned, borrowed int
	for _, l := range res.Leases {
		switch l.Spec.Owner {
		case "org:ai:rai":
			owned += len(l.Spec.Slice.Nodes)
		case "org:ai:mm:vision":
			borrowed += len(l.Spec.Slice.Nodes)
		}
	}
	if owned != 96 || borrowed != 32 {
		t.Errorf("owned/borrowed GPUs = %d/%d, want 96/32", owned, borrowed)
	}
}

// PerPodPayer attributes whole pods to envelopes in family-proximity order:
// the borrow gang's 96 owned + 32 borrowed GPUs become 24 rai pods + 8 vision
// pods at 4 GPUs/pod, owned first.
func TestPerPodPayerAttributesOwnedBeforeBorrowed(t *testing.T) {
	now := time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC)
	in := Input{
		Now: now,
		Budgets: []v1.Budget{
			{ObjectMeta: v1.ObjectMeta{Name: "rai"}, Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: sel(), Concurrency: 96,
			}}}},
			{ObjectMeta: v1.ObjectMeta{Name: "vision"}, Spec: v1.BudgetSpec{Owner: "org:ai:mm:vision", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: sel(), Concurrency: 64,
				Lending: &v1.LendingPolicy{Allow: true, To: []string{"org:ai:rai", "org:ai:rai:*"}, MaxConcurrency: i32(32)},
			}}}},
		},
	}
	for i := 0; i < 4; i++ {
		in.Nodes = append(in.Nodes, node(fmt.Sprintf("node-%d", i), 32))
	}
	in.Run = &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train-128", Namespace: "default"},
		Spec: v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 128},
			Locality: &v1.RunLocality{GroupGPUs: i32(32)},
			Funding:  &v1.RunFunding{AllowBorrow: true, MaxBorrowGPUs: i32(64), Sponsors: []string{"org:ai:mm:vision"}}},
	}
	in.Runs = map[string]*v1.Run{"default/train-128": in.Run}

	_, coverPlan, _, err := Feasible(in)
	if err != nil {
		t.Fatalf("feasible: %v", err)
	}
	payers, err := PerPodPayer(coverPlan, 4)
	if err != nil {
		t.Fatalf("perPodPayer: %v", err)
	}
	if len(payers) != 32 {
		t.Fatalf("pod count = %d, want 32 (128/4)", len(payers))
	}
	var rai, vision int
	for _, p := range payers {
		switch p.Owner {
		case "org:ai:rai":
			rai++
		case "org:ai:mm:vision":
			vision++
		}
	}
	if rai != 24 || vision != 8 {
		t.Errorf("rai/vision pods = %d/%d, want 24/8", rai, vision)
	}
	// Owned pods come before borrowed ones.
	if payers[0].Owner != "org:ai:rai" || payers[len(payers)-1].Owner != "org:ai:mm:vision" {
		t.Errorf("expected owned-first ordering, got first=%s last=%s", payers[0].Owner, payers[len(payers)-1].Owner)
	}

	// PodLease mints a well-formed per-pod lease against the actual bound node.
	l := PodLease(in.Run, payers[0], "node-2", 4, "train-128-pod-0", now, "Start")
	if l.Spec.PaidByEnvelope != "west-h100" || l.Spec.Owner != "org:ai:rai" {
		t.Errorf("lease payer = %s/%s, want org:ai:rai/west-h100", l.Spec.Owner, l.Spec.PaidByEnvelope)
	}
	if len(l.Spec.Slice.Nodes) != 4 || l.Spec.Slice.Nodes[0] != "node-2#0" {
		t.Errorf("lease slice = %v, want 4 slots on node-2", l.Spec.Slice.Nodes)
	}
	if l.Spec.Slice.Role != "Active" || l.Spec.Reason != "Start" {
		t.Errorf("lease role/reason = %s/%s, want Active/Start", l.Spec.Slice.Role, l.Spec.Reason)
	}
}

// Incremental delta funding (elastic grow): with the base leases already in the
// ledger, Feasible(Quantity=delta) funds ONLY the delta on top — not the whole
// run again — so a grow cohort is funded incrementally.
func TestFeasibleQuantityFundsDeltaOnly(t *testing.T) {
	now := time.Date(2024, 2, 2, 10, 0, 0, 0, time.UTC)
	build := func() Input {
		in := Input{
			Now: now,
			Budgets: []v1.Budget{{
				ObjectMeta: v1.ObjectMeta{Name: "team"},
				Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
					Name: "west", Flavor: "H100-80GB", Selector: sel(), Concurrency: 128,
				}}},
			}},
			Run: &v1.Run{
				ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
				Spec: v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 96},
					Locality: &v1.RunLocality{GroupGPUs: i32(32)}},
			},
		}
		for k := 0; k < 4; k++ {
			in.Nodes = append(in.Nodes, node(fmt.Sprintf("node-%d", k), 32))
		}
		in.Runs = map[string]*v1.Run{"default/train": in.Run}
		return in
	}

	// Base: fund the full run (96) and mint its leases into the ledger.
	base := build()
	baseRes, err := Plan(base)
	if err != nil {
		t.Fatalf("base admission: %v", err)
	}
	if totalGPUs(baseRes.Leases) != 96 {
		t.Fatalf("base leases = %d GPUs, want 96", totalGPUs(baseRes.Leases))
	}

	// Grow: with the 96 base leases in the ledger, fund a +32 delta only.
	grow := build()
	grow.Leases = baseRes.Leases
	grow.Quantity = 32
	grow.Reason = "Grow"
	growRes, err := Plan(grow)
	if err != nil {
		t.Fatalf("grow admission (delta funding): %v", err)
	}
	if got := totalGPUs(growRes.Leases); got != 32 {
		t.Errorf("grow leases = %d GPUs, want exactly the 32 delta (not the full run)", got)
	}
	for _, l := range growRes.Leases {
		if l.Spec.Reason != "Grow" {
			t.Errorf("grow lease reason = %q, want Grow", l.Spec.Reason)
		}
	}
}

// A grow that exceeds the budget's remaining headroom is not fundable: the
// envelope has room for the base but not the delta.
func TestFeasibleQuantityDeltaRespectsBudget(t *testing.T) {
	now := time.Date(2024, 2, 2, 10, 0, 0, 0, time.UTC)
	in := Input{
		Now: now,
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west", Flavor: "H100-80GB", Selector: sel(), Concurrency: 96, // exactly the base
			}}},
		}},
		Run: &v1.Run{
			ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"},
			Spec: v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 96},
				Locality: &v1.RunLocality{GroupGPUs: i32(32)}},
		},
	}
	for k := 0; k < 4; k++ {
		in.Nodes = append(in.Nodes, node(fmt.Sprintf("node-%d", k), 32))
	}
	in.Runs = map[string]*v1.Run{"default/train": in.Run}
	baseRes, err := Plan(in)
	if err != nil {
		t.Fatalf("base admission: %v", err)
	}
	in.Leases = baseRes.Leases
	in.Quantity = 32 // budget of 96 is fully consumed by the base
	if _, _, _, err := Feasible(in); err == nil {
		t.Fatalf("expected the +32 grow to be unfundable (budget of 96 exhausted by the base)")
	}
}

// capacity-missing: budget has headroom but the cluster is too small; pack fails
// → Plan errors, the caller's signal to reserve.
func TestPlanCapacityMissingErrors(t *testing.T) {
	now := time.Date(2024, 1, 1, 0, 0, 0, 0, time.UTC)
	in := Input{
		Now: now,
		Budgets: []v1.Budget{{
			ObjectMeta: v1.ObjectMeta{Name: "team"},
			Spec: v1.BudgetSpec{Owner: "org:ai:team", Envelopes: []v1.BudgetEnvelope{{
				Name: "west", Flavor: "H100-80GB", Selector: sel(), Concurrency: 16,
			}}},
		}},
		Nodes: []topology.SourceNode{node("node-a", 4)},
		Run: &v1.Run{
			ObjectMeta: v1.ObjectMeta{Name: "train-8", Namespace: "default"},
			Spec:       v1.RunSpec{Owner: "org:ai:team", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 8}},
		},
	}
	in.Runs = map[string]*v1.Run{"default/train-8": in.Run}

	if _, err := Plan(in); err == nil {
		t.Fatalf("expected a pack/cover error (capacity missing), got nil")
	}
}

// borrow-limited: the shortfall exceeds MaxBorrowGPUs, so cover cannot fund the
// full width → Plan errors (reservation fallback).
func TestPlanBorrowLimitedErrors(t *testing.T) {
	now := time.Date(2024, 1, 3, 0, 0, 0, 0, time.UTC)
	in := Input{
		Now: now,
		Budgets: []v1.Budget{
			{ObjectMeta: v1.ObjectMeta{Name: "rai"}, Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: sel(), Concurrency: 64,
			}}}},
			{ObjectMeta: v1.ObjectMeta{Name: "vision"}, Spec: v1.BudgetSpec{Owner: "org:ai:mm:vision", Envelopes: []v1.BudgetEnvelope{{
				Name: "west-h100", Flavor: "H100-80GB", Selector: sel(), Concurrency: 64,
				Lending: &v1.LendingPolicy{Allow: true, To: []string{"org:ai:rai", "org:ai:rai:*"}},
			}}}},
		},
	}
	for i := 0; i < 4; i++ {
		in.Nodes = append(in.Nodes, node(fmt.Sprintf("node-b-%d", i), 32))
	}
	in.Run = &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train-128", Namespace: "default"},
		Spec: v1.RunSpec{Owner: "org:ai:rai", Resources: v1.RunResources{GPUType: "H100-80GB", TotalGPUs: 128},
			Locality: &v1.RunLocality{GroupGPUs: i32(32)},
			Funding:  &v1.RunFunding{AllowBorrow: true, MaxBorrowGPUs: i32(8), Sponsors: []string{"org:ai:mm:vision"}}},
	}
	in.Runs = map[string]*v1.Run{"default/train-128": in.Run}

	if _, err := Plan(in); err == nil {
		t.Fatalf("expected a cover borrow-limit error, got nil")
	}
}

// R28b. The sole committer stamps the placement group onto every lease it mints,
// copied from the pod being bound. Before this it stamped none, and three separate
// consumers papered over the gap with a "0" default — so the resolver cut whole runs
// instead of groups and nobody could see it.
func TestPodLeaseWithRoleStampsThePlacementGroup(t *testing.T) {
	now := time.Now()
	run := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "default"}}
	seg := cover.Segment{Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "west"}

	lease := PodLeaseWithRole(run, seg, "node-a", 1, "train-pod-0-lease", now, "Start", binder.RoleActive, "3")
	if got := lease.Labels[binder.LabelGroupIndex]; got != "3" {
		t.Fatalf("minted lease group index = %q, want %q; the resolver, the elastic loop and the "+
			"node-failure swap all address work by it", got, "3")
	}

	// The plugin's in-memory phantom pending leases have no pod and no group. They
	// never reach the API, and pkg/invariant only constrains persisted leases.
	phantom := PodLeaseWithRole(run, seg, "node-a", 1, "pending-train-0", now, "Start", binder.RoleActive, "")
	if _, ok := phantom.Labels[binder.LabelGroupIndex]; ok {
		t.Errorf("a phantom pending lease must not invent a group index it does not have")
	}
}
