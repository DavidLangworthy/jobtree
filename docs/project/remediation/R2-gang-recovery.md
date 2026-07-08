# R2 — Gang recovery: de-wedge partial gangs, survive restart, adopt at correct width

**Priority:** P0 (the load-bearing one) · **Design:** complete (Fable) · **Next:** Opus implements, Sonnet verifies
**Depends on:** nothing hard; **enables** R1 (shares `PostBind` + sweep) and R3 (correct adoption width).

> **Implementation status (see IMPLEMENTATION-LOG.md):**
> - ✅ **Part 1 (pieces 1 + 4, merged):** Permit counts committed siblings
>   (`committedCount`) so a transient-failure partial gang re-assembles instead of
>   wedging; ABA lease-name nonce (`run-nonce`) so delete+resubmit mints a fresh
>   open lease. Plugin-side, fully unit-tested.
> - ⏳ **Part 2 (piece 3, next):** controller adopts at correct width (Degraded +
>   re-emit missing) instead of flipping Running on any open lease.
> - ⏳ **Part 3 / R2b (piece 2, follow-up):** full scheduler-restart reconstruction
>   from open leases + delta re-funding of un-minted survivors. Part 1's
>   committed-count is in-memory and does NOT survive a process restart, so a
>   restart-mid-gang can still wedge until this lands; it is the hardest sub-part
>   (needs cohort-labelled leases so leases can be grouped back into gangs, plus
>   delta funding of the survivors like a grow cohort).

## Problem (evidence)

After `Permit` allows a gang, members mint and bind **independently**. The gang
gate requires the *full* expected width to be simultaneously waiting
(`cmd/scheduler/plugin/plugin.go:167-178`): `waiting < expected → Wait`. Bound
siblings never re-enter Permit. So three failures each wedge the gang permanently:

1. **One member's PreBind/bind fails** (transient apiserver error; the Lease
   webhook is `failurePolicy=fail` and briefly down — `controllers/kube/
   webhooks.go:21`). That pod is rejected and re-queued; on retry it is alone,
   `waiting=1 < expected`, times out after 2m (`permitTimeout`), loops forever.
2. **Scheduler restart mid-gang.** `gangManager` state is in-memory only
   (`gang.go:30-36`) and is never reconstructed from existing Leases. After a
   restart, surviving unbound members can never reassemble with already-bound
   siblings.
3. Meanwhile the **controller adopts the run as Running on any open lease > 0**,
   width unchecked (`controllers/run_controller.go:197-212`), and the Running
   branch only does elastic reconcile (`:185-195`) — it never repairs or re-emits
   a missing member. So N−1 containers run and charge budget indefinitely, and the
   run reports Running: a silent violation of "start together or not at all"
   (`docs/index.md:12`).

There is also an **ABA hazard** on manual recovery (delete + resubmit same-named
run): deterministic pod names mean PreBind's `client.Create` collides with the
prior incarnation's *closed* lease, `IsAlreadyExists` is treated as success
(`plugin.go:236-239`), and the new gang runs with no open lease — unfunded work
the controller never adopts.

## Root cause

Gang atomicity is enforced only at *assembly* (Permit), never at *commit*. There
is no notion of "this gang already has k bound members" once a pod leaves the
waiting set, no reconstruction of that fact after a restart, and no controller
check that Running means full width.

## Design decision

Make "already-committed members" a first-class input to both the plugin gate and
the controller's adoption, and give the gang a recovery path instead of a wedge.

1. **Permit counts already-minted siblings toward width.** Change the completeness
   test from `waiting >= expected` to `waiting + alreadyCommitted >= expected`,
   where `alreadyCommitted` = this gang's pods that have already claimed a payer
   (`g.claimed`) / equivalently its open real leases in the API. A lone re-queued
   member then sees `1 + (expected-1) >= expected` and proceeds to PreBind, where
   `claimPayer` is already idempotent (`gang.go:145-165`) and returns its
   preassigned payer. This de-wedges cases 1 and 2 directly.
