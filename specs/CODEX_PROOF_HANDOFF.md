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

The exact finite universal check and the extra witnesses therefore remain on
TLC. The summary representation, stateful round trip, and representative
seeded-fold steps remain on Apalache/Z3.

## Next Work

1. Revisit a direct Apalache universal proof only after an inliner/encoding
   improvement or on a machine with materially more than 16 GB RAM.
2. Decide whether the new exact TLC target belongs in a path-filtered CI
   workflow once the recovered branch is ready to merge.
3. Review the uncommitted Apalache `_apalache-out` directories in the old
   Codespace before deleting it. They were intentionally not committed.
