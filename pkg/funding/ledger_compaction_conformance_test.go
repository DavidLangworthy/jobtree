package funding

import (
	"crypto/sha256"
	"fmt"
	"math"
	"os"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
)

// This is an executable abstraction map from specs/LedgerCompaction.tla to
// Evaluate. Keep it deliberately narrow: one owned envelope, three fixed-width
// claims, integer-hour boundaries, and the Capacity=2 model configuration.
// Aggregate caps, lending, and persisted window identity belong to the pt2b
// models and are not implemented by production compaction yet.
const (
	ledgerModelCapacity       = 2
	ledgerModelLeaseCount     = 3
	ledgerModelLastTick       = 3
	ledgerModelLastBoundary   = 4
	ledgerModelTLAHash        = "c763b35c5efc892d9b5f823b22642205f85a95f12195b9eecdb43a214e6c92a6"
	ledgerModelConfigHash     = "c74a45615fa33afc0f0ac0e63888657c0eb829343dd5c96bedd8e3cac5cad7f3"
	ledgerConformanceOwner    = "formal-owner"
	ledgerConformanceBudget   = "formal-budget"
	ledgerConformanceEnvelope = "formal-envelope"
)

var ledgerConformanceEnvelopeKey = EnvelopeKey{
	Namespace: "default",
	Budget:    ledgerConformanceBudget,
	Envelope:  ledgerConformanceEnvelope,
}

type ledgerModelLease struct {
	enabled bool
	start   int
	end     int
	width   int
}

type ledgerModelHistory [ledgerModelLeaseCount]ledgerModelLease

type ledgerModelSet [ledgerModelLeaseCount]bool

type ledgerModelProjection struct {
	activeNow   [ledgerModelLeaseCount]bool
	fundedNow   [ledgerModelLeaseCount]bool
	activeHours [ledgerModelLeaseCount]int
	fundedHours [ledgerModelLeaseCount]int
}

type ledgerConformanceWorld struct {
	input   Input
	leases  [ledgerModelLeaseCount]v1.Lease
	present [ledgerModelLeaseCount]bool
}

// TestLedgerCompactionExecutableConformance exhausts the exact finite history
// domain in LedgerCompaction.tla under LedgerCompaction.cfg. It is independent
// of Evaluate's fill implementation: funded sets come from the TLA module's
// explicit CandidateSet order, not from production's greedy walk.
func TestLedgerCompactionExecutableConformance(t *testing.T) {
	assertLedgerCompactionModelPinned(t)

	shapes := ledgerModelShapes()
	if got, want := len(shapes), 21; got != want {
		t.Fatalf("model adapter generated %d lease shapes, want %d", got, want)
	}

	var histories, fullCases, replayCases, summaryCases int
	for _, first := range shapes {
		for _, second := range shapes {
			for _, third := range shapes {
				history := ledgerModelHistory{first, second, third}
				histories++
				world := newLedgerConformanceWorld(history)

				var priors [ledgerModelLastBoundary + 1]map[EnvelopeKey]SettledAccrual
				for horizon := 0; horizon <= ledgerModelLastBoundary; horizon++ {
					settled := history.settledBy(horizon)
					want := ledgerModelReplay(history, settled, horizon)
					priors[horizon] = ledgerModelPrior(want)

					settleInput := world.input
					settleInput.Now = ledgerModelTime(ledgerModelLastTick)
					got := SettleAccrual(settleInput, ledgerModelTime(horizon))
					context := fmt.Sprintf("history=%s horizon=%d", history, horizon)
					if horizon > ledgerModelLastTick {
						if got != nil {
							t.Fatalf("%s: SettleAccrual accepted a horizon ahead of Now: %#v", context, got)
						}
					} else {
						assertLedgerSummary(t, got, want, context)
					}
					summaryCases++
				}

				all := history.enabledSet()
				for now := 0; now <= ledgerModelLastTick; now++ {
					in := world.input
					in.Now = ledgerModelTime(now)
					wantFull := ledgerModelReplay(history, all, now)
					full := Evaluate(in)
					context := fmt.Sprintf("history=%s now=%d full", history, now)
					assertLedgerEvaluation(t, full, world, wantFull, wantFull, all, context)
					fullCases++

					for horizon := 0; horizon <= ledgerModelLastBoundary; horizon++ {
						compactedInput := in
						compactedInput.SettlementHorizon = ledgerModelTime(horizon)
						// Feed the independent model summary, not SettleAccrual's output.
						// This keeps the evaluator and settlement checks from sharing an oracle.
						compactedInput.PriorAccrual = priors[horizon]
						got := Evaluate(compactedInput)
						safe := history.safeAt(horizon, now)
						runSet := all
						wantRuns := wantFull
						if safe {
							runSet = history.retainedFrom(horizon)
							wantRuns = ledgerModelReplay(history, runSet, now)
						}
						context := fmt.Sprintf("history=%s now=%d horizon=%d safe=%t", history, now, horizon, safe)
						assertLedgerEvaluation(t, got, world, wantFull, wantRuns, runSet, context)
						if safe {
							assertSettledRunsDropped(t, got, history, horizon, context)
						}
						replayCases++
					}
				}
			}
		}
	}

	if histories != 9261 {
		t.Fatalf("checked %d histories, want the model's 21^3 = 9,261", histories)
	}
	t.Logf("checked %d histories: %d full replays, %d horizon replays, %d settlement summaries",
		histories, fullCases, replayCases, summaryCases)
}

