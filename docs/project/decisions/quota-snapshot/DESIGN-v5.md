# Quota design v5 — identity by snapshot, allocation by concurrency × window

**Status: draft.** Supersedes `DESIGN-v4.md`. Rulings 7–18, both critics' final pass, and a
dead-weight sweep.

v5 is **shorter than v4**. Everything added here was already specified by a critic; everything removed
was machinery that existed to manage an integral Ruling 10 deleted.

## The design in three sentences

**Identity** comes from a compiled, versioned document an in-tree controller publishes from Budgets and
Grants — the scheduler stops deriving owners by scanning. **Allocation** is a concurrency number over a
mandatory window, clamped on every dimension by the grant that authorised it. **GPU-hours** are
observed and recorded, never revised and never enforced.

## What jobtree deliberately does not do

**Fungible compute credits** (Ruling 14). `0 ≤ u(t) ≤ 128` with `∫u(t)dt ≤ 10,000` — *"10,000 GPU-hours
in Q3, burst to 128, tenant chooses when"* — is not representable as concurrency × window. A tenant
wanting credit-style spending agrees burst timing with their manager.

**Revise its own usage record** (new in v5). See §5.

## What the security model promises (Ruling 17)

**Guaranteed:** a Grant can only be written where its author holds namespace access, the grantor is
derived from `metadata.namespace`'s immutable UID rather than any writable field, and **no global
quota is created**.

**Not guaranteed:** that a lead cannot enlarge the allocation *they* control. Nothing models one human
holding two principals; an actor with namespaces A and B writes `B → A` and every check passes.
Organisational, per Ruling 9.

**Not yet shipped:** `rbac.yaml` has no `grants` resource and no lead Role. Build item 1.

## 1. Allocation

An envelope grants **`concurrency` GPUs of `flavor` over `[start, end)`**. Both bounds required
(`INV-WINDOW-REQUIRED` — a half-windowed envelope violates it, which is the whole rule now that
`maxGPUHours` is gone).

No balance exists; idempotent under GitOps; cut now = lower the concurrency and work above the line
demotes and coasts; expiry is the default. Bursts come from opportunistic running, a temporary windowed
grant, or a peer loan.

**The born-opportunistic protection must be reimplemented in concurrency terms**, not dropped —
`AvailableWidth` currently gates on hours (`evaluate.go:1167`, `:1171`, `:1194-1196`, `:1215-1217`) to
enforce `quota-semantics.md:23-26`.

## 2. Authoring — Budgets and Grants

- **`Budget`** — what a principal *holds*, in its own namespace.
- **`Grant`** — what it *gives away*: `(grantee owner, grantee namespace UID, per-flavour caps,
  window)`, in the **grantor's** namespace.

Two objects because **RBAC is per-resource, not per-field**: as a Budget field, anyone who could
delegate could also raise their own concurrency. `Spec.Parents` becomes derived — the pointer flip
lands here, never in the scheduler.

### 2a. Everything clamps — no dimension rejects *(Sol's third compound)*

v4 clamped concurrency and left **time** as a rejection invariant, which recreated the
self-defeating-cut defect on the time axis: grant `[0,100)`, envelope `[0,100)`, grantor accelerates
the end to 50, and now every outcome is wrong — reject and the cut never lands, retain last-good and
the grantee runs to 100, credit-free and it dies at 20 instead of 50, quarantine and the fuse returns.

**The clamp generalises to every grant dimension:**

```
effectiveConcurrency = min(envelope.concurrency, grant.cap)
effectiveWindow      = envelope.window ∩ grant.window
```

- **`INV-EFFECTIVE-WITHIN-GRANT`** replaces `INV-ENVELOPE-WITHIN-GRANT`, which is deleted — Sol:
  *"actively harmful once the necessary temporal clamp is adopted."* **Authored overhang is legal,
  visible over-allocation, not invalid state.**
- Grant expiry clamps authority to zero; it never quarantines.
- Accelerating a grant's end takes effect at the new end.
- A Budget renewal may land before its Grant renewal and is simply effective through the intersection.
- Needs a **temporal diagnostic** — per-flavour `overAllocatedBy` cannot say *"your envelope extends
  12 days past its authority."*

### 2b. Composition

- **`INV-SINGLE-INBOUND-AUTHORITY`, time-indexed.** Non-overlapping `A→P [0,50)` and `B→P [50,100)` is
  a **staged handoff**, not two composing authorities. Counting objects globally instead would
  quarantine the replacement while the incumbent lives and drop P to zero at expiry. **Seamless
  reparenting is supported.**
- **Absent inbound authority means ZERO** — never fallback to the grantee's Budget, never implicit
  root.
- **A Budget exceeding its Grant compiles as clamped over-allocation**, surfaced via
  `overAllocatedBy`, never a validation failure (Ruling 2 at the grant boundary).

