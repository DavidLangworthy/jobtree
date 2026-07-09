package funding

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

var base = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

const testFlavor = "H100-80GB"

func env(name string, concurrency int32, mods ...func(*v1.BudgetEnvelope)) v1.BudgetEnvelope {
	e := v1.BudgetEnvelope{
		Name:        name,
		Flavor:      testFlavor,
		Selector:    map[string]string{"region": "us-west"},
		Concurrency: concurrency,
	}
	for _, mod := range mods {
		mod(&e)
	}
	return e
}

func withMaxHours(hours int64) func(*v1.BudgetEnvelope) {
	return func(e *v1.BudgetEnvelope) { e.MaxGPUHours = &hours }
}

func withWindow(start, end time.Time) func(*v1.BudgetEnvelope) {
	return func(e *v1.BudgetEnvelope) {
		s, en := v1.NewTime(start), v1.NewTime(end)
		e.Start, e.End = &s, &en
	}
}

func withSharing(mode string) func(*v1.BudgetEnvelope) {
	return func(e *v1.BudgetEnvelope) { e.Sharing = mode }
}

func withLending(policy v1.LendingPolicy) func(*v1.BudgetEnvelope) {
	return func(e *v1.BudgetEnvelope) { e.Lending = &policy }
}

func budgetOf(owner, name string, parents []string, envelopes ...v1.BudgetEnvelope) v1.Budget {
	return v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default"},
		Spec:       v1.BudgetSpec{Owner: owner, Envelopes: envelopes, Parents: parents},
	}
}

func runOf(name, owner string, created time.Time, malleable bool) *v1.Run {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: "default", CreationTimestamp: v1.NewTime(created)},
		Spec: v1.RunSpec{
			Owner:     owner,
			Resources: v1.RunResources{GPUType: testFlavor, TotalGPUs: 8},
		},
	}
	if malleable {
		run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 1, MaxTotalGPUs: 64, StepGPUs: 1}
	}
	return run
}

type leaseOpt func(*v1.Lease)

func closedAt(t time.Time) leaseOpt {
	return func(l *v1.Lease) {
		ended := v1.NewTime(t)
		l.Status.Ended = &ended
		l.Status.Closed = true
	}
}

// endingAt sets a scheduled end without closing the lease, so it is still live
// before that instant. effectiveEnd honors it either way.
func endingAt(t time.Time) leaseOpt {
	return func(l *v1.Lease) {
		end := v1.NewTime(t)
		l.Spec.Interval.End = &end
	}
}

func withRole(role string) leaseOpt {
	return func(l *v1.Lease) { l.Spec.Slice.Role = role }
}

func withGroup(idx int) leaseOpt {
	return func(l *v1.Lease) { l.Labels["rq.davidlangworthy.io/group-index"] = fmt.Sprintf("%d", idx) }
}

func leaseOf(name, runName, payerOwner, budget, envelope string, width int, start time.Time, opts ...leaseOpt) v1.Lease {
	nodes := make([]string, width)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("node-%s#%d", name, i)
	}
	lease := v1.Lease{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: "default",
			Labels:    map[string]string{"rq.davidlangworthy.io/group-index": "0"},
		},
		Spec: v1.LeaseSpec{
			Owner:          payerOwner,
			RunRef:         v1.RunReference{Name: runName, Namespace: "default"},
			Slice:          v1.LeaseSlice{Nodes: nodes, Role: "Active"},
			Interval:       v1.LeaseInterval{Start: v1.NewTime(start)},
			PaidByBudget:   budget,
			PaidByEnvelope: envelope,
			Reason:         "Start",
		},
	}
	for _, opt := range opts {
		opt(&lease)
	}
	return lease
}

func runsMap(runs ...*v1.Run) map[string]*v1.Run {
	m := make(map[string]*v1.Run, len(runs))
	for _, run := range runs {
		m[keys.NamespacedKey(run.Namespace, run.Name)] = run
	}
	return m
}

func classOf(t *testing.T, ev *Evaluation, leases []v1.Lease, name string) Class {
	t.Helper()
	for i := range leases {
		if leases[i].Name == name {
			class, ok := ev.Class(&leases[i])
			if !ok {
				t.Fatalf("lease %s has no classification", name)
			}
			return class
		}
	}
	t.Fatalf("lease %s not found", name)
	return ""
}

func TestOwnerClaimFunded(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8))}
	runs := runsMap(runOf("train", "team", base, false))
	leases := []v1.Lease{leaseOf("l1", "train", "team", "team-budget", "west", 4, base)}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runs, Now: base.Add(2 * time.Hour)})

	if got := classOf(t, ev, leases, "l1"); got != ClassOwned {
		t.Fatalf("expected Owned, got %s", got)
	}
	acct := ev.Envelope(EnvelopeKey{Budget: "team-budget", Envelope: "west"})
	if acct.FundedWidth() != 4 {
		t.Errorf("expected funded width 4, got %d", acct.FundedWidth())
	}
	run := ev.Run("default/train")
	if run.GPUs[ClassOwned] != 4 || math.Abs(run.GPUHours[ClassOwned]-8) > 1e-9 {
		t.Errorf("expected 4 owned GPUs and 8 owned GPU-hours, got %d / %v", run.GPUs[ClassOwned], run.GPUHours[ClassOwned])
	}
}

