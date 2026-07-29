package funding

import (
	"fmt"
	"math"
	"math/rand"
	"reflect"
	"strings"
	"testing"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

var base = time.Date(2026, 7, 1, 12, 0, 0, 0, time.UTC)

const testFlavor = "H100-80GB"

// env builds a legal envelope. INV-WINDOW-REQUIRED makes both bounds mandatory,
// so the default is a window WIDE enough not to interfere with tests that are
// about something else — open well before `base` and closing well after. Tests
// that care about window behaviour pass withWindow to narrow it.
func env(name string, concurrency int32, mods ...func(*v1.BudgetEnvelope)) v1.BudgetEnvelope {
	start, end := v1.NewTime(base.Add(-365*24*time.Hour)), v1.NewTime(base.Add(365*24*time.Hour))
	e := v1.BudgetEnvelope{
		Name:        name,
		Flavor:      testFlavor,
		Selector:    map[string]string{"region": "us-west"},
		Concurrency: concurrency,
		Start:       &start,
		End:         &end,
	}
	for _, mod := range mods {
		mod(&e)
	}
	return e
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

// nsForOwner maps an owner tier to its namespace (R7: one principal per
// namespace, owner derived from namespace). Every fixture budget/run/lease for
// an owner lands in the same namespace, so funding.OwnerOf(ns) round-trips back
// to that owner and no namespace ever holds two owners.
func nsForOwner(owner string) string {
	if owner == "" {
		return "default"
	}
	return strings.NewReplacer(":", "-", "/", "-").Replace(owner)
}

// idleEnvelope gives a binding-only Budget something legal to carry. api/v1
// rejects a Budget with no envelopes AND an envelope with concurrency <= 0, so
// "this tier is bound to a namespace but has no capacity of its own" cannot be
// expressed by an empty Budget — that state would never survive the API server,
// and a fixture that constructs it is not evidence about a legal system (R7 pt2
// review, test-integrity lens). An envelope of a DIFFERENT flavor is the legal
// way to say it: the tier owns something, but nothing the run under test can use,
// so it must still borrow from its parent exactly as the scenario intends.
func idleEnvelope() v1.BudgetEnvelope {
	start, end := v1.NewTime(base.Add(-365*24*time.Hour)), v1.NewTime(base.Add(365*24*time.Hour))
	return v1.BudgetEnvelope{
		Name:        "idle",
		Flavor:      "A100-40GB",
		Selector:    map[string]string{"region": "us-west"},
		Concurrency: 1,
		Start:       &start,
		End:         &end,
	}
}

func budgetOf(owner, name string, parents []string, envelopes ...v1.BudgetEnvelope) v1.Budget {
	return v1.Budget{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: nsForOwner(owner)},
		Spec:       v1.BudgetSpec{Owner: owner, Envelopes: envelopes, Parents: parents},
	}
}

func runOf(name, owner string, created time.Time, malleable bool) *v1.Run {
	run := &v1.Run{
		ObjectMeta: v1.ObjectMeta{Name: name, Namespace: nsForOwner(owner), CreationTimestamp: v1.NewTime(created)},
		Spec: v1.RunSpec{
			Resources: v1.RunResources{GPUType: testFlavor, TotalGPUs: 8},
		},
	}
	if malleable {
		run.Spec.Malleable = &v1.RunMalleability{MinTotalGPUs: 1, MaxTotalGPUs: 64, StepGPUs: 1}
	}
	return run
}

type leaseOpt func(*v1.GPULease)

func closedAt(t time.Time) leaseOpt {
	return func(l *v1.GPULease) {
		ended := v1.NewTime(t)
		l.Status.Ended = &ended
		l.Status.Closed = true
	}
}

// endingAt sets a scheduled end without closing the lease, so it is still live
// before that instant. effectiveEnd honors it either way.
func endingAt(t time.Time) leaseOpt {
	return func(l *v1.GPULease) {
		end := v1.NewTime(t)
		l.Spec.Interval.End = &end
	}
}

func withRole(role string) leaseOpt {
	return func(l *v1.GPULease) { l.Spec.Slice.Role = role }
}

func withGroup(idx int) leaseOpt {
	return func(l *v1.GPULease) { l.Labels["rq.davidlangworthy.io/group-index"] = fmt.Sprintf("%d", idx) }
}

// forRunOwner overrides the run's namespace on a lease whose paying owner is not
// the run's own owner — a BORROWED lease (run in owner A's namespace, paid by
// sponsor B's envelope). The lease and its RunRef live with the run (namespace
// nsForOwner(A)); only PaidByBudgetNamespace points at the sponsor (B).
func forRunOwner(owner string) leaseOpt {
	return func(l *v1.GPULease) {
		l.Namespace = nsForOwner(owner)
		l.Spec.RunRef.Namespace = nsForOwner(owner)
	}
}

func leaseOf(name, runName, payerOwner, budget, envelope string, width int, start time.Time, opts ...leaseOpt) v1.GPULease {
	nodes := make([]string, width)
	for i := range nodes {
		nodes[i] = fmt.Sprintf("node-%s#%d", name, i)
	}
	// Default: the run's own envelope pays, so the run, the lease and the payer
	// budget all share the owner's namespace. forRunOwner overrides the run/lease
	// namespace for borrowed leases.
	payerNS := nsForOwner(payerOwner)
	lease := v1.GPULease{
		ObjectMeta: v1.ObjectMeta{
			Name:      name,
			Namespace: payerNS,
			Labels:    map[string]string{"rq.davidlangworthy.io/group-index": "0"},
		},
		Spec: v1.GPULeaseSpec{
			Owner:                 payerOwner,
			RunRef:                v1.RunReference{Name: runName, Namespace: payerNS},
			Slice:                 v1.GPULeaseSlice{Nodes: nodes, Role: "Active"},
			Interval:              v1.GPULeaseInterval{Start: v1.NewTime(start)},
			PaidByBudgetNamespace: payerNS,
			PaidByBudget:          budget,
			PaidByEnvelope:        envelope,
			Reason:                "Start",
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

func classOf(t *testing.T, ev *Evaluation, leases []v1.GPULease, name string) Class {
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
	leases := []v1.GPULease{leaseOf("l1", "train", "team", "team-budget", "west", 4, base)}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runs, Now: base.Add(2 * time.Hour)})

	if got := classOf(t, ev, leases, "l1"); got != ClassOwned {
		t.Fatalf("expected Owned, got %s", got)
	}
	acct := ev.Envelope(EnvelopeKey{Namespace: "team", Budget: "team-budget", Envelope: "west"})
	if acct.FundedWidth() != 4 {
		t.Errorf("expected funded width 4, got %d", acct.FundedWidth())
	}
	run := ev.Run("team/train")
	if run.GPUs[ClassOwned] != 4 || math.Abs(run.GPUHours[ClassOwned]-8) > 1e-9 {
		t.Errorf("expected 4 owned GPUs and 8 owned GPU-hours, got %d / %v", run.GPUs[ClassOwned], run.GPUHours[ClassOwned])
	}
}

func TestSkipSemantics(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8))}
	big := runOf("big", "team", base, false)
	small := runOf("small", "team", base.Add(time.Minute), false)
	leases := []v1.GPULease{
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
	leases := []v1.GPULease{leaseOf("l-child", "child-train", "team", "team-budget", "west", 8, base, forRunOwner("team/child"))}

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
	if lenders := ev.Run("team-child/child-train").Lenders; len(lenders) != 0 {
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
	leases := []v1.GPULease{
		leaseOf("l-child", "child-train", "team", "team-budget", "west", 2, base, forRunOwner("team/child")),
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
	budgets := []v1.Budget{
		budgetOf("team", "team-budget", nil,
			env("west", 8, withLending(v1.LendingPolicy{Allow: true, MaxConcurrency: &six}))),
		// R7: a borrower must have a bound namespace to derive an owner from
		// (the empty-borrower guard). A pure pool-consumer is bound with a
		// nominal envelope (§5); here it only ever borrows the lender's west.
		budgetOf("org:other", "other-budget", nil, env("other", 1)),
	}
	stranger := runOf("guest", "org:other", base, false)
	owner := runOf("boss", "team", base.Add(time.Minute), false)
	leases := []v1.GPULease{
		leaseOf("l-guest", "guest", "team", "team-budget", "west", 6, base, forRunOwner("org:other")),
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
	budgets := []v1.Budget{
		budgetOf("team", "team-budget", nil,
			env("west", 8, withLending(v1.LendingPolicy{Allow: true, To: []string{"org:*"}, MaxConcurrency: &two}))),
		// R7: bind each borrower's namespace so OwnerOf derives its tier; the
		// lending ACL then matches (or rejects) the DERIVED owner.
		budgetOf("org:friend", "friend-budget", nil, env("friend", 1)),
		budgetOf("corp:foe", "foe-budget", nil, env("foe", 1)),
		budgetOf("org:late", "late-budget", nil, env("late", 1)),
	}
	allowed := runOf("guest-a", "org:friend", base, false)
	denied := runOf("guest-b", "corp:foe", base.Add(time.Minute), false)
	over := runOf("guest-c", "org:late", base.Add(2*time.Minute), false)
	leases := []v1.GPULease{
		leaseOf("l-a", "guest-a", "team", "team-budget", "west", 2, base, forRunOwner("org:friend")),
		leaseOf("l-b", "guest-b", "team", "team-budget", "west", 2, base, forRunOwner("corp:foe")),
		leaseOf("l-c", "guest-c", "team", "team-budget", "west", 2, base, forRunOwner("org:late")),
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
// A claim outside its envelope's window is Unfunded; moving the window forward
// re-funds it by pure arithmetic — nothing to resubmit. The integral half of
// this doctrine died with maxGPUHours (Ruling 10); the WINDOW half is the whole
// rule now (INV-WINDOW-REQUIRED, DESIGN-v5 §1).
func TestWindowReopenRefunds(t *testing.T) {
	run := runOf("train", "team", base, false)
	leases := []v1.GPULease{leaseOf("l1", "train", "team", "team-budget", "west", 4, base)}
	now := base.Add(4 * time.Hour)

	closed := []v1.Budget{budgetOf("team", "team-budget", nil,
		env("west", 8, withWindow(base.Add(-2*time.Hour), base.Add(time.Hour))))}
	ev := Evaluate(Input{Budgets: closed, Leases: leases, Runs: runsMap(run), Now: now})
	if got := classOf(t, ev, leases, "l1"); got != ClassUnfunded {
		t.Fatalf("window closed, expected Unfunded, got %s", got)
	}

	renewed := []v1.Budget{budgetOf("team", "team-budget", nil,
		env("west", 8, withWindow(base.Add(3*time.Hour), base.Add(9*time.Hour))))}
	ev = Evaluate(Input{Budgets: renewed, Leases: leases, Runs: runsMap(run), Now: now})
	if got := classOf(t, ev, leases, "l1"); got != ClassOwned {
		t.Errorf("renewed window should re-fund the claim, got %s", got)
	}
}

// Pre-window admissions (preActivation.allowAdmission) evaluate unfunded
// until the window opens, then fund by arithmetic.
func TestPreWindowLeaseFundsWhenWindowOpens(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil,
		env("west", 8, withWindow(base.Add(time.Hour), base.Add(24*time.Hour))))}
	run := runOf("train", "team", base, false)
	leases := []v1.GPULease{leaseOf("l1", "train", "team", "team-budget", "west", 4, base)}

	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(run), Now: base.Add(30 * time.Minute)})
	if got := classOf(t, ev, leases, "l1"); got != ClassUnfunded {
		t.Errorf("pre-window lease should be Unfunded, got %s", got)
	}

	ev = Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(run), Now: base.Add(2 * time.Hour)})
	if got := classOf(t, ev, leases, "l1"); got != ClassOwned {
		t.Errorf("lease should fund once the window opens, got %s", got)
	}
	acct := ev.Envelope(EnvelopeKey{Namespace: "team", Budget: "team-budget", Envelope: "west"})
	if math.Abs(acct.ConsumedGPUHours-4) > 1e-6 {
		t.Errorf("pre-window hours must not charge the integral: expected 4, got %v", acct.ConsumedGPUHours)
	}
}

