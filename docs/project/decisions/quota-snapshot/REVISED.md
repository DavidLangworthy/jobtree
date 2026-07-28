# Ruling 7 and the revised question set

**Owner ruling, 2026-07-28:** *"Let's keep what's already in kubernetes in kubernetes."*

This answers the severability question both critics raised independently, and it takes the option
Fable argued for in its §7. The design splits into two moves that the draft wrongly sold as one:

- **Move 1 — the scheduler consumes a compiled, versioned, validated document** instead of deriving
  identity by scanning raw Budgets. **TAKEN.**
- **Move 2 — authority leaves Kubernetes for an external control plane.** **REJECTED.**

Every dissolution in the draft's table came from move 1. Every new cost came from move 2. So this
ruling keeps the whole benefit and drops the whole bill.

## The architecture that survives

```
  authoring        Budgets (+ grants), in Kubernetes, kubectl + RBAC + GitOps as today
       │
       ▼
  producer         an IN-TREE controller: read → validate → compile → publish
       │
       ▼
  QuotaSnapshot    cluster-scoped, versioned, content-hashed, immutable once published
       │
       ▼
  consumers        run controller and scheduler plugin read the SNAPSHOT, never raw Budgets
```

`deriveOwners` (`evaluate.go:206-272`) is deleted from the engine and becomes the producer's job,
where its output is validated once, atomically, against stated invariants — rather than recomputed
per evaluation from a cluster-wide scan that any Budget anywhere can perturb.

## Questions that are now DEAD

Struck by the ruling, not deferred. Recording them so nobody reopens them by habit:

| Was | Why it is gone |
|---|---|
| **`staleMax`** — what to do when the snapshot is very old | The producer is in-cluster and **fate-shared with etcd**. If etcd is down, nothing schedules anyway. There is no second availability domain to go stale against. This was the largest new question and it simply evaporates. |
| **Does the Budget CRD survive?** | Yes. It is the authoring surface. `kubectl get budgets`, namespaced RBAC, GitOps, and every existing runbook keep working. |
| **Who produces the snapshot; is it in-tree?** | In-tree controller. Air-gap works, CI can generate documents, and the invariants are testable against artifacts this repo can produce. |
| **Push or poll?** | Neither — it is a watch on a cluster-scoped object. Standard controller-runtime. |
| **Cold start / break-glass / bespoke auth / delegation UX rebuild / Postgres schema** | All were consequences of move 2. Gone from the critical path. |

**Postgres is not dead — it is demoted.** Shipping published snapshots to an external store for
audit, blame and long-horizon history is still a good idea and still gets you provenance. It is now
**downstream of the funding path** rather than on it, so an outage there costs you reporting, not
scheduling.

**R1 shrinks rather than resolving.** Grantor-side `Budget.Spec.Grants` versus a separate binding
object is still open — but it is now a question about *authoring ergonomics and RBAC shape*, not
about the engine's identity source, because the compiled snapshot insulates the scheduler from
whichever wins. That drops it from a blocking architectural decision to a normal design choice, and
it is the one the parent→child pointer flip actually touches.

## Questions that SURVIVE, sharpened

These are independent of where authority lives, so the ruling does not touch them. Roughly in
dependency order.

### Q1 — Two epochs, not one *(Sol's sharpest finding)*

`snapshotVersion` is **not** the envelope window epoch, and the draft wrongly replaced one with the
other. Sol's counterexample: V10 window W cap 40 → V11 same window, cap 10 → V12 window rotates to
W2. If every version starts a fresh balance, an unrelated publish resets quota; if versions are
summed, rotation never releases the old window. **Both epochs are needed**, and the draft has one.

