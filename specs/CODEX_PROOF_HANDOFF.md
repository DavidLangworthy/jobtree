# Codex Proof Handoff

This handoff preserves proof context recovered from the old `tla-k8s` Codespace
on July 10, 2026.

## Source Session

- Codespace: `tla-k8s-tlc-vqjw6j4qv93xqgv`
- Codex thread/session id: `019f487b-98e6-7b51-8c65-2d643720c8e8`
- Raw rollout path on that Codespace:
  `/home/vscode/.codex/sessions/2026/07/09/rollout-2026-07-09T20-04-47-019f487b-98e6-7b51-8c65-2d643720c8e8.jsonl`

The raw rollout is not committed because this repository is public. To resume
the exact same Codex session while the old Codespace still exists:

```sh
gh codespace ssh -c tla-k8s-tlc-vqjw6j4qv93xqgv
cd /workspaces/jobtree
codex resume 019f487b-98e6-7b51-8c65-2d643720c8e8
```

Do not copy or commit `/home/vscode/.codex/auth.json`.

## Recovered Work

The recovered work adds formal specs and proof rails for node-failure behavior
and ledger-compaction accounting.

Important areas:

- `specs/NodeFailure.tla` and related configs model node-failure, spare-swap,
  reclaim, and ledger/workload plane agreement.
- `specs/LedgerCompaction.tla` models the one-shot settlement theorem for
  `settlementSafe` and `SettleAccrual`.
- `specs/LedgerCompactionStore.tla` models repeated settlement, time advance,
  and persisted summary window movement.
- `specs/LedgerCompactionAccounting.tla` broadens the accounting surface to
  aggregate caps, window identity, lender/class carry-forward, and seeded replay.

The last recovered Codex answer specifically called out these additions:

- New accounting witness states and properties in
  `specs/LedgerCompactionAccounting.tla`.
- Clean TLC witness configs:
  `LedgerCompactionAccountingClassHours.cfg`,
  `LedgerCompactionAccountingLender.cfg`,
  `LedgerCompactionAccountingCompositional.cfg`,
  `LedgerCompactionAccountingRepairedStart.cfg`, and
  `LedgerCompactionAccountingRepairedEnd.cfg`.
- TLC counterexample configs:
  `LedgerCompactionAccountingStaleClassHours.cfg` and
  `LedgerCompactionAccountingStaleLender.cfg`.
- Make targets:
  `ledger-compaction-accounting-witness-check` and
  `ledger-compaction-accounting-witness-counterexamples`.
- Docs in `specs/README.md`.

## Semantic Result To Preserve

Repaired window shifts must preserve stale out-of-window history as
`Unfunded` class-hours. The old session made that explicit with shifted start
and shifted end expected states in `LedgerCompactionAccounting.tla`.

## Verification Recovered And Continued

The old session reported:

- `make ledger-compaction-accounting-witness-check` passed.
- `make ledger-compaction-accounting-witness-counterexamples` failed as
  intended for stale class-hour history and stale lender-hour history.
- The main Apalache rail remained focused on summary representation, stateful
  round trip, representative seeded-fold steps, stale consumed-history witness,
  and stale aggregate-history witness.

The new extra witness checks were deliberately kept on TLC because the session
cap was killing one-shot Apalache runs that were otherwise simple.

Continuation on a 16 GB Codespace reported:

- `make ledger-compaction-accounting-apalache-check` with
  `APALACHE_JVM_ARGS=-Xmx10000m` passed all four accounting obligations.
- Direct `SeededSettlementFold` did not complete under Apalache 0.58.3. A
  10 GB heap exhausted during preprocessing; a 12.5 GB run reached bounded
  checking but then received SIGTERM near the VM limit.
- `LedgerCompactionAccountingSeededFoldUniversal.cfg` completed under TLC:
  `2 states generated`, `1 distinct state found`, `0 states left on queue`,
  with no error.

The accounting guarantee was then strengthened from one-shot states to the
real transition system:

- `AccountingInv` combines `TypeOK`, `SummaryRep`, and `StatefulRoundTrip` over
  the ordinary `Init`/`Next` lifecycle.
- `make ledger-compaction-accounting-stateful-check` completed under TLC with
  `39,168 states generated`, `5,386 distinct states found`, a maximum depth of
  12, and no error.