// R4 pt2: the ledger-compaction primitive. Settling closed leases before the
// earliest retained start and feeding SettleAccrual back must reproduce the full
// replay's funding decision EXACTLY — including a MaxGPUHours cap whose depletion
// the settled hours drive — while dropping the settled leases from the replay.
// The golden oracle does not capture GPU-hours, so this round-trip is the rail.
func TestLedgerCompactionRoundTrip(t *testing.T) {
	horizon := base.Add(5 * time.Hour)
	now := base.Add(10 * time.Hour)
	westKey := EnvelopeKey{Budget: "team-budget", Envelope: "west"}
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8, withMaxHours(30)))}
	runs := runsMap(runOf("old", "team", base, false), runOf("new", "team", horizon, false))
	// Settled: 4 GPUs, base→horizon = 20 GPU-hours. Retained: 4 GPUs open from the
	// horizon. The 30 GPU-hour cap is exhausted mid-way through the retained lease,
	// so the settled hours must be carried forward for the demotion to match.
	settled := leaseOf("settled", "old", "team", "team-budget", "west", 4, base, closedAt(horizon))
	retained := leaseOf("retained", "new", "team", "team-budget", "west", 4, horizon)
	leases := []v1.Lease{settled, retained}
	mkInput := func() Input { return Input{Budgets: budgets, Leases: leases, Runs: runs, Now: now} }

	full := Evaluate(mkInput())

	prior := SettleAccrual(mkInput(), horizon)
	if len(prior) == 0 {
		t.Fatalf("SettleAccrual produced no summary for a settled lease")
	}
	ci := mkInput()
	ci.SettlementHorizon = horizon
	ci.PriorAccrual = prior
	compact := Evaluate(ci)

	// Dropping the settled lease WITHOUT the seed changes the result — proof that
	// the drop is real and the summary is load-bearing: unseeded, the retained
	// lease no longer inherits the exhausted cap (20 GPU-hours vs the full 30).
	di := mkInput()
	di.SettlementHorizon = horizon
	if noSeed := Evaluate(di); math.Abs(noSeed.Envelope(westKey).ConsumedGPUHours-full.Envelope(westKey).ConsumedGPUHours) < 1e-6 {
		t.Errorf("expected dropping settled leases without the seed to change consumed hours; both = %v", full.Envelope(westKey).ConsumedGPUHours)
	}

	if fc, cc := classOf(t, full, leases, "retained"), classOf(t, compact, leases, "retained"); fc != cc {
		t.Errorf("retained lease class differs: full=%s compact=%s", fc, cc)
	}

	fe, ce := full.Envelope(westKey), compact.Envelope(westKey)
	if math.Abs(fe.ConsumedGPUHours-ce.ConsumedGPUHours) > 1e-9 {
		t.Errorf("ConsumedGPUHours: full=%v compact=%v", fe.ConsumedGPUHours, ce.ConsumedGPUHours)
	}
	for _, cl := range []Class{ClassOwned, ClassShared, ClassBorrowed, ClassUnfunded} {
		if math.Abs(fe.HoursByClass[cl]-ce.HoursByClass[cl]) > 1e-9 {
			t.Errorf("HoursByClass[%s]: full=%v compact=%v", cl, fe.HoursByClass[cl], ce.HoursByClass[cl])
		}
		if fe.WidthByClass[cl] != ce.WidthByClass[cl] {
			t.Errorf("WidthByClass[%s]: full=%d compact=%d", cl, fe.WidthByClass[cl], ce.WidthByClass[cl])
		}
	}
	// The cap actually bound (otherwise the round-trip proves nothing about
	// depletion): 30 GPU-hours consumed, and the retained 4-GPU lease demoted to
	// Unfunded once the settled hours exhausted the envelope.
	if math.Abs(fe.ConsumedGPUHours-30) > 1e-6 {
		t.Errorf("expected the 30 GPU-hour cap to bind, consumed=%v", fe.ConsumedGPUHours)
	}
	if fe.WidthByClass[ClassUnfunded] != 4 {
		t.Errorf("expected the retained 4-GPU lease demoted to Unfunded at Now, got unfunded width %d", fe.WidthByClass[ClassUnfunded])
	}
}

// R4 pt2: compaction is only applied when provably safe. A retained lease that
// STARTED before the horizon straddles the settled epoch, so Evaluate must ignore
// the settlement (poison PriorAccrual and all) and do a full replay — correct,
// just uncompacted.
func TestLedgerCompactionFallsBackOnStraddle(t *testing.T) {
	horizon := base.Add(5 * time.Hour)
	now := base.Add(10 * time.Hour)
	westKey := EnvelopeKey{Budget: "team-budget", Envelope: "west"}
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8))}
	runs := runsMap(runOf("a", "team", base, false))
	settled := leaseOf("settled", "a", "team", "team-budget", "west", 4, base, closedAt(horizon))
	// Open, started before the horizon → straddles it.
	straddle := leaseOf("straddle", "a", "team", "team-budget", "west", 4, base.Add(2*time.Hour))
	leases := []v1.Lease{settled, straddle}
	mkInput := func() Input { return Input{Budgets: budgets, Leases: leases, Runs: runs, Now: now} }

	full := Evaluate(mkInput())
	ci := mkInput()
	ci.SettlementHorizon = horizon
	ci.PriorAccrual = map[EnvelopeKey]SettledAccrual{westKey: {ConsumedGPUHours: 999}} // must be ignored
	compact := Evaluate(ci)

	if math.Abs(full.Envelope(westKey).ConsumedGPUHours-compact.Envelope(westKey).ConsumedGPUHours) > 1e-9 {
		t.Errorf("straddle must force a full replay (poison PriorAccrual ignored): full=%v compact=%v",
			full.Envelope(westKey).ConsumedGPUHours, compact.Envelope(westKey).ConsumedGPUHours)
	}
}