2. **Reconstruct gang state on startup.** In `New`, before serving, List open
   Leases and rebuild one `gangCommit` per `(run, cohort)`: `decided=true`,
   `fundable=true`, `payers`/`assigned` derived from each lease's provenance
   (owner/budget/envelope) and pod name, `claimed=len(openLeases)`. Now a restart
   mid-gang resumes instead of wedging, and `alreadyCommitted` from step 1 is
   correct across restarts.
3. **Controller adopts at correct width.** In the adoption path
   (`run_controller.go:197-212`), compare open leases (or minted active pods) to
   the run's expected active width (`intentPodShape`). If `open < expected`, do
   **not** report healthy Running: set Running but with a `Degraded` condition/
   message and re-emit the missing active pods (reuse `emitCohortPods`' top-up so
   only absent pod objects are created). If `open == expected`, adopt as today.
4. **Kill the ABA.** PreBind must treat `IsAlreadyExists` as success **only** if
   the existing lease is *open and owned by this gang*; if it is closed/foreign,
   mint under a fresh name. Simplest robust fix: include a per-incarnation nonce
   (the Run UID, or `run.CreationTimestamp` unix nanos) in the lease name so a new
   incarnation never collides with an old one. Emit that nonce as a pod annotation
   at build time so the plugin uses the same value.

**Why count-committed rather than "re-drive the whole gang":** rebinding already-
running pods would kill live containers. The gang is already partly real; recovery
must converge *toward* the committed state, not tear it down.

## Invariant

A run is reported healthy-Running **iff** it holds open leases for its full active
width. A single member failure or a scheduler restart converges back to full width
(re-emit + re-Permit with committed-count accounting) rather than wedging, and
never double-mints (idempotent payer + open-lease reconstruction).

## Implementation spec (Opus)

- `cmd/scheduler/plugin/plugin.go`
  - Permit: compute `committed := j.gm.committedCount(key)` and gate on
    `waiting + committed >= expected`. Do not double-count a pod that is both
    waiting and (somehow) committed.
  - PreBind: refine the `IsAlreadyExists` branch per step 4.
- `cmd/scheduler/plugin/gang.go`
  - Add `committedCount(key)` (reads `g.claimed` under mutex).
  - Add `Reconstruct(ctx)` that Lists open Leases and rebuilds `m.gangs`; call from
    `New` (or first Permit) once.
  - Lease naming / nonce plumbing for step 4.
- `controllers/run_controller.go`
  - Adoption path: width check + `Degraded` handling + top-up re-emit.
  - Add a `RunConditionDegraded` (ties to the API-conventions work R11, but a
    freeform status message is acceptable for now).
- Shared with R1: `PostBind` + stale-gang sweep.

## Verification spec (Sonnet)

1. **Unit — de-wedge.** Assemble a width-2 gang; mint pod A; fail pod B's PreBind
   once; on B's retry assert Permit proceeds (`waiting+committed >= 2`) and B
   mints. Assert exactly 2 open leases, no duplicates.
2. **Unit — restart.** Build a `gangManager`, mint a gang, drop the manager,
   `Reconstruct` from the leases, assert a late sibling still admits and no lease
   double-mints.
3. **Unit — ABA.** Close a gang's leases (simulate delete), resubmit same-named
   run, assert new leases are created (fresh nonce) and the run adopts.
4. **Envtest — adoption width.** Seed N−1 open leases for an N-wide run; reconcile;
   assert the run is Running-but-Degraded and the missing pod is re-emitted; add
   the Nth lease; assert healthy Running.
5. **Live (extends `swap-smoke.sh` or a new `wedge-smoke.sh`).** Kill one gang
   pod mid-bind on a real kind cluster; assert the run returns to full width
   without operator action.

## Interactions

- **R1** clears phantoms; R2 supplies the `PostBind`/sweep both use.
- **R3** relies on R2's correct adoption width (its pods must adopt at full width
  or re-emit, not report a false Running).
- **R6/R5** should land first so the re-emitted/reconstructed pods are trusted.