func assertLedgerCompactionModelPinned(t *testing.T) {
	t.Helper()
	for _, source := range []struct {
		path string
		want string
	}{
		{path: "../../specs/LedgerCompaction.tla", want: ledgerModelTLAHash},
		{path: "../../specs/LedgerCompaction.cfg", want: ledgerModelConfigHash},
	} {
		contents, err := os.ReadFile(source.path)
		if err != nil {
			t.Fatalf("read pinned formal source %s: %v", source.path, err)
		}
		got := fmt.Sprintf("%x", sha256.Sum256(contents))
		if got != source.want {
			t.Fatalf("formal source %s changed (sha256 %s, want %s); review and update the executable abstraction map before refreshing the pin",
				source.path, got, source.want)
		}
	}
}

func ledgerModelShapes() []ledgerModelLease {
	// Disabled fields are canonicalized exactly as Init does.
	shapes := []ledgerModelLease{{enabled: false, start: 0, end: 1, width: 1}}
	for start := 0; start <= ledgerModelLastTick; start++ {
		for end := 1; end <= ledgerModelLastBoundary; end++ {
			if start >= end {
				continue
			}
			for width := 1; width <= 2; width++ {
				shapes = append(shapes, ledgerModelLease{enabled: true, start: start, end: end, width: width})
			}
		}
	}
	return shapes
}

func (history ledgerModelHistory) enabledSet() ledgerModelSet {
	var set ledgerModelSet
	for i, lease := range history {
		set[i] = lease.enabled
	}
	return set
}

func (history ledgerModelHistory) settledBy(horizon int) ledgerModelSet {
	var set ledgerModelSet
	for i, lease := range history {
		set[i] = lease.enabled && lease.end <= horizon
	}
	return set
}

func (history ledgerModelHistory) retainedFrom(horizon int) ledgerModelSet {
	var set ledgerModelSet
	for i, lease := range history {
		set[i] = lease.enabled && lease.end > horizon
	}
	return set
}

func (history ledgerModelHistory) safeAt(horizon, now int) bool {
	if horizon > now {
		return false
	}
	for i, retained := range history.retainedFrom(horizon) {
		if retained && history[i].start < horizon {
			return false
		}
	}
	return true
}

func (history ledgerModelHistory) String() string {
	return fmt.Sprintf("[%s,%s,%s]", history[0], history[1], history[2])
}

func (lease ledgerModelLease) String() string {
	if !lease.enabled {
		return "off"
	}
	return fmt.Sprintf("%d-%dx%d", lease.start, lease.end, lease.width)
}

// Candidate order is CandidateSet(1)..CandidateSet(8), with bit zero denoting
// lease 1. This is intentionally not implemented as a greedy algorithm.
var ledgerModelCandidates = [...]uint8{0b111, 0b011, 0b101, 0b001, 0b110, 0b010, 0b100, 0}

func ledgerModelFundedAt(history ledgerModelHistory, set ledgerModelSet, tick int) [ledgerModelLeaseCount]bool {
	var active [ledgerModelLeaseCount]bool
	for i, lease := range history {
		active[i] = set[i] && lease.enabled && lease.start <= tick && tick < lease.end
	}
	for _, candidate := range ledgerModelCandidates {
		valid := true
		width := 0
		var funded [ledgerModelLeaseCount]bool
		for i := range history {
			included := candidate&(1<<i) != 0
			if included && !active[i] {
				valid = false
			}
			if included {
				funded[i] = true
				width += history[i].width
			}
		}
		if valid && width <= ledgerModelCapacity {
			return funded
		}
	}
	panic("empty candidate must always be valid")
}