// R4 pt2 (adversarial-review catch): a horizon ahead of Now would settle a lease
// that is still LIVE — an Interval.End in (Now, horizon] puts effectiveEnd at or
// before the horizon while the lease still holds width at Now. settlementSafe's
// no-straddle loop skips settled leases, so only an explicit horizon <= Now guard
// catches this one. Unguarded, compaction dropped the live lease's width (Owned 4
// -> 0) and SettleAccrual integrated it past the clock (16 -> 24 GPU-hours): both
// are gating outputs, and both would fail silently because the golden oracle
// captures widths and lenders, not GPU-hours.
func TestLedgerCompactionRefusesFutureHorizon(t *testing.T) {
	now := base.Add(5 * time.Hour)
	horizon := base.Add(8 * time.Hour)
	westKey := EnvelopeKey{Budget: "team-budget", Envelope: "west"}
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8))}
	runs := runsMap(runOf("ghost", "team", base, false))
	// Live at Now (5h < 7h), yet effectiveEnd (7h) is at or before the horizon (8h).
	ghost := leaseOf("ghost", "ghost", "team", "team-budget", "west", 4, base.Add(time.Hour), endingAt(base.Add(7*time.Hour)))
	leases := []v1.Lease{ghost}
	mkInput := func() Input { return Input{Budgets: budgets, Leases: leases, Runs: runs, Now: now} }

	if prior := SettleAccrual(mkInput(), horizon); prior != nil {
		t.Errorf("SettleAccrual must refuse a horizon past Now rather than integrate a live lease to it, got %v", prior)
	}

	full := Evaluate(mkInput())
	fe := full.Envelope(westKey)
	if math.Abs(fe.ConsumedGPUHours-16) > 1e-6 {
		t.Fatalf("setup: expected 4 GPUs x 4h = 16 GPU-hours accrued by Now, got %v", fe.ConsumedGPUHours)
	}
	if fe.WidthByClass[ClassOwned] != 4 {
		t.Fatalf("setup: expected the lease live and Owned at Now, got owned width %d", fe.WidthByClass[ClassOwned])
	}

	ci := mkInput()
	ci.SettlementHorizon = horizon
	// The summary a caller would have computed for this horizon; it must be ignored.
	ci.PriorAccrual = map[EnvelopeKey]SettledAccrual{westKey: {
		ConsumedGPUHours: 24,
		HoursByClass:     map[Class]float64{ClassOwned: 24},
	}}
	ce := Evaluate(ci).Envelope(westKey)

	if math.Abs(ce.ConsumedGPUHours-fe.ConsumedGPUHours) > 1e-9 {
		t.Errorf("a horizon past Now must force a full replay: full=%v compact=%v GPU-hours", fe.ConsumedGPUHours, ce.ConsumedGPUHours)
	}
	if ce.WidthByClass[ClassOwned] != fe.WidthByClass[ClassOwned] {
		t.Errorf("a horizon past Now must not drop a live lease's width: full=%d compact=%d", fe.WidthByClass[ClassOwned], ce.WidthByClass[ClassOwned])
	}
	if fc, cc := classOf(t, full, leases, "ghost"), classOf(t, Evaluate(ci), leases, "ghost"); fc != cc {
		t.Errorf("live lease class differs: full=%s compact=%s", fc, cc)
	}
}

// The fill has skip semantics (specs/QuotaEvaluation.tla): an oversized
// claim goes unfunded without blocking smaller claims ranked below it.
func TestSkipSemantics(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8))}
	big := runOf("big", "team", base, false)
	small := runOf("small", "team", base.Add(time.Minute), false)
	leases := []v1.Lease{
		leaseOf("l-big", "big", "team", "team-budget", "west", 16, base),
		leaseOf("l-small", "small", "team", "team-budget", "west", 4, base),
	}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(big, small), Now: base.Add(time.Hour)})

	if got := classOf(t, ev, leases, "l-big"); got != ClassUnfunded {
		t.Errorf("oversized claim should be unfunded, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-small"); got != ClassOwned {
		t.Errorf("small claim should fund despite the oversized one above it, got %s", got)
	}
}

// Owner recall: the owner's later claim outranks a family borrower's
// earlier one — the borrower re-evaluates as unfunded with no demotion
// event anywhere.
func TestFamilyShareAndOwnerRecall(t *testing.T) {
	budgets := []v1.Budget{
		budgetOf("team", "team-budget", nil, env("west", 8)),
		budgetOf("team/child", "child-budget", []string{"team"}, env("scratch", 1)),
	}
	childRun := runOf("child-train", "team/child", base, false)
	leases := []v1.Lease{leaseOf("l-child", "child-train", "team", "team-budget", "west", 8, base)}

	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(childRun), Now: base.Add(time.Hour)})
	if got := classOf(t, ev, leases, "l-child"); got != ClassShared {
		t.Fatalf("family excess should evaluate Shared, got %s", got)
	}

	ownerRun := runOf("boss-train", "team", base.Add(30*time.Minute), false)
	leases = append(leases, leaseOf("l-boss", "boss-train", "team", "team-budget", "west", 4, base.Add(30*time.Minute)))
	ev = Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(childRun, ownerRun), Now: base.Add(time.Hour)})

	if got := classOf(t, ev, leases, "l-boss"); got != ClassOwned {
		t.Errorf("owner claim must fund regardless of the earlier borrower, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-child"); got != ClassUnfunded {
		t.Errorf("recalled family claim should evaluate Unfunded, got %s", got)
	}
	if lenders := ev.Run("default/child-train").Lenders; len(lenders) != 0 {
		t.Errorf("unfunded claim should list no lenders, got %v", lenders)
	}
}

func TestSharingNoneOptsOutFamilyOnly(t *testing.T) {
	budgets := []v1.Budget{
		budgetOf("team", "team-budget", nil, env("west", 8, withSharing(v1.SharingNone))),
		budgetOf("team/child", "child-budget", []string{"team"}, env("scratch", 1)),
	}
	childRun := runOf("child-train", "team/child", base, false)
	ownerRun := runOf("boss-train", "team", base.Add(time.Minute), false)
	leases := []v1.Lease{
		leaseOf("l-child", "child-train", "team", "team-budget", "west", 2, base),
		leaseOf("l-boss", "boss-train", "team", "team-budget", "west", 2, base),
	}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(childRun, ownerRun), Now: base.Add(time.Hour)})

	if got := classOf(t, ev, leases, "l-child"); got != ClassUnfunded {
		t.Errorf("sharing:none must exclude family, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-boss"); got != ClassOwned {
		t.Errorf("sharing:none must not affect the owner, got %s", got)
	}
}

