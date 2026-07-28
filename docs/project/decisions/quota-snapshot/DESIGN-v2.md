# Quota design v2 — identity by snapshot, allocation by concurrency × window

**Status: draft for critique.** Supersedes `DESIGN.md`, which was drafted before Rulings 7–10 and was
wrong on two axes. Nothing here is ratified.

## The design in three sentences

**Identity** comes from a compiled, versioned document that an in-tree controller publishes from
Budgets — the scheduler stops deriving owners by scanning. **Allocation** is a concurrency number over
a mandatory window; nothing accumulates, so nothing can be retroactively rewritten. **GPU-hours** are
metered for chargeback and never enforce anything.

## What changed from v1, and why

| Ruling | Effect on v1 |
|---|---|
| **7** — keep what is in Kubernetes in Kubernetes | v1 bundled two moves and priced one. The scheduler consuming a compiled document (move 1) delivered every benefit; authority leaving Kubernetes (move 2) delivered every cost. Move 2 is **rejected**: the producer is an in-tree controller, fate-shared with etcd. |
| **8** — localize the effect of local problems | Whole-document *rejection* is replaced by **per-subtree quarantine**. One team's collision must not freeze every other tenant's updates. |
| **9** — spent quota cannot be taken back | Any mechanism that reaches backwards is refused without further argument. Settles the `accrue` clamp as a bug, not a choice. |
| **10** — allocation is concurrency × window; hours are metered | Deletes most of v1's hard part. See below. |

Both critics' central objections are honoured rather than answered: Fable's severability finding
(§7 of `CRITIQUE-fable.md`) became Ruling 7, and Sol's two-epoch finding (§2 of `CRITIQUE-sol.md`)
is **dissolved** by Ruling 10 rather than solved — with no enforced integral there is no window epoch
to reconcile against the snapshot version.

## 1. Allocation

An envelope grants **`concurrency` GPUs of `flavor`, over `[start, end)`**. Both bounds required.

That is the whole model. Consequences worth stating because they are what buys the simplicity:

- **No balance exists.** So "they spent half and I re-granted half — what do they have?" has no
  referent. They have the concurrency the current envelope states.
- **Idempotent.** Re-applying an unchanged manifest changes nothing. A GitOps resync cannot refund.
- **Cut now** = lower the concurrency; effective immediately, work above the line demotes to Unfunded
  and coasts (Ruling 2). **Cut going forward** = the next window carries a smaller number.
- **Expiry is the default.** A grant that nobody renews ends. Requiring windows is what makes this
  true — today `windowActive` returns true when both bounds are nil, so an envelope without a window
  is active forever.

**Bursts** come from three places, none of which is a saved-up balance: opportunistic running
(Unfunded, reclaimed only on demand), a temporary windowed grant from the manager, or a loan from a
peer via `LendingPolicy.MaxConcurrency`.

**GPU-hours** are computed from lease history for chargeback and for the "you are burning a lot"
conversation. They gate nothing. `maxGPUHours` must be removed, deprecated, or explicitly
reinterpreted as a reporting threshold — it may not survive looking enforcing while not enforcing.

## 2. The snapshot document

Published by an in-tree controller. One JSON document per version, whole-document swap.

```jsonc
{
  "schemaVersion": 2,
  "snapshotVersion": 4412,               // strictly monotonic
  "effectiveFrom": "2026-07-28T14:03:11Z",
  "producer": "jobtree-quota-compiler/0.1.0",
  "contentHash": "sha256:…",
  "roots": ["org:root"],                 // trusted roots; nothing self-nominates

  "principals": [
    {
      "owner": "org:ai",
      "status": "valid",                 // or "quarantined" (Ruling 8)
      "quarantineReason": null,
      "boundNamespace": {"name": "tenant-ai", "uid": "8f1c…"},   // UID, not name
      "children": ["org:ai:vision", "org:ai:nlp"],               // resolved edges
      "envelopes": [
        {
          "name": "h100-q3",
          "flavor": "H100-80GB",
          "selector": {"gpu.jobtree.io/flavor": "H100-80GB"},
          "concurrency": 64,
          "window": {"start": "2026-07-01T00:00:00Z", "end": "2026-10-01T00:00:00Z"},
          "sharing": "family",
          "lending": {"allow": true, "to": ["org:bio"], "maxConcurrency": 8}
        }
      ]
    }
  ]
}
```

**No `maxGPUHours`.** The snapshot carries only what the scheduler enforces.

**`children`, not `parents`** — resolved edges, so the planned flip from child-names-parent to
parent-names-children is a producer concern that never reaches the scheduler. That was v1's acceptance
test and it survives.

