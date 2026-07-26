package plugin

import (
	"context"
	"testing"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/cover"
)

// R7 §4: an UNBOUND or CONFLICTED namespace has no funding principal, and the
// fail-safe is that a fresh Run there is refused outright — cover finds no
// payer. The promise path deliberately SKIPS the funding gate and mints on
// pod-carried provenance, so unless it refuses here too, the refusal holds on
// every path except the one that actually commits: an admin adds a second-owner
// Budget, the namespace goes conflicted, and a Promise pod issued a moment
// earlier still mints its lease at PreBind.
//
// Namespace equality alone cannot catch this — BOTH budgets live in the run's
// own namespace, which is exactly what makes the namespace ambiguous.
func TestPromiseProvenanceRefusedWhenNamespaceConflicted(t *testing.T) {
	ctx := context.Background()
	good := cover.Segment{Namespace: "default", Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "west"}

	// Baseline: one owner in the namespace, provenance accepted.
	m := newManager(t, trainRun(), teamBudget(8))
	if !m.promiseProvenanceValid(ctx, "default", "train", good) {
		t.Fatalf("fixture is wrong: a bound namespace must accept its own envelope's provenance")
	}

	// The admin places a second Budget, with a different owner, in the SAME
	// namespace. OwnerOf("default") fails safe to "" and the provenance — still
	// naming a real budget and a real envelope in the run's own namespace — must
	// now be refused.
	second := &v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: "other", Namespace: "default"},
		Spec: v1.BudgetSpec{Owner: "org:ai:other", Envelopes: []v1.BudgetEnvelope{{
			Name: "east", Flavor: "H100-80GB", Concurrency: 8,
		}}},
	}
	conflicted := newManager(t, trainRun(), teamBudget(8), second)
	if conflicted.promiseProvenanceValid(ctx, "default", "train", good) {
		t.Errorf("a conflicted namespace derives no owner, so the sole committer must not mint for it; " +
			"the promise skipped the funding gate and this check is the only refusal left")
	}
}

// The same rule for a namespace with NO Budgets at all: unbound derives "" too,
// and a Promise pod naming a budget that does not exist there is already refused
// by the envelope walk — but one naming a budget in a namespace that has since
// had all its Budgets deleted must be refused for the tenancy reason, not by
// accident of lookup order.
func TestPromiseProvenanceRefusedWhenNamespaceUnbound(t *testing.T) {
	ctx := context.Background()
	m := newManager(t, trainRun()) // no Budgets at all
	seg := cover.Segment{Namespace: "default", Owner: "org:ai:team", BudgetName: "team", EnvelopeName: "west"}
	if m.promiseProvenanceValid(ctx, "default", "train", seg) {
		t.Errorf("an unbound namespace has no funding principal; its promises must not mint")
	}
}
