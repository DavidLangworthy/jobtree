package metrics

import (
	"testing"
	"time"
)

func TestMetricsRecording(t *testing.T) {
	Reset()

	ObserveAdmission("H100", "bound", 150*time.Millisecond)
	SetReservationBacklog("H100", 3600)
	IncResolverAction("Lottery")
	RecordBudgetUsage("org", "bud", "env", "H100", 32, 8, 4)
	SetSpareUsage("H100", 6)

	snap := Snapshot()

	if samples, ok := snap.AdmissionLatency["H100"]; !ok || len(samples["bound"]) == 0 || samples["bound"][0] <= 0 {
		t.Fatalf("expected admission latency to be recorded, got %#v", samples)
	}

	if v := snap.ReservationBacklog["H100"]; v != 3600 {
		t.Fatalf("expected backlog 3600, got %f", v)
	}

	if v := snap.ResolverActions["Lottery"]; v != 1 {
		t.Fatalf("expected resolver counter increment, got %f", v)
	}

	key := BudgetKey{Owner: "org", Budget: "bud", Envelope: "env", Flavor: "H100"}
	if usage, ok := snap.BudgetUsage[key]; !ok || usage.Owned != 32 || usage.Borrowed != 8 || usage.Spare != 4 {
		t.Fatalf("unexpected budget usage: %#v", usage)
	}

	if v := snap.SpareUsage["H100"]; v != 6 {
		t.Fatalf("expected spare usage 6, got %f", v)
	}
}
