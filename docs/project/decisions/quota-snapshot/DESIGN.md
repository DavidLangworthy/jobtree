# Quota snapshot — schema, invariants, and the fail-closed contract

**Status: draft for critique.** Nothing here is ratified. It supersedes the P5-P8 residue question
R1 ("where does the authority record live?") rather than answering it, because it proposes that the
authority record leaves Kubernetes entirely and the scheduler stops deriving identity at all.

Origin: David, 2026-07-28 — *"I like the way Envoy router works. The routes are just a json document
that the router polls for. How the document is created and maintained is completely outside of the
router."*

## Why this is smaller than it sounds

`pkg/funding.Evaluate` is **already a pure function of a snapshot**: `Input{Budgets, Leases, Runs,
Now, Period, SettlementHorizon, PriorAccrual}`. It watches nothing, reacts to no individual mutation,
and holds no state across calls. It also already rebuilds everything per call — `NewFamilyGraph`,
`deriveOwners`, the envelope index (`evaluate.go:314-340`).

So this is not an engine rewrite. It is a change to **who fills `Input`**, plus deleting
`deriveOwners` in favour of bindings the snapshot states outright.

## What it dissolves

Stated up front because it is the argument for the whole approach. These are findings from the
2026-07-27 formal campaign (`docs/project/formal-verification-results-2026-07-27.md`) and the P5-P8
recommendation:

| Finding | Why it goes away |
|---|---|
| **F2** unrooted assertions change another namespace's binding | there is no cluster-wide aggregation left to poison; an unauthorised Budget is simply absent from the snapshot |
| **F3** interior exemption permits cross-namespace Owned | bindings are stated, not derived, so there is no exemption to abuse |
| **F1** current-snapshot replay rewrites elapsed history | each accrual interval anchors to the snapshot **version** in effect during it |
| **F5** compaction equivalence is not history immutability | history is a series of immutable documents, not a recomputation under current inputs |
| P6 **deletion blindness** (the second unresolved disagreement) | a deleted grant is absent from version N+1 while version N still carries it |

It does **not** dissolve **F4** (subtree conservation): whether a subtree is *running* more than its
ancestor allocation depends on live consumption, which no control plane can see. That stays in the
scheduler — see §Runtime invariants.

---

## 1. The document

One JSON document per version. Whole-document semantics: there is no partial application and no
per-field patch.

```jsonc
{
  "schemaVersion": 1,
  "snapshotVersion": 4412,              // strictly monotonic, assigned by the producer
  "effectiveFrom": "2026-07-28T14:03:11Z",
  "producer": "quota-control-plane/2.1.0",
  "contentHash": "sha256:…",            // over the canonical encoding of everything below
  "provenanceRef": "pg://quota/changesets/8871",  // OPAQUE to the scheduler; blame lives there

  "roots": ["org:root"],                // trusted root principals; nothing self-nominates

  "principals": [
    {
      "owner": "org:ai",                // the principal id; unique within a snapshot
      "boundNamespace": {               // absent => principal exists but binds no namespace
        "name": "tenant-ai",
        "uid": "8f1c…"                  // UID, not name: a recreated namespace is a DIFFERENT one
      },
      "children": ["org:ai:vision", "org:ai:nlp"],   // resolved edges, authoring-direction agnostic
      "envelopes": [
        {
          "name": "h100-q3",
          "flavor": "H100-80GB",
          "selector": {"gpu.jobtree.io/flavor": "H100-80GB"},
          "concurrency": 64,
          "maxGPUHours": 40000,
          "window": {"start": "2026-07-01T00:00:00Z", "end": "2026-10-01T00:00:00Z"},
          "sharing": "Family",
          "lending": {"allow": true, "to": ["org:bio"], "maxConcurrency": 8, "maxGPUHours": 500}
        }
      ]
    }
  ]
}
```

### Notes on specific fields

**`boundNamespace.uid`, not name.** A namespace deleted and recreated with the same name is a
different namespace and must not inherit the old binding. This is Sol's point from the P5 exchange
(`E-sol-responds-to-fable.md:25-28`) and it is cheaper here than in any CRD design.

**`children`, not `parents`.** The snapshot carries **resolved** edges, so it is indifferent to how
the control plane authored them. This is the point David raised: the planned flip from
child-names-parent to parent-names-children becomes a **control-plane concern that never reaches the
scheduler**. The scheduler builds the reverse index at load (§3). Children is chosen over parents only
to match the authoring direction and keep the document readable; the loader materialises both.

