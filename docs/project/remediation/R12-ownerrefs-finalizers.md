# R12 — OwnerReferences + finalizers for API-native garbage collection

**Priority:** ~~P3~~ → **P1, promoted 2026-07-10** · **Design:** complete (Fable) · **Status:** implemented 2026-07-10 (finalizer + pod/Reservation ownerRefs + orphan-run rule deleted; fake-client tests green). **Remaining:** the real-apiserver envtest verification (§Verification spec items 1–5) runs under `make verify`/CI, and cascade-GC needs a live GC controller.
**Shares work with:** R5 (which already adds the Run OwnerReference to pods).

## Why this is now P1: it retires R27c's `orphan-run` rule instead of hardening it

R27c (`controllers/settle.go`) shipped a production sweep that closes an open lease —
**and deletes its pods** — when the lease's Run is absent from `state.Runs`. That rule,
`orphan-run`, infers a Run's deletion from the *silence* of a `List`. Absence of evidence
is not evidence of absence: if a load ever returns an incomplete `Runs` set, the sweep
destroys a live, funded, multi-day training job, quietly. See assumption **A4** in
[`../tla/spec-brief.md`](../tla/spec-brief.md) §3, which the TLA+ model **cannot check**,
because the world is the model's ground truth rather than one of its actors.

The finalizer below makes that state **unreachable rather than merely rare**, and this is
the whole argument for doing R12 before touching the sweep:

> A finalizer holds the Run object in the API until its leases are closed. `DeletionTimestamp`
> is set, but the Run is still present and still returned by `List`. So "an open lease whose
> Run is absent" cannot occur — there is no window left to guess about.

Note carefully which half does the work. **The ownerReference is not the mechanism**;
§Design decision explicitly refuses to owner-ref a Lease, because cascade-deleting a
funding fact erases accounting history and lets a force-deleted Run escape its charge.
It is the **finalizer** that closes the window. Implementing "the ownerRef half" of R12
would leave `orphan-run` exactly as dangerous as it is today while looking like progress.

Sequence, decided (David, 2026-07-10 — *"move implementing the correct solution ahead of
duct taping something that can't work"*):

1. **✅ Demote `orphan-run` to report-only.** *(Done — see `controllers/settle.go`
   `Sweep.Observed`, and `Bridge.reportSweep`.)* It no longer closes a lease or drops a
   pod; it records the lease in `Sweep.Observed`, which the bridge logs and counts
   (`jobtree_swept_leases_total{rule=orphan-run}`) and never panics on. That restores the
   pre-R27c property that a wrong world causes an *omission* (a leaked lease the sweep
   reports — note `pkg/invariant` does NOT catch it, its projection is keyed on
   `state.Runs`; R26's auditor will) rather than an *action* (work destroyed). `terminal-run`
   keeps acting: it rests on the positive evidence of a Run object whose phase says `Failed`.
2. **Land R12.** *(Finalizer done — `controllers/kube/reconcilers.go`
   `FundingClosureFinalizer`; the `RunReconciler` installs it on a live Run and, on
   deletion, closes the leases via `cleanupDeletedRun` under `WithWorld` and only THEN
   removes the finalizer, so the accounting is closed before the object can vanish even
   under `--force`. The `For()` watch predicate was widened to fire when a Run enters
   deletion, or the finalizer would strand it Terminating. Fake-client tests pin the
   lifecycle; the real-apiserver + `--force` cases are the envtest gate, verification
   items 1–3. **Still owed: the pod/Reservation ownerRefs (§Implementation spec).**)*
   The finalizer closes leases with reason `RunDeleted` before the Run goes.
3. **✅ Delete the `orphan-run` rule entirely.** *(Done — `controllers/settle.go` now
   has only the terminal-run rule; `Sweep.Observed` and the `orphan-run` reason are
   gone. `TestADeletingRunIsStillInTheLoadedWorldSoNoOrphanArises` is the licence: while
   a Run is deleted, the finalizer holds it in the load, so `SettleLeases` finds a Run
   present and its lease is never an orphan. A genuinely deleted run's leases are closed
   by `cleanupDeletedRun` on the Lease→Run watch, on positive evidence.)* Do not leave it
   in "just in case": a rule that cannot fire is a rule nobody maintains, and it will be
   the one that fires.

Do **not** harden `orphan-run` with a second read, a two-observation quorum, or a
consistency check on the load. Those were considered and are duct tape on a rule whose
premise is about to become impossible. (If R12 slips, revisit — a direct `Get` by name
fails differently than a `List` does, and that is real, if partial, independence.)

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
- **R27c** (`controllers/settle.go`): the finalizer makes the `orphan-run` sweep rule's
  premise unreachable. Delete the rule when this lands — see the P1 promotion note above.
  `cleanupDeletedRun` becomes authoritative rather than best-effort, which also retires
  the cloned-obligation smell (playbook class 7) of having two closers for a deleted run.
- **R26** (ledger auditor): the auditor is the *observer* of two-plane disagreement; the
  finalizer is what makes one of the disagreements impossible. Build the finalizer first
  and the auditor has one less legal-but-alarming state to special-case.

## Verification the sweep interaction needs (added 2026-07-10)

5. **Envtest — the `orphan-run` premise is unreachable.** With the finalizer installed,
   drive a Run deletion (graceful and `--force --grace-period=0`) while it holds open
   leases, and assert that at no `Bridge.WithWorld` pass does `SettleLeases` observe an
   open lease whose Run is absent. The sweep's `orphan-run` counter must stay at zero.
   That assertion is the licence to delete the rule.
