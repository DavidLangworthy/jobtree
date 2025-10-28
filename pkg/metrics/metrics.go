package metrics

import (
	"bufio"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

var (
	mu sync.Mutex

	admissionLatency   = make(map[string]map[string]*histogram)
	reservationBacklog = make(map[string]float64)
	resolverActions    = make(map[string]float64)
	budgetData         = make(map[BudgetKey]BudgetUsage)
	spareData          = make(map[string]float64)
)

var defaultBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

type histogram struct {
	buckets []float64
	counts  []uint64
	count   uint64
	sum     float64
}

func newHistogram() *histogram {
	return &histogram{
		buckets: append([]float64(nil), defaultBuckets...),
		counts:  make([]uint64, len(defaultBuckets)),
	}
}

func (h *histogram) observe(v float64) {
	h.count++
	h.sum += v
	for i, bound := range h.buckets {
		if v <= bound {
			h.counts[i]++
		}
	}
}

// ObserveAdmission records the duration of an admission attempt.
func ObserveAdmission(flavor, result string, dur time.Duration) {
	if flavor == "" || result == "" {
		return
	}
	mu.Lock()
	defer mu.Unlock()
	byResult, ok := admissionLatency[flavor]
	if !ok {
		byResult = make(map[string]*histogram)
		admissionLatency[flavor] = byResult
	}
	hist, ok := byResult[result]
	if !ok {
		hist = newHistogram()
		byResult[result] = hist
	}
	hist.observe(dur.Seconds())
}

// SetReservationBacklog updates the backlog forecast for a flavor.
func SetReservationBacklog(flavor string, seconds float64) {
	if flavor == "" {
		return
	}
	mu.Lock()
	reservationBacklog[flavor] = seconds
	mu.Unlock()
}

// IncResolverAction increments the resolver action counter for the kind.
func IncResolverAction(kind string) {
	if kind == "" {
		return
	}
	mu.Lock()
	resolverActions[kind]++
	mu.Unlock()
}

// RecordBudgetUsage emits usage gauges for owned and borrowed concurrency.
func RecordBudgetUsage(owner, budgetName, envelope, flavor string, owned, borrowed, spare float64) {
	if owner == "" || budgetName == "" || envelope == "" || flavor == "" {
		return
	}
	key := BudgetKey{Owner: owner, Budget: budgetName, Envelope: envelope, Flavor: flavor}
	mu.Lock()
	budgetData[key] = BudgetUsage{Owned: owned, Borrowed: borrowed, Spare: spare}
	mu.Unlock()
}

// SetSpareUsage updates the spare usage gauge.
func SetSpareUsage(flavor string, value float64) {
	if flavor == "" {
		return
	}
	mu.Lock()
	spareData[flavor] = value
	mu.Unlock()
}

// BudgetKey identifies a budget usage entry.
type BudgetKey struct {
	Owner    string
	Budget   string
	Envelope string
	Flavor   string
}

// BudgetUsage exposes owned, borrowed, and spare concurrency values.
type BudgetUsage struct {
	Owned    float64
	Borrowed float64
	Spare    float64
}

// Histogram aggregates observed values.
type Histogram struct {
	Buckets []float64
	Counts  []uint64
	Count   uint64
	Sum     float64
}

// MetricsSnapshot captures a copy of the metrics state.
type MetricsSnapshot struct {
	AdmissionLatency   map[string]map[string]Histogram
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
		AdmissionLatency:   make(map[string]map[string]Histogram, len(admissionLatency)),
		ReservationBacklog: make(map[string]float64, len(reservationBacklog)),
		ResolverActions:    make(map[string]float64, len(resolverActions)),
		BudgetUsage:        make(map[BudgetKey]BudgetUsage, len(budgetData)),
		SpareUsage:         make(map[string]float64, len(spareData)),
	}

	for flavor, byResult := range admissionLatency {
		snap.AdmissionLatency[flavor] = make(map[string]Histogram, len(byResult))
		for result, hist := range byResult {
			copyHist := Histogram{
				Buckets: append([]float64(nil), hist.buckets...),
				Counts:  append([]uint64(nil), hist.counts...),
				Count:   hist.count,
				Sum:     hist.sum,
			}
			snap.AdmissionLatency[flavor][result] = copyHist
		}
	}

	for flavor, seconds := range reservationBacklog {
		snap.ReservationBacklog[flavor] = seconds
	}

	for kind, count := range resolverActions {
		snap.ResolverActions[kind] = count
	}

	for key, usage := range budgetData {
		snap.BudgetUsage[key] = usage
	}

	for flavor, value := range spareData {
		snap.SpareUsage[flavor] = value
	}

	return snap
}

// Reset clears all recorded metrics (useful for tests).
func Reset() {
	mu.Lock()
	defer mu.Unlock()
	admissionLatency = make(map[string]map[string]*histogram)
	reservationBacklog = make(map[string]float64)
	resolverActions = make(map[string]float64)
	budgetData = make(map[BudgetKey]BudgetUsage)
	spareData = make(map[string]float64)
}

// Handler exposes the metrics using Prometheus' text exposition format.
func Handler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4")
		WritePrometheus(w)
	})
}