### 2c. Grant shape

`INV-NO-SELF-GRANT` (grantor from the namespace UID, never a writable field) · `INV-ACYCLIC`
strengthened to forbid naming the grantor or any ancestor of it · `INV-GRANT-ENDPOINTS-RESOLVE` — a
Grant naming a principal that does not exist is **rejected, not quarantined**, so it cannot spring
alive later.

## 3. The snapshot document

`schemaVersion`, `snapshotVersion`, `effectiveFrom`, `contentHash`, `roots`, `principals[]` with
`status`, `quarantineTrigger`, `boundNamespace.uid`, `children`, per-edge Grant provenance, and
`envelopes[]`. One document — sharding is not built (Ruling 18); revisit above ~2,000 principals.

**`effectiveFrom` is retained, and its reason is corrected.** v4 justified it by accrual anchoring;
that is gone. Its actual consumer is **the meter's interval-split boundary** — when a snapshot changes
a principal's class, the meter seals the old interval at that instant, and two meters using wall-clock
observation time would disagree. Sol: *"persisted effective time is required; wall-clock observation
time is insufficient."* `INV-SNAP-IMMUTABLE` survives for the same reason.

## 4. Quarantine — guilt-scoped, revision-granular

Quarantine attaches to a **write**, never to a principal. v3's window freeze is gone: it was inducible,
letting a principal about to expire collide deliberately and become effectively immortal.

1. Validate each Grant transition against the **prior accepted graph**, not the aggregate document.
2. **The incumbent wins.** Quarantine only the causative new or changed object.
3. **Revision-granular.** For an invalid *update* to an accepted Grant, the **accepted revision stays
   authoritative** while the candidate revision is credit-free — v4's "retain the previously accepted
   binding" and "a quarantined Grant authorises nothing" contradicted each other here.
4. **Deletion and valid shortening take effect immediately.** They are not ambiguity.
5. Independently valid Budget renewals, reductions and revocations still land.
6. Windows run against real time.

Ordinary expiry, renewal ordering and handoff must **never** become quarantine ambiguity — that is what
regenerates the fuse.

**R26 wiring is a precondition**, not a follow-on: a silent quarantine is a silent loss of authority.

## 5. The usage ledger — observation, never revision

**jobtree records what it observed and never revises it. If the meter was wrong, fix the meter; money
is settled downstream.**

v4 introduced *reversing entries* so a metering bug would not be permanent. Both critics showed the
disclosure defence fails — Fable: *"a control the attack satisfies is not a control; it is a receipt"*;
append `-500` then `+600` and history is re-priced while every entry stays visible. v5 does not
constrain reversing entries. **It deletes them**, and with them the reason-code set, the correction
principal, the second ID scheme, and the metered/adjustment/billable split.

That is sound because **nothing in jobtree consumes a corrected number**: hours gate nothing
(Ruling 10), conservation is concurrency-only, and chargeback is downstream of the funding path.
Ruling 9 already placed "the bill should have been different" outside Kubernetes. Sol said as much and
I did not take it: *"corrections live in a separate stream, preferably outside Kubernetes per Ruling 9."*

- **`INV-LEDGER-APPEND-ONLY`** — absolute, no exception clause. Enforced by storage and RBAC.
- **Deterministic record IDs** `(leaseUID, segmentStart, authorizingVersion)` with idempotent create,
  so a crash cannot duplicate.
- **Class transitions split a lease**, driven by snapshot `effectiveFrom` and by lease and window
  boundaries.
- **A gap is recorded as a gap.** A crashed writer leaves a hole; the hole is honest and costs billing
  precision, not correctness.
- **Status fields are projections of the ledger**, never independently computed.
- **The ledger never gates funding.** A ledger outage costs reporting, not scheduling.

### 5a. The hours replay leaves `Evaluate`

`admit` has six gates: three concurrency and three hours (`st.remaining`, lending `MaxGPUHours`,
aggregate `MaxGPUHours`). With the hours gates removed, **class is purely concurrency-determined**, and
the remaining hours consumers — `ConsumedGPUHours`, `HoursByClass` — are *reporting* fields that become
ledger projections.

So the hours replay, `nextDepletion`, and the `SettlementHorizon`/`PriorAccrual` compaction machinery
leave the engine, along with `specs/LedgerCompaction*.tla`. `Evaluate` computes classes; the meter
observes them and writes intervals.

**This is the largest deletion in the design and it is asserted from reading, not from attempting it.**
Verify by doing it, not by agreeing with it.

### 5b. P3 is demoted back

Ruling 6 promoted the settlement store from *"a feature deferral, not correctness"* to a correctness
requirement, because "spent is spent" had no enforcement on the window axis. **That gap was about the
hours integral, which Ruling 10 deleted**, and the append-only ledger now persists the facts P3 was
needed for. P3 returns to being a performance and reporting question.

