package funding

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// R7 tenancy amendment §4/§11: the funding owner is DERIVED from the run's
// namespace, not a run-writable field. These tests pin the derivation, its
// two admin-error fail-safes (multi-owner namespace, leaf owner spanning
// namespaces), and the empty-borrower guard.

func TestOwnerOfDerivesFromNamespace(t *testing.T) {
	budgets := []v1.Budget{
		budgetOf("org:ai:rai", "rai", nil, env("west", 8)),
		budgetOf("org:ai:mm:vision", "vision", nil, env("west", 8)),
	}
	ev := Evaluate(Input{Budgets: budgets, Now: base})

	if got := ev.OwnerOf(nsForOwner("org:ai:rai")); got != "org:ai:rai" {
		t.Errorf("OwnerOf(rai ns) = %q, want org:ai:rai", got)
	}
	if got := ev.OwnerOf(nsForOwner("org:ai:mm:vision")); got != "org:ai:mm:vision" {
		t.Errorf("OwnerOf(vision ns) = %q, want org:ai:mm:vision", got)
	}
	// An unbound namespace derives the empty owner (fail-safe sentinel).
	if got := ev.OwnerOf("nobody"); got != "" {
		t.Errorf("OwnerOf(unbound) = %q, want empty", got)
	}
	if len(ev.Conflicts()) != 0 {
		t.Errorf("no conflicts expected, got %v", ev.Conflicts())
	}
}

// §4: a namespace whose Budgets carry two distinct owners is ambiguous — who
// pays? — and fails safe to unbound with a surfaced conflict.
func TestMultiOwnerNamespaceFailsSafe(t *testing.T) {
	// Two owners, same namespace: hand-build so both land in "shared-ns".
	budgets := []v1.Budget{
		{ObjectMeta: v1.ObjectMeta{Name: "a", Namespace: "shared-ns"},
			Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
		{ObjectMeta: v1.ObjectMeta{Name: "b", Namespace: "shared-ns"},
			Spec: v1.BudgetSpec{Owner: "org:ai:vision", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
	}
	ev := Evaluate(Input{Budgets: budgets, Now: base})

	if got := ev.OwnerOf("shared-ns"); got != "" {
		t.Errorf("multi-owner namespace must derive empty owner, got %q", got)
	}
	conflicts := ev.Conflicts()
	if len(conflicts) != 1 || conflicts[0].Namespace != "shared-ns" || conflicts[0].Reason != ConflictMultipleOwners {
		t.Errorf("expected one MultipleOwners conflict on shared-ns, got %v", conflicts)
	}
}

// §4 (S-1): the converse invariant. The SAME leaf owner bound in two namespaces
// would let a Run in one mint an Owned charge across the boundary (cover is
// owner-keyed cluster-wide). Both namespaces fail safe to unbound and the
// conflict is surfaced.
func TestLeafOwnerSpanningNamespacesFailsSafe(t *testing.T) {
	budgets := []v1.Budget{
		{ObjectMeta: v1.ObjectMeta{Name: "rai", Namespace: "alice"},
			Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
		{ObjectMeta: v1.ObjectMeta{Name: "rai", Namespace: "bob"},
			Spec: v1.BudgetSpec{Owner: "org:ai:rai", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
	}
	ev := Evaluate(Input{Budgets: budgets, Now: base})

	if got := ev.OwnerOf("alice"); got != "" {
		t.Errorf("collided namespace alice must derive empty owner, got %q", got)
	}
	if got := ev.OwnerOf("bob"); got != "" {
		t.Errorf("collided namespace bob must derive empty owner, got %q", got)
	}
	seen := map[string]bool{}
	for _, c := range ev.Conflicts() {
		if c.Reason != ConflictLeafOwnerSpansNamespaces || c.Owner != "org:ai:rai" {
			t.Errorf("unexpected conflict %v", c)
		}
		seen[c.Namespace] = true
	}
	if !seen["alice"] || !seen["bob"] {
		t.Errorf("both alice and bob should be surfaced, got %v", ev.Conflicts())
	}
}

// §4/§5: an INTERIOR tier (a pool named as some Budget's Parent) is exempt from
// the converse invariant — nothing classes Owned against a pool, and pools are
// admin-written at both ends. Two child budgets naming the same parent do not
// make the parent "span namespaces" in the forbidden sense.
func TestInteriorTierExemptFromInjectivity(t *testing.T) {
	budgets := []v1.Budget{
		budgetOf("org:ai:rai", "rai", []string{"org:ai"}, env("west", 8)),
		budgetOf("org:ai:vision", "vision", []string{"org:ai"}, env("west", 8)),
		// The pool itself, interior, in its own admin namespace.
		{ObjectMeta: v1.ObjectMeta{Name: "pool", Namespace: "org-ai-pool"},
			Spec: v1.BudgetSpec{Owner: "org:ai", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
	}
	ev := Evaluate(Input{Budgets: budgets, Now: base})
	if len(ev.Conflicts()) != 0 {
		t.Errorf("interior parent must not trip the injectivity fail-safe, got %v", ev.Conflicts())
	}
	if got := ev.OwnerOf(nsForOwner("org:ai:rai")); got != "org:ai:rai" {
		t.Errorf("leaf child still resolves, got %q", got)
	}
}

// §4 (C-1): the empty-borrower guard. A pre-existing Borrowed lease from a
// wide-open (To: ["*"]) sponsor must DEMOTE to Unfunded once its borrower's
// namespace becomes unbound — otherwise the fail-safe leaks.
func TestEmptyBorrowerNeverLends(t *testing.T) {
	if lendingAllows(&v1.LendingPolicy{Allow: true, To: []string{"*"}}, "") {
		t.Error("open policy must not lend to an empty (unbound) borrower")
	}
	if lendingAllows(&v1.LendingPolicy{Allow: true}, "") {
		t.Error("empty-To policy must not lend to an empty borrower")
	}
	// A real borrower still borrows.
	if !lendingAllows(&v1.LendingPolicy{Allow: true, To: []string{"*"}}, "org:ai:rai") {
		t.Error("bound borrower must still borrow under an open policy")
	}

	// End-to-end: sponsor lends west with To:["*"]; the borrower's namespace is
	// unbound (no budget), so the borrowed lease coasts Unfunded, not Borrowed.
	budgets := []v1.Budget{budgetOf("org:ai:vision", "vision", nil,
		env("west", 8, withLending(v1.LendingPolicy{Allow: true, To: []string{"*"}})))}
	guest := &v1.Run{ObjectMeta: v1.ObjectMeta{Name: "guest", Namespace: "unbound-ns",
		CreationTimestamp: v1.NewTime(base)}, Spec: v1.RunSpec{Resources: v1.RunResources{GPUType: testFlavor, TotalGPUs: 4}}}
	lease := leaseOf("l-guest", "guest", "org:ai:vision", "vision", "west", 4, base, forRunOwner("nobody:unbound"))
	// forRunOwner set the run ns to nsForOwner("nobody:unbound"); align the run.
	guest.Namespace = lease.Spec.RunRef.Namespace
	ev := Evaluate(Input{Budgets: budgets, Leases: []v1.GPULease{lease},
		Runs: map[string]*v1.Run{lease.Spec.RunRef.Namespace + "/guest": guest}, Now: base.Add(time.Hour)})
	if got := classOf(t, ev, []v1.GPULease{lease}, "l-guest"); got != ClassUnfunded {
		t.Errorf("borrowed lease from an unbound namespace must demote to Unfunded, got %s", got)
	}
}