// WritePrometheus renders metrics into the Prometheus text exposition format.
func WritePrometheus(w io.Writer) {
	snap := Snapshot()
	buf := bufio.NewWriter(w)

	writeHeader(buf, "jobtree_runs_admission_latency_seconds", "Time to admit or reserve a run.", "histogram")
	flavors := sortedKeys(snap.AdmissionLatency)
	for _, flavor := range flavors {
		byResult := snap.AdmissionLatency[flavor]
		results := sortedKeys(byResult)
		for _, result := range results {
			hist := byResult[result]
			cumulative := uint64(0)
			for i, bound := range hist.Buckets {
				cumulative = hist.Counts[i]
				writeSample(buf, "jobtree_runs_admission_latency_seconds_bucket", map[string]string{
					"flavor": flavor,
					"result": result,
					"le":     formatFloat(bound),
				}, strconv.FormatUint(cumulative, 10))
			}
			writeSample(buf, "jobtree_runs_admission_latency_seconds_bucket", map[string]string{
				"flavor": flavor,
				"result": result,
				"le":     "+Inf",
			}, strconv.FormatUint(hist.Count, 10))
			writeSample(buf, "jobtree_runs_admission_latency_seconds_count", map[string]string{
				"flavor": flavor,
				"result": result,
			}, strconv.FormatUint(hist.Count, 10))
			writeSample(buf, "jobtree_runs_admission_latency_seconds_sum", map[string]string{
				"flavor": flavor,
				"result": result,
			}, formatFloat(hist.Sum))
		}
	}

	writeHeader(buf, "jobtree_reservations_backlog_seconds", "Forecasted backlog until pending reservations can start.", "gauge")
	for _, flavor := range sortedKeys(snap.ReservationBacklog) {
		value := snap.ReservationBacklog[flavor]
		writeSample(buf, "jobtree_reservations_backlog_seconds", map[string]string{"flavor": flavor}, formatFloat(value))
	}

	writeHeader(buf, "jobtree_resolver_actions_total", "Structural actions performed by the resolver.", "counter")
	for _, kind := range sortedKeys(snap.ResolverActions) {
		value := snap.ResolverActions[kind]
		writeSample(buf, "jobtree_resolver_actions_total", map[string]string{"kind": kind}, formatFloat(value))
	}

	writeHeader(buf, "jobtree_budgets_concurrency_gpus", "Current concurrency split into owned/borrowed/spare per envelope.", "gauge")
	budgetKeys := make([]BudgetKey, 0, len(snap.BudgetUsage))
	for key := range snap.BudgetUsage {
		budgetKeys = append(budgetKeys, key)
	}
	sort.Slice(budgetKeys, func(i, j int) bool {
		a, b := budgetKeys[i], budgetKeys[j]
		if a.Owner != b.Owner {
			return a.Owner < b.Owner
		}
		if a.Budget != b.Budget {
			return a.Budget < b.Budget
		}
		if a.Envelope != b.Envelope {
			return a.Envelope < b.Envelope
		}
		return a.Flavor < b.Flavor
	})
	for _, key := range budgetKeys {
		usage := snap.BudgetUsage[key]
		baseLabels := map[string]string{
			"owner":    key.Owner,
			"budget":   key.Budget,
			"envelope": key.Envelope,
			"flavor":   key.Flavor,
		}
		writeSample(buf, "jobtree_budgets_concurrency_gpus", mergeLabels(baseLabels, map[string]string{"class": "owned"}), formatFloat(usage.Owned))
		writeSample(buf, "jobtree_budgets_concurrency_gpus", mergeLabels(baseLabels, map[string]string{"class": "borrowed"}), formatFloat(usage.Borrowed))
		writeSample(buf, "jobtree_budgets_concurrency_gpus", mergeLabels(baseLabels, map[string]string{"class": "spare"}), formatFloat(usage.Spare))
	}

	writeHeader(buf, "jobtree_spares_concurrency_gpus", "Aggregate spare usage across envelopes.", "gauge")
	for _, flavor := range sortedKeys(snap.SpareUsage) {
		writeSample(buf, "jobtree_spares_concurrency_gpus", map[string]string{"flavor": flavor}, formatFloat(snap.SpareUsage[flavor]))
	}

	buf.Flush()
}

func writeHeader(w *bufio.Writer, name, help, kind string) {
	fmt.Fprintf(w, "# HELP %s %s\n", name, help)
	fmt.Fprintf(w, "# TYPE %s %s\n", name, kind)
}

func writeSample(w *bufio.Writer, name string, labels map[string]string, value string) {
	if len(labels) == 0 {
		fmt.Fprintf(w, "%s %s\n", name, value)
		return
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=\"%s\"", k, escapeLabel(labels[k]))
	}
	fmt.Fprintf(w, "%s{%s} %s\n", name, strings.Join(parts, ","), value)
}

func sortedKeys[K comparable, V any](m map[K]V) []K {
	keys := make([]K, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		switch ki := any(keys[i]).(type) {
		case string:
			return ki < any(keys[j]).(string)
		default:
			return fmt.Sprint(keys[i]) < fmt.Sprint(keys[j])
		}
	})
	return keys
}

func mergeLabels(base map[string]string, extra map[string]string) map[string]string {
	merged := make(map[string]string, len(base)+len(extra))
	for k, v := range base {
		merged[k] = v
	}
	for k, v := range extra {
		merged[k] = v
	}
	return merged
}

func escapeLabel(v string) string {
	v = strings.ReplaceAll(v, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\n", "\\n")
	v = strings.ReplaceAll(v, "\"", "\\\"")
	return v
}

func formatFloat(v float64) string {
	if v == 0 {
		return "0"
	}
	return strconv.FormatFloat(v, 'g', -1, 64)
}