// Borrowed capacity is a contract carve-out: the lender's later claims do
// not re-rank it opportunistic, and capacity it holds is unavailable to the
// family fill (quota-semantics.md Decision 2: "not subject to unilateral
// recall").
func TestSponsorContractCarveOut(t *testing.T) {
	six := int32(6)
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil,
		env("west", 8, withLending(v1.LendingPolicy{Allow: true, MaxConcurrency: &six})))}
	stranger := runOf("guest", "org:other", base, false)
	owner := runOf("boss", "team", base.Add(time.Minute), false)
	leases := []v1.Lease{
		leaseOf("l-guest", "guest", "team", "team-budget", "west", 6, base),
		leaseOf("l-boss", "boss", "team", "team-budget", "west", 4, base.Add(time.Minute)),
	}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(stranger, owner), Now: base.Add(time.Hour)})

	if got := classOf(t, ev, leases, "l-guest"); got != ClassBorrowed {
		t.Errorf("sponsored claim should stay Borrowed under owner pressure, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-boss"); got != ClassUnfunded {
		t.Errorf("owner claim exceeding the unlent remainder evaluates Unfunded, got %s", got)
	}
}

func TestLendingCapsAndACL(t *testing.T) {
	two := int32(2)
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil,
		env("west", 8, withLending(v1.LendingPolicy{Allow: true, To: []string{"org:*"}, MaxConcurrency: &two})))}
	allowed := runOf("guest-a", "org:friend", base, false)
	denied := runOf("guest-b", "corp:foe", base.Add(time.Minute), false)
	over := runOf("guest-c", "org:late", base.Add(2*time.Minute), false)
	leases := []v1.Lease{
		leaseOf("l-a", "guest-a", "team", "team-budget", "west", 2, base),
		leaseOf("l-b", "guest-b", "team", "team-budget", "west", 2, base),
		leaseOf("l-c", "guest-c", "team", "team-budget", "west", 2, base),
	}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(allowed, denied, over), Now: base.Add(time.Hour)})

	if got := classOf(t, ev, leases, "l-a"); got != ClassBorrowed {
		t.Errorf("ACL-matched sponsor claim should be Borrowed, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-b"); got != ClassUnfunded {
		t.Errorf("ACL-denied claim should be Unfunded, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-c"); got != ClassUnfunded {
		t.Errorf("claim beyond lending.maxConcurrency should be Unfunded, got %s", got)
	}
}

// Exhaustion demotes without killing: the integral drains to zero, the
// claim keeps its leases but evaluates Unfunded, and the envelope is never
// overdrawn.
func TestIntegralExhaustionDemotes(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8, withMaxHours(8)))}
	run := runOf("train", "team", base, false)
	leases := []v1.Lease{leaseOf("l1", "train", "team", "team-budget", "west", 4, base)}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(run), Now: base.Add(3 * time.Hour)})

	if got := classOf(t, ev, leases, "l1"); got != ClassUnfunded {
		t.Errorf("exhausted envelope should demote its claim, got %s", got)
	}
	acct := ev.Envelope(EnvelopeKey{Budget: "team-budget", Envelope: "west"})
	if acct.ConsumedGPUHours > 8+1e-6 {
		t.Errorf("no overdraft: consumed %v > cap 8", acct.ConsumedGPUHours)
	}
	runAcct := ev.Run("default/train")
	if math.Abs(runAcct.GPUHours[ClassOwned]-8) > 1e-3 || math.Abs(runAcct.GPUHours[ClassUnfunded]-4) > 1e-3 {
		t.Errorf("expected 8 funded / 4 unfunded GPU-hours, got %v / %v",
			runAcct.GPUHours[ClassOwned], runAcct.GPUHours[ClassUnfunded])
	}
}

// A window that moves forward stops charging hours spent in the old
// window, so the same claim re-funds by pure arithmetic — nothing to
// resubmit.
func TestWindowReopenRefunds(t *testing.T) {
	run := runOf("train", "team", base, false)
	leases := []v1.Lease{leaseOf("l1", "train", "team", "team-budget", "west", 4, base)}
	now := base.Add(4 * time.Hour)

	exhausted := []v1.Budget{budgetOf("team", "team-budget", nil,
		env("west", 8, withMaxHours(8), withWindow(base, base.Add(6*time.Hour))))}
	ev := Evaluate(Input{Budgets: exhausted, Leases: leases, Runs: runsMap(run), Now: now})
	if got := classOf(t, ev, leases, "l1"); got != ClassUnfunded {
		t.Fatalf("integral exhausted, expected Unfunded, got %s", got)
	}

	renewed := []v1.Budget{budgetOf("team", "team-budget", nil,
		env("west", 8, withMaxHours(8), withWindow(base.Add(3*time.Hour), base.Add(9*time.Hour))))}
	ev = Evaluate(Input{Budgets: renewed, Leases: leases, Runs: runsMap(run), Now: now})
	if got := classOf(t, ev, leases, "l1"); got != ClassOwned {
		t.Errorf("renewed window should re-fund the claim, got %s", got)
	}
	acct := ev.Envelope(EnvelopeKey{Budget: "team-budget", Envelope: "west"})
	if math.Abs(acct.ConsumedGPUHours-4) > 1e-6 {
		t.Errorf("only in-window hours charge the integral: expected 4, got %v", acct.ConsumedGPUHours)
	}
}

// Pre-window admissions (preActivation.allowAdmission) evaluate unfunded
// until the window opens, then fund by arithmetic.
func TestPreWindowLeaseFundsWhenWindowOpens(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil,
		env("west", 8, withWindow(base.Add(time.Hour), base.Add(24*time.Hour))))}
	run := runOf("train", "team", base, false)
	leases := []v1.Lease{leaseOf("l1", "train", "team", "team-budget", "west", 4, base)}

	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(run), Now: base.Add(30 * time.Minute)})
	if got := classOf(t, ev, leases, "l1"); got != ClassUnfunded {
		t.Errorf("pre-window lease should be Unfunded, got %s", got)
	}

	ev = Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(run), Now: base.Add(2 * time.Hour)})
	if got := classOf(t, ev, leases, "l1"); got != ClassOwned {
		t.Errorf("lease should fund once the window opens, got %s", got)
	}
	acct := ev.Envelope(EnvelopeKey{Budget: "team-budget", Envelope: "west"})
	if math.Abs(acct.ConsumedGPUHours-4) > 1e-6 {
		t.Errorf("pre-window hours must not charge the integral: expected 4, got %v", acct.ConsumedGPUHours)
	}
}

