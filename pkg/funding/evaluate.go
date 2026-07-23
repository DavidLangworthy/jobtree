package funding

import (
	"math"
	"sort"
	"strconv"
	"time"

	v1 "github.com/davidlangworthy/jobtree/api/v1"
	"github.com/davidlangworthy/jobtree/pkg/keys"
)

// DefaultPeriod is the cluster accounting horizon when none is configured.
// Admission lookahead measures width × period against the remaining
// integral; continuation is deliberately more generous and coasts until the
// integral actually exhausts (quota-semantics.md Decision 1 scopes the
// lookahead to admission — "running out does not kill work", and demotion
// at zero strands nothing).
const DefaultPeriod = 24 * time.Hour

// integralEpsilon (GPU-hours) breaks float ties at depletion crossings: the
// gate fails strictly once accrual passes the crossing point, so a
// depletion step always demotes instead of re-fitting on float equality.
const integralEpsilon = 1e-9

// Input gathers the facts the evaluation derives from. All fields are facts
// (CRD specs, lease intervals, the clock) — never a stored classification.
type Input struct {
	Budgets []v1.Budget
	Leases  []v1.GPULease
	Runs    map[string]*v1.Run // keyed by keys.NamespacedKey
	Now     time.Time
	Period  time.Duration // accounting horizon; <= 0 uses DefaultPeriod

	// R4 pt2 ledger compaction. When SettlementHorizon is non-zero, leases whose
	// accrual ended at or before it are SETTLED: they are dropped from the replay
	// and their per-envelope contribution is supplied instead by PriorAccrual
	// (compute it with SettleAccrual). Evaluate only compacts when it is provably
	// safe — the horizon is at or before Now (so nothing settled is still live),
	// it precedes every retained lease's start (no straddle), and no budget in
	// play uses aggregate caps (deferred to pt2b) — otherwise it falls back to a
	// full replay, so a wrongly-chosen horizon degrades to correct-but-uncompacted,
	// never to a wrong funding decision. A zero SettlementHorizon disables
	// compaction entirely: Evaluate is then bit-identical to the pre-pt2 engine
	// (the golden oracle's guarantee). If you change these semantics, update
	// `specs/LedgerCompaction.tla`, `specs/LedgerCompactionStore.tla`,
	// `specs/LedgerCompactionAccounting.tla`, and rerun
	// `make ledger-compaction-apalache-check`.
	SettlementHorizon time.Time
	PriorAccrual      map[EnvelopeKey]SettledAccrual
}

// SettledAccrual is one envelope's GPU-hour accrual from the settled epoch
// (leases that ended at or before a settlement horizon). Seeding it lets Evaluate
// replay only the retained leases while still charging envelope and lending
// MaxGPUHours caps against the full history and reporting the full consumed
// hours. Aggregate-cap accrual is intentionally absent — pt2a does not compact
// aggregate-capped budgets (see Input.SettlementHorizon); pt2b adds it.
type SettledAccrual struct {
	ConsumedGPUHours float64
	HoursByClass     map[Class]float64
}

// Evaluation is the derived classification at Input.Now plus the replayed
// hour attribution that led to it. Status blocks and metrics are views of
// this; nothing in the control path reads a classification back from status.
type Evaluation struct {
	Now    time.Time
	Period time.Duration
	Graph  *FamilyGraph

	classes   map[string]Class // by LeaseKey, open leases only
	envelopes map[EnvelopeKey]*EnvelopeAccount
	runs      map[string]*RunAccount

	// ranked claim state at Now, kept for AvailableWidth (admission).
	claimsByEnv map[EnvelopeKey][]*claim
	fundedWidth map[claimKey]int32
	aggWidth    map[*aggregateAccount]int32
}

// EnvelopeAccount reports one envelope's derived usage.
type EnvelopeAccount struct {
	Key   EnvelopeKey
	Owner string
	Spec  v1.BudgetEnvelope

	// WidthByClass is the active width at Now attributed to the envelope by
	// class. Unfunded width references the envelope (its leases name it as
	// payer) but is never charged against its caps.
	WidthByClass map[Class]int32
	// SpareWidth is the subset of funded width held by Spare-role leases.
	SpareWidth int32
	// ConsumedGPUHours is the replayed funded accrual within the envelope's
	// current window, structurally clamped to MaxGPUHours (no overdraft).
	// History is evaluated under the current spec: moving the window forward
	// (renewal) releases hours spent in the old window, which is exactly how
	// "a reopened budget window re-funds" falls out of the arithmetic.
	ConsumedGPUHours float64
	// HoursByClass attributes all accrued hours, including the separate
	// unfunded bucket.
	HoursByClass map[Class]float64

	aggregates []*aggregateAccount
}

// FundedWidth is the total width charged against the envelope at Now.
func (e *EnvelopeAccount) FundedWidth() int32 {
	return e.WidthByClass[ClassOwned] + e.WidthByClass[ClassShared] + e.WidthByClass[ClassBorrowed]
}

// RemainingGPUHours returns the envelope's remaining integral, or nil when
// it has no MaxGPUHours cap.
func (e *EnvelopeAccount) RemainingGPUHours() *float64 {
	if e.Spec.MaxGPUHours == nil {
		return nil
	}
	remaining := float64(*e.Spec.MaxGPUHours) - e.ConsumedGPUHours
	if remaining < 0 {
		remaining = 0
	}
	return &remaining
}

