# R21 — A cordon is not a node failure: stop the destructive swap + duplicate execution

**Priority:** P5 (but sharp — data-corrupting) · **Design:** complete, **amended 2026-07-09** (see the amendment at the foot of this file) · **Status:** landed with R22/R25
**Depends on:** R2 (the swap goes through the plugin). No longer depends on R8 — the amendment supersedes the pod-liveness gate.

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

> **AMENDED 2026-07-09 — see [R21 amendment](#amendment-notready-is-not-a-failure-signal-fencing-is)
> at the foot of this file.** The clause "or NotReady past grace" is **withdrawn**:
> NotReady does not imply the containers stopped, so a grace window of any length can
> still produce two live copies of a rank. A swap now requires a *fencing assertion*.
> Item 2 of the design decision below (gate the swap on pod liveness) is
> **superseded**, not deferred — on an unreachable node the pod's phase is stale by
> construction.

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

---

## Amendment: NotReady is not a failure signal, fencing is

*Recorded 2026-07-09, while implementing R21. The spec's own premise did not survive
contact with Kubernetes' semantics. Per the working agreement, the ruling is written
down rather than silently applied.*

### What changed the decision

The spec said a node is failed when it is **deleted** or **NotReady/Unknown past a
grace window (kubelet gone)**. That was implemented, with a 2-minute grace.

The parenthetical is false. NotReady does not mean the kubelet is gone; it means the
**control plane cannot hear it**. A partitioned kubelet keeps its containers running.

| Event | When | What actually happens |
|---|---|---|
| Node marked `Ready=Unknown` | `--node-monitor-grace-period`, default **50s** (40s before 1.32) | status only |
| `not-ready`/`unreachable:NoExecute` taint added | at that moment | scheduling + eviction trigger |
| Taint-eviction deletes the pods | `tolerationSeconds`, default **300s** | an **ordinary graceful** `Delete` |
| The kubelet honours that delete | never, if partitioned | pod stuck `Terminating`; **container still running** |

So the earliest Kubernetes even *attempts* eviction is ~350s after the last
heartbeat, and the attempt does not stop the container. Upstream says so plainly, in
*Force Delete StatefulSet Pods*:

> Force deletions do not wait for confirmation from the kubelet that the Pod has been
> terminated. […] this can lead to the duplication of a still-running Pod, and […]
> will violate the at most one semantics.

A 2-minute grace therefore swapped a rank onto a spare **before Kubernetes had begun
to evict**, while the original was almost certainly alive. That is the same silent
corruption R21 exists to eliminate, reached by a different door. A longer grace would
not have fixed it; no timer can.

### The two signals that are not guesses

Both cause Pod GC to **force-delete** (grace period 0), in
`pkg/controller/podgc/gc_controller.go`:

1. **The Node object is deleted** (`gcOrphaned`). Deletion is itself an assertion —
   by the cloud-controller-manager, meaning the instance is terminated, or by an
   operator.
2. **The node carries `node.kubernetes.io/out-of-service`** (`gcTerminating`). This
   is Kubernetes' sanctioned "I assert this node is dead" channel — non-graceful node
   shutdown, GA in 1.28. It is applied by a human or a fencing agent, never
   automatically.

Both are *fencing assertions* made by something that can actually know. Only a
fencing assertion licenses restarting a rank.

### Ruling

**NotReady, for any duration, does not trigger a swap.** The grace window and the
wall clock are deleted with it.

`nodeFailed(node) bool` is true iff the node is gone from the API or tainted
out-of-service. A NotReady node is logged so an operator can see it and decide.
jobtree takes no destructive action on its own.

This **supersedes** item 2 of the design decision above (gate the swap on the lease's
pod being gone or Failed, pending R8's pod watch). On an unreachable node the pod's
phase is stale by construction — the API cannot tell you the container stopped.
Waiting for a pod-liveness signal that cannot exist would have been a more elaborate
way to be wrong. R8's pod watch remains worth having; it is not what makes the swap
safe.

### What it costs, and why that is the right trade

A genuinely dead on-prem node whose Node object is never deleted and never tainted
will not swap: the run **stalls** instead of losing data. In cloud, the
cloud-controller-manager deletes the Node object automatically when the instance is
terminated, which is the common path and needs no operator.

For a system whose worst outcome is two live copies of one rank, stalling is the
correct failure mode.

### What it buys, beyond correctness

- **No wall clock.** `nodeFailed` is a pure function of the Node object, so the
  engine clock and `time.Now()` never meet.
- **No trust in `LastTransitionTime`.** Kubelets write their own node status; a
  compromised one could backdate the stamp to manufacture an immediate "failure" and
  trigger swaps, or forward-date it to prevent one forever.
- **#36 is closed, not narrowed.** A replayed NotReady event is no longer a failure
  at all, and a replayed delete is re-confirmed against the uncached `APIReader`.

### Peer check

Volcano, Kubeflow's training-operator, and JobSet all restart ranks off pod-phase or
disruption conditions that can fire before Pod GC's forced delete; none wait for node
deletion or for fencing. Ray comes closest — a worker raylet that cannot reach the GCS
kills its own process after 60s, which is self-fencing at the application layer.

Being the only one that stalls rather than corrupts is the right side of this trade.

### Residual gap (owned by R26)

A node **deleted while the manager is down** produces no watch event, so its leases
are not closed. No predicate can fix that; it is the ledger auditor's job.