// Malleable claims fund as much width as quota affords, lowest group index
// first — the same groups the shrink path would cut demote first.
func TestMalleablePartialFunding(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 6))}
	run := runOf("elastic", "team", base, true)
	leases := []v1.Lease{
		leaseOf("l-g0", "elastic", "team", "team-budget", "west", 4, base, withGroup(0)),
		leaseOf("l-g1", "elastic", "team", "team-budget", "west", 4, base, withGroup(1)),
	}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(run), Now: base.Add(time.Hour)})

	if got := classOf(t, ev, leases, "l-g0"); got != ClassOwned {
		t.Errorf("group 0 should be funded, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-g1"); got != ClassUnfunded {
		t.Errorf("group 1 should be the unfunded remainder, got %s", got)
	}
	runAcct := ev.Run("default/elastic")
	if runAcct.GPUs[ClassOwned] != 4 || runAcct.GPUs[ClassUnfunded] != 4 {
		t.Errorf("expected 4 owned / 4 unfunded GPUs, got %v", runAcct.GPUs)
	}
}

func TestAggregateCapBoundsAcrossEnvelopes(t *testing.T) {
	ten := int32(10)
	budget := budgetOf("team", "team-budget", nil, env("east", 8), env("west", 8))
	budget.Spec.AggregateCaps = []v1.AggregateCap{{
		Name: "global", Flavor: testFlavor, Envelopes: []string{"east", "west"}, MaxConcurrency: &ten,
	}}
	run1 := runOf("train-1", "team", base, false)
	run2 := runOf("train-2", "team", base.Add(time.Minute), false)
	leases := []v1.Lease{
		leaseOf("l-east", "train-1", "team", "team-budget", "east", 8, base),
		leaseOf("l-west", "train-2", "team", "team-budget", "west", 8, base),
	}
	ev := Evaluate(Input{Budgets: []v1.Budget{budget}, Leases: leases, Runs: runsMap(run1, run2), Now: base.Add(time.Hour)})

	if got := classOf(t, ev, leases, "l-east"); got != ClassOwned {
		t.Errorf("first envelope in walk order should fund, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-west"); got != ClassUnfunded {
		t.Errorf("aggregate cap should reject the second claim, got %s", got)
	}
}

// Owner recall holds THROUGH a shared aggregate cap: a family borrower on
// one member envelope must not lock the owner out of another member, even
// when the family envelope sorts earlier in the walk. Before the rank-aware
// fill, the lexically-first family claim consumed the aggregate and demoted
// the owner's own run to Unfunded (owner locked out of its own budget).
func TestAggregateCapHonorsOwnerRecall(t *testing.T) {
	eight := int32(8)
	budget := budgetOf("team", "team-budget", nil, env("east", 8), env("west", 8))
	budget.Spec.AggregateCaps = []v1.AggregateCap{{
		Name: "global", Flavor: testFlavor, Envelopes: []string{"east", "west"}, MaxConcurrency: &eight,
	}}
	child := budgetOf("team/child", "child-budget", []string{"team"})
	ownerRun := runOf("owner-run", "team", base, false)
	familyRun := runOf("family-run", "team/child", base, false)
	// 'east' sorts before 'west', so pre-fix the family claim on east would
	// win the aggregate; the owner's claim on west would demote.
	leases := []v1.Lease{
		leaseOf("l-family", "family-run", "team", "team-budget", "east", 8, base),
		leaseOf("l-owner", "owner-run", "team", "team-budget", "west", 8, base),
	}
	ev := Evaluate(Input{Budgets: []v1.Budget{budget, child}, Leases: leases, Runs: runsMap(ownerRun, familyRun), Now: base.Add(time.Hour)})

	if got := classOf(t, ev, leases, "l-owner"); got != ClassOwned {
		t.Errorf("owner's own claim must fund through the aggregate, got %s", got)
	}
	if got := classOf(t, ev, leases, "l-family"); got != ClassUnfunded {
		t.Errorf("recallable family claim should lose the aggregate to the owner, got %s", got)
	}
}

// The admission-side view agrees: with a family borrower holding all of an
// aggregate's capacity on one member, the owner still sees recallable width
// available on another member (AvailableWidth must not count junior family
// width as senior through the aggregate).
func TestAvailableWidthRecallsThroughAggregate(t *testing.T) {
	eight := int32(8)
	budget := budgetOf("team", "team-budget", nil, env("east", 8), env("west", 8))
	budget.Spec.AggregateCaps = []v1.AggregateCap{{
		Name: "global", Flavor: testFlavor, Envelopes: []string{"east", "west"}, MaxConcurrency: &eight,
	}}
	child := budgetOf("team/child", "child-budget", []string{"team"})
	familyRun := runOf("family-run", "team/child", base, false)
	leases := []v1.Lease{leaseOf("l-family", "family-run", "team", "team-budget", "east", 8, base)}
	ev := Evaluate(Input{Budgets: []v1.Budget{budget, child}, Leases: leases, Runs: runsMap(familyRun), Now: base.Add(time.Hour)})

	// The owner outranks the family borrower everywhere, so it can recall
	// the borrowed aggregate width on the empty member (west) and on the
	// member the family currently holds (east) alike.
	westKey := EnvelopeKey{Budget: "team-budget", Envelope: "west"}
	if got := ev.AvailableWidth(westKey, "team", base, "", false); got != 8 {
		t.Errorf("owner should recall the family borrower's aggregate width on west, want 8, got %d", got)
	}
	eastKey := EnvelopeKey{Budget: "team-budget", Envelope: "east"}
	if got := ev.AvailableWidth(eastKey, "team", base, "", false); got != 8 {
		t.Errorf("owner should recall the family borrower on east too, want 8, got %d", got)
	}
	// A junior cousin does NOT outrank the sitting family claim, so it sees
	// none of the aggregate width the owner could recall.
	cousin := budgetOf("team/cousin", "cousin-budget", []string{"team"})
	ev2 := Evaluate(Input{Budgets: []v1.Budget{budget, child, cousin}, Leases: leases, Runs: runsMap(familyRun), Now: base.Add(time.Hour)})
	if got := ev2.AvailableWidth(westKey, "team/cousin", base.Add(time.Minute), "", false); got != 0 {
		t.Errorf("a later cousin cannot recall the family claim through the aggregate, want 0, got %d", got)
	}
}

func TestOrphanLeaseUnfunded(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8))}
	leases := []v1.Lease{leaseOf("l1", "ghost", "team", "team-budget", "west", 4, base)}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: nil, Now: base.Add(time.Hour)})
	if got := classOf(t, ev, leases, "l1"); got != ClassUnfunded {
		t.Errorf("orphan lease should be Unfunded, got %s", got)
	}
}