// RunAccount reports one run's derived class breakdown.
type RunAccount struct {
	Key string
	// GPUs is the active non-spare width at Now per class.
	GPUs map[Class]int32
	// SpareGPUs is the active spare width at Now (all classes).
	SpareGPUs int32
	// GPUHours attributes the run's accrued hours per class; the Unfunded
	// bucket is the "this run consumed N unfunded GPU-hours" figure.
	GPUHours map[Class]float64
	// Lenders lists non-owner envelope owners currently funding active
	// width, with their share (shared and borrowed classes).
	Lenders map[string]int32
	// LenderHours attributes the run's funded accrual per lending owner.
	LenderHours map[string]float64
}

// aggregateAccount carries an aggregate cap's cumulative funded accrual.
// Funded width is per-instant state and lives in fillResult.aggWidth.
type aggregateAccount struct {
	spec     v1.AggregateCap
	consumed float64
}

// Evaluate derives the classification. It replays the lease facts from the
// earliest event so that GPU-hour integrals only count hours accrued while
// funded: exhaustion demotes every claim the envelope was covering — the
// ledger hit zero — and never overdraws it.
func Evaluate(in Input) *Evaluation {
	if in.Period <= 0 {
		in.Period = DefaultPeriod
	}
	if in.Now.IsZero() {
		in.Now = time.Now().UTC()
	}

	ev := &Evaluation{
		Now:         in.Now,
		Period:      in.Period,
		Graph:       NewFamilyGraph(in.Budgets),
		classes:     make(map[string]Class),
		envelopes:   make(map[EnvelopeKey]*EnvelopeAccount),
		runs:        make(map[string]*RunAccount),
		claimsByEnv: make(map[EnvelopeKey][]*claim),
		fundedWidth: make(map[claimKey]int32),
		aggWidth:    make(map[*aggregateAccount]int32),
	}

	envIndex := make(map[EnvelopeKey]*EnvelopeAccount)
	var envOrder []EnvelopeKey
	for i := range in.Budgets {
		b := &in.Budgets[i]
		byName := make(map[string]*EnvelopeAccount, len(b.Spec.Envelopes))
		for j := range b.Spec.Envelopes {
			env := b.Spec.Envelopes[j]
			key := EnvelopeKey{Namespace: b.Namespace, Budget: b.Name, Envelope: env.Name}
			acct := &EnvelopeAccount{
				Key:          key,
				Owner:        b.Spec.Owner,
				Spec:         env,
				WidthByClass: make(map[Class]int32),
				HoursByClass: make(map[Class]float64),
			}
			envIndex[key] = acct
			byName[env.Name] = acct
			envOrder = append(envOrder, key)
		}
		for j := range b.Spec.AggregateCaps {
			cap := b.Spec.AggregateCaps[j]
			acct := &aggregateAccount{spec: cap}
			for _, name := range cap.Envelopes {
				env, ok := byName[name]
				if !ok {
					continue
				}
				// A flavored cap bounds ONE flavor's usage. Attaching it to an envelope
				// of a DIFFERENT flavor makes every downstream consumer (fillClaim,
				// accrue, aggregateAvailable) sum that envelope's width/hours into the
				// cap — a cross-flavor mis-count that lets a flavored cap be exceeded or
				// spuriously refuse. The attach is the single choke point (all consumers
				// walk env.aggregates with no flavor awareness), so filter here.
				if cap.Flavor != "" && env.Spec.Flavor != cap.Flavor {
					continue
				}
				env.aggregates = append(env.aggregates, acct)
			}
		}
	}
	sort.Slice(envOrder, func(i, j int) bool {
		if envOrder[i].Budget != envOrder[j].Budget {
			return envOrder[i].Budget < envOrder[j].Budget
		}
		return envOrder[i].Envelope < envOrder[j].Envelope
	})
	ev.envelopes = envIndex

	// R4 pt2: seed the settled epoch's accrual and drop its leases from the
	// replay, but only where provably safe (see settlementSafe). Seeding the
	// per-envelope ConsumedGPUHours / HoursByClass carries forward exactly what the
	// depletion math and the envelope+lending caps read, so the retained replay
	// continues from the correct baseline.
	compact := settlementSafe(in)
	if compact {
		for key, prior := range in.PriorAccrual {
			acct := envIndex[key]
			if acct == nil {
				continue
			}
			acct.ConsumedGPUHours += prior.ConsumedGPUHours
			for cl, h := range prior.HoursByClass {
				acct.HoursByClass[cl] += h
			}
		}
	}

	facts := buildLeaseFacts(in, compact)

	// Replay: step through the event timeline, holding the funded set
	// constant between events, and split segments at integral-depletion
	// crossings where an exhausted budget demotes its claims.
	times := eventTimes(in, facts)
	for idx := 0; idx < len(times); idx++ {
		t0 := times[idx]
		var t1 time.Time
		if idx+1 < len(times) {
			t1 = times[idx+1]
		} else {
			t1 = in.Now
		}
		if !t0.Before(t1) {
			continue
		}
		// Each depletion crossing exhausts at least one budget dimension
		// for the rest of the segment, so splits are bounded; the cap is a
		// defensive backstop against float pathology, not a path correct
		// inputs take.
		for steps := len(facts) + 8; t0.Before(t1) && steps > 0; steps-- {
			fill := ev.fill(in, facts, envOrder, t0)
			step := t1
			if crossing, ok := fill.nextDepletion(t0); ok && crossing.Before(step) {
				step = crossing
			}
			fill.accrue(ev, t0, step)
			t0 = step
		}
	}

	// Final classification at Now.
	final := ev.fill(in, facts, envOrder, in.Now)
	final.commit(ev)
	return ev
}

