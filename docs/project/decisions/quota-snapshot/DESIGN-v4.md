# Quota design v4 — identity by snapshot, allocation by concurrency × window

**Status: draft.** Supersedes `DESIGN-v3.md`. Incorporates both critics' targeted pass
(`CRITIQUE-v3-fable.md`, `CRITIQUE-v3-sol.md`) and Owner Rulings 7–17.

Both critics judged v3's three new mechanisms *"the right shape and each one missing clause from
sound."* v4 adds those clauses. No architecture changed; Rulings 10, 14 and 15 are untouched.

## The design in three sentences

**Identity** comes from a compiled, versioned document that an in-tree controller publishes from
Budgets and Grants — the scheduler stops deriving owners by scanning. **Allocation** is a concurrency
number over a mandatory window, clamped by the grant that authorised it; nothing accumulates, so
nothing can be retroactively rewritten. **GPU-hours** are recorded in an append-only ledger for
chargeback and gate nothing.

## What jobtree deliberately does not do (Ruling 14)

jobtree allocates **capability over a period**, not **fungible compute credits**:

> *"10,000 GPU-hours during Q3, may burst up to 128 GPUs, tenant chooses when to spend them."*

`0 ≤ u(t) ≤ 128` with `∫u(t)dt ≤ 10,000` — **no concurrency × window rectangle represents that set.**
A tenant wanting credit-style spending agrees burst timing with their manager. This section is the
answer to any future request for compute credits; it is pointed at, not re-derived.

## What the security model does and does not promise (Ruling 17)

Stated before the mechanisms, because v3 claimed more than it delivered and both critics caught it.

**Guaranteed.** A `Grant` can only be written where its author holds namespace access, and the grantor
is derived from `metadata.namespace`'s **immutable UID**, never from a writable field. So a principal
cannot forge a grant from a namespace it does not control — the *location-forgery* property — and
**no global quota is created**: what the tree can run is bounded by what the roots allocated.

**Not guaranteed.** That a lead cannot enlarge the allocation *they* control. The system has no notion
of one human or team controlling two principals: an actor holding namespaces A and B writes `B → A`
and every injectivity, rootedness and acyclicity check passes. Preventing it needs admission-time
rules keyed on authenticated `UserInfo` plus an actor-to-principal registry — out of scope. Someone
controlling two principals moving quota between them is an organisational matter in the sense Ruling 9
established.

**And the RBAC is not shipped.** `deploy/helm/gpu-fleet/templates/rbac.yaml` has no `grants` resource
and no lead Role; the controller holds full CRUD on Budgets. Build item 1 ships real grantor/grantee
RBAC, or this section says "aspirational". No third option.

## 1. Allocation

An envelope grants **`concurrency` GPUs of `flavor` over `[start, end)`**. Both bounds required.

- **No balance exists**; **idempotent** (a GitOps resync cannot refund); **cut now** = lower the
  concurrency, work above the line demotes and coasts (Ruling 2); **expiry is the default**.
- **Bursts:** opportunistic running, a temporary windowed grant, or a peer loan.

**The born-opportunistic protection must be replaced, not dropped.** `AvailableWidth` gates on hours at
`evaluate.go:1167`, `:1171`, `:1194-1196`, `:1215-1217`, implementing `quota-semantics.md:23-26`. Under
concurrency-only the equivalent question — *"is this run opportunistic the moment it starts because the
envelope's concurrency is already committed?"* — is answerable from concurrency alone and must be
reimplemented.

**A pre-existing gap to close deliberately:** a half-windowed envelope (`Start` set, `End` nil) matches
neither `budget_types.go:286-290` nor `:274-285`, so its hours are validated against nothing.

## 2. Authoring — Budgets and Grants (Rulings 16, 17)

- **`Budget`** — what a principal *holds*: envelopes with concurrency, flavour, window, sharing,
  lending. In the principal's own namespace.
- **`Grant`** — what a principal *gives away*: `(grantee owner, grantee namespace UID, per-flavour
  caps, window)`. In the **grantor's** namespace, never the grantee's.

Two objects rather than a `Spec.Grants` field because **Kubernetes RBAC is per-resource, not
per-field**: with grants as a Budget field, anyone permitted to delegate could also raise their own
`concurrency`. Split, they are independently grantable. `Spec.Parents` becomes derived from grants —
the pointer flip lands here, never in the scheduler.

### 2a. Grants carry windows *(Fable Q1-6 — the containment claim was false on the time axis)*

A Grant without a duration means a grantee's envelope can outlive the authority that created it, so a
grantor who simply **stops acting revokes nothing**. Grants therefore carry `[start, end)`, and:

- **`INV-ENVELOPE-WITHIN-GRANT`** — a grantee envelope's window must be contained in its authorising
  grant's window. Delegation expires and must be renewed, exactly as allocation does.

### 2b. How caps compose — clamped, never rejected *(Fable Q1-5b × Q2; Sol Q1-4, Q1-5)*