func TestAvailableWidthRecallAndSponsor(t *testing.T) {
	six := int32(6)
	budgets := []v1.Budget{
		budgetOf("team", "team-budget", nil,
			env("west", 8, withLending(v1.LendingPolicy{Allow: true, MaxConcurrency: &six}))),
		budgetOf("team/child", "child-budget", []string{"team"}, env("scratch", 1)),
		budgetOf("team/child2", "child2-budget", []string{"team"}, env("scratch", 1)),
	}
	childRun := runOf("child-train", "team/child", base, false)
	leases := []v1.Lease{leaseOf("l-child", "child-train", "team", "team-budget", "west", 6, base)}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(childRun), Now: base.Add(time.Hour), Period: time.Hour})

	key := EnvelopeKey{Budget: "team-budget", Envelope: "west"}
	// The owner sees the full envelope: the child's shared claim is
	// recallable and does not count against an owner admission.
	if got := ev.AvailableWidth(key, "team", base.Add(time.Hour), "", false); got != 8 {
		t.Errorf("owner admission should see 8 available (recall), got %d", got)
	}
	// A sibling arriving later ranks below the child's existing claim.
	if got := ev.AvailableWidth(key, "team/child2", base.Add(time.Hour), "", false); got != 2 {
		t.Errorf("later same-tier claim should see the remainder 2, got %d", got)
	}
	// A sponsor is junior to all funded width and bounded by lending caps.
	if got := ev.AvailableWidth(key, "org:guest", base.Add(time.Hour), "", true); got != 2 {
		t.Errorf("sponsor should see min(capacity remainder, lending cap) = 2, got %d", got)
	}
	// A stranger without the sponsor path gets nothing.
	if got := ev.AvailableWidth(key, "org:guest", base.Add(time.Hour), "", false); got != 0 {
		t.Errorf("stranger without lending path should see 0, got %d", got)
	}
}

// Admission applies the deterministic name tiebreak so it agrees with the
// classifier on same-tier, same-second claims: a prospective run whose key
// sorts before an existing peer's outranks it (recall), one that sorts after
// does not. Without the name, admission would treat every same-time peer as
// senior and disagree with the placed classification.
func TestAvailableWidthNameTiebreak(t *testing.T) {
	budgets := []v1.Budget{
		budgetOf("team", "team-budget", nil, env("west", 8)),
		budgetOf("team/child", "child-budget", []string{"team"}, env("scratch", 1)),
		budgetOf("team/child2", "child2-budget", []string{"team"}, env("scratch", 1)),
	}
	// child-train (key default/child-train) holds the whole envelope, shared,
	// admitted at base.
	childRun := runOf("child-train", "team/child", base, false)
	leases := []v1.Lease{leaseOf("l-child", "child-train", "team", "team-budget", "west", 8, base)}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(childRun), Now: base.Add(time.Hour)})
	key := EnvelopeKey{Budget: "team-budget", Envelope: "west"}

	// Same tier (child), same admission second: a name-senior prospective
	// outranks the sitting claim and recalls all 8.
	if got := ev.AvailableWidth(key, "team/child2", base, "default/aaa-run", false); got != 8 {
		t.Errorf("name-senior peer should recall the sitting claim, want 8, got %d", got)
	}
	// A name-junior prospective ranks below it and sees nothing.
	if got := ev.AvailableWidth(key, "team/child2", base, "default/zzz-run", false); got != 0 {
		t.Errorf("name-junior peer must not recall the sitting claim, want 0, got %d", got)
	}
	// Empty name keeps the conservative estimate (every same-time peer
	// senior), so no recall.
	if got := ev.AvailableWidth(key, "team/child2", base, "", false); got != 0 {
		t.Errorf("empty name should be conservative (0), got %d", got)
	}
}

func TestAvailableWidthIntegralLookahead(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8, withMaxHours(4)))}
	ev := Evaluate(Input{Budgets: budgets, Now: base, Period: time.Hour})
	key := EnvelopeKey{Budget: "team-budget", Envelope: "west"}
	// 4 GPU-hours remaining at a 1h period funds at most 4 GPUs of new work.
	if got := ev.AvailableWidth(key, "team", base, "", false); got != 4 {
		t.Errorf("admission lookahead should cap width at remaining/period = 4, got %d", got)
	}

	zero := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8, withMaxHours(0)))}
	ev = Evaluate(Input{Budgets: zero, Now: base, Period: time.Hour})
	// R14 done-when: a zero-hour envelope cannot fund an admission.
	if got := ev.AvailableWidth(key, "team", base, "", false); got != 0 {
		t.Errorf("zero-hour envelope must not fund admissions, got %d", got)
	}
}

// --- property tests -------------------------------------------------------
//
// Hand-rolled generators in the style of the binder property tests: a
// seeded rand builds random worlds, and the invariants from
// specs/QuotaEvaluation.tla (plus conservation) must hold on every one.

type world struct {
	budgets []v1.Budget
	runs    map[string]*v1.Run
	leases  []v1.Lease
	now     time.Time
	period  time.Duration
}

