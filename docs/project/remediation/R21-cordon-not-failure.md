# R21 — A cordon is not a node failure: stop the destructive swap + duplicate execution

**Priority:** P5 (but sharp — data-corrupting) · **Design:** complete (Fable, root-caused) · **Next:** Opus implements, Sonnet verifies
**Depends on:** interacts with R8 (pod-health failure signal) and R2 (swap goes through the plugin).

## Problem + root cause (diagnosed)

`NodeReconciler` treats a node as failed whenever `nodeUsable` returns false, and
`nodeUsable` returns false the moment `node.Spec.Unschedulable` is true
(`controllers/kube/reconcilers.go:348-357`). `kubectl cordon` sets exactly that
bit. So a cordon fires the `unusable` predicate (`reconcilers.go:367-369,379`) →
`HandleNodeFailure` (`run_controller.go:1067`), which:

- closes the still-healthy active lease as `NodeFailure`, closes the spare (`Swap`),
  and emits a swap pod onto the spare node;
- **but cordon evicts nothing** — the original workload pod keeps running on the
  cordoned node.

Result: the original rank **and** the swapped-in rank run simultaneously
(duplicate execution — for training, two processes claiming the same rank corrupts
the job), and a spare is consumed for a node that never failed. A routine drain
operation triggers a destructive emergency swap.

## Design decision

Separate "unschedulable" (a scheduling hint) from "failed" (the node/pod is gone),
and prefer a **pod-liveness** signal over a node-schedulability one.

1. **Cordon must not trigger swap.** Remove `spec.Unschedulable` from the failure
   trigger. A node is *failed* only when it is **deleted** or **NotReady/Unknown
   past a grace window** (kubelet gone), not merely cordoned. Keep cordon's real
   effect — no *new* placement — which the default scheduler + Filter already honor;
   jobtree needs no action on a bare cordon.
2. **Drive the swap off the workload pod, not the node.** The robust signal that a
   rank is actually lost is that its **pod** is gone/NotRunning (ties to R8's pod
   watch). Gate `HandleNodeFailure`'s per-lease swap on "the active pod for this
   lease is missing or Failed," so a swap happens only when the workload actually
   stopped — never while it is still running on a cordoned-but-healthy node.
3. **Graceful drain (optional, better):** treat a cordon as a signal to *plan* a
   migration (checkpoint-and-reschedule per `spec.runtime.checkpoint`) rather than
   an emergency spare swap — but at minimum, (1)+(2) stop the corruption.

## Invariant

A healthy, merely-cordoned node never triggers a spare swap and never produces two
live copies of the same rank. A swap fires only when the workload on the node has
actually stopped (pod gone/Failed) or the node is truly down (deleted/NotReady past
grace).

## Implementation spec (Opus)

- `controllers/kube/reconcilers.go`: split the trigger — `nodeUsable` (schedulability)
  stays for scheduling, but the **failure** predicate keys on delete + NotReady-past-
  grace, not `Unschedulable`. Add the grace timer (reuse `checkpointGrace`/Period).
- `controllers/run_controller.go` `HandleNodeFailure`: before closing/ swapping a
  lease, confirm the lease's active pod is actually gone/Failed; otherwise skip
  (no-op) or route to the drain path.
- Ensure the swap still goes through the plugin (R2) with correct provenance (R5).

## Verification spec (Sonnet)

1. **Cordon is a no-op.** Envtest/live: cordon a node hosting a Running gang;
   assert **no** swap, **no** lease closure, the pod keeps running, the spare is
   untouched. (Pre-R21 this swaps.)
2. **Real failure still swaps.** Delete the node (or NotReady past grace) with the
   pod gone; assert the swap proceeds exactly as before.
3. **No duplicate rank.** Assert there is never a window with two open active leases
   for the same group on two nodes.
4. **Live (`swap-smoke.sh` extension).** Distinguish cordon (no swap) from drain-
   with-pod-eviction (swap) on kind.

## Interactions

- **R8** provides the pod-liveness signal this reuses.
- **R2/R5** — the swap it does perform must go through the plugin with trusted
  provenance.
- **R22** — the reclaim sweep inside the swap is separately buggy; fix together.
