# R8 — Workload failure handling: stop the immortal budget-charging zombie

**Priority:** P2 · **Design:** complete (Fable), **retry-policy decision for David** · **Next:** Opus implements, Sonnet verifies
**Depends on:** nothing hard; interacts with R2 (adoption/width) and R21 (node-vs-pod failure signal).

## Problem (evidence)

`RestartPolicy` is forced to `Never` (`controllers/kube/bridge.go:358`) and the pod
watch reacts only to `Succeeded` (`controllers/kube/reconcilers.go:136-154`).
`runGangComplete` requires every active pod Succeeded and notes in its own comment
that a Failed pod "holds the run open rather than completing or failing it"
(`controllers/run_controller.go:448-469`). Repo-wide, `PodFailed` appears only in
the antifake linter and a skipped e2e test. So a single OOM/NCCL/CUDA crash leaves
the run **Running forever**, its leases open and charging GPU-hours until the
envelope drains, and any `follow`-chained stage Waiting forever. The design doc's
own "v1 surfaces it" claim (`plan-follow-and-eta.md:97-99`) is unimplemented.

## Root cause

There is no failure edge in the lifecycle. The system models Pending → Running →
Completed but has no Running → Failed transition driven by pod failure, and no
lease cleanup on that path.

## Design decision

Add the failure edge, and make it policy-driven with a safe, honest default.

1. **Detect.** Extend the pod watch predicate to fire on `PodFailed` (and on pod
   deletion of an active, non-Succeeded member). Map to the owning run.
2. **Decide (per-role policy).** Add a `restartPolicy`/`failurePolicy` to the role
   (or run) with values:
   - `Fail` **(recommended default)** — any active pod failing fails the whole gang
     (fixed-world-size training cannot survive a lost rank). Transition the run to
     `Failed`, set a clear message + Warning event naming the pod and its
     terminated reason, and **close the run's open leases** (reason `WorkloadFailed`)
     so funding stops immediately.
   - `Retry(n, backoff)` — re-emit the failed member up to `n` times (reuse R2's
     top-up re-emit + a backoff), then `Fail`. Track attempts in status.
   - `Ignore` — for embarrassingly-parallel roles where one pod dying is fine;
     completion gate counts Succeeded+Failed as terminal.
3. **Surface.** Always write status.message + an Event at the failure, even under
   Retry/Ignore, so the run's state is never silently wrong. Feed the run-level
   `Failed` into the existing `follow` grace path so downstream stages fail
   honestly instead of hanging.

**Why `Fail` as default:** it matches real distributed training (a lost rank hangs
the job) and it is the safe direction for budget integrity — the run stops
charging. `Retry`/`Ignore` are opt-in for workloads that tolerate them.

**Relationship to JobSet lowering (R9):** the JobSet path
(`pkg/lowering/lowering.go:65-68`) describes exactly this `failurePolicy`. If R9
finishes JobSet lowering, JobSet's own failurePolicy provides this; if R9 takes the
direct-inject route, this controller-side handler is the implementation. Design the
handler so it is a no-op when a JobSet owns the pods (avoid double-handling).

### Decision for David (flagged) — ✅ DECIDED 2026-07-09

Default failure policy (`Fail` vs `Retry(n)`), and whether policy is per-role or
per-run. Recommendation: per-role, default `Fail`, `Retry`/`Ignore` opt-in.

> **David ruled: take the recommendation.** Policy is **per-role**; the default is
> **`Fail`**; `Retry(n, backoff)` and `Ignore` are opt-in. Implemented as phase
> **9A-3** of the amended R9 ([R9-jobset-amendment.md](R9-jobset-amendment.md)) — this
> item is absorbed there, but at its own cost: we build the failure edge ourselves,
> to this spec. Note the provision at :53-54 and :79 ("design the handler so it is a
> no-op when a JobSet owns the pods") is **deleted** — no JobSet will ever own the
> pods.

## Invariant

A run whose active gang cannot make progress (a member terminally Failed under a
non-tolerant policy) reaches a terminal `Failed` state within one reconcile,
closing its leases and unblocking followers. No failed workload charges budget
indefinitely, and no failure is silent.

## Implementation spec (Opus)

- `api/v1/run_types.go`: add `RunRole.FailurePolicy` (enum `Fail`/`Retry`/`Ignore`,
  default `Fail`) + optional `Retries`/`Backoff`; webhook validation.
- `controllers/kube/reconcilers.go`: predicate fires on `PodFailed` + active-pod
  deletion; enqueue the run.
- `controllers/run_controller.go`: new `handleWorkloadFailure(run)` — inspect active
  pods; apply policy; on `Fail`, set `RunPhaseFailed`, `closeLease(..., "WorkloadFailed")`
  for the run's open leases, emit Warning, and set the follow grace. On `Retry`,
  re-emit via the R2 top-up path with backoff + attempt count in status. Wire into
  `Reconcile` and into `runGangComplete`'s terminal accounting.
- Guard against double-handling if a JobSet owns the pods (R9).

## Verification spec (Sonnet)

1. **Envtest — Fail.** Seed a Running 2-pod run; mark one pod Failed; reconcile;
   assert run Failed, both leases closed `WorkloadFailed`, Warning event present,
   funding no longer charges.
2. **Envtest — follower unblock.** A follower of the failed run reaches its grace/
   fail path instead of hanging.
3. **Envtest — Retry.** With `Retry(2)`, assert the failed member is re-emitted up
   to 2× then the run Fails; attempts recorded in status.
4. **Envtest — Ignore.** With `Ignore`, one Failed + rest Succeeded → run Completed.
5. **Live (`failure-smoke.sh`).** On kind, run a pod that `exit 1`s; assert the run
   goes Failed and its lease closes (pre-R8 it hangs Running).

## Interactions

- **R2** supplies the re-emit path used by `Retry`.
- **R9** may subsume this via JobSet failurePolicy; keep them mutually exclusive.
- **R21** — a pod-health failure signal is the more robust swap trigger than node
  schedulability; coordinate the two failure detectors.
