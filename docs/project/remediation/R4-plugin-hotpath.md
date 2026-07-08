# R4 — Plugin hot path: cached reads + ledger compaction

**Priority:** P0 (perf/scale; lowest of the P0s) · **Design:** complete (Fable) · **Status: SPLIT — pt1 IMPLEMENTED, pt2 pending**
**Depends on:** land **after** R1 (which removes half the cost) and must not reopen R1's overspend window.

> **Split into three (2026-07-08).** The spec's two "composable changes" split
> further once an adversarial review found the caching unsafe as first drafted:
> - **pt1 (DONE): hot-path observability only.** `jobtree_plugin_decide_latency_seconds`
>   (histogram, fundable/unfundable/error) and `jobtree_plugin_evaluate_input_leases`
>   (gauge = ledger size fed to the replay) — measure-before-optimize. Reads stay on
>   the direct, read-your-write client. Unit + `-race`.
> - **pt1b (DEFERRED — caching + snapshot).** The first draft moved `loadWorld`
>   before `m.mu` and backed it with an informer cache. **An adversarial review
>   (workflow) confirmed this can double-fund a gang (critical):** the cross-gang
>   pending fold retires another gang's phantom the instant its `minted[i]` flips,
>   assuming the snapshot already shows that gang's real lease — a **read-your-write**
>   guarantee that (a) taking the snapshot before the lock and (b) an
>   eventually-consistent cache both break, so a gang can be funded against capacity
>   another already holds. `PostBind`'s GC relies on the same assumption. So safe
>   caching first requires making the fold **and** PostBind staleness-robust
>   (fold/skip by whether the real lease is actually in the snapshot, not by the
>   `minted` flag), then a kind live-proof. Sized as its own careful pass; reverted
>   from pt1.
> - **pt2 (pending): ledger compaction** (spec option **a**). Genuinely needed: the
>   accrual replay has **no rolling `Now-Period` lower clamp** (verified in
>   `pkg/funding/evaluate.go` — old leases accrue, bounded only by an envelope's
>   explicit `Start`), so "drop ancient input leases" is **not** golden-safe for the
>   common no-window envelope. pt2 must add a maintained per-envelope accrual
>   summary to `Evaluate` + a budget-controller settlement store, with the golden
>   oracle + a bench as the safety rail. Its own careful pass.

## Problem (evidence)

`gangManager.decide` calls `loadWorld`, which issues **four uncached full-cluster
LISTs** (Runs, Budgets, Leases, Nodes — `cmd/scheduler/plugin/gang.go:198-227`)
while holding `m.mu`, inside the scheduler's serial cycle. Each decide then runs
`admission.Feasible` → `funding.Evaluate`, which replays the **entire lease
ledger**; leases are never deleted, only closed, and `Evaluate` builds an event
per lease endpoint, so cost grows with total cluster history — effectively
O(F²) in the number of leases ever created. On a busy, long-lived scheduler this
is both a latency problem (mutex-serialized, blocking the scheduling cycle) and a
throughput ceiling that contradicts index.md's "keep large fleets busy".

## Root cause

The plugin was built read-through for correctness (direct, non-cached client) and
correctness-first Evaluate (replay everything). Neither was revisited for scale
after the cutover made the plugin the hot path.

## Design decision

Two independent, composable changes:

1. **Back reads with an informer cache, snapshot before locking.** Build the
   plugin's client on a cached reader (controller-runtime cache / SharedInformer)
   for Runs, Budgets, Leases, Nodes. In `decide`, take the cache snapshot
   **before** acquiring `m.mu`, then hold the mutex only for the bookkeeping
   (gang map, payer assignment, pending fold) — not for I/O. The pending-lease
   fold (fixed by R1) is exactly what covers the cache's staleness for the
   decide→mint window, so caching is safe *because* R1 keeps that guard honest.
   Bound acceptable staleness (informer resync) explicitly; see "David's call".
2. **Compact the ledger.** `funding.Evaluate` should not need every historical
   lease. Two options, in preference order:
   - a. **Summarize closed history per envelope.** Precompute a per-envelope
     rolling accrual (GPU-hours already spent) so `Evaluate` replays only *open*
     leases plus a constant summary, making it O(open leases). Requires a small
     accrual store the budget controller maintains.
   - b. **GC fully-settled closed leases.** Delete closed leases whose accrual has
     been folded into budget status and whose window has passed. Simpler but loses
     per-lease audit history (keep an event/metric trail first).
   Recommend (a): it preserves audit history and bounds Evaluate to live state.

**Why not drop the pending fold to save work:** it is the overspend guard; R1
gives it a proper end. Keep it.

## Invariant

A `decide` never holds `m.mu` across network I/O; funding correctness is identical
to the uncached path within the bounded staleness window (the pending fold covers
in-flight commits); `Evaluate` cost is O(open leases), not O(history).

## Implementation spec (Opus)

- `cmd/scheduler/plugin/plugin.go` `New`: construct a cached client
  (controller-runtime `cluster`/`cache`, or client-go informers) for the four
  types; start it and wait for sync before serving.
- `cmd/scheduler/plugin/gang.go`: `loadWorld` reads from the cache; restructure
  `decide` to snapshot-then-lock. Keep the pending fold.
- `pkg/funding` (compaction, option a): add a per-envelope accrual summary input to
  `Evaluate`; have `controllers/budget_controller.go` maintain it. Guard behind the
  existing golden oracle — the classification output must not change, only the
  inputs' shape and cost.
- Metrics: add a decide-latency histogram and an Evaluate-input-size gauge so the
  improvement (and any regression) is observable.

## Verification spec (Sonnet)

1. **Bench.** A benchmark over a synthetic ledger of 10⁴–10⁵ closed leases: assert
   `Evaluate` time is ~flat in *closed*-lease count after compaction (pre-R4 it
   grows).
2. **Golden parity.** `UPDATE_GOLDEN` must show **no diff** — classification is
   unchanged; only cost/inputs change. This is the safety rail for the compaction.
3. **Concurrency.** `-race` with many concurrent decides; assert no mutex is held
   across a cache read (add a test hook or inspect via the latency metric under a
   stalled fake apiserver).
4. **Staleness bound.** Decide gang A against the cache, immediately decide gang B
   before A's lease propagates; assert the pending fold still prevents overspend
   (this is the R1 guarantee, re-checked under caching).

### Decision for David (flagged)

Acceptable informer staleness bound (resync period) for funding reads, and
compaction option (a) summarize vs (b) GC. Recommendation: (a) + a short resync;
the pending fold makes short staleness safe.

## Interactions

- **R1** is a hard prerequisite (its phantom clearing is what makes caching safe
  and removes half the growth).
- Neutral to R2/R3/R5–R7.
