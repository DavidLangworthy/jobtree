package funding

import (
	"math"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

const (
	accrualPrefixOwner    = "formal-prefix-owner"
	accrualPrefixBudget   = "formal-prefix-budget"
	accrualPrefixEnvelope = "formal-prefix-envelope"
	accrualPrefixRun      = "formal-prefix-run"
)

func accrualPrefixFixture(maxHours int64) (Input, EnvelopeKey) {
	budget := budgetOf(
		accrualPrefixOwner,
		accrualPrefixBudget,
		nil,
		env(accrualPrefixEnvelope, 8, withMaxHours(maxHours)),
	)
	budget.CreationTimestamp = v1.NewTime(base.Add(-time.Hour))
	run := runOf(accrualPrefixRun, accrualPrefixOwner, base, false)
	lease := leaseOf(
		"formal-prefix-lease",
		accrualPrefixRun,
		accrualPrefixOwner,
		accrualPrefixBudget,
		accrualPrefixEnvelope,
		8,
		base,
	)
	return Input{
			Budgets: []v1.Budget{budget},
			Leases:  []v1.GPULease{lease},
			Runs:    runsMap(run),
			Now:     base.Add(4 * time.Hour),
			Period:  time.Hour,
		}, EnvelopeKey{
			Namespace: nsForOwner(accrualPrefixOwner),
			Budget:    accrualPrefixBudget,
			Envelope:  accrualPrefixEnvelope,
		}
}

func requireAccrualPrefixHours(t *testing.T, got, want float64, label string) {
	t.Helper()
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("%s = %.9f GPU-hours, want %.9f", label, got, want)
	}
}

// This is an executable known-bad specimen, not an endorsement. The lease
// burns 32 funded GPU-hours under a nonzero, legal effective time. Moving Start
// into the future later makes the current snapshot replay erase all 32 hours.
// Owner Ruling 6 requires the elapsed charge to remain 32 and only the
// post-edit interval to become Unfunded; production cannot do that until the P3
// persisted-history mechanism exists.
func TestCurrentSnapshotDelayedStartRewritesAccrualPrefix(t *testing.T) {
	in, key := accrualPrefixFixture(40)
	before := Evaluate(in).Envelope(key)
	requireAccrualPrefixHours(t, before.ConsumedGPUHours, 32, "before delayed Start")

	mutated := in
	mutated.Budgets = []v1.Budget{*in.Budgets[0].DeepCopy()}
	future := v1.NewTime(in.Now.Add(time.Hour))
	mutated.Budgets[0].Spec.Envelopes[0].Start = &future

	after := Evaluate(mutated).Envelope(key)
	requireAccrualPrefixHours(t, after.ConsumedGPUHours, 0, "current-snapshot charge after delayed Start")
	requireAccrualPrefixHours(t, after.HoursByClass[ClassUnfunded], 32, "rewritten Unfunded history")
	t.Log("KNOWN COUNTEREXAMPLE: delaying Start rewrites 32 spent GPU-hours to 0")
}

// This is the integral-axis twin of the delayed-Start defect. Reducing a
// 40-hour cap to 10 does not merely demote the run forward: the current replay
// clamps the historical charge to 10 and rewrites the other 22 elapsed hours
// as Unfunded. Ruling 6 requires the already-spent 32 to survive.
func TestCurrentSnapshotReducedCapRewritesAccrualPrefix(t *testing.T) {
	in, key := accrualPrefixFixture(40)
	before := Evaluate(in).Envelope(key)
	requireAccrualPrefixHours(t, before.ConsumedGPUHours, 32, "before reduced cap")

	mutated := in
	mutated.Budgets = []v1.Budget{*in.Budgets[0].DeepCopy()}
	reduced := int64(10)
	mutated.Budgets[0].Spec.Envelopes[0].MaxGPUHours = &reduced

	after := Evaluate(mutated).Envelope(key)
	requireAccrualPrefixHours(t, after.ConsumedGPUHours, 10, "current-snapshot charge after reduced cap")
	requireAccrualPrefixHours(t, after.HoursByClass[ClassOwned], 10, "retained Owned history")
	requireAccrualPrefixHours(t, after.HoursByClass[ClassUnfunded], 22, "rewritten Unfunded history")
	t.Log("KNOWN COUNTEREXAMPLE: reducing MaxGPUHours rewrites 32 spent GPU-hours to 10")
}
