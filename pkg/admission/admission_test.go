package admission

import (
	"fmt"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
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
