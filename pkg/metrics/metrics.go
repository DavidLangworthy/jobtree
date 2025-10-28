package metrics

import (
	"sync"
	"time"
)

type budgetKey struct {
	Owner    string
	Budget   string
	Envelope string
	Flavor   string
}

type budgetUsage struct {
	Owned    float64
	Borrowed float64
	Spare    float64
}

var (
	mu sync.Mutex

	admissionLatency   = make(map[string]map[string][]float64)
	reservationBacklog = make(map[string]float64)
	resolverActions    = make(map[string]float64)
	budgetData         = make(map[budgetKey]budgetUsage)
	spareData          = make(map[string]float64)
)

// ObserveAdmission records the duration of an admission attempt.
func ObserveAdmission(flavor, result string, dur time.Duration) {
	mu.Lock()
	defer mu.Unlock()
	if _, ok := admissionLatency[flavor]; !ok {
		admissionLatency[flavor] = make(map[string][]float64)
	}
	admissionLatency[flavor][result] = append(admissionLatency[flavor][result], dur.Seconds())
}

// SetReservationBacklog updates the backlog forecast for a flavor.
func SetReservationBacklog(flavor string, seconds float64) {
	mu.Lock()
	defer mu.Unlock()
	reservationBacklog[flavor] = seconds
}

// IncResolverAction increments the resolver action counter for the kind.
func IncResolverAction(kind string) {
	mu.Lock()
	defer mu.Unlock()
	resolverActions[kind]++
}

// RecordBudgetUsage emits usage gauges for owned and borrowed concurrency.
func RecordBudgetUsage(owner, budget, envelope, flavor string, owned, borrowed, spare float64) {
	mu.Lock()
	defer mu.Unlock()
	budgetData[budgetKey{Owner: owner, Budget: budget, Envelope: envelope, Flavor: flavor}] = budgetUsage{
		Owned:    owned,
		Borrowed: borrowed,
		Spare:    spare,
	}
}

// SetSpareUsage updates the spare usage gauge.
func SetSpareUsage(flavor string, value float64) {
	mu.Lock()
	defer mu.Unlock()
	spareData[flavor] = value
}

// BudgetKey identifies a budget usage entry.
type BudgetKey = budgetKey

// BudgetUsage exposes owned, borrowed, and spare concurrency values.
type BudgetUsage struct {
	Owned    float64
	Borrowed float64
	Spare    float64
}

// MetricsSnapshot captures a copy of the in-memory metrics state.
type MetricsSnapshot struct {
	AdmissionLatency   map[string]map[string][]float64
	ReservationBacklog map[string]float64
	ResolverActions    map[string]float64
	BudgetUsage        map[BudgetKey]BudgetUsage
	SpareUsage         map[string]float64
}

// Snapshot returns the current metrics data for inspection/testing.
func Snapshot() MetricsSnapshot {
	mu.Lock()
	defer mu.Unlock()
	snap := MetricsSnapshot{
		AdmissionLatency:   make(map[string]map[string][]float64, len(admissionLatency)),
		ReservationBacklog: make(map[string]float64, len(reservationBacklog)),
		ResolverActions:    make(map[string]float64, len(resolverActions)),
		BudgetUsage:        make(map[BudgetKey]BudgetUsage, len(budgetData)),
		SpareUsage:         make(map[string]float64, len(spareData)),
	}
	for flavor, byResult := range admissionLatency {
		snap.AdmissionLatency[flavor] = make(map[string][]float64, len(byResult))
		for result, samples := range byResult {
			snap.AdmissionLatency[flavor][result] = append([]float64(nil), samples...)
		}
	}
	for flavor, seconds := range reservationBacklog {
		snap.ReservationBacklog[flavor] = seconds
	}
	for kind, count := range resolverActions {
		snap.ResolverActions[kind] = count
	}
	for key, usage := range budgetData {
		snap.BudgetUsage[BudgetKey(key)] = BudgetUsage(usage)
	}
	for flavor, spare := range spareData {
		snap.SpareUsage[flavor] = spare
	}
	return snap
}

// Reset clears all recorded metrics (useful for tests).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	admissionLatency = make(map[string]map[string][]float64)
	reservationBacklog = make(map[string]float64)
	resolverActions = make(map[string]float64)
	budgetData = make(map[budgetKey]budgetUsage)
	spareData = make(map[string]float64)
}
