package funding

import (
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// R7 §4 diagnostics must be a specification, not a coin flip. Two leaf owners
// can both span namespaces and both touch the SAME namespace; Go randomizes map
// iteration per range, so an unsorted derivation names a different culprit on
// each evaluation of identical input, and emits the conflicts in a different
// order. R26's auditor alarms off these records: a diagnostic that reorders
// looks like new events when nothing changed, and one that renames the culprit
// is one an operator cannot act on.
func TestConflictDiagnosticsAreDeterministic(t *testing.T) {
	// org:x is bound in {shared, x-only}; org:y in {shared, y-only}. Both are
	// leaves (no Budget names either as a Parent), so both span namespaces and
	// both collide on `shared`.
	budgets := []v1.Budget{
		{ObjectMeta: v1.ObjectMeta{Name: "x1", Namespace: "shared"},
			Spec: v1.BudgetSpec{Owner: "org:x", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
		{ObjectMeta: v1.ObjectMeta{Name: "x2", Namespace: "x-only"},
			Spec: v1.BudgetSpec{Owner: "org:x", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
		{ObjectMeta: v1.ObjectMeta{Name: "y1", Namespace: "shared"},
			Spec: v1.BudgetSpec{Owner: "org:y", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
		{ObjectMeta: v1.ObjectMeta{Name: "y2", Namespace: "y-only"},
			Spec: v1.BudgetSpec{Owner: "org:y", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
	}

	render := func(cs []BindingConflict) string {
		out := ""
		for _, c := range cs {
			out += c.Namespace + "|" + c.Owner + "|" + string(c.Reason) + ";"
		}
		return out
	}

	first := render(Evaluate(Input{Budgets: budgets, Now: base}).Conflicts())
	if first == "" {
		t.Fatalf("fixture is wrong: expected LeafOwnerSpansNamespaces conflicts, got none")
	}
	// One process, many evaluations: map iteration order is re-randomized per
	// range, so a non-deterministic walk shows up within a handful of rounds.
	for i := 0; i < 200; i++ {
		if got := render(Evaluate(Input{Budgets: budgets, Now: base}).Conflicts()); got != first {
			t.Fatalf("conflict diagnostics are not deterministic:\n  first:   %s\n  round %d: %s", first, i, got)
		}
	}
}

// The other half of the class-3 rail: the repetition test above re-runs identical
// input to catch Go's randomized MAP iteration, which no permutation of a slice
// can reach. This one permutes the INPUT SLICE, which no amount of repetition can
// reach — deriveOwners reads `budgets` in order, and the first writer into
// `ownersByNS`/`nsByOwner`/`interior` could otherwise decide the answer.
//
// Four budgets, twenty-four orderings, one answer — the shape
// controllers/order_independence_test.go established for the engine's other folds.
func TestOwnerDerivationIsInvariantUnderBudgetOrder(t *testing.T) {
	budgets := []v1.Budget{
		{ObjectMeta: v1.ObjectMeta{Name: "x1", Namespace: "shared"},
			Spec: v1.BudgetSpec{Owner: "org:x", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
		{ObjectMeta: v1.ObjectMeta{Name: "x2", Namespace: "x-only"},
			Spec: v1.BudgetSpec{Owner: "org:x", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
		{ObjectMeta: v1.ObjectMeta{Name: "y1", Namespace: "shared"},
			Spec: v1.BudgetSpec{Owner: "org:y", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
		{ObjectMeta: v1.ObjectMeta{Name: "solo", Namespace: "solo-ns"},
			Spec: v1.BudgetSpec{Owner: "org:solo", Parents: []string{"org:x"}, Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
	}
	namespaces := []string{"shared", "x-only", "solo-ns", "absent"}

	// A total, order-insensitive fingerprint of everything the derivation decides.
	fingerprint := func(ev *Evaluation) string {
		out := ""
		for _, ns := range namespaces {
			out += ns + "=>" + ev.OwnerOf(ns) + ";"
		}
		out += "|"
		for _, c := range ev.Conflicts() {
			out += c.Namespace + "|" + c.Owner + "|" + string(c.Reason) + ";"
		}
		return out
	}

	var permute func([]int) [][]int
	permute = func(idx []int) [][]int {
		if len(idx) <= 1 {
			return [][]int{append([]int(nil), idx...)}
		}
		var out [][]int
		for i := range idx {
			rest := append(append([]int{}, idx[:i]...), idx[i+1:]...)
			for _, p := range permute(rest) {
				out = append(out, append([]int{idx[i]}, p...))
			}
		}
		return out
	}

	want := fingerprint(Evaluate(Input{Budgets: budgets, Now: base}))
	for _, order := range permute([]int{0, 1, 2, 3}) {
		shuffled := make([]v1.Budget, 0, len(order))
		for _, i := range order {
			shuffled = append(shuffled, budgets[i])
		}
		if got := fingerprint(Evaluate(Input{Budgets: shuffled, Now: base})); got != want {
			t.Fatalf("owner derivation depends on Budget slice order:\n  order %v: %s\n  baseline: %s", order, got, want)
		}
	}
}

// PINNED BEHAVIOUR, not an endorsement (DECISIONS-NEEDED P5). The interior-tier
// exemption is deliberate — §4/§5 and C-4 say a pool may span admin namespaces,
// and TestInteriorTierExemptFromInjectivity covers that legitimate shape, where
// the pool owner is bound in exactly ONE namespace and merely NAMED as a Parent
// by children elsewhere.
//
// The shape below is the one nothing covered: an owner that is BOTH directly
// leaf-bound in two runnable namespaces AND named as some Budget's Parent. The
// `interior` set is keyed on the owner string alone, so being a Parent anywhere
// exempts the owner everywhere — and the injectivity fail-safe that exists to
// stop exactly this never fires. Removing the one child Budget makes it fire.
//
// Whether to narrow the exemption is David's call (it risks reaping the
// legitimate multi-namespace-pool case), so this test does not assert a fix. It
// asserts the hole, so the decision is taken against a fact and so the hole
// cannot close or widen without someone noticing.
func TestInteriorExemptionAdmitsALeafOwnerInTwoNamespaces(t *testing.T) {
	spanning := []v1.Budget{
		{ObjectMeta: v1.ObjectMeta{Name: "a-wallet", Namespace: "tenant-a"},
			Spec: v1.BudgetSpec{Owner: "org:ai", Envelopes: []v1.BudgetEnvelope{env("west", 8)}}},
		{ObjectMeta: v1.ObjectMeta{Name: "b-wallet", Namespace: "tenant-b"},
			Spec: v1.BudgetSpec{Owner: "org:ai", Envelopes: []v1.BudgetEnvelope{env("east", 8)}}},
	}

	// Control: with org:ai a pure leaf, the fail-safe fires on both namespaces.
	control := Evaluate(Input{Budgets: spanning, Now: base})
	if len(control.Conflicts()) != 2 || control.OwnerOf("tenant-a") != "" || control.OwnerOf("tenant-b") != "" {
		t.Fatalf("control is wrong: a leaf owner in two namespaces must fail safe, got owners %q/%q conflicts %v",
			control.OwnerOf("tenant-a"), control.OwnerOf("tenant-b"), control.Conflicts())
	}

	// Add ONE unrelated child Budget naming org:ai as a Parent. org:ai is now
	// "interior", and the same two-namespace binding sails through.
	child := v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "child", Namespace: "child-ns"},
		Spec: v1.BudgetSpec{Owner: "org:ai:child", Parents: []string{"org:ai"},
			Envelopes: []v1.BudgetEnvelope{env("south", 8)}},
	}
	exempt := Evaluate(Input{Budgets: append(append([]v1.Budget{}, spanning...), child), Now: base})
	if len(exempt.Conflicts()) != 0 || exempt.OwnerOf("tenant-a") != "org:ai" || exempt.OwnerOf("tenant-b") != "org:ai" {
		t.Fatalf("PINNED BEHAVIOUR CHANGED: the interior exemption no longer admits a leaf owner bound in two "+
			"namespaces (owners %q/%q, conflicts %v). If this is the P5 decision landing, replace this test with "+
			"an assertion of the new rule.",
			exempt.OwnerOf("tenant-a"), exempt.OwnerOf("tenant-b"), exempt.Conflicts())
	}
	t.Logf("pinned: one child Budget naming org:ai as a Parent makes OwnerOf(tenant-a)==OwnerOf(tenant-b)==%q with zero conflicts",
		exempt.OwnerOf("tenant-a"))
}

// PINNED BEHAVIOUR, not an endorsement: a namespace becoming conflicted
// retroactively erases the GPU-hours an already-open lease accrued while it WAS
// funded, and hands the envelope back headroom it had already spent. Whether the
// fail-safe should reach backwards through the replay is a tenancy/quota design
// question parked as P6 in docs/project/DECISIONS-NEEDED.md; this test exists so
// that decision is taken against a fact rather than a recollection, and so the
// behaviour cannot change silently.
func TestConflictRetroactivelyErasesAccruedHours(t *testing.T) {
	now := base.Add(4 * time.Hour)
	aBudget := v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "a-budget", Namespace: "tenant-a"},
		Spec:       v1.BudgetSpec{Owner: "org:ai", Envelopes: []v1.BudgetEnvelope{env("west", 8, withMaxHours(40))}},
	}
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "tenant-a", CreationTimestamp: v1.NewTime(base)},
		Spec:       v1.RunSpec{Resources: v1.RunResources{GPUType: testFlavor, TotalGPUs: 8}},
	}
	lease := v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Name: "l1", Namespace: "tenant-a",
			Labels: map[string]string{"rq.davidlangworthy.io/group-index": "0"}},
		Spec: v1.GPULeaseSpec{
			Owner:                 "org:ai",
			RunRef:                v1.RunReference{Name: "train", Namespace: "tenant-a"},
			Slice:                 v1.GPULeaseSlice{Nodes: []string{"n0", "n1", "n2", "n3", "n4", "n5", "n6", "n7"}, Role: "Active"},
			Interval:              v1.GPULeaseInterval{Start: v1.NewTime(base)},
			PaidByBudgetNamespace: "tenant-a",
			PaidByBudget:          "a-budget",
			PaidByEnvelope:        "west",
			Reason:                "Start",
		},
	}
	runs := map[string]*v1.Run{"tenant-a/train": run}
	leases := []v1.GPULease{lease}

	stats := func(ev *Evaluation) (consumed float64, remaining float64) {
		remaining = -1
		for _, acct := range ev.Envelopes() {
			consumed += acct.ConsumedGPUHours
			if r := acct.RemainingGPUHours(); r != nil {
				remaining = *r
			}
		}
		return
	}

	consumedBefore, remainingBefore := stats(Evaluate(Input{Budgets: []v1.Budget{aBudget}, Leases: leases, Runs: runs, Now: now}))
	if consumedBefore != 32 || remainingBefore != 8 {
		t.Fatalf("fixture: expected 32 accrued / 8 remaining GPU-hours before the conflict, got %.1f / %.1f",
			consumedBefore, remainingBefore)
	}

	other := v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "b-budget", Namespace: "tenant-a"},
		Spec:       v1.BudgetSpec{Owner: "org:other", Envelopes: []v1.BudgetEnvelope{env("east", 8)}},
	}
	consumedAfter, remainingAfter := stats(Evaluate(Input{Budgets: []v1.Budget{aBudget, other}, Leases: leases, Runs: runs, Now: now}))
	if consumedAfter != 0 || remainingAfter != 40 {
		t.Fatalf("PINNED BEHAVIOUR CHANGED: after the conflict the envelope reads %.1f consumed / %.1f remaining, "+
			"not 0 / 40. If this is the P6 decision landing, replace this test with an assertion of the new semantics.",
			consumedAfter, remainingAfter)
	}
	t.Logf("pinned: 32 GPU-hours really burned read as %.1f consumed, and %.1f of already-spent headroom is handed back",
		consumedAfter, remainingAfter)

	// And the other direction, which is the sharper statement of the same thing:
	// once the admin removes the conflicting Budget, the hours accrued DURING the
	// conflict are charged too — even though the lease was classed Unfunded for
	// every one of them. The envelope is not merely refunded and re-charged; the
	// conflicted interval is billed retroactively at the resolved rate.
	//
	// Timeline: bound for hour 1, conflicted for hour 2, resolved at hour 3, one
	// 4-GPU lease open throughout. Temporally-attributed accounting would read 8
	// charged (hours 1 and 3) + 4 unfunded (hour 2).
	small := v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "main", Namespace: "alice"},
		Spec:       v1.BudgetSpec{Owner: "org:x", Envelopes: []v1.BudgetEnvelope{env("west", 8)}},
	}
	rival := v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "second", Namespace: "alice"},
		Spec:       v1.BudgetSpec{Owner: "org:y", Envelopes: []v1.BudgetEnvelope{env("east", 8)}},
	}
	aliceRun := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: "train", Namespace: "alice", CreationTimestamp: v1.NewTime(base)},
		Spec:       v1.RunSpec{Resources: v1.RunResources{GPUType: testFlavor, TotalGPUs: 4}},
	}
	aliceLease := v1.GPULease{
		ObjectMeta: v1.ObjectMeta{Name: "l-alice", Namespace: "alice",
			Labels: map[string]string{"rq.davidlangworthy.io/group-index": "0"}},
		Spec: v1.GPULeaseSpec{
			Owner:                 "org:x",
			RunRef:                v1.RunReference{Name: "train", Namespace: "alice"},
			Slice:                 v1.GPULeaseSlice{Nodes: []string{"n1#0", "n1#1", "n1#2", "n1#3"}, Role: "Active"},
			Interval:              v1.GPULeaseInterval{Start: v1.NewTime(base)},
			PaidByBudgetNamespace: "alice",
			PaidByBudget:          "main",
			PaidByEnvelope:        "west",
			Reason:                "Start",
		},
	}
	aliceRuns := map[string]*v1.Run{"alice/train": aliceRun}
	aliceLeases := []v1.GPULease{aliceLease}
	at := func(budgets []v1.Budget, h int) float64 {
		c, _ := stats(Evaluate(Input{Budgets: budgets, Leases: aliceLeases, Runs: aliceRuns,
			Now: base.Add(time.Duration(h) * time.Hour)}))
		return c
	}
	bound, conflicted, resolved := at([]v1.Budget{small}, 1), at([]v1.Budget{small, rival}, 2), at([]v1.Budget{small}, 3)
	if bound != 4 || conflicted != 0 || resolved != 12 {
		t.Fatalf("PINNED BEHAVIOUR CHANGED: charged hours across bound/conflicted/resolved read %.1f/%.1f/%.1f, "+
			"not 4/0/12. If this is the P6 decision landing, assert the new semantics here.",
			bound, conflicted, resolved)
	}
	t.Logf("pinned: charged GPU-hours read %.1f while bound, %.1f while conflicted, %.1f once resolved "+
		"— the conflicted hour is billed retroactively despite having been classed Unfunded throughout",
		bound, conflicted, resolved)
}
