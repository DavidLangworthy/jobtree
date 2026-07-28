# Quota design v3 — identity by snapshot, allocation by concurrency × window

**Status: draft.** Supersedes `DESIGN-v2.md`. Incorporates both critics' second pass
(`CRITIQUE-v2-fable.md`, `CRITIQUE-v2-sol.md`) and Owner Rulings 7–14.

## The design in three sentences

**Identity** comes from a compiled, versioned document that an in-tree controller publishes from
Budgets — the scheduler stops deriving owners by scanning. **Allocation** is a concurrency number over
a mandatory window; nothing accumulates, so nothing can be retroactively rewritten. **GPU-hours** are
recorded as they accrue, for chargeback only, and enforce nothing.

## What jobtree deliberately does not do (Ruling 14)

Stated first, because v2 claimed an equivalence that is mathematically false and both critics caught it.

jobtree allocates **capability over a period**, not **fungible compute credits**. The pattern it does
not support:

> *"10,000 GPU-hours during Q3, may burst up to 128 GPUs, tenant chooses when to spend them."*

Formally `0 ≤ u(t) ≤ 128` with `∫ u(t) dt ≤ 10,000`. **No concurrency × window rectangle represents
that set** — 128 across Q3 permits far more than 10,000 hours; lowering concurrency destroys the burst;
shortening the window picks the tenant's burst timing for them. And the burst mechanisms do not
substitute: opportunistic work is not an entitlement, lending moves concurrency rather than a time
budget, and repeated temporary grants make the manager an online allocator.

A tenant wanting credit-style spending gets it by agreeing burst timing with their manager. That is
the product boundary. **This section is the answer to any future request for compute credits** — it is
not re-derived, it is pointed at.

## 1. Allocation

An envelope grants **`concurrency` GPUs of `flavor` over `[start, end)`**. Both bounds required.

- **No balance exists**, so "they spent half and I re-granted half" has no referent.
- **Idempotent** — a GitOps resync cannot refund.
- **Cut now** = lower the concurrency, effective immediately, work above the line demotes and coasts
  (Ruling 2). **Cut going forward** = the next window carries a smaller number.
- **Expiry is the default** — a grant nobody renews ends.

**Bursts:** opportunistic running, a temporary windowed grant, or a peer loan
(`LendingPolicy.MaxConcurrency`).

### The admission protection that must be replaced, not just deleted

Ruling 10 undercounted the hours-enforcement surface. Beyond the three gates and `nextDepletion`, the
admission lookahead in `AvailableWidth` gates on hours at `evaluate.go:1167`, `:1171`, `:1194-1196`,
`:1215-1217`, and **those implement the born-opportunistic protection** of `quota-semantics.md:23-26` —
"this prevents admitting work that would be born opportunistic."

Deleting hours removes that protection. Under concurrency-only the equivalent question is *"will this
run be opportunistic the moment it starts because the envelope's concurrency is already committed?"*,
which is answerable from concurrency alone and **must be reimplemented**, not dropped. Also to remove:
the accrue clamps (`:993-997`, `:1024-1027`) and `pkg/funding/admission.go:89-136`.

### A pre-existing validation gap this ruling found

A half-windowed envelope — `Start` set, `End` nil — matches neither the windowless branch
(`budget_types.go:286-290`) nor the `concurrency × window` rail (`:274-285`), so **its hours are
validated against nothing at all**. `INV-WINDOW-REQUIRED` closes it as a side effect; note it in the
migration so it is fixed deliberately rather than incidentally.

## 2. The snapshot document

As v2 (`schemaVersion`, `snapshotVersion`, `effectiveFrom`, `contentHash`, `roots`, `principals[]`
with `status`, `boundNamespace.uid`, `children`, `envelopes[]`) — **without `maxGPUHours`**, and with
these additions from the critiques:

- **`quarantinedSince`** on a principal, alongside `status` and `quarantineReason` — the operator
  surface for "how long has this been stale", and the input to the fuse rule below.
- **`windowFrozenAt`** on a quarantined principal's envelopes — see §3.

## 3. The quarantine fuse — Fable's compound finding, and the fix

Ruling 8 says a quarantined subtree holds **last-good** so a neighbour's authoring error is not an
innocent tenant's funding loss. Ruling 10 makes windows **mandatory**, so grants expire. Together:
a quarantined subtree's window **lapses while it is quarantined**, and it loses funding anyway —
*"every quarantine under Ruling 10 carries a fuse."* Each ruling individually forbids exactly that
outcome. Neither could see it alone.