**Not a series.** v1 required retaining every version back to the settlement horizon so accrual could
anchor to the version in effect during each interval. With no enforced integral there is nothing to
anchor: the scheduler needs the **current** version to decide funding. Older versions are still worth
publishing to an external store for audit, but the scheduler does not depend on them. This deletes
v1's retention floor, its unbounded-memory problem, and its dependence on P3.

## 3. Structural invariants — validated at load

Failure **quarantines the offending subtree** and publishes the rest (Ruling 8). Only a structurally
broken document — bad hash, non-monotonic version, unparseable — is rejected whole, because there is
no subtree to localize to.

- **`INV-SNAP-MONOTONE`** — `snapshotVersion` strictly increases; `effectiveFrom` never moves backwards.
- **`INV-SNAP-IMMUTABLE`** — a given version always has the same `contentHash`.
- **`INV-PRINCIPAL-UNIQUE`** — `owner` is unique within the document. *(v1 asserted this in a comment
  and omitted it from the list; `INV-REFS-RESOLVE` is ill-defined without it.)*
- **`INV-BINDING-INJECTIVE`** — each `boundNamespace.uid` on at most one principal, each principal
  binding at most one namespace.
- **`INV-ROOTED`** — every principal reachable from `roots`.
- **`INV-ACYCLIC`** — no cycle; at most one parent. *(An amendment to today's multi-parent
  `Spec.Parents` DAG — must be listed in §8.)*
- **`INV-REFS-RESOLVE`** — every `children` and `lending.to` entry names a principal present here.
- **`INV-WINDOW-REQUIRED`** — every envelope has `start` and `end`, with `start < end`. **New in v2**
  and the rail that makes allocation bounded.
- **`INV-ENVELOPE-UNIQUE`** — `(owner, envelope.name)` unique.

**Deliberately not invariants:** over-allocation is legal data (Ruling 2); a principal with no
envelopes is legal (known identity, no current allocation — what makes the GitOps window benign).

## 4. Runtime invariants — the scheduler's job

- **`INV-SUBTREE-CONSERVE`** — for every principal `P` and flavour `f`: funded **concurrency** whose
  payer's lineage traverses `P` never exceeds `P`'s window-active allocation for `f`. Keyed on the
  **payer's** lineage, not the spender's namespace, so a loan charges the lender's ancestors and each
  funded GPU counts once. **One dimension only** — v1 asserted a windowed-hours dimension the formal
  campaign had explicitly parked; Ruling 10 removes it rather than deciding it.
- **`INV-OWNED-IS-LOCAL`** — every open Owned lease has `PaidByBudgetNamespace == RunRef.Namespace`.
- **`INV-VERSION-PINNED-AT-MINT`** — every lease records the `snapshotVersion` that authorised it.

## 5. Fail-closed contract

| Situation | Behaviour |
|---|---|
| Subtree fails validation | **Quarantine that subtree**, publish the rest. A quarantined principal holds its **last good** state — it does not go unbound, or a neighbour's authoring error becomes an innocent tenant's funding loss. |
| Document structurally broken | Reject whole; keep last good; alarm. |
| Producer down | Keep serving last good. Fate-shared with etcd, so this is a controller outage, not a second availability domain — no `staleMax` question. |
| No snapshot ever loaded | **Fund nothing.** Everything evaluates Unfunded; nothing is destroyed and nothing is charged. Ruling 4 makes this safe: with no funded demand, `Resolve` returns on non-positive deficit and evicts nothing. |
| Running lease's payer principal absent | Unfunded, coasts (`quota-semantics.md:27`). Not closed, not failed, not billed. |
| Version republished with different content | Reject, alarm loudly. This is the one that would silently rewrite history. |

**Alarms are currently vapour and must not be assumed.** `Conflicts()` has no production consumer
(`evaluate.go:176-182`). Every "alarm" above needs a named emitter and consumer, or this contract has
real validation and imaginary observability. Wiring R26 is a prerequisite, not a follow-on.

## 6. What must be built

1. **The producer** — in-tree controller: read Budgets, validate, compile, publish. Reference
   implementation and the thing that makes every invariant testable in CI.
2. **`INV-WINDOW-REQUIRED`** plus a migration for existing open-ended envelopes.
3. **Lease `paidByPrincipal` + `snapshotVersion`** *(Fable §3)* — the conservation predicate is keyed
   on the payer's lineage, but a lease names its payer by namespace **name** and Budget **name** while
   the snapshot keys `(owner, envelope)` with namespace by **UID**. There is no join without this. It
   also fixes the two-consumers problem: the run controller and the plugin poll independently
   (`gang.go:795`), so without a recorded version they can disagree at a mint instant.