**`provenanceRef` is opaque.** The scheduler never parses, follows, or validates it. It exists so an
operator holding a snapshot can find who changed what. Blame lives in Postgres; the scheduler carries
a pointer and no logic.

**No `spec.owner` free string anywhere.** Identity is the `owner` key of a principal the control plane
published. That is the assertion-by-someone-other-than-the-claimant that Ruling 1 requires.

---

## 2. Snapshots are a SERIES, not a single document

**This is the most easily missed constraint in the design.**

Ruling 6 says accrued history is immutable and spec changes are prospective. Under this model an
accrual interval is attributed using the snapshot **in effect during that interval**. So the scheduler
must hold every snapshot version covering the replay window — from the settlement horizon to now — not
merely the current one.

- **Retention floor:** all versions with `effectiveFrom >= SettlementHorizon`, plus the one in effect
  *at* the horizon.
- Versions older than that may be dropped locally; Postgres keeps them forever for audit.
- Settling accrual (P3) advances the horizon and therefore *releases* snapshots — which is the second
  reason P3 matters, alongside making "spent is spent" enforceable.

This is also where **F5** is closed, and the closure is structural rather than careful: a settled
accrual record must be keyed by `(envelopeKey, snapshotVersion)`. Keyed by envelope alone — which is
what `PriorAccrual map[EnvelopeKey]SettledAccrual` does today (`evaluate.go:50`) — a later window
change silently reinterprets a settled fact, which is exactly the rewrite the store exists to prevent.

---

## 3. What the loader does

On receiving a candidate snapshot, before any use:

1. Verify `contentHash` over the canonical encoding.
2. Check `snapshotVersion` is strictly greater than the current one and `effectiveFrom` is
   non-decreasing.
3. Validate every **structural invariant** in §4. Any violation ⇒ **reject the whole document**.
4. Build the derived indices: parent map from `children`, ancestor chains, namespace-UID → owner.
5. Atomically swap. Readers observe the old or the new version, never a mix.

A rejected snapshot is **never partially applied**. The previous good version stays in force and the
rejection is alarmed with the failing invariant named, in Envoy's NACK spirit.

---

## 4. Structural invariants — validated at load, fail-closed

These are properties of the **data**. They are checkable on the document alone, without reference to
runtime state, and each is a hard reject.

- **`INV-SNAP-MONOTONE`** — `snapshotVersion` strictly increases; `effectiveFrom` never moves
  backwards. A producer that rewinds is malfunctioning, and accepting it would let history be rewritten
  by republication.
- **`INV-SNAP-IMMUTABLE`** — a given `snapshotVersion` always has the same `contentHash`. Republishing
  a version with different content is the rewrite this whole design forbids, wearing a version number.
- **`INV-BINDING-INJECTIVE`** — each `boundNamespace.uid` appears on at most one principal, and each
  principal binds at most one namespace. This is P5's injectivity, now a data property the producer
  must satisfy rather than a fail-safe the scheduler derives.
- **`INV-ROOTED`** — every principal is reachable from `roots` by `children` edges. An unrooted
  principal is exactly the squatter case and it is rejected at the door.
- **`INV-ACYCLIC`** — the `children` relation has no cycle, and each principal has at most one parent
  (a tree, not a general DAG). Delegation is a chain of authority; two parents means two authorities
  over one allocation, which conservation cannot express.
- **`INV-REFS-RESOLVE`** — every `children` entry, and every `lending.to` entry, names a principal
  present in the same snapshot. No dangling references.
- **`INV-WINDOW-SANE`** — `window.start < window.end`; `concurrency >= 0`; `maxGPUHours >= 0`.
- **`INV-ENVELOPE-UNIQUE`** — `(owner, envelope.name)` is unique within the snapshot.

### Deliberately NOT invariants

**Over-allocation is legal data.** `sum(children allocations) > parent allocation` must load cleanly.
Owner Ruling 2 makes a director's reduction instantly and legally over-allocating, so rejecting it
would refuse a correct document and block a legitimate action. It is surfaced as status
(`overAllocatedBy`), never as a validation failure.

**A principal with no envelopes is legal** — known identity, no current allocation. That is what makes
the GitOps delete-and-recreate window benign: identity is stated in the snapshot and does not depend
on a Budget object existing at that instant.

