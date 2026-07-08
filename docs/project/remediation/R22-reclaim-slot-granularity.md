# R22 — Swap reclaim must be GPU-slot-scoped, not whole-node (stop closing innocent co-located runs)

**Priority:** P5 (sharp — cross-run data loss) · **Design:** complete (Fable, root-caused) · **Next:** Opus implements, Sonnet verifies
**Depends on:** fix alongside R21 (same `HandleNodeFailure` swap path).

## Problem + root cause (diagnosed)

Inside `HandleNodeFailure`, before minting the swap, a sweep closes every other
open lease that "overlaps" the spare's nodes
(`controllers/run_controller.go:1114-1126`):

```
if !leasesOverlap(other, spareSet) { continue }
closeLease(other, "ReclaimedBySpare", now)
```

`spareSet` is `buildNodeSet(leaseNodeNames(spareLease))`, and `leasesOverlap`
compares `nodeFromSlot(slot)` — which **deliberately strips the `#ordinal`**
(`run_controller.go:2131-2138,2150-2155`). So overlap is at **whole-node**
granularity and spans **all runs**: any other run holding a lease on a GPU of the
*same node* as the spare — even entirely different GPU slots — is closed as
`ReclaimedBySpare`. A node-failure swap for run A silently terminates run B's
funded, running work that merely shares the node.

## Design decision

The swap targets the spare's **own** slots (the swap pod hard-targets that node and
the spare already holds those exact `node#ordinal` slots). So:

1. **Slot-granularity overlap.** Compare full `node#ordinal` slot identity, not
   `nodeFromSlot`. Only a lease occupying the **exact GPU slots** the swap will use
   is a genuine conflict. In the correct common case (the spare holds its own
   slots, nothing else is on them) the sweep closes **nothing**.
2. **Question cross-run reclaim at all.** The spare's slots are, by construction,
   held by the spare lease for run A; no other lease should legitimately occupy
   them. If one does, that is either a bug or a deliberate oversubscription — and a
   deliberate oversubscription must be arbitrated by the **resolver** (funding-class-
   aware eviction), not a blind `closeLease` that ignores whether the victim is
   higher-funded. So: reclaim only exact-slot conflicts, and route any such reclaim
   through the resolver path (as the normal reclaim/eviction already does per §9),
   not a direct close.
3. Keep same-run cleanup (the failed lease + the spare lease) as-is; the bug is
   only in the *other*-lease sweep.

## Invariant

A node-failure swap closes only leases occupying the exact GPU slots the swap
re-places onto, and any cross-run reclaim goes through the funding-aware resolver.
A run sharing the node on different slots is never touched.

## Implementation spec (Opus)

- `controllers/run_controller.go`: replace `buildNodeSet`/`leasesOverlap`
  (node-level) in the swap sweep with a **slot-set** built from the spare's full
  `Slice.Nodes` (`node#ordinal`) and an overlap test on full slot identity. Leave
  the node-level helpers for their other callers, or add `slotsOverlap`.
- Route any surviving exact-slot cross-run conflict through the resolver
  (`pkg/resolver`) so funding classes are respected, instead of a direct
  `closeLease(..., "ReclaimedBySpare")`.
- Confirm the swap pod's target slots equal the spare's slots (they should).

## Verification spec (Sonnet)

1. **Co-located innocent run untouched.** Envtest: run A with a spare on node N
   slots {0,1}; run B active on node N slots {2,3}; fail A's active node; assert B's
   lease stays **open** (pre-R22 it is closed `ReclaimedBySpare`).
2. **Exact-slot conflict handled correctly.** Contrive a lease on A's exact spare
   slots; assert it is reclaimed via the resolver (funding-aware), not blind-closed,
   and only if lower/equal funded.
3. **Swap still works.** The normal single-run swap (no co-tenant) still closes the
   failed+spare leases and mints the Swap lease.
4. **Golden.** Regenerate the node-failure scenarios; audit the diff is exactly the
   narrowed reclaim.

## Interactions

- **R21** — same `HandleNodeFailure` path; land together.
- **R2/R5** — the swap mint goes through the plugin with trusted provenance.
- Uses **`pkg/resolver`** (the existing funding-aware eviction) rather than a raw
  close.