// buildLeaseFacts parses widths and group indices once. When compact is set it
// drops settled leases (accrual ended at or before Input.SettlementHorizon) —
// their contribution is supplied by Input.PriorAccrual, so replaying them again
// would double-count.
func buildLeaseFacts(in Input, compact bool) []*leaseFact {
	facts := make([]*leaseFact, 0, len(in.Leases))
	for i := range in.Leases {
		lease := &in.Leases[i]
		if compact && leaseSettled(lease, in.SettlementHorizon) {
			continue
		}
		width := int32(len(lease.Spec.Slice.Nodes))
		if width == 0 {
			width = 1
		}
		groupIndex := 0
		if lease.Labels != nil {
			if idx, err := strconv.Atoi(lease.Labels["rq.davidlangworthy.io/group-index"]); err == nil {
				groupIndex = idx
			}
		}
		facts = append(facts, &leaseFact{
			lease:      lease,
			width:      width,
			groupIndex: groupIndex,
			name:       LeaseKey(lease),
		})
	}
	return facts
}

// leaseSettled reports whether a lease's accrual ended at or before the horizon
// (so it belongs to the settled epoch, not the retained replay). An open lease
// (zero effectiveEnd) is never settled.
func leaseSettled(lease *v1.GPULease, horizon time.Time) bool {
	end := effectiveEnd(lease)
	return !end.IsZero() && !end.After(horizon)
}

// settlementSafe reports whether Evaluate may compact this input. It requires
// (1) a non-zero horizon that does not lead Now, (2) no budget in play using
// aggregate caps — pt2a seeds only envelope-level accrual, so aggregate-capped
// budgets are left to a full replay (pt2b), and (3) the no-straddle invariant:
// every RETAINED lease starts at or after the horizon, so the settled and
// retained epochs never co-occur in the fill and the settled accrual is
// independent of anything retained. When any fails, Evaluate replays the full
// ledger — correct, just uncompacted. The local one-shot theorem lives in
// `specs/LedgerCompaction.tla`; the persisted-store / window-invalidation
// theorem lives in `specs/LedgerCompactionStore.tla`; the broader aggregate /
// lender / full-window carry-forward model lives in
// `specs/LedgerCompactionAccounting.tla`.
func settlementSafe(in Input) bool {
	if in.SettlementHorizon.IsZero() {
		return false
	}
	// A horizon ahead of Now would settle leases that are still LIVE at Now: a
	// lease whose Interval.End lies in (Now, horizon] has an effectiveEnd at or
	// before the horizon, so leaseSettled calls it settled while leaseLiveAt still
	// funds it. The no-straddle loop below only inspects retained leases, so it
	// cannot catch that one. Requiring horizon <= Now makes it impossible: a
	// settled lease's end is then <= Now, and leaseLiveAt is half-open, so no
	// settled lease can hold width at Now. Its accrual is also complete, which is
	// what lets SettleAccrual integrate it as of the horizon rather than as of Now.
	if in.SettlementHorizon.After(in.Now) {
		return false
	}
	for i := range in.Budgets {
		if len(in.Budgets[i].Spec.AggregateCaps) > 0 {
			return false
		}
	}
	for i := range in.Leases {
		l := &in.Leases[i]
		if leaseSettled(l, in.SettlementHorizon) {
			continue
		}
		if l.Spec.Interval.Start.Time.Before(in.SettlementHorizon) {
			return false
		}
	}
	return true
}

// SettleAccrual computes the per-envelope settled-epoch summary for compaction:
// it replays only the leases that end at or before horizon, evaluated as of
// horizon (so each accrues its full settled life), and returns their envelope
// accrual. Feed the result back as Input.PriorAccrual with the same
// SettlementHorizon to drop those leases from later replays with no change to the
// funding result (R4 pt2). Returns nil for an empty epoch, and for a horizon that
// leads Now — a summary taken past the clock would integrate leases that are
// still live, and persisting it (pt2b) would over-charge the envelope forever
// after. Callers should only advance the horizon past leases that can no longer
// be reclassified — i.e. below every retained lease's start and within the
// current envelope window.
func SettleAccrual(in Input, horizon time.Time) map[EnvelopeKey]SettledAccrual {
	if horizon.IsZero() || horizon.After(in.Now) {
		return nil
	}
	epoch := in
	epoch.Now = horizon
	epoch.SettlementHorizon = time.Time{}
	epoch.PriorAccrual = nil
	var settled []v1.GPULease
	for i := range in.Leases {
		if leaseSettled(&in.Leases[i], horizon) {
			settled = append(settled, in.Leases[i])
		}
	}
	if len(settled) == 0 {
		return nil
	}
	epoch.Leases = settled
	ev := Evaluate(epoch)
	out := make(map[EnvelopeKey]SettledAccrual, len(ev.envelopes))
	for key, acct := range ev.envelopes {
		if acct.ConsumedGPUHours == 0 && len(acct.HoursByClass) == 0 {
			continue
		}
		sa := SettledAccrual{
			ConsumedGPUHours: acct.ConsumedGPUHours,
			HoursByClass:     make(map[Class]float64, len(acct.HoursByClass)),
		}
		for cl, h := range acct.HoursByClass {
			sa.HoursByClass[cl] = h
		}
		out[key] = sa
	}
	return out
}

