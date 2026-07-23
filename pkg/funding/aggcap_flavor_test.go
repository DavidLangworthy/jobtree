package funding

import (
	"math"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// A flavored AggregateCap bounds ONE flavor's usage. It used to be attached to its
// named envelopes purely by name (evaluate.go), so a cap naming an envelope of a
// DIFFERENT flavor summed that envelope's width/hours into the cap — a cross-flavor
// mis-count that could exceed or spuriously refuse a flavored cap. Codex #3,
// reproduced. The attach now filters by flavor.
func TestAggregateCapDoesNotCountAcrossFlavors(t *testing.T) {
	h100 := env("h100env", 8, func(e *v1.BudgetEnvelope) { e.Flavor = "H100-80GB" })
	a100 := env("a100env", 8, func(e *v1.BudgetEnvelope) { e.Flavor = "A100-40GB" })

	budget := budgetOf("team", "team-budget", nil, h100, a100)
	budget.Spec.AggregateCaps = []v1.AggregateCap{{
		Name:      "h100-cap",
		Flavor:    "H100-80GB",                    // scoped to H100 only...
		Envelopes: []string{"h100env", "a100env"}, // ...but names BOTH (an admission would now reject this)
	}}

	runH100 := runOf("run-h100", "team", base, false)
	runA100 := runOf("run-a100", "team", base.Add(time.Minute), false)
	leases := []v1.GPULease{
		leaseOf("l-h100", "run-h100", "team", "team-budget", "h100env", 4, base),
		leaseOf("l-a100", "run-a100", "team", "team-budget", "a100env", 4, base),
	}

	ev := Evaluate(Input{
		Budgets: []v1.Budget{budget},
		Leases:  leases,
		Runs:    runsMap(runH100, runA100),
		Now:     base.Add(2 * time.Hour),
	})

	// Both leases are funded on their own envelopes (sanity — both really contribute
	// width/hours somewhere), so any leak would be visible.
	if got := classOf(t, ev, leases, "l-h100"); got != ClassOwned {
		t.Fatalf("l-h100 should be Owned, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-a100"); got != ClassOwned {
		t.Fatalf("l-a100 should be Owned, got %s", got)
	}

	aggs := ev.Aggregates("team-budget")
	if len(aggs) != 1 {
		t.Fatalf("expected one aggregate usage entry, got %d", len(aggs))
	}
	agg := aggs[0]
	// Only h100env's 4 GPUs / 8 GPU-hours — NOT the A100 envelope's usage.
	if agg.FundedWidth != 4 {
		t.Fatalf("the H100 cap counted cross-flavor usage: FundedWidth=%d, want 4 (only its own flavor)", agg.FundedWidth)
	}
	if math.Abs(agg.ConsumedGPUHours-8.0) > 1e-9 {
		t.Fatalf("the H100 cap counted cross-flavor hours: ConsumedGPUHours=%v, want 8", agg.ConsumedGPUHours)
	}
}

// Validation rejects a cap that names an envelope of a different flavor, closing the
// hole at admission-webhook time (the funding-math fix above is the defense in depth
// for any budget already in this state).
func TestAggregateCapRejectsCrossFlavorEnvelopeAtValidation(t *testing.T) {
	h100 := env("h100env", 8, func(e *v1.BudgetEnvelope) { e.Flavor = "H100-80GB" })
	a100 := env("a100env", 8, func(e *v1.BudgetEnvelope) { e.Flavor = "A100-40GB" })
	budget := budgetOf("team", "team-budget", nil, h100, a100)
	budget.Spec.AggregateCaps = []v1.AggregateCap{{
		Name: "h100-cap", Flavor: "H100-80GB", Envelopes: []string{"h100env", "a100env"},
	}}
	if err := budget.ValidateCreate(); err == nil {
		t.Fatalf("a flavored cap naming a different-flavor envelope must be rejected at validation")
	}

	// The same cap naming only its own flavor's envelope validates.
	budget.Spec.AggregateCaps[0].Envelopes = []string{"h100env"}
	if err := budget.ValidateCreate(); err != nil {
		t.Fatalf("a flavor-consistent cap must validate, got %v", err)
	}
}
