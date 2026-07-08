# R12 — OwnerReferences + finalizers for API-native garbage collection

**Priority:** P3 · **Design:** complete (Fable) · **Next:** Opus implements, Sonnet verifies
**Shares work with:** R5 (which already adds the Run OwnerReference to pods).

## Problem (evidence)

Zero `ownerReferences` and zero finalizers exist anywhere. Emitted pods carry no
owner (`controllers/kube/bridge.go:400-408`); Leases are created free-standing by
the plugin's PreBind; cleanup is hand-rolled and manager-dependent (the controller
closes leases in code paths; nothing GCs orphaned pods/leases if the manager is
down or a Run is force-deleted). This is fragile and non-idiomatic.

## Root cause

Object lifecycle was managed imperatively by the controller rather than via the
apiserver's ownership/GC and finalizer machinery.

## Design decision

Establish an explicit ownership graph and use finalizers for the one cleanup that
must not be skipped (funding closure).

1. **Ownership graph.**
   - **Run owns its Pods** — set `OwnerReference(Run, controller=true)` in
     `buildPod` (this is the same edge R5 needs; do it once). Pods are GC'd when the
     Run is deleted.
   - **Run owns its Reservation** — the controller already creates it; add the
     owner ref so a deleted Run cleans up its Reservation.
   - **Leases: do NOT owner-ref to the Run for cascade delete.** A Lease is a
     funding *fact* that must be *closed* (audited), not silently cascade-deleted
     when the Run vanishes. Instead use a finalizer (below). (This is the subtle
     call — cascade-deleting leases would erase funding history and let a
     force-deleted Run escape accounting.)
2. **Finalizer on Run** (`rq.davidlangworthy.io/funding-closure`): on Run deletion,
   the controller closes the run's open leases (reason `RunDeleted`) and only then
   removes the finalizer, so a deleted Run can never leave open leases charging a
   budget. This is the durable fix for the "force-delete leaks funding" hazard.
3. **Optional finalizer on Lease** to guarantee closure is observed before the
   Lease object is removed by any later GC/compaction (ties to R4).

## Invariant

Deleting a Run deterministically GCs its pods/reservation and *closes* (not
deletes) its leases via a finalizer, so no deletion path — including force-delete —
leaves capacity funded or accounting open. Funding history survives.

## Implementation spec (Opus)

- `controllers/kube/bridge.go`: `OwnerReference` on emitted pods (shared with R5)
  and on the Reservation.
- `controllers/run_controller.go` / reconcilers: add the Run finalizer; on
  `DeletionTimestamp != nil`, run funding-closure then remove the finalizer.
- Decide `blockOwnerDeletion` per edge; keep Leases finalizer-closed, not owned.
- Ensure the plugin's PreBind mint is compatible (leases not owner-ref'd to Run).

## Verification spec (Sonnet)

1. **Envtest — pod GC.** Delete a Run; assert its pods and Reservation are GC'd.
2. **Envtest — lease closure on delete.** Delete a Running Run; assert its open
   leases are *closed* (`RunDeleted`), not left open, before the Run object is gone.
3. **Envtest — force delete.** `--grace-period=0 --force` a Run; assert the
   finalizer still closes leases (accounting cannot be escaped).
4. **Golden.** No funding-class change for live runs.

## Interactions

- **R5** adds the same pod OwnerReference; do it once.
- **R4** compaction must respect the Lease finalizer/closure ordering.
- **R7** ownership anchor (Run) is consistent with the tenancy identity model.
