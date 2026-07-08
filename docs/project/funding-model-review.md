# Funding-model review: derived class, ledger trust, and the quota↔capacity gap

**Origin:** David's design challenge (2026-07-08): *"Is putting funding class as
a property on the GPU really the best design? Can't that change during a run?
Can the system trust that all the allocs and frees add up? … Total quota does
not have to equal resource availability — it can be over or under at any time
and at any level. The only point at which they reconcile is the occasional dice
roll that makes conflicting aspirations fit within physical reality."*

Verified against the code by a four-way evidence sweep (funding engine, ledger
lifecycle, quota↔capacity coupling, doc claims). File:line receipts inline.

## 1. Funding class is derived, never stored — the design already agrees

- The Lease CRD (`api/v1/lease_types.go:16-57`) carries **no class field**.
  `quota-semantics.md` Decision 3 is explicit: class is *"a pure, deterministic
  function of (budgets, leases, clock), recomputed by whoever needs it."*
- Class changes during a run **by design** (demote-not-kill): proven with zero
  lease writes by `TestIntegralExhaustionDemotes` (clock alone),
  `TestWindowReopenRefunds` (budget spec alone), and peer-close promotion.
  Demotion order is deterministic: greedy fill by `(tier, admission time, name)`
  (`pkg/funding/funding.go:213-221`), pinned by fuzz property tests
  (`NoOverdraft`, `Conservation`, `Determinism`, `RemovalNeverDemotes`).
- Surfaced class (`Run.Status.Funding`, `Budget.Status.Usage`, metrics) is a
  **write-only cache**. Exhaustive grep: the only non-test readers are two CLI
  display commands, one of which re-derives anyway. Nothing in any control path
  branches on a stored class.

**What IS stored per lease is the payer** (`PaidByBudget`/`PaidByEnvelope`),
plus role and reason — transaction facts, not judgments. Storing the payer is
correct (attribution needs a frozen record; deriving it would make billing
unstable and evaluation a global assignment problem). **Known consequence,
accepted:** the payment source is frozen at mint; re-funding is arithmetic
*within the recorded envelope only*. A run can sit Unfunded while its owner has
headroom in a different envelope — re-pointing would need a re-mint that does
not exist (lease spec is webhook-immutable). Predictable attribution over
solver churn. Documented here so it is a decision, not a surprise.

**Doc defect:** `docs/index.md` frames budget as a gate ("governs what can be
scheduled", "ensuring scheduled work stays inside each budget window"), which
contradicts the real model ("quota is a claim, not a wall"). The concepts docs
still conflate role with class and document a `Fail` closure reason that is
dead enum. → folded into R24.

## 2. The ledger cannot currently be trusted — the arithmetic can

`Evaluate` is a pure, fuzz-tested function; every trust problem is in the
ledger's **writes**. Open holes, all one root ("the physical plane is assumed,
not verified"):

| Leak | Mechanism | Status |
|---|---|---|
| Failed pod charges forever | pod watch fires only on `Succeeded`; `RestartPolicy=Never` | R8 (known) |
| User-deleted pod | `DeleteFunc` returns false (`reconcilers.go:152`); lease stays open; can mask as `"Completed"` | caught by R26 |
| Spare-only node deletion | `HandleNodeFailure` skips `Role==Spare` before node match; error swallowed | **R25 (new)** |
| Scheduler restart mid-gang | gang state memory-only; survivors wedge | R2 pt3 (known) |
| Opportunistic mint | second committer; non-colliding lease names; **soft affinity only** → `Slice.Nodes` may diverge from real bind node | R3 (known; divergence note added) |
| No auditor at all | declared invariants #2/#8 have no checker (`pkg/invariant` doesn't exist) | **R26 (new)** |

What exists is reactive and narrow: startup lease→run replay closes
run-deletion orphans; `Evaluate` defensively classes run-orphaned leases
Unfunded; `closeLease` is idempotent; spec immutability + run-nonce close the
double-mint/ABA holes on the main path. Good walls, no auditor.

**Structural answer: R26**, a runtime ledger auditor enforcing the invariant
(every open lease ↔ live pod on live node; every jobtree pod ↔ open lease)
rather than patching causes. It catches the leaks we haven't found yet.

## 3. Quota and capacity are independent — correctly, but silently

Confirmed: **zero coupling anywhere.** `funding.Input` has no Nodes field;
budget validation never reads inventory; node death never touches a Budget.

This is the right call. Quota is **policy** (aspirations; legitimately
overcommittable; changes at human speed). Capacity is **physics** (changes by
failure). Any "total quota = total capacity" invariant would be false within
minutes of a node failure. The system's real model is financial: liabilities
need not equal assets; **funding classes are the mark-to-market** absorbing
whatever the mismatch is right now. Overcommit → junior claims read Unfunded,
deterministically. Node death → the lease closes immediately (charging stops
the instant capacity vanishes — `HandleNodeFailure` closes first).

**The "dice roll" framing, corrected in two places:**

1. The **financial** books re-mark continuously — a Budget edit fans out to
   every Run (`budgetToRuns`), leases are watched unfiltered, clock drift is
   covered by 30s/1m/5m resyncs. It is **placement** that is frozen between
   commitment instants: pack+cover are jointly checked only inside
   `admission.Feasible()` at Permit; running work is never moved (no rebalance,
   no defrag — confirmed by search). For ML gangs that is a feature: moving a
   gang is a checkpoint event, not a scheduler whim.
2. There is exactly one act of violence and it is literally a dice roll:
   `runLottery` → `RandomPreempt(seed)`, fenced to reservation activation
   against a genuine **physical** deficit after unfunded-reclaim, spare-drop,
   and shrink all fail. Pure budget shortfall never preempts (it routes to
   opportunistic/Unfunded instead) — per `quota-semantics.md:127`.

**The model in one paragraph (docs are missing this — → R24):** Three planes.
*Policy* (budgets) may over- or under-commit freely. *Physics* (nodes/pods)
changes by failure. The *lease ledger* is the only surface where they touch,
and they touch only at commitment instants — admission, swap, lottery. Between
instants the financial plane re-marks continuously against the ledger; the
physical plane is assumed, not verified. R26 removes the "not verified."

## Seams found in passing

- Permit reports **every** rejection as "not fundable" even for pure physical
  `pack.PlanError` failures — misleading pod events for anyone debugging
  overcommit. → folded into R20.
- A Pending run's status cannot distinguish "waiting on quota" from "waiting on
  capacity" (`Status.Funding` nil pre-admission, generic message).
- Capacity **return** has no watch (node recovery/join fires no predicate);
  pending runs poll at `pendingRunResync`=1m. Acceptable; noted.
- `computeUsage`/`planPlacement` exist byte-identical in `pkg/admission` and
  `controllers/run_controller.go` — the P2b cutover that deletes the copies
  hasn't happened; hand-sync hazard until it does.