4. **Subtree conservation** — the largest unbuilt piece. Nothing aggregates descendants today
   (`evaluate.go:339`), so a manager's cut propagates nowhere and the excess stays *funded*, meaning
   other tenants pay for it. Enforcing it changes greedy fill from "against an envelope's concurrency"
   to "against a path of caps" — an amendment to `quota-semantics.md:71-81`, the normative core.
5. **Specimen successors** — deleting `deriveOwners` does not turn the compiled counterexamples green,
   it makes them stop compiling. Needed: loader-quarantine conformance tests per invariant, a
   producer-authorization specimen (a grant authored outside the author's subtree must not compile in),
   and a concurrency-conservation specimen (manager 100, two descendants at 60 each).
6. **Wire R26** to `Conflicts()`.
7. **Fix the `accrue` clamp** (`evaluate.go:993-997`) — now a reporting bug under Ruling 9.
8. **`effectiveFrom` sanity** *(Fable §1e)* — anchor at `max(effectiveFrom, firstSeen)` or reject an
   `effectiveFrom` earlier than the predecessor's receipt.

## 7. Sharding (Ruling 11)

The document is **sharded by root subtree**, one object per shard, each independently versioned. This
is a storage decision with one load-bearing constraint: **a whole lineage lives in one shard.**

- **Ancestor walks never cross a shard**, so `INV-SUBTREE-CONSERVE` always evaluates against one
  consistent version. Any other sharding key breaks this and lets conservation decide against
  half-stale data.
- **Sharding and quarantine are the same boundary**, so Ruling 8's localization falls out of the
  layout rather than needing separate machinery.
- **Cross-shard updates are not atomic, deliberately.** Two subtrees may sit at different versions
  momentarily. The only edge spanning subtrees is lending, where a revoked loan taking effect late is
  the same bounded staleness the replay already has (`evaluate.go:797-798`).

Objects cap near 1.5 MiB. The rule is to stay **well clear** rather than compress or optimise toward
it. Size a real org tree, pick the boundary with generous headroom, and record the number.

## 8. Open questions — three left

- **Q1 — Where do grants live?** Grantor-side `Budget.Spec.Grants` versus a separate binding object.
  Authoring ergonomics and RBAC shape only, since the compiled snapshot insulates the scheduler. This
  is the one the pointer flip touches.
- **Q2 — Shard sizing.** A measurement against a real org tree, not a design choice.
- **Q3 — `U`'s default and floor.** Ruling 13 settled that `U` is settable **cluster** policy, not
  tenant-declared. What remains is the shipped default and the enforced floor.

**Closed since the first v2 draft:** `maxGPUHours` is deleted outright, not deprecated (Ruling 11 —
`AGENTS.md:178` forbids side-by-side compatibility paths). Skipped versions are not a case (Ruling 12
— versions are never skipped, only observed late; the scheduler acts on the most recent state it holds
and converges correctly, so no gap detection, no continuity requirement, no `firstSeen` bookkeeping).

## 9. Amendments to binding text

v1 carried none, which is the process defect that produced the P8 oscillation. Required before
ratification:

- `quota-semantics.md:64-69` — Decision 3's input tuple becomes `(snapshot, leases, clock)`.
- `quota-semantics.md:71-81` — the normative ranking core, if subtree conservation makes fill run
  against a path of caps.
- `quota-semantics.md:41-44` and `evaluate.go:104-108` — release-on-renewal and the
  `ConsumedGPUHours` current-window doctrine become **metering** statements, not funding ones.
- Decision 2's family-edge model — multi-parent `Spec.Parents` versus `INV-ACYCLIC`'s single parent.
- `R7-tenancy-amendment.md` §4 identity derivation — replaced by the compiled snapshot.

## What the critics should attack

1. Is Ruling 10 load-bearing in a way that breaks something not yet seen? The claim is that
   concurrency × mandatory window plus opportunistic/temporary-grant/lending covers every real
   allocation pattern. Find one it does not.
2. Does quarantine (Ruling 8) actually contain? A quarantined subtree holds last-good — is there a
   state where that is worse than going unbound?
3. Is "not a series" right? v1 needed retention for accrual anchoring. With hours unenforced, is there
   any remaining scheduler dependency on a past version?
4. The producer is now the whole trust boundary. What must it enforce that no document invariant can
   express, and is that stated anywhere?