v3 never said how a Grant's caps compose with the Budget its grantee holds. Three rules, and the third
is what stops every reduction from self-defeating:

- **`INV-SINGLE-INBOUND-AUTHORITY`** — every non-root principal has exactly **one** valid inbound
  Grant. Multiple inbound grants do not compose by sum, max, or fallback.
- **Absent inbound authority means ZERO**, never fallback to the grantee's own Budget and never
  implicit root status. Otherwise a principal enlarges itself by making the restrictive Grant
  disappear or fail validation.
- **A Budget exceeding its inbound Grant compiles as CLAMPED over-allocation** —
  `effective = min(envelope.concurrency, grant.cap)` per flavour — surfaced via `overAllocatedBy`,
  **never as a validation failure.** This is Ruling 2 at the grant boundary. Without it, a director's
  cut makes the grantee's Budget invalid, which quarantines it, which (under v3's freeze) preserved
  the pre-cut state — so **the cut would never take effect.**

### 2c. Grant-shape invariants *(Sol Q1-1, Q1-2, Q1-3, Q1-6)*

- **`INV-NO-SELF-GRANT`** — `grantor(G) ≠ grantee(G)`, with the grantor derived from the namespace UID
  rather than any writable field. This was the hole in Ruling 16 as first written.
- **`INV-ACYCLIC`**, strengthened — no Grant may name its grantor **or any ancestor of its grantor**.
- **`INV-GRANT-ENDPOINTS-RESOLVE`** — a Grant naming a principal that does not yet exist is
  **rejected, not quarantined**; a dormant grant must not spring alive when its target is created
  later. This is a *transition* check the producer performs, not a final-document invariant.

## 3. The snapshot document

As v3, plus:

- **`quarantineTrigger`** — the object that caused a quarantine, so guilt is legible (§4).
- **Per-edge provenance** — each principal records the Grant that authorises it, so
  `INV-SINGLE-INBOUND-AUTHORITY` and the clamp are checkable from the document rather than trusted.
- **No `windowFrozenAt`.** Removed — see §4.

## 4. Quarantine, guilt-scoped *(replaces v3's freeze; Sol Q2, Fable Q2)*

v3 froze a quarantined principal's window so it could not expire while quarantined. **That was
exploitable and is withdrawn.** Quarantine is inducible — a neighbour claims your owner, or you claim
theirs — so a principal whose window was about to expire could collide deliberately and become
*effectively immortal*, keeping funded seniority rather than merely coasting. My fix treated the
symptom; the cause is quarantining the **victim** instead of the **write**.

The rule:

1. **Validate each Grant transition against the prior accepted graph** — not the aggregate document.
2. **The incumbent wins.** On a collision, retain the previously accepted binding and quarantine only
   the **causative new or changed object**. Never quarantine both principals because the aggregate is
   ambiguous.
3. **Object-granular** — a Grant is quarantined, not a principal, and `quarantineTrigger` names it.
4. **Credit-free** — a quarantined Grant authorises nothing. Last-good never supplies a revoked edge.
5. **Independently valid changes still land** — Budget renewals, ancestor reductions and revocations
   are accepted while some other Grant is quarantined.
6. **Windows run against real time.** No freeze.

**This does not reintroduce Fable's fuse.** The fuse existed because quarantine blocked the victim's
renewal; under incumbent-adjudication a neighbour cannot block it. A window now expires only when
nobody supplies a valid renewal — which is the mandatory-window rule working, not quarantine
destroying allocation.

**R26 wiring is a PRECONDITION of shipping quarantine, not a follow-on.** `Conflicts()` has no
production consumer (`evaluate.go:176-182`), and a silent quarantine is a silent loss of authority.

## 5. The ledger — append-only, with reversing entries *(Sol Q3, Fable Q3)*

v3's claim that append-only accrual makes Rulings 6 and 9 structural is **conditionally true and false
as specified**: *"written once, never re-derived"* was prose, not a storage invariant, and the shipped
controller role includes `update`, `patch` and `delete`.

- **`INV-LEDGER-APPEND-ONLY`** — enforced by storage and RBAC, not by convention.
- **Corrections are reversing entries, never edits.** This answers the cost I raised — a metering bug
  becoming permanent — without reopening the retroactivity Rulings 6 and 9 forbid: the original entry
  stands, a compensating entry is appended, and the audit trail shows both.
- **Status fields are projections of the ledger**, not independently computed, or they drift and lie.
- **The ledger never gates funding.** A ledger outage costs reporting, not scheduling.
- **Crash policy: deterministic record IDs** `(leaseUID, segmentStart, authorizingVersion)` with
  idempotent create, so replay after a crash cannot duplicate or gap.
- **Class transitions split a lease**, and are driven by snapshot `effectiveFrom` as well as lease and
  window boundaries — a mint-time record cannot capture them.
