# R4 pt2 — ledger compaction (design)

**Priority:** P0 (perf/scale) · **Design:** this doc (sharpens the R4 spec's option a) · **Depends on:** R4 pt1 (metrics, merged #52)

The R4 spec's option (a) — "precompute a per-envelope rolling accrual so
`Evaluate` replays only open leases plus a constant summary" — is the right
shape, but it glosses over the two facts that make this a careful, crown-jewel
change rather than an input filter. This doc pins both down, gives a provably-safe
settlement condition, and phases the implementation so the risky, stateful half is
isolated behind a bit-identical primitive.

## Why "just drop old closed leases" is wrong (two findings)

**Finding 1 — closed leases DO gate funding, through GPU-hour caps.**
`funding.Evaluate` replays every lease's accrual from its start to `Now`; there is
**no rolling `Now-Period` lower clamp** (`eventTimes` starts at the earliest lease
event, `pkg/funding/evaluate.go`). Accrual is bounded per envelope only by its
*explicit* `Start`/`End` (`windowActive`), so a no-window envelope — the common
case — accrues from the first lease ever. That accrual gates real decisions:
- envelope `MaxGPUHours` depletion (`fill.accrue` / `nextDepletion`, evaluate.go),
- aggregate-cap `MaxGPUHours` (`agg.consumed`, `admission.go:AvailableWidth`),
- lending `MaxGPUHours` (`HoursByClass[ClassBorrowed]`, `lentHours`).
For an envelope with **no** hour cap the accrual gates nothing (width-at-`Now`,
computed only from live leases, is what funds) — but it is still **reported**.

**Finding 2 — the golden oracle does NOT cover GPU-hours, so it is a weak rail.**
`goldenFunding` captures "counts and lenders, **not** the wall-clock-derived
GPU-hour floats" (`controllers/golden_test.go:266`); no golden JSON contains
`gpuHours`. So a compaction that changed reported/gating hours would **pass the
golden oracle silently**. Consequences:
- Golden parity is necessary but **insufficient** as pt2's safety rail.
- pt2 MUST add dedicated **accrual round-trip tests**: `Evaluate(full ledger)`
  must equal `Evaluate(settled-summary + retained leases)` on the hour fields
  (`ConsumedGPUHours`, `HoursByClass`, aggregate `consumed`, lender hours) AND the
  width classification — not just the golden snapshot.

Consumers of the hour fields to keep correct: `budget_controller.go` (Budget
status `ConsumedGPUHours`, remaining-hours headroom), `run_controller.go:1975+`
(Run status per-class + per-lender GPU-hours), `admission.go` (aggregate-cap
`AvailableWidth`).

## The settlement-boundary correctness problem

Naively summarizing closed leases is wrong because an open lease's *accrual
classification* over `[start, Now]` depends on the funded set at every instant,
which included closed leases **while they were live**. Drop a closed lease that
temporally overlaps an open lease and you change the open lease's historical fill
(fewer competitors) → different accrued hours / depletion crossings. So a closed
lease is safe to settle only if its accrual **cannot interact** with any retained
lease's accrual.

**Provably-safe settlement condition.** Let `H` (the *settlement horizon*) be a
time such that **`H ≤ Now`**, and such that **every retained (open or unsettled)
lease starts at or after `H`, and every settled lease's `effectiveEnd` ≤ `H`** (no
lease straddles `H`). Then:
- settled and retained leases never co-occur in the fill, so the settled epoch's
  accrual is fully determined independently of anything retained;
- compute that epoch's contribution **once** (a full replay of `[…, H]`) into a
  per-envelope summary; thereafter replay only retained leases, seeding each
  accumulator from the summary.
`Evaluate(full) == Evaluate(summary + retained)` holds exactly under this
condition. The natural choice: `H = min(Now, min(start over all open/pending
leases))`, and settle only closed leases with `effectiveEnd ≤ H`.

**Why `H ≤ Now` is a separate requirement, not a consequence.** "Settled" is
`effectiveEnd ≤ H`, and `effectiveEnd` honors a lease's *scheduled*
`Interval.End` — not only its observed `Status.Ended`. So with `H > Now`, a lease
that is still **live at `Now`** (`Now < End ≤ H`) classes as settled: it is
dropped from the replay (losing its width at `Now`) while `SettleAccrual`
integrates it all the way to `H`, past the clock. The no-straddle test above
inspects only *retained* leases, so it cannot see this. An adversarial review of
pt2a found exactly this hole (16 → 24 GPU-hours, `Owned` width 4 → 0). Both
outputs gate. `H ≤ Now` closes it: a settled lease's end is then `≤ Now`, and
`leaseLiveAt` is half-open, so nothing settled can hold width at `Now`, and its
accrual is complete — which is what licenses integrating it as of `H`. This is
also why `SettleAccrual` **refuses** (returns nil) a horizon past `Now` rather
than clamping: a clamped summary would silently under-charge once `Now` advanced
past `H` and pt2b's persisted store turned compaction on.

**Window-movement caveat.** An envelope `Start`/`End` edit (renewal) "releases"
pre-window hours (`windowActive`). A summary baked under the old window would
over/under-count after a renewal, so settlement must be **keyed to the current
window** and **recomputed (or invalidated) when the envelope spec's window
changes**. Track the window boundary the summary was computed under.