func genWorld(rng *rand.Rand) world {
	owners := []string{"team", "team/a", "team/b", "org", "org/x"}
	// team is parent of team/a and team/b; org parent of org/x; team/a and
	// team/b are siblings; strangers: "corp".
	budgets := []v1.Budget{}
	parents := map[string][]string{"team/a": {"team"}, "team/b": {"team"}, "org/x": {"org"}}
	for _, owner := range owners {
		if rng.Intn(5) == 0 {
			continue // some owners have no budget at all
		}
		n := 1 + rng.Intn(2)
		envs := make([]v1.BudgetEnvelope, 0, n)
		for i := 0; i < n; i++ {
			e := env(fmt.Sprintf("env-%d", i), int32(1+rng.Intn(16)))
			if rng.Intn(2) == 0 {
				hours := int64(rng.Intn(64))
				e.MaxGPUHours = &hours
			}
			if rng.Intn(4) == 0 {
				s := v1.NewTime(base.Add(time.Duration(rng.Intn(5)-2) * time.Hour))
				en := v1.NewTime(s.Add(time.Duration(1+rng.Intn(8)) * time.Hour))
				e.Start, e.End = &s, &en
			}
			if rng.Intn(4) == 0 {
				e.Sharing = v1.SharingNone
			}
			if rng.Intn(3) == 0 {
				policy := v1.LendingPolicy{Allow: true}
				if rng.Intn(2) == 0 {
					c := int32(1 + rng.Intn(8))
					policy.MaxConcurrency = &c
				}
				if rng.Intn(2) == 0 {
					h := int64(rng.Intn(32))
					policy.MaxGPUHours = &h
				}
				e.Lending = &policy
			}
			envs = append(envs, e)
		}
		b := budgetOf(owner, fmt.Sprintf("budget-%s", sanitize(owner)), parents[owner], envs...)
		if len(envs) > 1 && rng.Intn(3) == 0 {
			c := int32(1 + rng.Intn(20))
			b.Spec.AggregateCaps = []v1.AggregateCap{{
				Name: "agg", Flavor: testFlavor,
				Envelopes:      []string{envs[0].Name, envs[1].Name},
				MaxConcurrency: &c,
			}}
		}
		budgets = append(budgets, b)
	}

	runOwners := append(append([]string{}, owners...), "corp")
	runs := make(map[string]*v1.Run)
	var leases []v1.Lease
	nRuns := rng.Intn(8)
	for i := 0; i < nRuns; i++ {
		owner := runOwners[rng.Intn(len(runOwners))]
		created := base.Add(time.Duration(rng.Intn(240)-120) * time.Minute)
		run := runOf(fmt.Sprintf("run-%d", i), owner, created, rng.Intn(3) == 0)
		runs[keys.NamespacedKey(run.Namespace, run.Name)] = run
		nLeases := 1 + rng.Intn(3)
		for j := 0; j < nLeases; j++ {
			if len(budgets) == 0 {
				break
			}
			b := budgets[rng.Intn(len(budgets))]
			e := b.Spec.Envelopes[rng.Intn(len(b.Spec.Envelopes))]
			start := base.Add(time.Duration(rng.Intn(240)-180) * time.Minute)
			opts := []leaseOpt{withGroup(j)}
			if rng.Intn(4) == 0 {
				opts = append(opts, closedAt(start.Add(time.Duration(rng.Intn(120))*time.Minute)))
			}
			if rng.Intn(6) == 0 {
				opts = append(opts, withRole("Spare"))
			}
			leases = append(leases, leaseOf(fmt.Sprintf("lease-%d-%d", i, j), run.Name,
				b.Spec.Owner, b.Name, e.Name, 1+rng.Intn(4), start, opts...))
		}
	}
	return world{
		budgets: budgets,
		runs:    runs,
		leases:  leases,
		now:     base.Add(time.Duration(60+rng.Intn(180)) * time.Minute),
		period:  time.Duration(1+rng.Intn(24)) * time.Hour,
	}
}

func sanitize(owner string) string {
	out := []byte(owner)
	for i := range out {
		if out[i] == '/' || out[i] == ':' {
			out[i] = '-'
		}
	}
	return string(out)
}

func evaluateWorld(w world) *Evaluation {
	return Evaluate(Input{Budgets: w.budgets, Leases: w.leases, Runs: w.runs, Now: w.now, Period: w.period})
}

// concurrencyOnly strips every integral cap from the world. The ranking
// properties below (owner independence, removal monotonicity) are exact on
// the concurrency dimension — the one specs/QuotaEvaluation.tla models. The
// integral dimension is deliberately not independent: family consumption
// really does drain a shared envelope's GPU-hours ("counts against lender's
// envelope usage"), so accrual history couples claims across tiers there.
func concurrencyOnly(w world) world {
	budgets := make([]v1.Budget, len(w.budgets))
	for i := range w.budgets {
		b := *w.budgets[i].DeepCopy()
		for j := range b.Spec.Envelopes {
			b.Spec.Envelopes[j].MaxGPUHours = nil
			if b.Spec.Envelopes[j].Lending != nil {
				b.Spec.Envelopes[j].Lending.MaxGPUHours = nil
			}
		}
		for j := range b.Spec.AggregateCaps {
			b.Spec.AggregateCaps[j].MaxGPUHours = nil
		}
		budgets[i] = b
	}
	w.budgets = budgets
	return w
}

// NoOverdraft: funded width and funded accrual never exceed any cap.
func TestPropertyNoOverdraft(t *testing.T) {
	for seed := int64(0); seed < 150; seed++ {
		w := genWorld(rand.New(rand.NewSource(seed)))
		ev := evaluateWorld(w)
		for _, acct := range ev.Envelopes() {
			if acct.FundedWidth() > acct.Spec.Concurrency {
				t.Fatalf("seed %d: envelope %v funded width %d exceeds concurrency %d",
					seed, acct.Key, acct.FundedWidth(), acct.Spec.Concurrency)
			}
			if acct.Spec.MaxGPUHours != nil && acct.ConsumedGPUHours > float64(*acct.Spec.MaxGPUHours)+1e-6 {
				t.Fatalf("seed %d: envelope %v consumed %v exceeds maxGPUHours %d",
					seed, acct.Key, acct.ConsumedGPUHours, *acct.Spec.MaxGPUHours)
			}
			if policy := acct.Spec.Lending; policy != nil {
				if policy.MaxConcurrency != nil && acct.WidthByClass[ClassBorrowed] > *policy.MaxConcurrency {
					t.Fatalf("seed %d: envelope %v borrowed width %d exceeds lending cap %d",
						seed, acct.Key, acct.WidthByClass[ClassBorrowed], *policy.MaxConcurrency)
				}
				// Depletion crossings land on millisecond boundaries, so the
				// borrowed-hours attribution may exceed the lending cap by
				// the sliver accrued past the exact crossing (width × 1ms).
				if policy.MaxGPUHours != nil && acct.HoursByClass[ClassBorrowed] > float64(*policy.MaxGPUHours)+1e-3 {
					t.Fatalf("seed %d: envelope %v borrowed hours %v exceed lending cap %d",
						seed, acct.Key, acct.HoursByClass[ClassBorrowed], *policy.MaxGPUHours)
				}
			}
		}
	}
}