// eventTimes collects the sorted, deduplicated timeline: lease starts and
// effective ends plus envelope window boundaries, all clamped to Now.
func eventTimes(in Input, facts []*leaseFact) []time.Time {
	set := make(map[int64]time.Time)
	add := func(t time.Time) {
		if t.IsZero() || t.After(in.Now) {
			return
		}
		set[t.UnixNano()] = t
	}
	for _, f := range facts {
		add(f.lease.Spec.Interval.Start.Time)
		add(effectiveEnd(f.lease))
	}
	for i := range in.Budgets {
		for _, env := range in.Budgets[i].Spec.Envelopes {
			if env.Start != nil {
				add(env.Start.Time)
			}
			if env.End != nil {
				add(env.End.Time)
			}
		}
	}
	out := make([]time.Time, 0, len(set))
	for _, t := range set {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Before(out[j]) })
	return out
}

// effectiveEnd returns when the lease stops accruing, or the zero time for
// an open-ended lease. A Closed lease without an end fact is defensive
// territory (the engine always stamps Ended): treat it as never accruing.
func effectiveEnd(lease *v1.GPULease) time.Time {
	var end time.Time
	if lease.Spec.Interval.End != nil && !lease.Spec.Interval.End.IsZero() {
		end = lease.Spec.Interval.End.Time
	}
	if lease.Status.Ended != nil && !lease.Status.Ended.IsZero() {
		if end.IsZero() || lease.Status.Ended.Time.Before(end) {
			end = lease.Status.Ended.Time
		}
	}
	if end.IsZero() && lease.Status.Closed {
		end = lease.Spec.Interval.Start.Time
	}
	return end
}

// leaseLiveAt reports whether the lease accrues at t. An open-ended lease is
// live at the evaluation instant itself, so the final fill classifies it.
func leaseLiveAt(f *leaseFact, t time.Time) bool {
	if t.Before(f.lease.Spec.Interval.Start.Time) {
		return false
	}
	end := effectiveEnd(f.lease)
	return end.IsZero() || t.Before(end)
}

// windowActive reports whether the envelope can be charged at t. Work
// admitted before the window (preActivation.allowAdmission) evaluates
// unfunded until the window opens and re-funds by arithmetic.
func windowActive(env *v1.BudgetEnvelope, t time.Time) bool {
	if env.Start != nil && t.Before(env.Start.Time) {
		return false
	}
	if env.End != nil && !t.Before(env.End.Time) {
		return false
	}
	return true
}

// fillResult captures one instant's ranked greedy fill: which lease is
// funded with which class, and the per-envelope funded rates needed for
// accrual and depletion prediction.
type fillResult struct {
	classes    map[*leaseFact]Class
	claimOwner map[*leaseFact]string // envelope owner for funded leases
	// live keeps input order so float accumulation is deterministic —
	// map-order iteration would make repeated evaluations disagree in the
	// last bits.
	live       []*leaseFact
	envs       []*fillEnv
	aggWidth   map[*aggregateAccount]int32 // funded width at this instant
	fundedByCk map[claimKey]int32
	claims     map[EnvelopeKey][]*claim
}

type fillEnv struct {
	acct *EnvelopeAccount
	// fundedWidth is the width charged at this instant — the envelope's
	// integral accrual rate.
	fundedWidth int32
	// lentWidth is the sponsor-funded subset — the lending integral's rate.
	lentWidth int32
}