- The reachable stale-start negative control follows `AdvanceNow`,
  `SettleTo(1)`, and `ShiftWindowStart`, then violates
  `ClassHoursRoundTrip`.
- The reachable stale-end negative control follows two `AdvanceNow` steps,
  `SettleTo(2)`, and `ShiftWindowEnd`, then violates `LenderRoundTrip`.
- `make ledger-compaction-accounting-stateful-apalache-check` completed under
  Apalache 0.58.3 with a 10 GB heap: `NoError` through computation length 2 in
  11 minutes 54 seconds. This is an opt-in large-VM target.
- A path-filtered GitHub Actions workflow runs the exact stateful and witness
  TLC rails, including their negative controls. The large-VM Apalache target
  remains outside hosted CI.
- The shared TLC command now uses a cleaned metadata directory per Make target.
  This prevents fast consecutive checks from mistaking a timestamp-directory
  startup collision for the expected failure of a negative control.

The fixed lease history was then generalized without weakening the lifecycle
invariant:

- `CanonicalLeaseFamily` gives each lease one canonical disabled shape or 24
  enabled shapes: two envelopes, owned/borrowed attribution, and six valid
  bounded start/end intervals. The cross-product contains 625 initial
  histories.
- `GeneralizedAccountingInv` checks `AccountingInv` plus preservation of that
  family over the ordinary lifecycle actions.
- `make ledger-compaction-accounting-generalized-check` completed under TLC in
  2 minutes 42 seconds: `15,637,584 states generated`, `2,252,746 distinct
  states found`, maximum depth 12, zero states left on the queue, and no error.
- The path-filtered accounting workflow runs this exploration as a separate
  parallel job so the fixed-history witness rail stays fast.

Dynamic post-summary admission is also checked:

- `DynamicInit` starts with both canonical lease slots disabled.
- `AdmitLease` may enable either slot with any envelope, owned/borrowed
  attribution, and valid interval, but requires `newStart >= now` and
  `newStart >= horizon`; the persisted prefix is therefore immutable.
- `make ledger-compaction-accounting-dynamic-check` completed under TLC in
  2 minutes 57 seconds: `17,028,864 states generated`, `2,252,746 distinct
  states found`, maximum depth 15, zero states left on the queue, and no error.
  The distinct-state count matches the generalized family; admission adds
  checked edges and deeper construction paths without escaping that family.
- The backdated mutation follows `AdvanceNow`, `SettleTo(1)`, and
  `BackdatedAdmitLease(1, 1, FALSE, 0, 1)`, then violates `SummaryRep`. This
  demonstrates that retroactive admission must invalidate or repair a live
  summary.
- A separate parallel CI job runs the dynamic proof and its mutation.

Dynamic closure and historical immutability are checked as well:

- `CloseLeaseAtNow` changes an admitted open lease from `NoEnd` to `now`. It
  requires `leaseStart < now` and `horizon < now`, so the close remains wholly
  in the uncompacted suffix.
- `make ledger-compaction-accounting-closure-check` completed under TLC in
  2 minutes 56 seconds: `17,872,816 states generated`, `2,252,746 distinct
  states found`, maximum depth 14, zero states left on the queue, and no error.
- The historical-rewrite mutation follows `AdmitOpenLease`, `AdvanceNow`,
  `CloseLeaseAtNow`, `SettleTo(1)`, and `RewriteSettledLeaseEnd(1, 2)`, then
  violates `SummaryRep`. Once a closure is folded into the summary, its end is
  immutable unless the summary is invalidated or repaired.
- A separate parallel CI job runs the closure proof and its mutation.

The exact finite universal check, fixed/generalized/dynamic admission/closure
lifecycles, and extra witnesses therefore remain on TLC. The summary
representation, bounded lifecycle preservation, stateful round trip, and
representative seeded-fold steps also have Apalache/Z3 coverage.

## Next Work

1. Extend the abstraction to a third ranked lease in a separate exploration
   config. Start with two representative leases plus one canonical variable
   lease to measure state growth before attempting the full 25^3 family.
2. Add another aggregate-membership shape after the third-lease rail remains
   tractable.
3. Revisit a direct Apalache universal seeded-fold proof only after an
   inliner/encoding improvement or on a machine with materially more than
   16 GB RAM.