**Fix: quarantine freezes the window.** While a principal is quarantined, its envelopes do not expire;
`windowFrozenAt` records the instant. The reasoning is that quarantine means *"we cannot currently
determine this subtree's allocation"*, and letting the window advance would be **making a decision
(deny) on data we have just admitted we cannot trust**. Holding last-good has to include holding the
window, or it is not holding last-good.

The residual cost, named: a quarantined subtree can run past its intended expiry. That is bounded by
how long an operator leaves a quarantine unrepaired, and quarantine must therefore be **loud and
escalating** — which is why §6 makes alarm wiring a prerequisite rather than a follow-on. **No
timeout that destroys**: a fuse that eventually denies is the defect this section exists to remove.

## 4. Metering, and the bounded history that survives

v2 claimed the snapshot need not be a series. Sol found two surviving dependencies on past state:

1. **In-flight mints** — a version-pinned Promise or commitment cannot be verified against an
   unrelated current version.
2. **Classed chargeback** — the API reports Owned/Shared/Borrowed/Unfunded GPU-hours and lender hours
   (`api/v1/run_types.go:304`). Those classes depend on the binding, family graph and lending policy
   **in effect at the time**, and today the replay derives them from the *current* graph
   (`evaluate.go:700`) and attributes historical hours by that derived class (`:1008`).

**Resolution: record classed accrual as it happens; do not recompute it.** Accrual becomes
append-only — an interval, a payer principal, a class, a lender — written when it occurs and never
re-derived. Then:

- No snapshot retention is required. The scheduler still needs only the current version.
- **Rulings 6 and 9 become structural rather than aspirational**: a record written once and never
  recomputed cannot be retroactively rewritten by a later spec change. v2 moved the honesty burden onto
  a meter still computed from current spec, which could not satisfy them — Fable's §1c.
- The `accrue` clamp (`:993-997`) disappears with the recomputation rather than needing a separate fix.

For (1), the lease carries its authorising facts (§5), so PreBind validates against what it holds
rather than re-deriving from a current version.

