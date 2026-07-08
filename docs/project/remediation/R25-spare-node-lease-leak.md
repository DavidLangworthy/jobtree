# R25 — Deleting a node that hosts only a held spare leaks an immortal spare lease

**Priority:** P0-adjacent (silent budget charge, unreclaimable) · **Design:** complete (Fable) · **Next:** Opus implements with R21/R22, Sonnet verifies
**Land with:** R21/R22 — same `HandleNodeFailure` code path.

## Problem (evidence)

`HandleNodeFailure` (`controllers/run_controller.go:1067-1145`) walks the run's
leases looking for the failed node, but the loop **skips `Role == Spare` leases
before checking whether they name the failed node** (`run_controller.go:1077-1079`).
A node that hosts *only* a held spare (no active lease) therefore never matches:
`handled` stays false and the function returns `"no active lease found on node %s"`.

The caller then **swallows that exact error string** as "nothing was running
there" (`controllers/kube/reconcilers.go:338-340`). Net effect: the spare's lease
stays **open forever, referencing a node that no longer exists**, silently
charging its envelope. No code path can ever reclaim it — the node's delete event
was the only trigger, and it was consumed.

This is distinct from R21 (cordon misread as failure) and R22 (reclaim closes
co-located runs): those are about the swap being too aggressive; this is about
the failure handler being blind to spare-only nodes entirely.

## Invariant

A node ceasing to exist closes **every** lease that names it — active or spare —
with an appropriate reason. No open lease may reference a nonexistent node
(this is also one of the invariants the R26 ledger auditor checks as a backstop).

## Implementation spec (Opus)

- In `HandleNodeFailure`, handle spare-role leases on the failed node: close them
  (reason `"NodeFailure"`); do not attempt a swap for them (a spare has no active
  pod to re-place). If the run's spare policy wants a replacement spare, let the
  normal reconcile re-plan it rather than special-casing here.
- Only return "no active lease found" when *no lease of any role* named the node,
  and stop string-matching that error in `reconcilers.go` — return a typed
  sentinel (`ErrNoLeaseOnNode`) and check with `errors.Is`.

## Verification spec (Sonnet)

1. Unit: run with one active lease on node A and a spare lease on node B; delete
   node B; assert the spare lease closes (`NodeFailure`), the active lease is
   untouched, and the run is not failed.
2. Unit: delete a node with no leases at all; assert clean no-op (typed sentinel,
   no error logged as failure).
3. Regression: existing active-lease node-failure tests unchanged.
