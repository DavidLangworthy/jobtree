package controllers

import (
	"math"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/funding"
	"github.com/davidlangworthy/jobtree/pkg/metrics"
)

// Clock abstracts time for testing.
type Clock interface {
	Now() time.Time
}

// RealClock uses the system clock.
type RealClock struct{}

// Now returns the current UTC time.
func (RealClock) Now() time.Time { return time.Now().UTC() }

// BudgetMetrics records usage snapshots per envelope for observability tests.
type BudgetMetrics struct {
	entries map[metricKey]usageSnapshot
}

type metricKey struct {
	Owner    string
	Budget   string
	Envelope string
	Flavor   string
}

type usageSnapshot struct {
	Owned            float64
	Shared           float64
	Borrowed         float64
	Unfunded         float64
	Spare            float64
	ConsumedGPUHours float64
}

// NewBudgetMetrics constructs an empty recorder.
func NewBudgetMetrics() *BudgetMetrics {
	return &BudgetMetrics{entries: make(map[metricKey]usageSnapshot)}
}

// Snapshot returns a copy of the stored metrics.
func (m *BudgetMetrics) Snapshot() map[metricKey]usageSnapshot {
	out := make(map[metricKey]usageSnapshot, len(m.entries))
	for k, v := range m.entries {
		out[k] = v
	}
	return out
}

func (m *BudgetMetrics) record(key metricKey, usage usageSnapshot) {
	if m == nil {
		return
	}
	m.entries[key] = usage
}

// BudgetController updates Budget status and metrics from the funding
// derivation.
type BudgetController struct {
	clock   Clock
	metrics *BudgetMetrics
}

// NewBudgetController constructs a controller.
func NewBudgetController(clock Clock, metrics *BudgetMetrics) *BudgetController {
	if clock == nil {
		clock = RealClock{}
	}
	return &BudgetController{clock: clock, metrics: metrics}
}

// ReconcileBudget derives the budget's status from one funding evaluation.
// The evaluation is global on purpose: family sharing and lending mean
// another owner's leases can consume this budget's envelopes, so per-budget
// lease filtering cannot classify anything.
func (c *BudgetController) ReconcileBudget(budgetObj *v1.Budget, ev *funding.Evaluation) v1.BudgetStatus {
	if ev == nil {
		ev = funding.Evaluate(funding.Input{
			Budgets: []v1.Budget{*budgetObj},
			Now:     c.clock.Now(),
		})
	}
	headroom := make([]v1.EnvelopeHeadroom, 0, len(budgetObj.Spec.Envelopes))
	usage := make([]v1.EnvelopeUsage, 0, len(budgetObj.Spec.Envelopes))
	for i := range budgetObj.Spec.Envelopes {
		spec := &budgetObj.Spec.Envelopes[i]
		acct := ev.Envelope(funding.EnvelopeKey{Budget: budgetObj.ObjectMeta.Name, Envelope: spec.Name})
		if acct == nil {
			continue
		}
		// Only funded width consumes the envelope: unfunded leases name it
		// as payer but never charge its caps.
		remaining := spec.Concurrency - acct.FundedWidth()
		if remaining < 0 {
			remaining = 0
		}
		head := v1.EnvelopeHeadroom{
			Name:        spec.Name,
			Flavor:      spec.Flavor,
			Concurrency: remaining,
		}
		if r := acct.RemainingGPUHours(); r != nil {
			value := int64(math.Floor(*r + 1e-9))
			head.GPUHours = &value
		}
		headroom = append(headroom, head)
		usage = append(usage, v1.EnvelopeUsage{
			Name:             spec.Name,
			Flavor:           spec.Flavor,
			OwnedGPUs:        acct.WidthByClass[funding.ClassOwned],
			SharedGPUs:       acct.WidthByClass[funding.ClassShared],
			BorrowedGPUs:     acct.WidthByClass[funding.ClassBorrowed],
			UnfundedGPUs:     acct.WidthByClass[funding.ClassUnfunded],
			SpareGPUs:        acct.SpareWidth,
			ConsumedGPUHours: acct.ConsumedGPUHours,
		})
		c.updateMetrics(budgetObj, acct)
	}

	aggUsage := ev.Aggregates(budgetObj.ObjectMeta.Name)
	aggregateHeadroom := make([]v1.AggregateHeadroom, 0, len(aggUsage))
	for _, agg := range aggUsage {
		head := v1.AggregateHeadroom{
			Name:   agg.Name,
			Flavor: agg.Flavor,
		}
		if agg.MaxConcurrency != nil {
			remaining := *agg.MaxConcurrency - agg.FundedWidth
			if remaining < 0 {
				remaining = 0
			}
			head.Concurrency = ptrInt32(remaining)
		}
		if agg.MaxGPUHours != nil {
			remaining := float64(*agg.MaxGPUHours) - agg.ConsumedGPUHours
			if remaining < 0 {
				remaining = 0
			}
			value := int64(math.Floor(remaining + 1e-9))
			head.GPUHours = &value
		}
		aggregateHeadroom = append(aggregateHeadroom, head)
	}

	// jobtree_spares_concurrency_gpus is a cluster aggregate keyed only by
	// flavor, but each budget is reconciled independently. Computing it from
	// this budget's envelopes alone would let every budget clobber the gauge
	// with only its own spares. The evaluation is cluster-wide, so sum spare
	// width across all envelopes: every budget's reconcile then writes the
	// same correct total instead of racing.
	spareByFlavor := make(map[string]float64)
	for _, acct := range ev.Envelopes() {
		if acct.SpareWidth > 0 {
			spareByFlavor[acct.Spec.Flavor] += float64(acct.SpareWidth)
		}
	}
	for flavor, value := range spareByFlavor {
		metrics.SetSpareUsage(flavor, value)
	}

	updated := v1.BudgetStatus{
		Headroom:          headroom,
		AggregateHeadroom: aggregateHeadroom,
		Usage:             usage,
		UpdatedAt:         ptrTime(v1.NewTime(ev.Now)),
	}
	return updated
}

func (c *BudgetController) updateMetrics(budgetObj *v1.Budget, acct *funding.EnvelopeAccount) {
	snapshot := usageSnapshot{
		Owned:            float64(acct.WidthByClass[funding.ClassOwned]),
		Shared:           float64(acct.WidthByClass[funding.ClassShared]),
		Borrowed:         float64(acct.WidthByClass[funding.ClassBorrowed]),
		Unfunded:         float64(acct.WidthByClass[funding.ClassUnfunded]),
		Spare:            float64(acct.SpareWidth),
		ConsumedGPUHours: acct.ConsumedGPUHours,
	}
	key := metricKey{
		Owner:    acct.Owner,
		Budget:   budgetObj.ObjectMeta.Name,
		Envelope: acct.Spec.Name,
		Flavor:   acct.Spec.Flavor,
	}
	c.metrics.record(key, snapshot)
	metrics.RecordBudgetUsage(acct.Owner, budgetObj.ObjectMeta.Name, acct.Spec.Name, acct.Spec.Flavor, metrics.BudgetUsage{
		Owned:    snapshot.Owned,
		Shared:   snapshot.Shared,
		Borrowed: snapshot.Borrowed,
		Unfunded: snapshot.Unfunded,
		Spare:    snapshot.Spare,
	})
}

func ptrInt32(v int32) *int32 { return &v }

func ptrTime(t v1.Time) *v1.Time { return &t }
