# Design-level specs (TLA+)

Three deliberately tiny TLA+ specifications, per the scoping in
`docs/project/testing-and-simulation.md` ("Design-level model checking — a gate
for the Kubernetes port") and the decided semantics in
`docs/project/quota-semantics.md`. Each is tens of lines of state, close enough
to the design that it cannot drift far from reality.

| Spec | Models | Checked invariants |
| ---- | ------ | ------------------ |
| `ReservationLifecycle` | plan / direct-bind / activate racing for one run | pods materialize at most once; no Pending reservation for a Running run (invariant 8) |
| `BudgetConservation` | reconcilers admitting against one envelope from possibly-stale lease snapshots | the concurrency cap is never overspent |
| `QuotaEvaluation` | the ranked greedy fill from `quota-semantics.md` Decision 3 | no overdraft; an owner's funding never depends on borrower claims (owner recall) |
| `NodeFailure` | the node-failure / spare-swap / reclaim seam | no duplicate rank; exact-slot-only unfunded reclaim; no failed-node leak; no terminal immortal lease; phase is the join; ledger/workload plane agreement |
| `LedgerCompaction` | the R4 pt2 settlement theorem for `pkg/funding/evaluate.go` | if `horizon <= now` and no retained lease starts before the horizon, replaying `summary + retained` matches full replay on funded lease-hours and the funded set at `Now` |
| `LedgerCompactionStore` | the pt2b persisted-settlement store semantics | repeated settlement, time advance, and window movement preserve equivalence to full replay, provided window shifts invalidate or recompute the summary before reuse |
| `LedgerCompactionAccounting` | the broader pt2b accounting surface: aggregate caps, full window identity, lender/class carry-forward, and post-summary lease admission | persisted summary representation and full stateful round-trip equivalence across settlement, time advance, window shifts, summary repair, and safe dynamic admission for both a representative SMT history and 625 canonical two-lease histories; exact finite seeded-fold equivalence |

## Running

```bash
make spec-check            # all three specs must check clean (the port gate)
make spec-counterexamples  # the historical bugs, demonstrated: these MUST fail
```

`spec-check` downloads `tla2tools.jar` into `specs/.cache/` on first use and
needs a Java runtime.

The `NodeFailure` spec is intentionally checked by dedicated targets rather than
the global `spec-check` gate:

```bash
make node-failure-spec-check
make node-failure-spec-counterexamples
```

That seam has its own path-filtered CI workflow because the relevant Go files
change on a different cadence from the rest of the design-level specs.

The primary equivalence rails for `LedgerCompaction`,
`LedgerCompactionStore`, and `LedgerCompactionAccounting` are checked with
Apalache. They are bounded equivalence proofs, not just reachability searches:

```bash
make ledger-compaction-apalache-check
make ledger-compaction-apalache-counterexamples
make ledger-compaction-accounting-apalache-check
make ledger-compaction-accounting-apalache-counterexamples
```

The accounting companion also has a TLC rail for the exact finite universal
seeded-fold property and for cheap representative one-shot properties that do
not need SMT:

```bash
make ledger-compaction-accounting-seeded-fold-universal-check
make ledger-compaction-accounting-witness-check
make ledger-compaction-accounting-witness-counterexamples
```

The stronger accounting rail checks the real `Init`/`Next` lifecycle rather
than an injected one-shot state:

```bash
make ledger-compaction-accounting-stateful-check
make ledger-compaction-accounting-generalized-check
make ledger-compaction-accounting-dynamic-check
make ledger-compaction-accounting-dynamic-counterexample
make ledger-compaction-accounting-stateful-counterexamples
make ledger-compaction-accounting-stateful-apalache-check  # large VM
```

`AccountingInv` conjoins type safety, exact summary representation, and the
full consumed/class/aggregate/lender/current-class round trip. TLC exhausts
the finite model (5,386 distinct states, maximum depth 12). The negative
controls reach stale summaries through ordinary actions, and Apalache reports
no error through two transitions. The symbolic target defaults to a 10 GB
heap and took about 12 minutes on a 16 GB VM, so it is intentionally not part
of hosted CI.

The generalized TLC target replaces the fixed lease assignment with 625
canonical initial histories. Each lease is either disabled, with irrelevant
fields canonicalized, or enabled across both envelopes, owned/borrowed
attribution, and all six valid bounded start/end intervals. TLC exhausts the
resulting lifecycle graph: 15,637,584 states generated, 2,252,746 distinct
states, maximum depth 12, zero states left on the queue, and no invariant
error. It runs as a separate parallel job in the path-filtered workflow.

The dynamic rail starts with two empty lease slots and admits either slot in
any order. A safe admission must start at or after `Now` and therefore at or
after the persisted horizon. TLC generates 17,028,864 states and finds the
same 2,252,746 distinct generalized states, maximum depth 15, and no invariant
error in about three minutes. The backdated mutation follows `AdvanceNow`,
`SettleTo(1)`, then installs a lease covering `[0, 1)` without invalidating the
summary; TLC immediately reports a `SummaryRep` violation.

`LedgerCompaction` is the one-shot theorem for `settlementSafe` and
`SettleAccrual`. `LedgerCompactionStore` is the stronger, stateful theorem for
the persisted settlement store the budget controller will eventually carry.
`LedgerCompactionAccounting` broadens that store to include aggregate-cap,
window-end, and lender/class accounting. The TLC witness rail checks the extra
representative properties that are cheap to evaluate directly:

- class-hour round-trip,
- lender-hour round-trip,
- direct-vs-incremental representative settlement,
- repaired start-shift summaries,
- repaired end-shift summaries.

The first two models intentionally abstract to one envelope, one greedy
capacity dimension, and discrete ticks. `LedgerCompactionAccounting` widens the
surface to two envelopes, one shared aggregate bucket plus one env-local
aggregate bucket, full window identity, and lender/class buckets. Its SMT rail
keeps a fixed two-lease history for tractability; its TLC rail exhausts the
canonical two-lease history family.

One important result of that broader model: the naive "add the newly settled
chunk onto the old summary" law is false once depletion-sensitive accounting is
included. The checked theorem is therefore a seeded replay law. On the current
rail, Apalache discharges the substantive theorem as two representative steps
(`0 -> 1` and `1 -> 2`), while TLC checks the exact finite universally
quantified operator. A direct Apalache retry on a 16 GB VM exhausted a 10 GB
heap during preprocessing; a 12.5 GB run reached bounded checking but then
received SIGTERM near the VM limit. Keep the exact universal check on TLC until
the Apalache encoding or available proof memory changes.

Together the compaction specs reproduce the failure shapes that mattered in the
design review:

- a retained lease straddling the settlement horizon,
- a settlement horizon ahead of `Now`.
- a window shift that keeps a previously-valid summary live.
- a window-end change that leaves aggregate-accounting history stale.

## The counterexample configurations

- `ReservationLifecycleBug.cfg` sets `GuardEnabled = FALSE` — the state of the
  code before R9. TLC finds the double-bind (a run that reserves, direct-binds,
  and then re-materializes on the activation tick) in a handful of states,
  exactly as the design review predicted a model checker would.
- `BudgetConservationRacy.cfg` sets `Serialized = FALSE` — concurrent
  admissions deciding from the same stale snapshot overspend the envelope.
  This is the result that pins the manager's Run reconciler to a single
  admission worker (`MaxConcurrentReconciles = 1`); revisit the spec before
  relaxing that.
- `NodeFailureR21.cfg` widens "node failed" back to a schedulability/signal
  test, and TLC finds duplicate execution of a rank.
- `NodeFailureR22.cfg` coarsens reclaim from exact slot to same node, and TLC
  finds a funded co-tenant reclaimed by a swap.
- `NodeFailureR25.cfg` skips spare-only failed nodes, and TLC finds an open
  lease still naming the failed node after the sweep completes.
- `NodeFailureDeclinedSwap.cfg` leaves the declined spare open, and TLC finds a
  terminal failed run still holding a lease.
- `NodeFailureLastWriter.cfg` writes run phase directly instead of folding the
  worst verdict, and TLC finds an order-dependent final phase.
- `NodeFailureHalfPlane.cfg` fixes only the ledger plane during reclaim/swap,
  and TLC finds the victim still machine-running after its lease was closed.
- `LedgerCompactionStraddle.cfg` forces a retained lease to start before the
  horizon, and Apalache finds that dropping settled competitors changes the
  replay result.
- `LedgerCompactionFutureHorizon.cfg` pushes the horizon ahead of `Now`, and
  Apalache finds a still-live lease incorrectly treated as settled.
- `LedgerCompactionStoreStaleWindow.cfg` keeps the persisted summary valid
  across a window shift, and both Apalache and TLC find the resulting
  over-charged replay.
- `LedgerCompactionAccountingStaleWindow.cfg` reuses a summary across a window
  start shift, and Apalache finds the compacted replay over-charging the
  envelope-consumed history.
- `LedgerCompactionAccountingStaleEnd.cfg` reuses a summary across a window end
  change, and Apalache finds the compacted replay over-charging aggregate
  history.
- `LedgerCompactionAccountingStaleClassHours.cfg` reuses a summary across a
  window start shift, and TLC finds stale owned-vs-unfunded class history.
- `LedgerCompactionAccountingStaleLender.cfg` reuses a summary across a window
  end change, and TLC finds stale lender-hour history.
- `LedgerCompactionAccountingStatefulStaleStart.cfg` reaches the stale class
  history from the ordinary initial state via `AdvanceNow`, `SettleTo(1)`, and
  `ShiftWindowStart`.
- `LedgerCompactionAccountingStatefulStaleEnd.cfg` reaches stale lender
  history from the ordinary initial state via two `AdvanceNow` steps,
  `SettleTo(2)`, and `ShiftWindowEnd`.
- `LedgerCompactionAccountingBackdatedAdmission.cfg` creates an empty summary
  at horizon 1 and then retroactively installs a lease that already ended at
  that horizon; TLC finds that the persisted summary no longer represents the
  settled prefix.

## What is deliberately out of scope

The GPU-hour integral is a second budget dimension filled by the same walk as
concurrency; modeling one dimension covers the walk. Demotion/promotion
message protocols are not modeled because, per Decision 3, they do not exist —
classification is a derived function, so staleness bugs in it are
unrepresentable. The stability property ("equal claims never reshuffle") is
structural: a claim's class depends only on claims ranked above it, and ranks
are `(tier, admission index)`, which mutations never reorder for survivors.