## Summary structure

Per `EnvelopeKey`, the settled epoch contributes:
```
type SettledAccrual struct {
    ConsumedGPUHours   float64            // envelope total (drives MaxGPUHours depletion)
    HoursByClass       map[Class]float64  // owned/shared/borrowed/unfunded (lending caps, reporting)
    AggregateConsumed  map[string]float64 // aggregate-cap name -> hours (aggregate MaxGPUHours)
    LentHours          map[string]float64 // lender owner -> hours (lending caps, reporting)
    WindowStart        *time.Time         // the envelope window this summary is valid under
}
```
Per-**run** hours are deliberately **not** in the summary — see the decision below.

## Phased implementation

- **pt2a — the Evaluate-side primitive (additive, bit-identical when unused). DONE.**
  `funding.Input` gained `SettlementHorizon time.Time` (zero ⇒ disabled, today's
  behavior) and `PriorAccrual map[EnvelopeKey]SettledAccrual`. When the horizon is
  set *and* compaction is provably safe (`settlementSafe`), `Evaluate` drops leases
  with `effectiveEnd ≤ horizon` from the replay and seeds each envelope's
  `ConsumedGPUHours` / `HoursByClass` from `PriorAccrual`. `SettleAccrual(in,
  horizon)` computes the summary (replays the settled epoch as of the horizon). A
  **round-trip equivalence test** proves `Evaluate(full) == Evaluate(summary +
  retained)` on the gating outputs, including a `MaxGPUHours` cap whose depletion
  the settled hours drive; the golden oracle is unchanged (bit-identical default).
  **Scope guard (decided in implementation):** pt2a seeds only *envelope-level*
  accrual, which covers the envelope and lending `MaxGPUHours` caps
  (`HoursByClass[ClassBorrowed]`). Budgets with **aggregate caps** are NOT compacted
  — `settlementSafe` returns false for them, so they get a correct full replay; the
  per-aggregate summary is completed in pt2b. The **`horizon ≤ Now`** and
  **no-straddle** (every retained lease starts ≥ horizon) invariants are also
  enforced by `settlementSafe`, which falls back to a full replay (ignoring
  `PriorAccrual`) when either is violated; `SettleAccrual` likewise returns nil for
  a horizon past `Now`. No controller wiring yet ⇒ zero production behavior change
  until pt2b turns it on.
- **pt2b — budget-controller settlement + persistence (the stateful half).** The
  budget controller periodically: picks `H = min open/pending lease start`,
  replays `[…, H]` to compute `SettledAccrual`, folds it into a persisted store
  (Budget `status` sub-resource or a dedicated object), advances the horizon, and
  lets `Evaluate` callers pass the store + horizon. Recompute on window change.
  pt2b also **extends the summary to aggregate caps** (per-aggregate consumed
  hours, attributed per envelope) so aggregate-capped budgets — which pt2a leaves
  on the full-replay path — can compact too. Add a kind live-proof (long-lived
  ledger stays bounded; funding decisions unchanged across a settlement). This is
  where the risk lives (a stale/incorrect store drifts funding), so it lands only
  on pt2a's proven primitive.

  **pt2b's caller contract** (both items are safe in pt2a only because nothing
  persists a summary there — a single `Evaluate` call always computes its summary
  under the same budgets it replays):
  1. **Clamp the horizon: `H = min(Now, min open/pending start)`.** With no open
     leases the inner `min` is `+∞`. `settlementSafe`/`SettleAccrual` already refuse
     `H > Now`, so getting this wrong costs compaction, not correctness — but the
     caller should not rely on that backstop.
  2. **Add `WindowStart` to `SettledAccrual` and invalidate on window movement.**
     pt2a's `SettledAccrual` deliberately omits it. A renewal that moves an
     envelope's `Start` forward *releases* pre-window hours in a live replay
     (`TestWindowReopenRefunds`); a summary baked under the old window would keep
     charging them, over-consuming the integral and wrongly demoting. Recompute or
     drop the summary when the envelope spec's window changes.

## Decisions (made per standing instruction; flag for review)

- **Per-run GPU-hour reporting on settlement.** When a run's closed lease is
  settled, its hours roll into the **envelope** summary and leave the run's
  per-class `GPUHours` report. Recommendation (adopted): a Run's reported hours
  reflect its **currently-retained** (open/unsettled) leases — a "current
  consumption" semantic. Rationale: per-run history is report-only (not gating,
  not in the golden), and keeping it would force a per-run summary that grows with
  the run count. If lifetime per-run accounting is required, that is a separate
  reporting store, not a funding-engine concern.
- **Settlement cadence / horizon.** Recommendation: settle lazily in the budget
  controller's existing reconcile, horizon = `min(Now, min open/pending start)`; no
  separate timer. Keeps the store advancing without new machinery. The `min(Now, …)`
  is load-bearing, not cosmetic — see the `H ≤ Now` note above; with no open leases
  the `min` over starts is `+∞`, so the clamp is what keeps the horizon sane.

## Invariant

`Evaluate` cost is O(retained leases + summary size), not O(history); its output
(gating **and** reported hours, and the width classification) is identical to the
full replay within the no-straddle settlement condition; the golden oracle is
unchanged and is **backstopped by** the accrual round-trip test (which the golden
does not cover).
