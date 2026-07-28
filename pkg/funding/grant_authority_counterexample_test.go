package funding

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// Known-bad production correspondence for GrantAuthorityUnauthenticated.cfg.
// This is a pin, not an endorsement: an unrooted Budget in the attacker's own
// namespace self-names the victim's owner. The current snapshot treats both
// assertions as authoritative, so its global leaf-injectivity check erases the
// otherwise-unchanged victim binding.
func TestUnrootedSquatterChangesVictimBinding(t *testing.T) {
	victim := v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "wallet", Namespace: "victim"},
		Spec:       v1.BudgetSpec{Owner: "org:victim", Envelopes: []v1.BudgetEnvelope{env("west", 8)}},
	}
	squatter := v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "squatter", Namespace: "attacker"},
		Spec:       v1.BudgetSpec{Owner: "org:victim", Envelopes: []v1.BudgetEnvelope{env("east", 8)}},
	}
	for _, budget := range []v1.Budget{victim, squatter} {
		if err := budget.ValidateCreate(); err != nil {
			t.Fatalf("fixture Budget %s/%s is not API-legal: %v", budget.Namespace, budget.Name, err)
		}
	}

	before := Evaluate(Input{Budgets: []v1.Budget{victim}, Now: base})
	if got := before.OwnerOf("victim"); got != "org:victim" {
		t.Fatalf("fixture: victim owner before squatter = %q, want org:victim", got)
	}

	after := Evaluate(Input{Budgets: []v1.Budget{victim, squatter}, Now: base})
	if got := after.OwnerOf("victim"); got != "" {
		t.Fatalf("PINNED BEHAVIOUR CHANGED: unrooted squatter left victim owner %q; replace this pin with the rooted-authority regression when P5 lands", got)
	}
	if len(after.Conflicts()) != 2 {
		t.Fatalf("PINNED BEHAVIOUR CHANGED: got %d conflicts, want the victim and attacker both conflicted: %v",
			len(after.Conflicts()), after.Conflicts())
	}
	t.Log("pinned: an API-legal unrooted Budget in attacker changes victim OwnerOf from org:victim to empty")
}

// Known-bad production correspondence for GrantAuthorityOwnedNonlocal.cfg.
// Naming org:ai as a Parent makes the current evaluator exempt that principal
// from namespace injectivity. A run can then charge an envelope in the other
// namespace and still be classified Owned.
func TestInteriorExemptionAllowsOwnedChargeAcrossNamespaces(t *testing.T) {
	budgets := []v1.Budget{
		{
			ObjectMeta: v1.ObjectMeta{Name: "a-wallet", Namespace: "tenant-a"},
			Spec:       v1.BudgetSpec{Owner: "org:ai", Envelopes: []v1.BudgetEnvelope{env("west", 8)}},
		},
		{
			ObjectMeta: v1.ObjectMeta{Name: "b-wallet", Namespace: "tenant-b"},
			Spec:       v1.BudgetSpec{Owner: "org:ai", Envelopes: []v1.BudgetEnvelope{env("east", 8)}},
		},
		{
			ObjectMeta: v1.ObjectMeta{Name: "child", Namespace: "child"},
			Spec: v1.BudgetSpec{
				Owner:     "org:ai:child",
				Parents:   []string{"org:ai"},
				Envelopes: []v1.BudgetEnvelope{idleEnvelope()},
			},
		},
	}
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "tenant-a", CreationTimestamp: v1.NewTime(base)},
		Spec:       v1.RunSpec{Resources: v1.RunResources{GPUType: testFlavor, TotalGPUs: 4}},
	}
	lease := leaseOf("cross-ns", "train", "org:ai", "b-wallet", "east", 4, base)
	lease.Namespace = "tenant-a"
	lease.Spec.RunRef.Namespace = "tenant-a"
	lease.Spec.PaidByBudgetNamespace = "tenant-b"

	ev := Evaluate(Input{
		Budgets: budgets,
		Runs:    map[string]*v1.Run{"tenant-a/train": run},
		Leases:  []v1.GPULease{lease},
		Now:     base.Add(time.Hour),
	})
	if got := classOf(t, ev, []v1.GPULease{lease}, "cross-ns"); got != ClassOwned {
		t.Fatalf("PINNED BEHAVIOUR CHANGED: cross-namespace payer class = %s, want known-bad Owned", got)
	}
	if lease.Spec.PaidByBudgetNamespace == lease.Spec.RunRef.Namespace {
		t.Fatal("fixture: payer and run namespaces must differ")
	}
	t.Logf("pinned: class=Owned while payer namespace %q differs from run namespace %q",
		lease.Spec.PaidByBudgetNamespace, lease.Spec.RunRef.Namespace)
}

// Known-bad production correspondence for GrantAuthorityLocalCapsOnly.cfg.
// Each lead is locally within its own 60-GPU envelope, but both paying
// lineages traverse a manager whose incoming instantaneous allocation is 100.
// The current evaluator never consults that ancestor, so all 120 GPUs remain
// Owned instead of demoting at least 20.
func TestLocalEnvelopeCapsDoNotConserveAncestorAllocation(t *testing.T) {
	manager := budgetOf("org:manager", "manager", nil, env("manager-cap", 100))
	leadA := budgetOf("org:manager:lead-a", "lead-a", []string{"org:manager"}, env("lead-cap", 60))
	leadB := budgetOf("org:manager:lead-b", "lead-b", []string{"org:manager"}, env("lead-cap", 60))

	runA := runOf("train-a", "org:manager:lead-a", base, false)
	runA.Spec.Resources.TotalGPUs = 60
	runB := runOf("train-b", "org:manager:lead-b", base, false)
	runB.Spec.Resources.TotalGPUs = 60
	leases := []v1.GPULease{
		leaseOf("lease-a", "train-a", "org:manager:lead-a", "lead-a", "lead-cap", 60, base),
		leaseOf("lease-b", "train-b", "org:manager:lead-b", "lead-b", "lead-cap", 60, base),
	}

	ev := Evaluate(Input{
		Budgets: []v1.Budget{manager, leadA, leadB},
		Runs:    runsMap(runA, runB),
		Leases:  leases,
		Now:     base.Add(time.Hour),
	})
	owned := ev.Run(runA.Namespace + "/" + runA.Name).GPUs[ClassOwned] +
		ev.Run(runB.Namespace + "/" + runB.Name).GPUs[ClassOwned]
	if owned != 120 {
		t.Fatalf("PINNED BEHAVIOUR CHANGED: locally admitted subtree owns %d GPUs, want known-bad 120; when ancestor conservation lands, assert <=100", owned)
	}
	if classOf(t, ev, leases, "lease-a") != ClassOwned || classOf(t, ev, leases, "lease-b") != ClassOwned {
		t.Fatalf("fixture: both locally-valid leases must expose the missing ancestor check")
	}
	t.Logf("pinned: manager allocation=100, descendant paying envelopes remain Owned at %d GPUs", owned)
}
