# Build backlog — PreBind placement fix, then quota v5

Operational handoff for an unattended or watched build session. Point an agent at this file; it is the
task, in order.

## How to use this

Work the phases **in order**. One PR per phase, based on `origin/main`, pushed as you go. Do not batch.
Re-read this file and `AGENTS.md` at the start of each phase — they may have changed under you.

## The rules

**The design is the specification. Do not re-derive it, and do not redesign it.**
`docs/project/decisions/quota-snapshot/DESIGN-v5.md` and Owner Rulings 7–18 in
`docs/project/decisions/p5-p8/OWNER-RULINGS.md` are the spec. They took five drafts, four adversarial
rounds with two vendor-decorrelated critics, and twelve owner rulings. **If you believe the design is
wrong, STOP and say so with evidence — do not fix it yourself.** Silent redesign is what produced the
P8 oscillation this whole exercise exists to end.

**Park owner decisions.** Still parked: **P2** (R4 pt1b informer staleness bound) and **P4** (ROLES
track). **P3** is demoted to a performance question by Ruling 10 — do not treat it as blocking. P5, P6
and P7 are resolved by the design. Anything genuinely new that is a policy question goes to
`DECISIONS-NEEDED.md` and you move on.

**The gate, every phase:** `make verify` green (fmt, vet, generate, antifake, `-race`, **envtest**,
golden, helm) **plus** the 800-seed quiescence/eviction fuzzer for anything touching
engine/plugin/funding, **plus** mutation-verify every fix — revert the load-bearing line, confirm red,
restore. A test that passes with its repair deleted is not a test; that has happened on this branch
before.

**Never `gh pr merge`.** `main` is protected and merging is David's.

**Amendments to binding text are explicit.** `AGENTS.md:176` makes `quota-semantics.md` and the concept
docs binding: *"disagree in writing rather than diverging in code."* DESIGN-v5 §13 lists every sentence
this work contradicts. Amend them in the PR that changes the behaviour, not later.

**Status.** Post progress like a developer giving standup notes — what you did, what it cost, what you
found. Never a heartbeat, never the same line twice.

---

## Phase 0 — the PreBind placement bug (start here)

**Why first:** it is the only *shipped defect* in this backlog rather than a design item, it is
independent of everything else, and it proves the box works end to end before a large refactor bets on
it.

The 2026-07-27 formal campaign classified this **"genuinely new production bug"**
(`docs/project/formal-verification-results-2026-07-27.md`, and see `specs/README.md`):

> PreBind mints `node-a#0..3`; Unreserve then PreBind on node B returns success; **the sole lease
> remains on A**; with the pod Running on B and only A deleted, `AuditLedger` returns repairable
> `lease_dead_node`.

So a lease can name a different node than its pod, and when that node goes away the auditor closes a
**healthy** run's lease. The campaign's diagnosis: *"repository plans cover generic R2 retry/restart
recovery but never require placement equality."*

1. Check whether `TestPreBindCrossNodeRetryLeavesStaleLeaseAndAuditorWouldCloseIt` is committed — the
   results doc says the specimens were in an uncommitted artifact layer. If absent, **write the
   reproduction first** and confirm it fails.
2. Fix it so lease placement and pod placement cannot diverge across a retry. This is the sole
   committer's path (`cmd/scheduler/plugin/`), so be conservative and say what you chose.
3. Mutation-verify, and check the auditor no longer reports `lease_dead_node` for the healthy case.

---

## Phase 1 — delete hours enforcement (DESIGN-v5 §5a, build items 7 and 9)

**Why second, deliberately:** v5's largest claim is *asserted from reading, not from attempting it*.
The design says so in its own text. If this goes badly, that is the design telling us something, and it
is far cheaper to learn now than after the producer and ledger are built on top.

Ruling 10 makes GPU-hours metered, never enforced. `admit` (`pkg/funding/evaluate.go`) has six gates —
three concurrency, three hours. Remove the hours gates and class becomes purely
concurrency-determined.

Remove: `MaxGPUHours` from `BudgetEnvelope`, `AggregateCap` and `LendingPolicy` · the three `admit`
gates (`evaluate.go:125`, `:858`, `:866`) · `nextDepletion` · `SettlementHorizon`/`PriorAccrual`
compaction · `ValidateMaxHoursWindow` (`budget_types.go:282`, `:313-323`) · the accrue clamps
(`:993-997`, `:1024-1027`) · `pkg/funding/admission.go:89-136` · `specs/LedgerCompaction*.tla`.

**Two things that are NOT simple deletions:**

- **The born-opportunistic protection must be reimplemented, not dropped** (build item 9).
  `AvailableWidth` gates on hours at `evaluate.go:1167`, `:1171`, `:1194-1196`, `:1215-1217` to enforce
  `quota-semantics.md:23-26` — *"this prevents admitting work that would be born opportunistic."* The
  concurrency-only equivalent is *"is this run opportunistic the moment it starts because the
  envelope's concurrency is already committed?"* Answerable from concurrency alone. Implement it.
- **`ConsumedGPUHours` / `HoursByClass` are reporting fields.** They stay for now and become ledger
  projections in Phase 6. Do not delete them here; do not let them gate anything.