// fill runs the normative ranked greedy fill at time t. Sponsor claims are
// contract carve-outs: they fill first among themselves (admission order)
// within the lending caps, then family tiers 1-4 fill the remainder in
// (tier, admission, name) order. A claim's class therefore depends only on
// claims ranked above it — the stability and owner-recall structure from
// specs/QuotaEvaluation.tla.
func (ev *Evaluation) fill(in Input, facts []*leaseFact, envOrder []EnvelopeKey, t time.Time) *fillResult {
	res := &fillResult{
		classes:    make(map[*leaseFact]Class),
		claimOwner: make(map[*leaseFact]string),
		aggWidth:   make(map[*aggregateAccount]int32),
		fundedByCk: make(map[claimKey]int32),
		claims:     make(map[EnvelopeKey][]*claim),
	}

	// Group live leases into claims per envelope.
	claimIndex := make(map[claimKey]*claim)
	for _, f := range facts {
		if !leaseLiveAt(f, t) {
			continue
		}
		res.live = append(res.live, f)
		lease := f.lease
		envKey := EnvelopeKey{Namespace: lease.Spec.PaidByBudgetNamespace, Budget: lease.Spec.PaidByBudget, Envelope: lease.Spec.PaidByEnvelope}
		acct := ev.envelopes[envKey]
		if acct == nil {
			// No such envelope (deleted budget, empty payer): backed by
			// nothing, so unfunded by definition.
			res.classes[f] = ClassUnfunded
			continue
		}
		runKey := keys.NamespacedKey(lease.Spec.RunRef.Namespace, lease.Spec.RunRef.Name)
		ck := claimKey{env: envKey, runKey: runKey}
		cl, ok := claimIndex[ck]
		if !ok {
			run := in.Runs[runKey]
			cl = &claim{key: ck, name: runKey}
			if run != nil {
				cl.tier = ev.Graph.Tier(acct.Owner, run.Spec.Owner)
				cl.sponsored = cl.tier == tierNone
				cl.admitted = run.CreationTimestamp.Time
				cl.malleable = run.Spec.Malleable != nil
			} else {
				// Orphan claim: the run is gone and cleanup will close these
				// leases; until then they are backed by nothing.
				cl.tier = -1
			}
			if cl.admitted.IsZero() {
				cl.admitted = lease.Spec.Interval.Start.Time
			}
			claimIndex[ck] = cl
			res.claims[envKey] = append(res.claims[envKey], cl)
		}
		cl.leases = append(cl.leases, f)
		cl.width += f.width
	}

	// Deterministic lease order inside a claim: lowest group first, so
	// partial funding of malleable runs demotes the same groups the shrink
	// path would cut (highest index first).
	for _, claims := range res.claims {
		for _, cl := range claims {
			sort.Slice(cl.leases, func(i, j int) bool {
				if cl.leases[i].groupIndex != cl.leases[j].groupIndex {
					return cl.leases[i].groupIndex < cl.leases[j].groupIndex
				}
				return cl.leases[i].name < cl.leases[j].name
			})
		}
	}

	// Build each envelope's fill state and partition its claims. Sponsors and
	// family/owner claims are filled in two separate passes (below) rather
	// than envelope-by-envelope, so owner recall holds across a shared
	// aggregate cap: an owner claim on one member envelope must fund before a
	// recallable family claim on another.
	type envFill struct {
		acct     *EnvelopeAccount
		st       *fillState
		sponsors []*claim
		family   []*claim
	}
	var fills []*envFill
	for _, envKey := range envOrder {
		acct := ev.envelopes[envKey]
		fe := &fillEnv{acct: acct}
		res.envs = append(res.envs, fe)
		claims := res.claims[envKey]
		if len(claims) == 0 {
			continue
		}
		if !windowActive(&acct.Spec, t) {
			for _, cl := range claims {
				res.markClaim(cl, nil, acct)
			}
			continue
		}

		ef := &envFill{acct: acct}
		for _, cl := range claims {
			switch {
			case cl.tier == -1:
				res.markClaim(cl, nil, acct) // orphan: all unfunded
			case cl.sponsored:
				ef.sponsors = append(ef.sponsors, cl)
			case cl.tier != TierOwner && !envelopeSharable(&acct.Spec):
				res.markClaim(cl, nil, acct) // family opted out
			default:
				ef.family = append(ef.family, cl)
			}
		}
		sort.Slice(ef.sponsors, func(i, j int) bool { return rankLess(ef.sponsors[i], ef.sponsors[j]) })
		ef.st = &fillState{
			res:        res,
			env:        fe,
			remaining:  acct.RemainingGPUHours(),
			lendPolicy: acct.Spec.Lending,
			lentHours:  acct.HoursByClass[ClassBorrowed],
		}
		fills = append(fills, ef)
	}

	// Pass 1: sponsors, per envelope. Existing sponsor leases are contract
	// facts, senior on the envelope they borrow (the sponsor carve-out); the
	// lending caps bound this pool only.
	for _, ef := range fills {
		for _, cl := range ef.sponsors {
			run := in.Runs[cl.key.runKey]
			eligible := ef.acct.Spec.Lending != nil && ef.acct.Spec.Lending.Allow &&
				run != nil && lendingAllows(ef.acct.Spec.Lending, run.Spec.Owner)
			if !eligible {
				res.markClaim(cl, nil, ef.acct)
				continue
			}
			res.fillClaim(cl, ef.st, ef.acct, true)
		}
		ef.st.lendPolicy = nil // lending caps bound the sponsor pool only
	}

	// Pass 2: owner and family claims in GLOBAL rank order (tier, admission,
	// name) across every envelope. An owner's own claim (tier 1) therefore
	// funds before any family claim (tier >= 2) even when they compete for a
	// shared aggregate cap on different member envelopes — the recallable
	// family claim gets only the aggregate width the owner leaves, which is
	// owner recall through the aggregate. For envelopes not sharing an
	// aggregate this is identical to filling each independently, since each
	// claim is still charged only against its own envelope's caps.
	type rankedClaim struct {
		cl *claim
		ef *envFill
	}
	var ranked []rankedClaim
	for _, ef := range fills {
		for _, cl := range ef.family {
			ranked = append(ranked, rankedClaim{cl: cl, ef: ef})
		}
	}
	sort.Slice(ranked, func(i, j int) bool { return rankLess(ranked[i].cl, ranked[j].cl) })
	for _, rc := range ranked {
		res.fillClaim(rc.cl, rc.ef.st, rc.ef.acct, false)
	}
	return res
}

// fillState tracks one envelope's running totals during a fill walk.
type fillState struct {
	res        *fillResult
	env        *fillEnv
	remaining  *float64 // envelope integral remaining, nil = uncapped
	lendPolicy *v1.LendingPolicy
	lentHours  float64
}

