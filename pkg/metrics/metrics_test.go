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
	SetReservationBacklog("H100", 3600)
	IncResolverAction("Lottery")
	RecordBudgetUsage("org", "bud", "env", "H100", BudgetUsage{Owned: 32, Shared: 5, Borrowed: 8, Unfunded: 3, Spare: 4})
	SetSpareUsage("H100", 6)

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

	if v := snap.ReservationBacklog["H100"]; v != 3600 {
		t.Fatalf("expected backlog 3600, got %f", v)
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
}

func TestWritePrometheus(t *testing.T) {
	Reset()
	ObserveAdmission("H100", "bound", 100*time.Millisecond)
	SetReservationBacklog("H100", 120)
	IncResolverAction("Shrink")
	// All five derived classes are exposed per envelope (R14/R15).
	RecordBudgetUsage("org", "bud", "env", "H100", BudgetUsage{Owned: 10, Shared: 6, Borrowed: 2, Unfunded: 5, Spare: 1})
	SetSpareUsage("H100", 3)

	var buf bytes.Buffer
	WritePrometheus(&buf)
	output := buf.String()

	for _, needle := range []string{
		"jobtree_runs_admission_latency_seconds_count{flavor=\"H100\",result=\"bound\"} 1",
		"jobtree_reservations_backlog_seconds{flavor=\"H100\"} 120",
		"jobtree_resolver_actions_total{kind=\"Shrink\"} 1",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"owned\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 10",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"shared\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 6",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"borrowed\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 2",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"unfunded\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 5",
		"jobtree_budgets_concurrency_gpus{budget=\"bud\",class=\"spare\",envelope=\"env\",flavor=\"H100\",owner=\"org\"} 1",
		"jobtree_spares_concurrency_gpus{flavor=\"H100\"} 3",
	} {
		if !strings.Contains(output, needle) {
			t.Fatalf("expected output to contain %q, got:\n%s", needle, output)
		}
	}
}