Amend `quota-semantics.md:41-44`, `:23-26`, and the `ConsumedGPUHours` doctrine at `evaluate.go:104-108`
in this PR.

**Report honestly if the deletion does not go as described.** That is the point of doing it early.

---

## Phase 2 — the preconditions (build items 1 and 2)

Both were **asserted as existing and turned out not to be**, twice. Verify before you build.

**2a. Grantor/grantee RBAC in the chart.** `deploy/helm/gpu-fleet/templates/rbac.yaml` has no `grants`
resource and no lead Role; the controller holds full CRUD on Budgets. Ruling 16's containment property
depends on RBAC that does not exist. Ship it: a grantor Role granting `create/update/delete` on grants
in one's own namespace and `get/list` on budgets — the split that lets a lead sub-divide without
enlarging their own allocation.

**2b. Wire R26 to `Conflicts()`.** `evaluate.go:176-182` says in its own words that nothing consumes
conflict records. Every alarm in the design is currently a no-op, and DESIGN-v5 §4 makes this a
**precondition of quarantine** — a silent quarantine is a silent loss of authority.

---

## Phase 3 — mandatory windows (build item 4)

`INV-WINDOW-REQUIRED`. Today `windowActive` returns true when `Start` and `End` are both nil, so an
envelope with no window is active forever. Requiring both is what makes expiry the default rather than
a convention.

Migration: existing open-ended envelopes need an end date. A half-windowed envelope (`Start` set, `End`
nil) violates the invariant — that is now the whole rule, since the old hours-validation gap died with
`maxGPUHours`.

---

## Phase 4 — lease schema (build item 5)

Leases gain **`paidByPrincipal`** and the mint **`snapshotVersion`**.

`paidByPrincipal` is the conservation join: a lease names its payer by namespace *name* and Budget
*name*, while the snapshot keys `(owner, envelope)` with namespace by **UID**, so without it
`INV-SUBTREE-CONSERVE` is uncomputable. `snapshotVersion` is justified **only** by in-flight mint
verification — not by accrual anchoring, which is gone. Record that reason in the field comment or
someone will delete it for the wrong one.

Batch with the lease-schema outage `R7-tenancy-amendment.md:473-475` already schedules.

---

## Phase 5 — the producer (build item 3)

The in-tree controller: read Budgets and Grants, **validate each transition against the prior accepted
graph**, compile, publish the versioned document, quarantine **guilt-scoped and revision-granular**.

DESIGN-v5 §2, §3, §4 and §7 are the specification. The structural invariants are all validated here.
Note §11: the producer is the whole trust boundary — no document invariant can express *"this changeset
was authored by someone entitled to make it"* — so it needs a **producer-authorization specimen**: a
grant authored by a principal outside its own subtree must not compile in.

---

## Phase 6 — the meter and the ledger (build item 6)

DESIGN-v5 §5. Append-only, **observation only, never revision**. `INV-LEDGER-APPEND-ONLY` enforced by
storage and RBAC, not convention. Deterministic record IDs `(leaseUID, segmentStart,
authorizingVersion)` with idempotent create. Class transitions split a lease, driven by snapshot
`effectiveFrom` and by lease and window boundaries. **A gap is recorded as a gap.** Status fields become
projections. The ledger never gates funding.

There are **no reversing entries and no corrections**. If the meter is wrong, the meter gets fixed and
money is settled downstream — Ruling 9.

---

## Phase 7 — subtree conservation (build item 8)

The largest build. Nothing aggregates descendant Budgets today (`evaluate.go:339` allocates `byName`
fresh inside the per-Budget loop), so a manager's cut propagates nowhere and the excess stays **funded**
— meaning other tenants pay for one manager's over-allocation.

`INV-SUBTREE-CONSERVE`: funded **concurrency** whose *payer's* lineage traverses P never exceeds P's
window-active allocation. Keyed on the payer's lineage, not the spender's namespace, so a loan charges
the lender's ancestors and each funded GPU counts exactly once.

This changes greedy fill from *"against an envelope's concurrency"* to *"against a path of caps"* — an
amendment to `quota-semantics.md:71-81`, **the normative core**. Write the amendment.

---

## Phase 8 — surfaces and specimens (build items 10 and 11)

Ruling 3's surfaces: `overAllocatedBy` per flavour (**absent, not zero**, if conservation is not built)
plus the temporal diagnostic from §2a · `descheduleOdds` with its unit and conditioning *in the field
text* (*per contention event*, per placement group) · `lastDraw`.

Fix the operator message that says *"…and resubmit"* — it contradicts `quota-semantics.md:38-39` in a
tenant-facing string.

Specimen successors, since deleting `deriveOwners` makes the existing counterexamples stop compiling:
producer-authorization, loader-quarantine, concurrency-conservation, ledger-append-only.

---

## Stop conditions

Write a summary and halt if: `make verify` cannot be made green after reasonable effort; the only
remaining work is parked; a step needs a credential you do not have; **you would have to make an owner
decision**; or **you conclude the design is wrong.** That last one is not failure — it is the most
valuable thing you can report, and it is why Phase 1 is early.