// Malleable claims fund as much width as quota affords, lowest group index
// first — the same groups the shrink path would cut demote first.
func TestMalleablePartialFunding(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 6))}
	run := runOf("elastic", "team", base, true)
	leases := []v1.GPULease{
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
	runAcct := ev.Run("team/elastic")
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
	leases := []v1.GPULease{
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
	child := budgetOf("team/child", "child-budget", []string{"team"}, idleEnvelope())
	ownerRun := runOf("owner-run", "team", base, false)
	familyRun := runOf("family-run", "team/child", base, false)
	// 'east' sorts before 'west', so pre-fix the family claim on east would
	// win the aggregate; the owner's claim on west would demote.
	leases := []v1.GPULease{
		leaseOf("l-family", "family-run", "team", "team-budget", "east", 8, base, forRunOwner("team/child")),
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
	child := budgetOf("team/child", "child-budget", []string{"team"}, idleEnvelope())
	familyRun := runOf("family-run", "team/child", base, false)
	leases := []v1.GPULease{leaseOf("l-family", "family-run", "team", "team-budget", "east", 8, base, forRunOwner("team/child"))}
	ev := Evaluate(Input{Budgets: []v1.Budget{budget, child}, Leases: leases, Runs: runsMap(familyRun), Now: base.Add(time.Hour)})

	// The owner outranks the family borrower everywhere, so it can recall
	// the borrowed aggregate width on the empty member (west) and on the
	// member the family currently holds (east) alike.
	westKey := EnvelopeKey{Namespace: "team", Budget: "team-budget", Envelope: "west"}
	if got := ev.AvailableWidth(westKey, "team", base, "", false); got != 8 {
		t.Errorf("owner should recall the family borrower's aggregate width on west, want 8, got %d", got)
	}
	eastKey := EnvelopeKey{Namespace: "team", Budget: "team-budget", Envelope: "east"}
	if got := ev.AvailableWidth(eastKey, "team", base, "", false); got != 8 {
		t.Errorf("owner should recall the family borrower on east too, want 8, got %d", got)
	}
	// A junior cousin does NOT outrank the sitting family claim, so it sees
	// none of the aggregate width the owner could recall.
	cousin := budgetOf("team/cousin", "cousin-budget", []string{"team"}, idleEnvelope())
	ev2 := Evaluate(Input{Budgets: []v1.Budget{budget, child, cousin}, Leases: leases, Runs: runsMap(familyRun), Now: base.Add(time.Hour)})
	if got := ev2.AvailableWidth(westKey, "team/cousin", base.Add(time.Minute), "", false); got != 0 {
		t.Errorf("a later cousin cannot recall the family claim through the aggregate, want 0, got %d", got)
	}
}

func TestOrphanLeaseUnfunded(t *testing.T) {
	budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8))}
	leases := []v1.GPULease{leaseOf("l1", "ghost", "team", "team-budget", "west", 4, base)}
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
	leases := []v1.GPULease{leaseOf("l-child", "child-train", "team", "team-budget", "west", 6, base, forRunOwner("team/child"))}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(childRun), Now: base.Add(time.Hour), Period: time.Hour})

	key := EnvelopeKey{Namespace: "team", Budget: "team-budget", Envelope: "west"}
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
	leases := []v1.GPULease{leaseOf("l-child", "child-train", "team", "team-budget", "west", 8, base, forRunOwner("team/child"))}
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(childRun), Now: base.Add(time.Hour)})
	key := EnvelopeKey{Namespace: "team", Budget: "team-budget", Envelope: "west"}

	// Same tier (child), same admission second: a name-senior prospective
	// outranks the sitting claim and recalls all 8.
	if got := ev.AvailableWidth(key, "team/child2", base, "team-child/aaa-run", false); got != 8 {
		t.Errorf("name-senior peer should recall the sitting claim, want 8, got %d", got)
	}
	// A name-junior prospective ranks below it and sees nothing.
	if got := ev.AvailableWidth(key, "team/child2", base, "team-child/zzz-run", false); got != 0 {
		t.Errorf("name-junior peer must not recall the sitting claim, want 0, got %d", got)
	}
	// Empty name keeps the conservative estimate (every same-time peer
	// senior), so no recall.
	if got := ev.AvailableWidth(key, "team/child2", base, "", false); got != 0 {
		t.Errorf("empty name should be conservative (0), got %d", got)
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
	leases  []v1.GPULease
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
	var leases []v1.GPULease
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
			// The run lives in its OWN owner's namespace; the lease pays budget b
			// (owner b.Spec.Owner). When they differ the lease is borrowed, so pin
			// the run/lease namespace to the run's owner via forRunOwner.
			opts := []leaseOpt{withGroup(j), forRunOwner(owner)}
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

// NoOverdraft: funded width never exceeds any concurrency cap. The GPU-hour
// half of this property died with maxGPUHours — there is no integral to
// overdraw (Ruling 10), and DESIGN-v5 §5 records observed hours without
// clamping them.
func TestPropertyNoOverdraft(t *testing.T) {
	for seed := int64(0); seed < 150; seed++ {
		w := genWorld(rand.New(rand.NewSource(seed)))
		ev := evaluateWorld(w)
		for _, acct := range ev.Envelopes() {
			if acct.FundedWidth() > acct.Spec.Concurrency {
				t.Fatalf("seed %d: envelope %v funded width %d exceeds concurrency %d",
					seed, acct.Key, acct.FundedWidth(), acct.Spec.Concurrency)
			}
			if policy := acct.Spec.Lending; policy != nil {
				if policy.MaxConcurrency != nil && acct.WidthByClass[ClassBorrowed] > *policy.MaxConcurrency {
					t.Fatalf("seed %d: envelope %v borrowed width %d exceeds lending cap %d",
						seed, acct.Key, acct.WidthByClass[ClassBorrowed], *policy.MaxConcurrency)
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
// are contractual and deliberately excluded).
//
// This is now exact on EVERY generated world. It used to hold only after
// stripping the integral caps, because family consumption really did drain a
// shared envelope's GPU-hours and so coupled claims across tiers. With hours
// metered rather than enforced (Ruling 10) that coupling is gone and the
// property holds unconditionally.
func TestPropertyOwnerIndependentOfFamilyBorrowers(t *testing.T) {
	for seed := int64(0); seed < 150; seed++ {
		w := genWorld(rand.New(rand.NewSource(seed)))
		ev := evaluateWorld(w)

		ownerClasses := make(map[string]Class)
		var trimmed []v1.GPULease
		for i := range w.leases {
			lease := &w.leases[i]
			runKey := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
			run := w.runs[runKey]
			payerIsRunOwner := run != nil && ev.OwnerOf(run.Namespace) == lease.Spec.Owner
			if payerIsRunOwner {
				if class, ok := ev.Class(lease); ok {
					ownerClasses[LeaseKey(lease)] = class
				}
				trimmed = append(trimmed, *lease)
				continue
			}
			tier := tierNone
			if run != nil {
				tier = ev.Graph.Tier(lease.Spec.Owner, ev.OwnerOf(run.Namespace))
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
		w := genWorld(rng)
		if len(w.runs) == 0 {
			continue
		}
		ev := evaluateWorld(w)

		runKeys := make([]string, 0, len(w.runs))
		for key := range w.runs {
			runKeys = append(runKeys, key)
		}
		victim := runKeys[rng.Intn(len(runKeys))]

		var trimmed []v1.GPULease
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

// Codex #1 / task #62: two tenants each own a Budget named "team-budget" with an
// envelope "west" in their OWN namespace. Before the namespace was part of the
// funding key, their envelopes collided in envIndex — one overwrote the other, and
// leases pointing at {team-budget, west} all charged the survivor. This pins that
// the two envelopes stay distinct and each tenant's lease charges its own budget.
func TestSameNamedBudgetsInDifferentNamespacesDoNotCollide(t *testing.T) {
	nsBudget := func(ns, owner string) v1.Budget {
		b := budgetOf(owner, "team-budget", nil, env("west", 8))
		b.Namespace = ns
		return b
	}
	nsLease := func(name, ns, owner string) v1.GPULease {
		l := leaseOf(name, "train", owner, "team-budget", "west", 4, base)
		l.Namespace = ns
		l.Spec.RunRef.Namespace = ns
		l.Spec.PaidByBudgetNamespace = ns
		return l
	}
	runNS := func(ns, owner string) *v1.Run {
		r := runOf("train", owner, base, false)
		r.Namespace = ns
		return r
	}

	budgets := []v1.Budget{nsBudget("ns-a", "tenant-a"), nsBudget("ns-b", "tenant-b")}
	leases := []v1.GPULease{nsLease("la", "ns-a", "tenant-a"), nsLease("lb", "ns-b", "tenant-b")}
	runs := runsMap(runNS("ns-a", "tenant-a"), runNS("ns-b", "tenant-b"))
	ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runs, Now: base.Add(2 * time.Hour)})

	// Each tenant's lease is funded by ITS OWN budget — not demoted to Unfunded by a
	// collision, and not charged to the other tenant.
	if got := classOf(t, ev, leases, "la"); got != ClassOwned {
		t.Errorf("tenant-a's lease should be Owned by ns-a's budget, got %s", got)
	}
	if got := classOf(t, ev, leases, "lb"); got != ClassOwned {
		t.Errorf("tenant-b's lease should be Owned by ns-b's budget, got %s", got)
	}
	// Both envelopes exist as DISTINCT accounts, each funding exactly its own 4 GPUs.
	// Under the collision, one key would be missing and the other would show 4 (not 8),
	// or a single account would absorb both tenants' width.
	a := ev.Envelope(EnvelopeKey{Namespace: "ns-a", Budget: "team-budget", Envelope: "west"})
	b := ev.Envelope(EnvelopeKey{Namespace: "ns-b", Budget: "team-budget", Envelope: "west"})
	if a == nil || b == nil {
		t.Fatalf("both namespaces' envelopes must exist as distinct accounts: ns-a=%v ns-b=%v", a, b)
	}
	if a.FundedWidth() != 4 || b.FundedWidth() != 4 {
		t.Errorf("each envelope funds its own tenant's 4 GPUs; got ns-a=%d ns-b=%d", a.FundedWidth(), b.FundedWidth())
	}
}

// INV-WINDOW-REQUIRED holds in the ENGINE, not only in the webhook (DESIGN-v5
// §1, §7). Validation rejects a half- or un-windowed envelope at admission, but
// funding.Evaluate is a pure function over whatever specs it is handed —
// including objects written before the rule existed. The old behaviour for
// exactly those was the dangerous one: no bounds meant active FOREVER, an
// envelope that never expires and cannot be cut.
//
// Each of these funds nothing. Expiry is the default, and a missing bound fails
// closed rather than open.
func TestUnwindowedEnvelopeFundsNothing(t *testing.T) {
	run := runOf("train", "team", base, false)
	leases := []v1.GPULease{leaseOf("l1", "train", "team", "team-budget", "west", 4, base)}

	noWindow := func(e *v1.BudgetEnvelope) { e.Start, e.End = nil, nil }
	startOnly := func(e *v1.BudgetEnvelope) {
		s := v1.NewTime(base.Add(-time.Hour))
		e.Start, e.End = &s, nil
	}
	endOnly := func(e *v1.BudgetEnvelope) {
		en := v1.NewTime(base.Add(time.Hour))
		e.Start, e.End = nil, &en
	}

	for _, tc := range []struct {
		name string
		mod  func(*v1.BudgetEnvelope)
	}{
		{"no bounds at all", noWindow},
		{"half-windowed: start, no end", startOnly},
		{"half-windowed: end, no start", endOnly},
	} {
		t.Run(tc.name, func(t *testing.T) {
			budgets := []v1.Budget{budgetOf("team", "team-budget", nil, env("west", 8, tc.mod))}
			ev := Evaluate(Input{Budgets: budgets, Leases: leases, Runs: runsMap(run), Now: base})
			if got := classOf(t, ev, leases, "l1"); got != ClassUnfunded {
				t.Errorf("an envelope without both bounds must fund nothing, got %s", got)
			}
			acct := ev.Envelope(EnvelopeKey{Namespace: "team", Budget: "team-budget", Envelope: "west"})
			if acct != nil && acct.FundedWidth() != 0 {
				t.Errorf("funded width = %d, want 0", acct.FundedWidth())
			}
		})
	}
}