func ledgerModelReplay(history ledgerModelHistory, set ledgerModelSet, limit int) ledgerModelProjection {
	var out ledgerModelProjection
	for tick := 0; tick <= ledgerModelLastTick && tick < limit; tick++ {
		funded := ledgerModelFundedAt(history, set, tick)
		for i, lease := range history {
			if !set[i] || !lease.enabled || tick < lease.start || tick >= lease.end {
				continue
			}
			out.activeHours[i] += lease.width
			if funded[i] {
				out.fundedHours[i] += lease.width
			}
		}
	}
	out.fundedNow = ledgerModelFundedAt(history, set, limit)
	for i, lease := range history {
		out.activeNow[i] = set[i] && lease.enabled && lease.start <= limit && limit < lease.end
	}
	return out
}

func ledgerModelPrior(projection ledgerModelProjection) map[EnvelopeKey]SettledAccrual {
	funded, active := projection.totals()
	if active == 0 {
		return nil
	}
	return map[EnvelopeKey]SettledAccrual{
		ledgerConformanceEnvelopeKey: {
			ConsumedGPUHours: float64(funded),
			HoursByClass: map[Class]float64{
				ClassOwned:    float64(funded),
				ClassUnfunded: float64(active - funded),
			},
		},
	}
}

func (projection ledgerModelProjection) totals() (funded, active int) {
	for i := range projection.activeHours {
		funded += projection.fundedHours[i]
		active += projection.activeHours[i]
	}
	return funded, active
}

func newLedgerConformanceWorld(history ledgerModelHistory) ledgerConformanceWorld {
	budget := budgetOf(ledgerConformanceOwner, ledgerConformanceBudget, nil,
		env(ledgerConformanceEnvelope, ledgerModelCapacity))
	world := ledgerConformanceWorld{}
	var runs []*v1.Run
	var leases []v1.Lease
	for i, shape := range history {
		if !shape.enabled {
			continue
		}
		runName := ledgerConformanceRunName(i)
		runs = append(runs, runOf(runName, ledgerConformanceOwner,
			base.Add(time.Duration(i)*time.Minute), false))
		lease := leaseOf(ledgerConformanceLeaseName(i), runName, ledgerConformanceOwner,
			ledgerConformanceBudget, ledgerConformanceEnvelope, shape.width,
			ledgerModelTime(shape.start), endingAt(ledgerModelTime(shape.end)))
		world.leases[i] = lease
		world.present[i] = true
		leases = append(leases, lease)
	}
	world.input = Input{
		Budgets: []v1.Budget{budget},
		Leases:  leases,
		Runs:    runsMap(runs...),
		Period:  time.Hour,
	}
	return world
}

func ledgerModelTime(tick int) time.Time {
	return base.Add(time.Duration(tick) * time.Hour)
}

func ledgerConformanceRunName(index int) string {
	return fmt.Sprintf("formal-run-%d", index+1)
}

func ledgerConformanceLeaseName(index int) string {
	return fmt.Sprintf("formal-lease-%d", index+1)
}

func assertLedgerSummary(t *testing.T, got map[EnvelopeKey]SettledAccrual, want ledgerModelProjection, context string) {
	t.Helper()
	wantFunded, wantActive := want.totals()
	if wantActive == 0 {
		if len(got) != 0 {
			t.Fatalf("%s: empty settled epoch produced summary %#v", context, got)
		}
		return
	}
	if len(got) != 1 {
		t.Fatalf("%s: settlement summary has %d envelopes, want 1: %#v", context, len(got), got)
	}
	account, ok := got[ledgerConformanceEnvelopeKey]
	if !ok {
		t.Fatalf("%s: settlement summary omitted envelope %v: %#v", context, ledgerConformanceEnvelopeKey, got)
	}
	assertLedgerFloat(t, account.ConsumedGPUHours, wantFunded, context, "summary consumed GPU-hours")
	assertLedgerFloat(t, account.HoursByClass[ClassOwned], wantFunded, context, "summary owned GPU-hours")
	assertLedgerFloat(t, account.HoursByClass[ClassUnfunded], wantActive-wantFunded, context, "summary unfunded GPU-hours")
	assertLedgerFloat(t, account.HoursByClass[ClassShared], 0, context, "summary shared GPU-hours")
	assertLedgerFloat(t, account.HoursByClass[ClassBorrowed], 0, context, "summary borrowed GPU-hours")
}