**Alternatives considered and rejected:** retaining referenced snapshots (reintroduces a retention
floor bounded by the longest-running job, and needs *every* version spanning a lease, not just the
mint one, because class changes over a lease's life); and deleting classed chargeback to report raw
GPU-hours only (cheapest, but discards the Owned/Shared/Borrowed distinction operators use).

## 5. Runtime invariants

- **`INV-SUBTREE-CONSERVE`** — for every principal `P` and flavour `f`: funded **concurrency** whose
  payer's lineage traverses `P` never exceeds `P`'s window-active allocation for `f`. Keyed on the
  **payer's** lineage. One dimension only.
- **`INV-OWNED-IS-LOCAL`** — every open Owned lease has `PaidByBudgetNamespace == RunRef.Namespace`.
- **`INV-VERSION-PINNED-AT-MINT`** — every lease records `paidByPrincipal` and the `snapshotVersion`
  that authorised it. Without these the conservation predicate has no join: a lease names its payer by
  namespace *name* and Budget *name*, while the snapshot keys `(owner, envelope)` with namespace by
  **UID**.
- **`INV-ODDS-PUBLISHED-MATCH-DRAWN`** — restored from Ruling 3; see §7.

## 6. Structural invariants and the fail-closed contract

As v2 §3 and §5, with quarantine per Ruling 8 and the window freeze per §3 above.

**Alarms are vapour today and are a PREREQUISITE, not a follow-on.** `Conflicts()` has no production
consumer (`evaluate.go:176-182`). Quarantine is only safe if it is loud: the fuse fix in §3 trades an
expiry for an escalating alarm, so shipping quarantine without R26 wired converts a bounded failure
into a silent indefinite one.

## 7. Ruling 3's surfaces — restored

v2 dropped these without flagging it, which is the failure mode this whole process exists to prevent.
An explicit owner requirement disappeared in a redraft.

- **`overAllocatedBy`** on the principal — how far a subtree's grants exceed its allocation, per
  flavour. Legal state (Ruling 2), surfaced as status, never an error. **Absent, not zero**, until
  subtree conservation is built — an absent field is honest, a zero is a lie.
- **`descheduleOdds`** on the Run, with its **unit and conditioning written into the field**:
  *"probability this placement group is drawn per contention event."* Not "odds of being descheduled",
  which is undefined without a demand model. Today the unit is the **placement group, not the job**
  (`resolver.go:536-552`), so a multi-group run needs per-group odds or a stated product.
- **`lastDraw`** — the seed and the named arriving claim, so the tenant sentence in §8 is generated
  rather than composed by a human at 3am. The seed is already persisted (`resolver.go:599`).

## 8. The human test — v1 and v2 both omitted it

**A tenant asks why their job is Unfunded.** `kubectl describe run` names the cause: no funding
principal, or the subtree is over its allocation and this work is the excess. If the namespace's
principal is quarantined, it says so **and since when**.

**A lead grants a researcher quota.** `kubectl apply` on a Budget in the lead's own namespace, gated
by the namespaced RBAC they already have. This is the test Ruling 7 was chosen to pass, and it is why
the authority record stays in Kubernetes.

**A tenant whose work was paused.** *"Your job was paused because Run X in namespace Y, which is fully
funded, needed 8 H100s, and your subtree is 20 GPUs over its allocation. Nothing was destroyed. It
resumes when capacity frees or your lead's grant is restored."* Every descheduling is attributable to
a **named arriving claim** — Ruling 4 guarantees there is one, because `Resolve` returns on
non-positive deficit (`resolver.go:74-75`).

**What fails this test today and must be fixed:** the operator-facing message still says
*"An administrator must fix the namespace→owner binding and resubmit"* — and "resubmit" contradicts
`quota-semantics.md:38-39` in a tenant-facing string.

## 9. What must be built

1. **The producer** — in-tree controller: read, validate, compile, publish, quarantine per subtree.
2. **`INV-WINDOW-REQUIRED`** plus migration for open-ended envelopes, and the half-windowed gap (§1).
3. **Lease `paidByPrincipal` + `snapshotVersion`** — batch with the lease-schema outage `R7:473-475`.
4. **Append-only classed accrual** (§4) — replaces recomputed metering.
5. **Subtree conservation** — the largest piece. Nothing aggregates descendants today
   (`evaluate.go:339`), so a manager's cut propagates nowhere and the excess stays *funded*.
6. **Replace the born-opportunistic admission protection** in concurrency terms (§1).
7. **Wire R26** — prerequisite for quarantine, not a follow-on (§6).
8. **Ruling 3's surfaces** (§7), and the "resubmit" message (§8).
9. **Specimen successors** — deleting `deriveOwners` makes the compiled counterexamples stop
   compiling; needed are loader-quarantine conformance tests, a producer-authorization specimen, and a
   concurrency-conservation specimen.

## 10. Open questions

- **Q1 — Where do grants live?** Grantor-side `Budget.Spec.Grants` versus a separate binding object.
  Authoring ergonomics and RBAC shape only. The pointer flip touches this.
- **Q2 — Shard sizing.** A measurement against a real org tree.
- **Q3 — `U`'s default and floor.** Settable cluster policy per Ruling 13; the shipped default and the
  enforced floor remain.

## 11. The producer is the whole trust boundary

Ruling 7 moved the producer in-cluster, which answered availability and verification but **not**
containment. No document invariant can express *"this changeset was authored by a principal entitled
to change these bindings"* — `INV-ROOTED` checks the document is internally rooted, not that the write
producing it was authorized.

So the producer must enforce, and no snapshot check can substitute: **a change to principal P's
bindings or envelopes may only originate from a writer with authority over P's subtree.** In-cluster
this is expressible — namespaced RBAC on the grantor's Budget — which is precisely the argument that
won Ruling 7. It must be stated as a producer obligation with its own specimen (§9), or F2 is
relocated rather than closed.

## 12. Amendments to binding text

- `quota-semantics.md:64-69` — Decision 3's input tuple becomes `(snapshot, leases, clock)`.
- `quota-semantics.md:71-81` — the normative ranking core, if conservation makes fill run against a
  path of caps.
- `quota-semantics.md:23-26` — born-opportunistic protection, restated in concurrency terms (§1).
- `quota-semantics.md:41-44` and `evaluate.go:104-108` — release-on-renewal and the
  `ConsumedGPUHours` current-window doctrine, both **deleted** rather than rescoped: with append-only
  accrual there is no recomputation for them to govern.
- Decision 2's family-edge model — multi-parent `Spec.Parents` versus `INV-ACYCLIC`'s single parent.
- `R7-tenancy-amendment.md` §4 identity derivation — replaced by the compiled snapshot.