// admit reports whether width more GPUs fit under every cap at this point
// of the walk, and charges them if so. Concurrency is the ranked dimension;
// the integrals are exhaustion gates — a drained budget funds nothing, a
// live one keeps covering its claims until it drains (demote-not-kill,
// nothing stranded).
func (st *fillState) admit(width int32, acct *EnvelopeAccount, sponsored bool) bool {
	if st.env.fundedWidth+width > acct.Spec.Concurrency {
		return false
	}
	if st.remaining != nil && *st.remaining <= integralEpsilon {
		return false
	}
	if sponsored && st.lendPolicy != nil {
		if st.lendPolicy.MaxConcurrency != nil && st.env.lentWidth+width > *st.lendPolicy.MaxConcurrency {
			return false
		}
		if st.lendPolicy.MaxGPUHours != nil && st.lentHours >= float64(*st.lendPolicy.MaxGPUHours)-integralEpsilon {
			return false
		}
	}
	for _, agg := range acct.aggregates {
		if agg.spec.MaxConcurrency != nil && st.res.aggWidth[agg]+width > *agg.spec.MaxConcurrency {
			return false
		}
		if agg.spec.MaxGPUHours != nil && agg.consumed >= float64(*agg.spec.MaxGPUHours)-integralEpsilon {
			return false
		}
	}
	st.env.fundedWidth += width
	if sponsored {
		st.env.lentWidth += width
	}
	for _, agg := range acct.aggregates {
		st.res.aggWidth[agg] += width
	}
	return true
}

// fillClaim classifies one claim: fixed-width claims all-or-nothing (skip
// semantics — an oversized claim never blocks smaller lower-ranked ones),
// malleable claims lease by lease ("the greedy fill funds as much width as
// quota affords").
func (res *fillResult) fillClaim(cl *claim, st *fillState, acct *EnvelopeAccount, sponsored bool) {
	class := classForTier(cl.tier)
	if sponsored {
		class = ClassBorrowed
	}
	if !cl.malleable {
		if st.admit(cl.width, acct, sponsored) {
			res.markClaim(cl, &class, acct)
			res.fundedByCk[cl.key] += cl.width
		} else {
			res.markClaim(cl, nil, acct)
		}
		return
	}
	for _, f := range cl.leases {
		if st.admit(f.width, acct, sponsored) {
			res.markLease(f, class, acct)
			res.fundedByCk[cl.key] += f.width
		} else {
			res.markLease(f, ClassUnfunded, acct)
		}
	}
}

// markClaim assigns every lease of the claim; nil class means unfunded.
func (res *fillResult) markClaim(cl *claim, class *Class, acct *EnvelopeAccount) {
	c := ClassUnfunded
	if class != nil {
		c = *class
	}
	for _, f := range cl.leases {
		res.markLease(f, c, acct)
	}
}

func (res *fillResult) markLease(f *leaseFact, class Class, acct *EnvelopeAccount) {
	res.classes[f] = class
	if class != ClassUnfunded && acct != nil {
		res.claimOwner[f] = acct.Owner
	}
}

// nextDepletion returns the earliest instant after t at which a budget
// integral (envelope, lending, or aggregate) exhausts under this fill's
// accrual rates, demoting the claims it was covering.
func (res *fillResult) nextDepletion(t time.Time) (time.Time, bool) {
	best := time.Time{}
	found := false
	consider := func(remaining float64, rate int32) {
		if rate <= 0 {
			return
		}
		if remaining < 0 {
			remaining = 0
		}
		dt := remaining / float64(rate) // hours until exhaustion
		// Round up to the next millisecond: past the crossing the accrual
		// exceeds the gate's epsilon by orders of magnitude, so the
		// demotion provably fires and every step makes progress.
		// Millisecond error on demotion timing is noise against the
		// accounting period.
		crossing := t.Add(time.Duration(math.Ceil(dt*float64(time.Hour)/float64(time.Millisecond))) * time.Millisecond)
		if !crossing.After(t) {
			crossing = t.Add(time.Millisecond)
		}
		if !found || crossing.Before(best) {
			best = crossing
			found = true
		}
	}
	for _, fe := range res.envs {
		if fe.acct.Spec.MaxGPUHours != nil && fe.fundedWidth > 0 {
			if r := fe.acct.RemainingGPUHours(); r != nil {
				consider(*r, fe.fundedWidth)
			}
		}
		if policy := fe.acct.Spec.Lending; policy != nil && policy.MaxGPUHours != nil && fe.lentWidth > 0 {
			consider(float64(*policy.MaxGPUHours)-fe.acct.HoursByClass[ClassBorrowed], fe.lentWidth)
		}
	}
	for agg, rate := range res.aggWidth {
		if agg.spec.MaxGPUHours != nil && rate > 0 {
			consider(float64(*agg.spec.MaxGPUHours)-agg.consumed, rate)
		}
	}
	return best, found
}

