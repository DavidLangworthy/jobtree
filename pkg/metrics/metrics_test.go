package metrics

import (
	"bytes"
	"math"
	"strings"
	"testing"
	"time"
)

func TestMetricsRecording(t *testing.T) {
	Reset()

	ObserveAdmission("H100", "bound", 150*time.Millisecond)
	ObserveForecastLatency("H100", 25*time.Millisecond)
	SetReservationBacklog("default/train-res-1", "H100", 3600)
	IncResolverAction("Lottery")
	RecordBudgetUsage("org", "bud", "env", "H100", BudgetUsage{Owned: 32, Shared: 5, Borrowed: 8, Unfunded: 3, Spare: 4})
	SetSpareUsage("H100", 6)
	IncElasticGrow("H100")
	IncElasticGrow("H100")
	IncElasticShrink("H100")
	SetElasticWidth("default/train", 128)
	ObserveDecideLatency("fundable", 5*time.Millisecond)
	ObserveDecideLatency("fundable", 7*time.Millisecond)
	ObserveDecideLatency("unfundable", 2*time.Millisecond)
	SetEvaluateInputSize(42)

	snap := Snapshot()

	hist, ok := snap.AdmissionLatency["H100"]["bound"]
	if !ok {
		t.Fatalf("expected histogram entry for H100 bound")
	}
	if hist.Count != 1 {
		t.Fatalf("expected histogram count 1, got %d", hist.Count)
	}
	if math.Abs(hist.Sum-0.150) > 1e-3 {
		t.Fatalf("expected histogram sum close to 0.150, got %f", hist.Sum)
	}

	forecastHist, ok := snap.ForecastLatency["H100"]
	if !ok {
		t.Fatalf("expected forecast latency entry for H100")
	}
	if forecastHist.Count != 1 {
		t.Fatalf("expected forecast latency count 1, got %d", forecastHist.Count)
	}

	if v := snap.ReservationBacklog["default/train-res-1"]; v.Seconds != 3600 || v.Flavor != "H100" {
		t.Fatalf("expected backlog {H100 3600}, got %+v", v)
	}

	if v := snap.ResolverActions["Lottery"]; v != 1 {
		t.Fatalf("expected resolver counter increment, got %f", v)
	}

	key := BudgetKey{Owner: "org", Budget: "bud", Envelope: "env", Flavor: "H100"}
	usage, ok := snap.BudgetUsage[key]
	if !ok {
		t.Fatalf("expected budget usage entry")
	}
	if usage.Owned != 32 || usage.Shared != 5 || usage.Borrowed != 8 || usage.Unfunded != 3 || usage.Spare != 4 {
		t.Fatalf("unexpected budget usage: %#v", usage)
	}

	if v := snap.SpareUsage["H100"]; v != 6 {
		t.Fatalf("expected spare usage 6, got %f", v)
	}

	if v := snap.ElasticGrows["H100"]; v != 2 {
		t.Fatalf("expected 2 elastic grows, got %f", v)
	}
	if v := snap.ElasticShrinks["H100"]; v != 1 {
		t.Fatalf("expected 1 elastic shrink, got %f", v)
	}
	if v := snap.ElasticWidth["default/train"]; v != 128 {
		t.Fatalf("expected elastic width 128, got %f", v)
	}
	if h := snap.DecideLatency["fundable"]; h.Count != 2 || math.Abs(h.Sum-0.012) > 1e-6 {
		t.Fatalf("expected 2 fundable decides summing to 0.012s, got count=%d sum=%f", h.Count, h.Sum)
	}
	if h := snap.DecideLatency["unfundable"]; h.Count != 1 {
		t.Fatalf("expected 1 unfundable decide, got count=%d", h.Count)
	}
	if snap.EvaluateInputSize != 42 {
		t.Fatalf("expected evaluate-input-size gauge 42, got %f", snap.EvaluateInputSize)
	}

	// Clearing removes the series entirely rather than zeroing it in place,
	// so a completed reservation/run does not linger in the gauge forever.
	ClearReservationBacklog("default/train-res-1")
	ClearElasticWidth("default/train")
	snap = Snapshot()
	if _, ok := snap.ReservationBacklog["default/train-res-1"]; ok {
		t.Fatalf("expected reservation backlog entry to be cleared")
	}
	if _, ok := snap.ElasticWidth["default/train"]; ok {
		t.Fatalf("expected elastic width entry to be cleared")
	}

	// The R4 hot-path series clear on Reset like the rest.
	Reset()
	if empty := Snapshot(); len(empty.DecideLatency) != 0 || empty.EvaluateInputSize != 0 {
		t.Fatalf("expected decide metrics cleared on Reset, got %d latencies / size %f", len(empty.DecideLatency), empty.EvaluateInputSize)
	}
}

func TestWritePrometheus(t *testing.T) {
	Reset()
	ObserveAdmission("H100", "bound", 100*time.Millisecond)
	ObserveForecastLatency("H100", 10*time.Millisecond)
	SetReservationBacklog("default/train-res-1", "H100", 120)
	IncResolverAction("Shrink")
	// All five derived classes are exposed per envelope (R14/R15).
	RecordBudgetUsage("org", "bud", "env", "H100", BudgetUsage{Owned: 10, Shared: 6, Borrowed: 2, Unfunded: 5, Spare: 1})
	SetSpareUsage("H100", 3)
	IncElasticGrow("H100")
	IncElasticShrink("H100")
	SetElasticWidth("default/train", 64)
	ObserveDecideLatency("fundable", 20*time.Millisecond)
	SetEvaluateInputSize(7)

	var buf bytes.Buffer
	WritePrometheus(&buf)
	output := buf.String()

	for _, needle := range []string{
		"jobtree_runs_admission_latency_seconds_count{flavor=\"H100\",result=\"bound\"} 1",
		"jobtree_forecast_latency_seconds_count{flavor=\"H100\"} 1",
		"jobtree_reservations_backlog_seconds{flavor=\"H100\",reservation=\"default/train-res-1\"} 120",
		"jobtree_resolver_actions_total{kind=\"Shrink\"} 1",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"owned\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 10",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"shared\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 6",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"borrowed\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 2",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"unfunded\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 5",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"spare\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 1",
		"jobtree_spares_concurrency_gpus{flavor=\"H100\"} 3",
		"jobtree_elastic_grows_total{flavor=\"H100\"} 1",
		"jobtree_elastic_shrinks_total{flavor=\"H100\"} 1",
		"jobtree_elastic_width_current{run=\"default/train\"} 64",
		"jobtree_plugin_decide_latency_seconds_count{result=\"fundable\"} 1",
		"jobtree_plugin_evaluate_input_leases 7",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("expected output to contain %q, got:\n%s", needle, output)
		}
	}
}