- **Recovery needs retained history.** v3's *"no snapshot retention is required"* is **withdrawn**: the
  writer needs a journal of class-affecting transitions retained until every meter has crossed them.
  Bounded by meter lag, not by the settlement horizon — far smaller than v1's, but not zero.

## 6. Runtime invariants

`INV-SUBTREE-CONSERVE` (concurrency only, keyed on the payer's lineage) · `INV-OWNED-IS-LOCAL` ·
`INV-VERSION-PINNED-AT-MINT` (`paidByPrincipal` + `snapshotVersion` on every lease) ·
`INV-ODDS-PUBLISHED-MATCH-DRAWN` (Ruling 3).

## 7. Structural invariants

`INV-SNAP-MONOTONE` · `INV-SNAP-IMMUTABLE` · `INV-PRINCIPAL-UNIQUE` · `INV-BINDING-INJECTIVE` ·
`INV-ROOTED` · `INV-ACYCLIC` (strengthened, §2c) · `INV-REFS-RESOLVE` · `INV-WINDOW-REQUIRED` ·
`INV-ENVELOPE-UNIQUE` · `INV-NO-SELF-GRANT` · `INV-SINGLE-INBOUND-AUTHORITY` ·
`INV-ENVELOPE-WITHIN-GRANT` · `INV-GRANT-ENDPOINTS-RESOLVE`.

**Not invariants:** over-allocation is legal and clamped (§2b); a principal with no envelopes is legal.

## 8. Fail-closed contract

As v3, with quarantine per §4. Cold start funds nothing — safe because Ruling 4 means no funded demand
produces no eviction. A running lease whose payer principal vanishes coasts Unfunded.

## 9. Ruling 3's surfaces

`overAllocatedBy` (per flavour; **absent, not zero**, until conservation is built — and now also
carrying the §2b clamp) · `descheduleOdds` with unit and conditioning in the field text
(*per contention event*, per placement group) · `lastDraw` (seed + the named arriving claim).

## 10. The human test

**Why is my job Unfunded?** `kubectl describe run` names it: no funding principal, over allocation, or
principal quarantined **and since when**. **How does a lead grant a researcher quota?** `kubectl apply`
a Grant in the lead's own namespace under RBAC build item 1 ships. **Why was my work paused?**
*"Run X in namespace Y, fully funded, needed 8 H100s; your subtree is 20 GPUs over. Nothing was
destroyed."* Every descheduling names an arriving claim (Ruling 4).

**Still to fix:** the operator message says *"an administrator must fix the binding and resubmit"* —
"resubmit" contradicts `quota-semantics.md:38-39`.

## 11. What must be built

1. **Grantor/grantee RBAC in the chart** — precondition, not a follow-on (Ruling 17).
2. **Wire R26** — precondition of quarantine (§4).
3. **The producer** — in-tree controller: read, validate transitions against the prior graph, compile,
   publish, quarantine guilt-scoped.
4. **`INV-WINDOW-REQUIRED`** + migration, and the half-windowed gap (§1).
5. **Lease `paidByPrincipal` + `snapshotVersion`** — batch with the `R7:473-475` lease-schema outage.
6. **The ledger** (§5) with its crash policy and a writer conformance specimen.
7. **Subtree conservation** — the largest piece; nothing aggregates descendants today
   (`evaluate.go:339`).
8. **Replace the born-opportunistic protection** in concurrency terms (§1).
9. **Ruling 3's surfaces** (§9) and the "resubmit" message (§10).
10. **Specimen successors** — producer-authorization, loader-quarantine, conservation, ledger.

## 12. Open questions — none

Both closed by Ruling 18.

- **Sharding is not built.** One document. At ~600 bytes per principal an object holds ~2,600, and the
  target org is hundreds — an order of magnitude clear. Ruling 11's shard-by-root-subtree stands as the
  strategy *if* it is ever needed; the only thing carried now is that the document keeps no
  cross-subtree data in its header, so a later split stays mechanical. Revisit above ~2,000 principals.
- **`U` = 1 hour default, 5 minute floor**, cluster policy. The floor is derived, not chosen: a gang
  below minimum width is a *waiting* run and re-evaluates every 30s
  (`controllers/kube/reconcilers.go:65`), so 5 minutes is 10 evaluation ticks — clear of
  destroy-at-one-tick, and the 1h default leaves 120 ticks of headroom.

## 13. Amendments to binding text

`quota-semantics.md:64-69` (input tuple → `(snapshot, leases, clock)`) · `:71-81` (the normative
ranking core, if conservation makes fill run against a path of caps) · `:23-26` (born-opportunistic,
restated in concurrency terms) · `:41-44` and `evaluate.go:104-108` (release-on-renewal and the
`ConsumedGPUHours` doctrine — **deleted**, not rescoped: with an append-only ledger there is no
recomputation for them to govern) · Decision 2's family-edge model (multi-parent `Spec.Parents` vs
single inbound authority) · `R7-tenancy-amendment.md` §4 identity derivation.