// accrue integrates the segment [t0, t1) under this fill: funded hours
// charge the envelope (and its aggregates), unfunded hours flow to the
// separate bucket. Per-lease and per-run attribution accumulate in ev.
func (res *fillResult) accrue(ev *Evaluation, t0, t1 time.Time) {
	hours := t1.Sub(t0).Hours()
	if hours <= 0 {
		return
	}
	aggDelta := make(map[*aggregateAccount]float64)
	for _, f := range res.live {
		class := res.classes[f]
		leaseHours := float64(f.width) * hours
		envKey := EnvelopeKey{Namespace: f.lease.Spec.PaidByBudgetNamespace, Budget: f.lease.Spec.PaidByBudget, Envelope: f.lease.Spec.PaidByEnvelope}
		acct := ev.envelopes[envKey]
		if acct != nil {
			acct.HoursByClass[class] += leaseHours
			if class != ClassUnfunded {
				// The charge clamps at the cap: depletion crossings land on
				// millisecond boundaries, and the sliver of accrual past the
				// exact crossing must not overdraw the envelope.
				charge := leaseHours
				if acct.Spec.MaxGPUHours != nil {
					if room := float64(*acct.Spec.MaxGPUHours) - acct.ConsumedGPUHours; charge > room {
						charge = math.Max(0, room)
					}
				}
				acct.ConsumedGPUHours += charge
				// Aggregates accumulate the same envelope-clamped charge, not
				// the raw leaseHours: the sliver past an envelope's own cap is
				// not funded consumption, so it must not overstate aggregate
				// usage either (the aggregate then applies its own cap below).
				for _, agg := range acct.aggregates {
					aggDelta[agg] += charge
				}
			}
		}
		runKey := keys.NamespacedKey(f.lease.Spec.RunRef.Namespace, f.lease.Spec.RunRef.Name)
		run := ev.runAccount(runKey)
		// Every GPU-hour is attributed to a class (spares included): the run's
		// per-class hours conserve to its total accrual. Width, by contrast,
		// separates spares into SpareGPUs — the two dimensions use different
		// conventions on purpose.
		run.GPUHours[class] += leaseHours
		if class == ClassShared || class == ClassBorrowed {
			if owner, ok := res.claimOwner[f]; ok {
				run.LenderHours[owner] += leaseHours
			}
		}
	}
	for _, fe := range res.envs {
		for _, agg := range fe.acct.aggregates {
			if delta, ok := aggDelta[agg]; ok {
				if agg.spec.MaxGPUHours != nil {
					if room := float64(*agg.spec.MaxGPUHours) - agg.consumed; delta > room {
						delta = math.Max(0, room)
					}
				}
				agg.consumed += delta
				delete(aggDelta, agg)
			}
		}
	}
}

// commit records the classification at Now onto the evaluation.
func (res *fillResult) commit(ev *Evaluation) {
	for _, f := range res.live {
		class := res.classes[f]
		ev.classes[f.name] = class
		envKey := EnvelopeKey{Namespace: f.lease.Spec.PaidByBudgetNamespace, Budget: f.lease.Spec.PaidByBudget, Envelope: f.lease.Spec.PaidByEnvelope}
		acct := ev.envelopes[envKey]
		if acct != nil {
			acct.WidthByClass[class] += f.width
			if class != ClassUnfunded && f.lease.Spec.Slice.Role == "Spare" {
				acct.SpareWidth += f.width
			}
		}
		runKey := keys.NamespacedKey(f.lease.Spec.RunRef.Namespace, f.lease.Spec.RunRef.Name)
		run := ev.runAccount(runKey)
		if f.lease.Spec.Slice.Role == "Spare" {
			run.SpareGPUs += f.width
		} else {
			run.GPUs[class] += f.width
			// Lender attribution mirrors the per-class width: only non-spare
			// borrowed/shared width credits a lender, so sum(Lenders) never
			// exceeds SharedGPUs+BorrowedGPUs.
			if class == ClassShared || class == ClassBorrowed {
				if owner, ok := res.claimOwner[f]; ok {
					run.Lenders[owner] += f.width
				}
			}
		}
	}
	ev.claimsByEnv = res.claims
	ev.fundedWidth = res.fundedByCk
	ev.aggWidth = res.aggWidth
}

func (ev *Evaluation) runAccount(runKey string) *RunAccount {
	acct, ok := ev.runs[runKey]
	if !ok {
		acct = &RunAccount{
			Key:         runKey,
			GPUs:        make(map[Class]int32),
			GPUHours:    make(map[Class]float64),
			Lenders:     make(map[string]int32),
			LenderHours: make(map[string]float64),
		}
		ev.runs[runKey] = acct
	}
	return acct
}

func lendingAllows(policy *v1.LendingPolicy, borrower string) bool {
	if policy == nil {
		return false
	}
	if len(policy.To) == 0 {
		return true
	}
	for _, pattern := range policy.To {
		if pattern == "*" {
			return true
		}
		if n := len(pattern); n > 0 && pattern[n-1] == '*' {
			prefix := pattern[:n-1]
			if len(borrower) >= len(prefix) && borrower[:len(prefix)] == prefix {
				return true
			}
			continue
		}
		if pattern == borrower {
			return true
		}
	}
	return false
}

