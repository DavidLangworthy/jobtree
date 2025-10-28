package controllers

import (
	"math"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/budget"
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
	Concurrency         float64
	GPUHours            float64
	BorrowedConcurrency float64
	BorrowedGPUHours    float64
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

// BudgetController updates Budget status and metrics based on leases.
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

// ReconcileBudget computes headroom status from the provided leases.
func (c *BudgetController) ReconcileBudget(budgetObj *v1.Budget, leases []v1.Lease) v1.BudgetStatus {
	now := c.clock.Now()
	state := budget.BuildBudgetState(budgetObj, leases, now)
	headroom := make([]v1.EnvelopeHeadroom, 0, len(state.Envelopes))
	for _, env := range state.Envelopes {
		additional := budget.Usage{}
		h := budget.EnvelopeHeadroom(env, additional)
		head := v1.EnvelopeHeadroom{
			Name:        env.Spec.Name,
			Flavor:      env.Spec.Flavor,
			Concurrency: h.Concurrency,
		}
		if env.Spec.MaxGPUHours != nil && h.GPUHours != nil {
			value := int64(math.Floor(*h.GPUHours + 1e-9))
			head.GPUHours = &value
		}
		headroom = append(headroom, head)
		c.updateMetrics(budgetObj, env)
	}

	aggregateHeadroom := make([]v1.AggregateHeadroom, 0, len(state.Aggregates))
	for _, agg := range state.Aggregates {
		additional := budget.Usage{}
		h := budget.AggregateHeadroom(agg, additional)
		head := v1.AggregateHeadroom{
			Name:   agg.Spec.Name,
			Flavor: agg.Spec.Flavor,
		}
		if agg.Spec.MaxConcurrency != nil {
			head.Concurrency = ptrInt32(h.Concurrency)
		}
		if agg.Spec.MaxGPUHours != nil && h.GPUHours != nil {
			value := int64(math.Floor(*h.GPUHours + 1e-9))
			head.GPUHours = &value
		}
		aggregateHeadroom = append(aggregateHeadroom, head)
	}

	updated := v1.BudgetStatus{
		Headroom:          headroom,
		AggregateHeadroom: aggregateHeadroom,
		UpdatedAt:         ptrTime(v1.NewTime(now)),
	}
	return updated
}

func (c *BudgetController) updateMetrics(budgetObj *v1.Budget, env *budget.EnvelopeState) {
	if c.metrics == nil {
		return
	}
	key := metricKey{
		Owner:    env.Owner,
		Budget:   budgetObj.ObjectMeta.Name,
		Envelope: env.Spec.Name,
		Flavor:   env.Spec.Flavor,
	}
	snapshot := usageSnapshot{
		Concurrency:         float64(env.Usage.Concurrency),
		GPUHours:            env.Usage.GPUHours,
		BorrowedConcurrency: float64(env.Usage.BorrowedConcurrency),
		BorrowedGPUHours:    env.Usage.BorrowedGPUHours,
	}
	c.metrics.record(key, snapshot)
}

func ptrInt32(v int32) *int32 { return &v }

func ptrTime(t v1.Time) *v1.Time { return &t }