---

## 5. Runtime invariants — the scheduler's job, not the snapshot's

These cannot be validated on the document because they depend on live consumption.

- **`INV-SUBTREE-CONSERVE`** — for every principal `P`, flavour `f`, and dimension `d` (instantaneous
  concurrency and windowed GPU-hours, **separately**): funded consumption whose *payer's* lineage
  traverses `P` never exceeds `P`'s allocation for `(f,d)`. Keyed on the **payer's** position, not the
  spender's namespace, so a loan charges the lender's ancestors and each funded unit counts exactly
  once.
- **`INV-ACCRUAL-PREFIX-IMMUTABLE`** — an accrual interval's attribution is fixed by the snapshot
  version in effect during it. Under this design it is true *by construction*; the invariant exists to
  catch an implementation that reaches for the current snapshot instead of the effective one.
- **`INV-OWNED-IS-LOCAL`** — every open Owned lease has `PaidByBudgetNamespace == RunRef.Namespace`.
  Kept from the P5 recommendation as the end-to-end rail: it catches any future road to cross-namespace
  Owned regardless of how identity is sourced.

---

## 6. The fail-closed contract

What happens when the control plane is unreachable, slow, or wrong. Envoy's answer is "keep serving
the last good config"; quota needs that plus a bound, because stale quota admits work against grants
that may have been revoked.

| Situation | Behaviour |
|---|---|
| Snapshot fails validation | Reject whole document. Keep last good. Alarm naming the failed invariant. |
| Control plane unreachable | Keep serving last good. Alarm after `staleWarn`. |
| Last good is older than `staleMax` | **OWNER DECISION — see §7.** |
| No snapshot has *ever* been loaded (cold start) | **Fund nothing.** Every run evaluates Unfunded; nothing is destroyed and nothing is charged. Refusing to guess is the only safe reading of an unknown quota tree. |
| Snapshot loads but a running lease's payer principal is absent | That work becomes Unfunded and coasts, per `quota-semantics.md:27`. It is not closed, not failed, not billed. Identical to any other funding loss. |
| Producer republishes a version with different content | Reject (`INV-SNAP-IMMUTABLE`) and alarm loudly. This is the failure that would silently rewrite history. |

**Cold start deserves emphasis.** "Fund nothing" means a cluster that loses its snapshot store cannot
admit new funded work — deliberately. The alternative, treating an absent snapshot as "no limits", is
the failure mode where an outage silently becomes unlimited quota.

**Local durability:** the last good snapshot series is cached on disk (or a ConfigMap) so a scheduler
restart during a control-plane outage does not degrade to cold start.

---

## 7. Open questions — owner decisions, not designer choices

**Q1. `staleMax` — what happens when the last good snapshot is very old?** Options: (a) keep scheduling
indefinitely, maximising availability and accepting that a revoked grant keeps funding work; (b) stop
admitting *new* funded work after `staleMax` while running work coasts; (c) demote everything to
Unfunded. This is the same shape as the parked R4 pt1b staleness bound — a safety-defining number only
the owner can set. **(b) is the recommendation** but the number is David's.

**Q2. Does the Budget CRD survive at all?** Either it is deleted and quota is authored only in the
control plane, or it remains as a *read-only projection* of the snapshot so `kubectl get budgets` still
works. The projection costs a controller and can drift; deleting it costs operator ergonomics and every
existing runbook.

**Q3. Who produces the snapshot, and is it in-tree?** The design is indifferent, which is the point —
but somebody must build and operate it, and this repo's ethos is self-containment. A minimal in-tree
producer reading a static file would keep air-gapped clusters working and give the schema a reference
implementation.

**Q4. Push or poll?** Polling is simpler and matches "the router polls for it". Streaming (xDS-style)
lowers latency, which quota probably does not need. Poll unless there is a reason.

---

## 8. What stays outside the scheduler, permanently

Authoring and the delegation UX; provenance and blame; validation that a grant was *authorised* by its
grantor; history beyond the retention floor; approval workflow; and the pointer-direction question
entirely. The scheduler consumes resolved edges and never learns how they were written.

That last point is the test David proposed for this design: **if the snapshot is right, flipping from
child-names-parent to parent-names-children changes no scheduler code.** Under §1 it does not — the
document already carries `children`, and the loader materialises the reverse index.