// AvailableWidth reports how much additional width a claim by runOwner,
// ranked at its run's admission time, could get funded on this envelope
// right now. This is the admission-side view of the same ranking: capacity
// held by claims ranked below the prospective claim is available (they
// would demote — that is recall), capacity ranked above it is not. The
// integral uses the width × period admission lookahead so that work is
// never admitted born-opportunistic (quota-semantics.md Decision 1).
// Sponsor claims are junior to everything at admission and bounded by the
// lending caps. The window gate stays with the cover planner
// (preActivation may deliberately admit pre-window work, which evaluates
// unfunded until the window opens).
// name is the prospective run's key; it applies the deterministic name
// tiebreak against same-tier, same-second peers so admission agrees with the
// classifier's ranking. An empty name falls back to the conservative
// estimate (every same-time peer treated as senior).
func (ev *Evaluation) AvailableWidth(key EnvelopeKey, runOwner string, admitted time.Time, name string, sponsor bool) int32 {
	acct := ev.envelopes[key]
	if acct == nil {
		return 0
	}
	period := ev.Period.Hours()
	borrowedWidth := acct.WidthByClass[ClassBorrowed]
	available := int32(math.MaxInt32)
	bound := func(w int32) {
		if w < available {
			available = w
		}
	}
	boundHours := func(remaining float64) {
		if period <= 0 {
			return
		}
		if remaining < 0 {
			remaining = 0
		}
		bound(int32(math.Floor(remaining / period)))
	}

	if sponsor {
		policy := acct.Spec.Lending
		if policy == nil || !policy.Allow || !lendingAllows(policy, runOwner) {
			return 0
		}
		funded := acct.FundedWidth()
		bound(acct.Spec.Concurrency - funded)
		if policy.MaxConcurrency != nil {
			bound(*policy.MaxConcurrency - borrowedWidth)
		}
		if r := acct.RemainingGPUHours(); r != nil {
			boundHours(*r - float64(funded)*period)
		}
		if policy.MaxGPUHours != nil {
			boundHours(float64(*policy.MaxGPUHours) - acct.HoursByClass[ClassBorrowed] - float64(borrowedWidth)*period)
		}
	}

	tier := tierNone
	if !sponsor {
		tier = ev.Graph.Tier(acct.Owner, runOwner)
		if tier == tierNone {
			return 0
		}
		if tier != TierOwner && !envelopeSharable(&acct.Spec) {
			return 0
		}
		counted := borrowedWidth
		for _, cl := range ev.claimsByEnv[key] {
			if cl.sponsored || cl.tier < TierOwner {
				continue
			}
			if claimAtOrAbove(cl, tier, admitted, name) {
				counted += ev.fundedWidth[cl.key]
			}
		}
		bound(acct.Spec.Concurrency - counted)
		if r := acct.RemainingGPUHours(); r != nil {
			boundHours(*r - float64(counted)*period)
		}
	}

	// Aggregate caps honor recall the same way a single envelope does: for a
	// family/owner claim only the funded width the prospective claim does NOT
	// outrank (senior peers + contractual sponsors) counts against it — the
	// junior family width it could recall across member envelopes is
	// excluded. A sponsor admission is junior to all funded width, so its
	// aggregate bound counts everything (no recall through a lending
	// contract). The consumed-hours history is spent either way and is not
	// refunded by recall.
	for _, agg := range acct.aggregates {
		width := ev.aggWidth[agg]
		if !sponsor {
			width = ev.aggFundedNotOutranked(agg, tier, admitted, name)
		}
		if agg.spec.MaxConcurrency != nil {
			bound(*agg.spec.MaxConcurrency - width)
		}
		if agg.spec.MaxGPUHours != nil {
			boundHours(float64(*agg.spec.MaxGPUHours) - agg.consumed - float64(width)*period)
		}
	}

	if available < 0 {
		return 0
	}
	return available
}

// aggFundedNotOutranked sums the funded width across an aggregate's member
// envelopes that a prospective family/owner claim (of the given tier and
// admission time) does not outrank: senior or equal-ranked family/owner
// peers, plus every sponsor (borrowed capacity is contractual and never
// recallable). The junior family width the prospective claim could recall is
// excluded, so the aggregate cap honors owner recall just as a single
// envelope's concurrency does.
func (ev *Evaluation) aggFundedNotOutranked(agg *aggregateAccount, tier int, admitted time.Time, name string) int32 {
	var total int32
	for key, claims := range ev.claimsByEnv {
		acct := ev.envelopes[key]
		if acct == nil || !aggregateMember(acct, agg) {
			continue
		}
		for _, cl := range claims {
			if cl.sponsored {
				total += ev.fundedWidth[cl.key]
				continue
			}
			if cl.tier < TierOwner {
				continue
			}
			if claimAtOrAbove(cl, tier, admitted, name) {
				total += ev.fundedWidth[cl.key]
			}
		}
	}
	return total
}

// claimAtOrAbove reports whether an existing claim ranks at or above a
// prospective claim of (tier, admitted, name) — i.e. it is senior and holds
// capacity the prospective claim cannot recall. It mirrors rankLess's
// (tier, admission, name) order, counting an equal key as senior (the run's
// own existing width). An empty prospective name falls back to counting
// every same-time peer as senior, the conservative estimate for callers
// without a run identity.
func claimAtOrAbove(cl *claim, tier int, admitted time.Time, name string) bool {
	if cl.tier != tier {
		return cl.tier < tier
	}
	if !cl.admitted.Equal(admitted) {
		return cl.admitted.Before(admitted)
	}
	return name == "" || cl.name <= name
}

// aggregateMember reports whether the envelope belongs to the aggregate cap.
func aggregateMember(acct *EnvelopeAccount, agg *aggregateAccount) bool {
	for _, member := range acct.aggregates {
		if member == agg {
			return true
		}
	}
	return false
}

// Class returns the derived class of an open lease at Now. Leases outside
// the evaluation (closed before Now) report ok=false.
func (ev *Evaluation) Class(lease *v1.GPULease) (Class, bool) {
	class, ok := ev.classes[LeaseKey(lease)]
	return class, ok
}

// Run returns the run's derived account, or nil.
func (ev *Evaluation) Run(runKey string) *RunAccount {
	return ev.runs[runKey]
}

// Envelope returns the envelope's derived account, or nil.
func (ev *Evaluation) Envelope(key EnvelopeKey) *EnvelopeAccount {
	return ev.envelopes[key]
}

// Envelopes visits every envelope account in deterministic order.
func (ev *Evaluation) Envelopes() []*EnvelopeAccount {
	out := make([]*EnvelopeAccount, 0, len(ev.envelopes))
	for _, acct := range ev.envelopes {
		out = append(out, acct)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Key.Budget != out[j].Key.Budget {
			return out[i].Key.Budget < out[j].Key.Budget
		}
		return out[i].Key.Envelope < out[j].Key.Envelope
	})
	return out
}