// Conservation: every accrued lease hour lands in exactly one class bucket.
func TestPropertyConservation(t *testing.T) {
	for seed := int64(0); seed < 150; seed++ {
		w := genWorld(rand.New(rand.NewSource(seed)))
		ev := evaluateWorld(w)
		perRun := make(map[string]float64)
		for i := range w.leases {
			lease := &w.leases[i]
			start := lease.Spec.Interval.Start.Time
			end := effectiveEnd(lease)
			if end.IsZero() || end.After(w.now) {
				end = w.now
			}
			if end.Before(start) {
				end = start
			}
			width := float64(len(lease.Spec.Slice.Nodes))
			runKey := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
			perRun[runKey] += width * end.Sub(start).Hours()
		}
		for runKey, want := range perRun {
			acct := ev.Run(runKey)
			got := 0.0
			if acct != nil {
				for _, hours := range acct.GPUHours {
					got += hours
				}
			}
			if math.Abs(got-want) > 1e-6*(1+math.Abs(want)) {
				t.Fatalf("seed %d: run %s class hours %v != accrued %v", seed, runKey, got, want)
			}
		}
	}
}

// Owner recall, structurally: removing every family borrower's leases never
// changes the classification of the owner's own claims (sponsor carve-outs
// are contractual and deliberately excluded). Exact on concurrency-only
// worlds — see concurrencyOnly for why the integral dimension is excluded.
func TestPropertyOwnerIndependentOfFamilyBorrowers(t *testing.T) {
	for seed := int64(0); seed < 150; seed++ {
		w := concurrencyOnly(genWorld(rand.New(rand.NewSource(seed))))
		ev := evaluateWorld(w)

		ownerClasses := make(map[string]Class)
		var trimmed []v1.Lease
		for i := range w.leases {
			lease := &w.leases[i]
			runKey := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
			run := w.runs[runKey]
			payerIsRunOwner := run != nil && run.Spec.Owner == lease.Spec.Owner
			if payerIsRunOwner {
				if class, ok := ev.Class(lease); ok {
					ownerClasses[LeaseKey(lease)] = class
				}
				trimmed = append(trimmed, *lease)
				continue
			}
			tier := tierNone
			if run != nil {
				tier = ev.Graph.Tier(lease.Spec.Owner, run.Spec.Owner)
			}
			if tier == tierNone || run == nil {
				trimmed = append(trimmed, *lease) // keep sponsors and orphans
			}
		}

		w2 := w
		w2.leases = trimmed
		ev2 := evaluateWorld(w2)
		for i := range trimmed {
			lease := &trimmed[i]
			want, tracked := ownerClasses[LeaseKey(lease)]
			if !tracked {
				continue
			}
			got, ok := ev2.Class(lease)
			if !ok || got != want {
				t.Fatalf("seed %d: owner lease %s class changed %s -> %s when family borrowers were removed",
					seed, lease.Name, want, got)
			}
		}
	}
}

// Stability: removing any one claim's leases never demotes a surviving
// funded lease (capacity only frees; ranks never reorder). Exact on
// concurrency-only worlds: with integrals, removing a claim can promote a
// mid-history rival whose funded accrual then drains the envelope sooner.
func TestPropertyRemovalNeverDemotes(t *testing.T) {
	for seed := int64(0); seed < 100; seed++ {
		rng := rand.New(rand.NewSource(seed))
		w := concurrencyOnly(genWorld(rng))
		if len(w.runs) == 0 {
			continue
		}
		ev := evaluateWorld(w)

		runKeys := make([]string, 0, len(w.runs))
		for key := range w.runs {
			runKeys = append(runKeys, key)
		}
		victim := runKeys[rng.Intn(len(runKeys))]

		var trimmed []v1.Lease
		for i := range w.leases {
			lease := &w.leases[i]
			if keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name) == victim {
				continue
			}
			trimmed = append(trimmed, *lease)
		}
		w2 := w
		w2.leases = trimmed
		ev2 := evaluateWorld(w2)

		for i := range trimmed {
			lease := &trimmed[i]
			before, ok := ev.Class(lease)
			if !ok || before == ClassUnfunded {
				continue
			}
			after, ok := ev2.Class(lease)
			if !ok || after == ClassUnfunded {
				t.Fatalf("seed %d: removing run %s demoted lease %s (%s -> %s)",
					seed, victim, lease.Name, before, after)
			}
		}
	}
}

// Determinism: the same facts evaluate to the same answer, bit for bit.
func TestPropertyDeterministic(t *testing.T) {
	for seed := int64(0); seed < 50; seed++ {
		w := genWorld(rand.New(rand.NewSource(seed)))
		ev1 := evaluateWorld(w)
		ev2 := evaluateWorld(w)
		if !reflect.DeepEqual(ev1.classes, ev2.classes) {
			t.Fatalf("seed %d: classifications differ between evaluations", seed)
		}
		for _, acct := range ev1.Envelopes() {
			other := ev2.Envelope(acct.Key)
			if acct.ConsumedGPUHours != other.ConsumedGPUHours {
				t.Fatalf("seed %d: envelope %v consumed differs: %v vs %v",
					seed, acct.Key, acct.ConsumedGPUHours, other.ConsumedGPUHours)
			}
		}
	}
}