Also unspecified: half-open interval semantics, tie-breaking when two versions share an
`effectiveFrom` (which the draft's own non-decreasing rule permits), and the actual replay split for
a lease spanning a boundary — `[09:00,12:00)` across a 10:00 boundary must become
`[09:00,10:00)×V10 + [10:00,12:00)×V11`.

### Q2 — Partial settlement, or retention is unbounded

Today a lease settles only if its **entire** accrual ends before the horizon
(`evaluate.go:471-477`), and a retained lease that started before it disables compaction outright
(`:479-519`). So **one long-lived training run pins the horizon and every snapshot version behind
it.** That makes the retention floor unbounded in practice for exactly this project's workload.

Needs: a settled-through watermark or per-lease interval facts, plus an exact-once/no-straddle proof
for partial prefixes. Ruling 6's invariant is over `(leaseUID × payerEnvelopeKey × class)` tuples,
not envelope totals — so an envelope-level aggregate cannot prove which prefix it already contains.

### Q3 — The lease→principal join *(Fable; a lease-schema migration)*

The central conservation predicate is keyed on the **payer's** lineage. But a lease names its payer
as `{PaidByBudgetNamespace, PaidByBudget, PaidByEnvelope}` — namespace *name* plus Budget *object
name* — while the snapshot keys `(owner, envelope)` with namespace by **UID**. There is no join.

Leases must carry **`paidByPrincipal`** and the **mint `snapshotVersion`**. The same field fixes
Fable's two-consumers problem: the run controller and the scheduler plugin poll independently
(`gang.go:795` calls `OwnerOfNamespace`), so they can hold different versions at a mint instant, and
the anti-drift rail dissolves unless the lease records which version authorized it.

Batch with the lease-schema outage `R7:473-475` already schedules.

### Q4 — Whole-document rejection versus per-namespace containment *(now the main values question)*

Today an injectivity conflict fails **the touched namespaces** safe and the rest of the cluster
proceeds (`evaluate.go:253-271`). Under whole-document semantics, one team's collision freezes
**every** tenant's updates at last-good — including revocations the rejected version carried.

This survives the ruling intact: an in-cluster producer that refuses to publish has exactly the same
effect. Options: reject wholly (global consistency, global update outage), or publish with the
offending subtree marked invalid and the rest live (localized fail-safe, partial inconsistency).
**Owner's call**, and the draft chose consistency silently.

### Q5 — `effectiveFrom` in the past

Nothing requires `effectiveFrom` to be at or after the consumer's receipt, so a producer can publish
N+1 dated earlier and retroactively reassign already-attributed intervals — the F1 rewrite in the new
design's clothes. Fix: anchor at `max(effectiveFrom, firstSeen(version))`, or reject an
`effectiveFrom` earlier than the predecessor's receipt. Cheap; must be explicit.

### Q6 — Skipped versions *(Sol)*

N grants, N+1 revokes for five minutes, N+2 restores. If the consumer never saw N+1, N+2 passes every
invariant and the monotonicity check, and the revoked interval is **unrecoverable**. A watch can miss
intermediate versions by design. Needs either gap detection (reject a version whose predecessor was
never observed) or acceptance that attribution is best-effort across gaps — and if the latter, say so
where Ruling 6 is stated.

### Q7 — Does the compiled document fit in etcd?

Fable named this as the one genuine scale ceiling of the in-cluster variant: objects are capped near
1.5 MiB. Needs sizing against a real org tree before it is treated as a non-issue *or* as decisive.
If it does not fit: shard by subtree, or store the document in a ConfigMap series, or compress.
**Measure before designing around it.**

## Unchanged by all of this

**F4 subtree conservation** stays in the scheduler, stays the largest piece of unscheduled work, and
still needs the windowed-hours decision the formal campaign parked. The draft asserted that dimension
as settled; it is not. **The grandparent tier** is still absent (`funding.go:132-150`), so a
four-level chain works for identity and breaks for capacity. **P3** is still a named prerequisite for
Ruling 6, and Q2 above is why. **R26 alarms** are still no-ops.

## Specimen successors — a debt the draft created

Deleting `deriveOwners` does not turn the compiled counterexamples green; it makes them **stop
compiling**, and they get deleted. `TestUnrootedSquatterChangesVictimBinding`,
`TestInteriorExemptionAllowsOwnedChargeAcrossNamespaces`, and both accrual-prefix specimens are the
only executable proof that these defects were ever real.

Successors must be named in the design before it is ratified: loader-reject conformance tests for
every structural invariant, a producer-authorization specimen (a grant authored by a principal
outside its own subtree must not compile into the snapshot), and the versioned-replay differential
(burn 40 hours under version N, publish N+1 delaying `Start`, assert the 40 stay charged).

## Amendments to binding text

Still required even in-cluster, and the draft carried none. At minimum: Decision 3's input tuple
(`quota-semantics.md:64-69`) becomes `(snapshot-series, leases, clock)`; Decision 2's family-edge
authoring and multi-parent model change if `INV-ACYCLIC`'s single-parent rule stands; and the
`ConsumedGPUHours` current-window doctrine (`evaluate.go:104-108`) is scoped by Ruling 6.

## The pointer flip

Now clearly safe, and clearly *last*. It touches the authoring layer and the producer only — the
scheduler reads compiled edges and never learns how they were written. That was the draft's own
acceptance test and it survives the ruling intact.
