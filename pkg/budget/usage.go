package budget

import (
	"math"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// Usage captures live and cumulative consumption attributed to an envelope.
type Usage struct {
	Concurrency         int32
	GPUHours            float64
	BorrowedConcurrency int32
	BorrowedGPUHours    float64
	SpareConcurrency    int32
	SpareGPUHours       float64
}

// Headroom expresses the remaining capacity for an envelope or aggregate cap.
type Headroom struct {
	Concurrency int32
	GPUHours    *float64
}

// BudgetState summarises usage for a Budget.
type BudgetState struct {
	Budget     *v1.Budget
	Envelopes  map[string]*EnvelopeState
	Aggregates map[string]*AggregateState
}

// EnvelopeState tracks consumption for a single envelope.
type EnvelopeState struct {
	BudgetName string
	Owner      string
	Spec       v1.BudgetEnvelope
	Usage      Usage
	Aggregates []*AggregateState
}

// AggregateState tracks aggregate cap usage.
type AggregateState struct {
	Spec  v1.AggregateCap
	Usage Usage
}

// BuildBudgetState computes usage for all envelopes in a budget given the current leases.
func BuildBudgetState(budget *v1.Budget, leases []v1.Lease, now time.Time) *BudgetState {
	envelopes := make(map[string]*EnvelopeState)
	for i := range budget.Spec.Envelopes {
		env := budget.Spec.Envelopes[i]
		envelopes[env.Name] = &EnvelopeState{
			BudgetName: budget.ObjectMeta.Name,
			Owner:      budget.Spec.Owner,
			Spec:       env,
		}
	}

	for i := range leases {
		lease := leases[i]
		if lease.Spec.Owner != budget.Spec.Owner {
			continue
		}
		envState, ok := envelopes[lease.Spec.PaidByEnvelope]
		if !ok {
			continue
		}
		usage := computeLeaseUsage(&lease, now)
		envState.Usage.Concurrency += usage.Concurrency
		envState.Usage.GPUHours += usage.GPUHours
		if lease.Spec.Slice.Role == "Borrowed" {
			envState.Usage.BorrowedConcurrency += usage.Concurrency
			envState.Usage.BorrowedGPUHours += usage.GPUHours
		}
		if lease.Spec.Slice.Role == "Spare" {
			envState.Usage.SpareConcurrency += usage.SpareConcurrency
			envState.Usage.SpareGPUHours += usage.SpareGPUHours
		}
	}

	aggregates := make(map[string]*AggregateState)
	for i := range budget.Spec.AggregateCaps {
		cap := budget.Spec.AggregateCaps[i]
		aggregates[cap.Name] = &AggregateState{Spec: cap}
	}

	for _, envState := range envelopes {
		for i := range budget.Spec.AggregateCaps {
			cap := budget.Spec.AggregateCaps[i]
			if !contains(cap.Envelopes, envState.Spec.Name) {
				continue
			}
			agg := aggregates[cap.Name]
			agg.Usage.Concurrency += envState.Usage.Concurrency
			agg.Usage.GPUHours += envState.Usage.GPUHours
			agg.Usage.SpareConcurrency += envState.Usage.SpareConcurrency
			agg.Usage.SpareGPUHours += envState.Usage.SpareGPUHours
			envState.Aggregates = append(envState.Aggregates, agg)
		}
	}

	return &BudgetState{
		Budget:     budget,
		Envelopes:  envelopes,
		Aggregates: aggregates,
	}
}

func computeLeaseUsage(lease *v1.Lease, now time.Time) Usage {
	var quantity int32
	if lease.Spec.Slice.Nodes != nil {
		quantity = int32(len(lease.Spec.Slice.Nodes))
	}
	if quantity == 0 {
		quantity = 1
	}
	start := lease.Spec.Interval.Start.Time
	end := effectiveEnd(lease, now)
	if end.Before(start) {
		end = start
	}
	duration := end.Sub(start)
	if duration < 0 {
		duration = 0
	}
	hours := duration.Hours()
	if hours < 0 {
		hours = 0
	}
	usage := Usage{
		GPUHours: float64(quantity) * hours,
	}
	if isActive(lease, now) {
		usage.Concurrency = quantity
	}
	if lease.Spec.Slice.Role == "Spare" {
		usage.SpareConcurrency = quantity
		usage.SpareGPUHours = usage.GPUHours
	}
	return usage
}

// ComputeLeaseUsage exposes lease usage calculation for other packages.
func ComputeLeaseUsage(lease *v1.Lease, now time.Time) Usage {
	return computeLeaseUsage(lease, now)
}

func effectiveEnd(lease *v1.Lease, now time.Time) time.Time {
	candidates := []time.Time{}
	if lease.Spec.Interval.End != nil && !lease.Spec.Interval.End.IsZero() {
		candidates = append(candidates, lease.Spec.Interval.End.Time)
	}
	if lease.Status.Ended != nil && !lease.Status.Ended.IsZero() {
		candidates = append(candidates, lease.Status.Ended.Time)
	}
	candidates = append(candidates, now)
	min := candidates[0]
	for _, ts := range candidates[1:] {
		if ts.Before(min) {
			min = ts
		}
	}
	return min
}

func isActive(lease *v1.Lease, now time.Time) bool {
	start := lease.Spec.Interval.Start.Time
	if now.Before(start) {
		return false
	}
	if lease.Spec.Interval.End != nil && !lease.Spec.Interval.End.IsZero() {
		if !now.Before(lease.Spec.Interval.End.Time) {
			return false
		}
	}
	if lease.Status.Ended != nil && !lease.Status.Ended.IsZero() {
		if !now.Before(lease.Status.Ended.Time) {
			return false
		}
	}
	return true
}

// EnvelopeHeadroom returns the remaining capacity for an envelope.
func EnvelopeHeadroom(env *EnvelopeState, additional Usage) Headroom {
	used := env.Usage.Concurrency + additional.Concurrency
	remaining := env.Spec.Concurrency - used
	if remaining < 0 {
		remaining = 0
	}
	var gpuHeadroom *float64
	if env.Spec.MaxGPUHours != nil {
		max := float64(*env.Spec.MaxGPUHours)
		usedHours := env.Usage.GPUHours + additional.GPUHours
		remainingHours := max - usedHours
		if remainingHours < 0 {
			remainingHours = 0
		}
		gpuHeadroom = ptrFloat(math.Max(0, remainingHours))
	}
	return Headroom{Concurrency: remaining, GPUHours: gpuHeadroom}
}

// AggregateHeadroom computes remaining capacity for an aggregate cap.
func AggregateHeadroom(cap *AggregateState, additional Usage) Headroom {
	var concurrency int32 = math.MaxInt32
	if cap.Spec.MaxConcurrency != nil {
		used := cap.Usage.Concurrency + additional.Concurrency
		remaining := *cap.Spec.MaxConcurrency - used
		if remaining < 0 {
			remaining = 0
		}
		concurrency = remaining
	}
	var gpuHeadroom *float64
	if cap.Spec.MaxGPUHours != nil {
		max := float64(*cap.Spec.MaxGPUHours)
		used := cap.Usage.GPUHours + additional.GPUHours
		remaining := max - used
		if remaining < 0 {
			remaining = 0
		}
		gpuHeadroom = ptrFloat(remaining)
	}
	return Headroom{Concurrency: concurrency, GPUHours: gpuHeadroom}
}

func ptrFloat(v float64) *float64 {
	return &v
}

func contains(list []string, target string) bool {
	for _, item := range list {
		if item == target {
			return true
		}
	}
	return false
}