## 6. Runtime invariants

`INV-SUBTREE-CONSERVE` (concurrency only, keyed on the payer's lineage) · `INV-OWNED-IS-LOCAL` ·
`INV-VERSION-PINNED-AT-MINT` — `paidByPrincipal` **and** `snapshotVersion` on every lease.
`paidByPrincipal` is the conservation join; `snapshotVersion` is now justified **only** by in-flight
mint verification, not by accrual anchoring. Record that, or someone deletes it for the wrong reason.

`INV-ODDS-PUBLISHED-MATCH-DRAWN` (Ruling 3).

## 7. Structural invariants

`INV-SNAP-MONOTONE` · `INV-SNAP-IMMUTABLE` · `INV-PRINCIPAL-UNIQUE` · `INV-BINDING-INJECTIVE` ·
`INV-ROOTED` · `INV-ACYCLIC` · `INV-REFS-RESOLVE` · `INV-WINDOW-REQUIRED` · `INV-ENVELOPE-UNIQUE` ·
`INV-NO-SELF-GRANT` · `INV-SINGLE-INBOUND-AUTHORITY` (time-indexed) · `INV-EFFECTIVE-WITHIN-GRANT` ·
`INV-GRANT-ENDPOINTS-RESOLVE` · `INV-LEDGER-APPEND-ONLY`.

**Not invariants:** over-allocation is legal and clamped; a principal with no envelopes is legal.

## 8. Fail-closed

Quarantine per §4. Cold start funds nothing — safe because Ruling 4 means no funded demand produces no
eviction. A lease whose payer principal vanishes coasts Unfunded.

## 9. Ruling 3's surfaces

`overAllocatedBy` per flavour (**absent, not zero**, until conservation is built) **plus the temporal
diagnostic** from §2a · `descheduleOdds` with unit and conditioning in the field text (*per contention
event*, per placement group) · `lastDraw` (seed + the named arriving claim).

## 10. The human test

**Why is my job Unfunded?** `kubectl describe run` names it: no principal, over allocation, or the
authorising Grant is quarantined **and since when**. **How does a lead grant quota?** `kubectl apply` a
Grant in their own namespace, under the RBAC build item 1 ships. **Why was my work paused?** *"Run X in
namespace Y, fully funded, needed 8 H100s; your subtree is 20 GPUs over."* Every descheduling names an
arriving claim (Ruling 4).

**Still to fix:** the operator message says *"…and resubmit"*, contradicting `quota-semantics.md:38-39`.

## 11. What must be built

1. **Grantor/grantee RBAC in the chart** — precondition (Ruling 17).
2. **Wire R26** — precondition of quarantine.
3. **The producer** — read, validate transitions against the prior graph, compile, publish, quarantine
   guilt-scoped and revision-granular.
4. **`INV-WINDOW-REQUIRED`** + migration for open-ended envelopes.
5. **Lease `paidByPrincipal` + `snapshotVersion`** — batch with the `R7:473-475` outage.
6. **The meter and the ledger** (§5), with a writer conformance specimen.
7. **Remove the hours replay from `Evaluate`** (§5a) — verify by attempting.
8. **Subtree conservation** — the largest build; nothing aggregates descendants today
   (`evaluate.go:339`).
9. **Replace the born-opportunistic protection** in concurrency terms.
10. **Ruling 3's surfaces** and the "resubmit" message.
11. **Specimen successors** — producer-authorization, loader-quarantine, conservation, ledger.

## 12. Open questions

**None.**

## 13. Amendments to binding text

`quota-semantics.md:64-69` (input tuple → `(snapshot, leases, clock)`) · `:71-81` (the normative
ranking core, if conservation makes fill run against a path of caps) · `:23-26` (born-opportunistic,
in concurrency terms) · `:41-44` and `evaluate.go:104-108` (release-on-renewal and the
`ConsumedGPUHours` doctrine — **deleted**: with no enforced integral and an observation ledger there is
no recomputation for them to govern) · Decision 2's family-edge model (multi-parent `Spec.Parents` vs
single time-indexed inbound authority) · `R7-tenancy-amendment.md` §4 identity derivation.

## Dead weight removed in v5

Reversing entries and every dependent · `INV-ENVELOPE-WITHIN-GRANT` · the half-windowed
hours-validation gap (obsolete with `maxGPUHours`) · "principal quarantined" terminology · the hours
replay, `nextDepletion`, `SettlementHorizon`/`PriorAccrual` and `LedgerCompaction*.tla` (§5a) · P3's
promotion to correctness (§5b) · the recovery journal, replaced by recording gaps honestly.

**Kept after verification, against my own proposal to cut them:** `effectiveFrom` and
`INV-SNAP-IMMUTABLE` — I claimed they had no consumer; the meter's split boundary is one, and Sol had
already said persisted effective time is required.