func assertLedgerEvaluation(
	t *testing.T,
	got *Evaluation,
	world ledgerConformanceWorld,
	wantEnvelope ledgerModelProjection,
	wantRuns ledgerModelProjection,
	runSet ledgerModelSet,
	context string,
) {
	t.Helper()
	account := got.Envelope(ledgerConformanceEnvelopeKey)
	if account == nil {
		t.Fatalf("%s: production evaluation omitted envelope %v", context, ledgerConformanceEnvelopeKey)
	}
	wantFunded, wantActive := wantEnvelope.totals()
	assertLedgerFloat(t, account.ConsumedGPUHours, wantFunded, context, "consumed GPU-hours")
	assertLedgerFloat(t, account.HoursByClass[ClassOwned], wantFunded, context, "owned GPU-hours")
	assertLedgerFloat(t, account.HoursByClass[ClassUnfunded], wantActive-wantFunded, context, "unfunded GPU-hours")
	assertLedgerFloat(t, account.HoursByClass[ClassShared], 0, context, "shared GPU-hours")
	assertLedgerFloat(t, account.HoursByClass[ClassBorrowed], 0, context, "borrowed GPU-hours")

	var wantOwnedWidth, wantUnfundedWidth int32
	for i, lease := range world.leases {
		if !world.present[i] {
			continue
		}
		class, ok := got.Class(&lease)
		if ok != wantEnvelope.activeNow[i] {
			t.Fatalf("%s: lease %d classification presence=%t, want %t", context, i+1, ok, wantEnvelope.activeNow[i])
		}
		if !wantEnvelope.activeNow[i] {
			continue
		}
		wantClass := ClassUnfunded
		if wantEnvelope.fundedNow[i] {
			wantClass = ClassOwned
			wantOwnedWidth += int32(len(lease.Spec.Slice.Nodes))
		} else {
			wantUnfundedWidth += int32(len(lease.Spec.Slice.Nodes))
		}
		if class != wantClass {
			t.Fatalf("%s: lease %d class=%s, want %s", context, i+1, class, wantClass)
		}
	}
	if got := account.WidthByClass[ClassOwned]; got != wantOwnedWidth {
		t.Fatalf("%s: owned width=%d, want %d", context, got, wantOwnedWidth)
	}
	if got := account.WidthByClass[ClassUnfunded]; got != wantUnfundedWidth {
		t.Fatalf("%s: unfunded width=%d, want %d", context, got, wantUnfundedWidth)
	}
	if got := account.WidthByClass[ClassShared]; got != 0 {
		t.Fatalf("%s: shared width=%d, want 0", context, got)
	}
	if got := account.WidthByClass[ClassBorrowed]; got != 0 {
		t.Fatalf("%s: borrowed width=%d, want 0", context, got)
	}

	for i, included := range runSet {
		if !included {
			continue
		}
		run := got.Run("default/" + ledgerConformanceRunName(i))
		fundedHours := float64(0)
		unfundedHours := float64(0)
		ownedWidth := int32(0)
		unfundedWidth := int32(0)
		if run != nil {
			fundedHours = run.GPUHours[ClassOwned]
			unfundedHours = run.GPUHours[ClassUnfunded]
			ownedWidth = run.GPUs[ClassOwned]
			unfundedWidth = run.GPUs[ClassUnfunded]
			assertLedgerFloat(t, run.GPUHours[ClassShared], 0, context, fmt.Sprintf("run %d shared GPU-hours", i+1))
			assertLedgerFloat(t, run.GPUHours[ClassBorrowed], 0, context, fmt.Sprintf("run %d borrowed GPU-hours", i+1))
		}
		assertLedgerFloat(t, fundedHours, wantRuns.fundedHours[i], context, fmt.Sprintf("run %d owned GPU-hours", i+1))
		assertLedgerFloat(t, unfundedHours, wantRuns.activeHours[i]-wantRuns.fundedHours[i], context, fmt.Sprintf("run %d unfunded GPU-hours", i+1))
		wantOwned := int32(0)
		wantUnfunded := int32(0)
		if wantRuns.activeNow[i] {
			if wantRuns.fundedNow[i] {
				wantOwned = int32(historyWidth(world, i))
			} else {
				wantUnfunded = int32(historyWidth(world, i))
			}
		}
		if ownedWidth != wantOwned || unfundedWidth != wantUnfunded {
			t.Fatalf("%s: run %d widths owned/unfunded=%d/%d, want %d/%d",
				context, i+1, ownedWidth, unfundedWidth, wantOwned, wantUnfunded)
		}
	}
}

func assertSettledRunsDropped(t *testing.T, got *Evaluation, history ledgerModelHistory, horizon int, context string) {
	t.Helper()
	for i, settled := range history.settledBy(horizon) {
		if settled && got.Run("default/"+ledgerConformanceRunName(i)) != nil {
			t.Fatalf("%s: safe compact replay retained settled run %d", context, i+1)
		}
	}
}

func historyWidth(world ledgerConformanceWorld, index int) int {
	if !world.present[index] {
		return 0
	}
	return len(world.leases[index].Spec.Slice.Nodes)
}

func assertLedgerFloat(t *testing.T, got float64, want int, context, field string) {
	t.Helper()
	if math.Abs(got-float64(want)) > 1e-9 {
		t.Fatalf("%s: %s=%v, want %d", context, field, got, want)
	}
}
