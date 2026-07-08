# R3 — Reconcile the opportunistic-activation fork with the cutover

**Priority:** P0 · **Design:** complete (Fable) · **Status: IMPLEMENTED** (Promise path; David's decision resolved to "keep it")
**Depends on:** R5/R6 (its pods must carry trusted provenance) and R2 (correct adoption width).

> **Implemented 2026-07-08.** The controller no longer mints on the opportunistic
> path: a budget-only reservation activation emits a *promised* intent gang
> (`lease-reason=Promise` + payer provenance) and the plugin mints the (Unfunded)
> leases at PreBind, skipping its funding gate like a swap. A new Reconcile guard
> keeps the run Pending until those leases land and the adoption path flips it
> Running. See the "Implementation spec" below (all points done) and the
> IMPLEMENTATION-LOG R3 entry for the judgment calls. This makes index.md's
> single-"sole committer" claim TRUE (R24 can drop its "false until R3 lands"
> caveat).

## Problem (evidence)

"Promised-but-unfunded" reservation activation is the one remaining controller
mint. On a pure budget shortfall at activation it calls `binder.Materialize` and
appends the minted pods+leases directly to state, then sets the run Running
(`controllers/run_controller.go:955-982`). This was coherent pre-cutover
(pods were `nodeName`-pinned and just ran). Post-cutover it is not:

- `binder`'s materialized pods carry **labels only, no annotations**
  (`pkg/binder/binder.go:269-285`), so when rendered by `kube.buildPod` they get
  `expected-width` defaulting to 1 and no `cohort`/`lease-reason`, and
  `schedulerName=jobtree` is stamped unconditionally (`controllers/kube/
  bridge.go:356`). They route to the plugin.
- The plugin's `Permit` then re-derives **full-width** funding for the run against
  a world that already contains the run's just-minted opportunistic leases on an
  already-exhausted envelope — the exact gang the design says the gate "would
  refuse" (`cascade-plan.md:35`). So `decide` rejects, the pods loop
  Unschedulable forever, the minted leases keep charging the envelope and block
  the nodes, and the run is nonetheless marked Running with zero bindable pods.

No envtest or e2e exercises a budget-shortfall activation with the plugin in the
loop, which is why this survived.

## Root cause

The cutover moved funding authority to the plugin but left one controller mint
whose whole premise — "start anyway, unfunded" — the plugin is designed to reject.
The two authorities were never reconciled.

## Design decision

**Recommended (keep the feature, route it through the plugin): add an explicit,
authenticated `Promise` path.** The controller stops minting; it emits normal
intent pods stamped with a `lease-reason=Promise` (or `promised=true`) marker and
the intended payer provenance (owner/budget/envelope), exactly as the swap path
already stamps provenance (`run_controller.go:1692-1699`). The plugin treats a
`Promise` pod like the swap case: **skip the funding gate**, mint the lease from
the carried provenance, but tag it `ClassUnfunded` so `funding.Evaluate` re-funds
it by arithmetic when quota returns (the R14 demote-not-kill semantic the comment
at `:935-940` already intends). This keeps a single committer (the plugin),
removes the controller mint, and preserves the "honor the promise" behavior.

Crucially this is only safe **with R5/R6**: a `Promise` marker that bypasses the
funding gate is a forgeable capability unless the mandatory-scheduler policy
restricts `lease-reason`/`payer-*` to the controller ServiceAccount. So R3 must
land after R5/R6, and its marker joins the controller-only annotation set.

**Alternatives considered:**
- *Drop the feature.* Simplest and fully honest: on shortfall, keep the run
  Pending/Reserved instead of faking Running, and let the normal funded path admit
  it when quota returns. Loses the "start the moment the promise was made" behavior
  but removes a whole class of edge cases. **This is the fallback if David does not
  want a gate-bypass path at all.**
- *Give Permit a generic "bypass funding" flag.* Rejected — reintroduces the
  dual-authority fuzziness §9 removed; the `Promise` marker is narrow and
  authenticated, a flag is broad.

### Decision for David (flagged) — RESOLVED during implementation

Does the promised-but-unfunded opportunistic start **survive** or is it
**dropped**? **Resolved: it survives (via the `Promise` path).** On starting the
work I confirmed the opportunistic Unfunded-start is a *documented quota
semantic* with pure-engine tests asserting it
(`reservation_semantics_test.go:TestActivateReservationBudgetOnlyShortfallAdmitsOpportunistically`,
`quota_semantics_test.go` window-close cases: the run coasts Running/Unfunded and
is re-funded when quota returns). "Drop it" would delete that documented
semantic, so it is withdrawn. Implement the `Promise` path below.

**Implementation note (why this is its own careful pass):** cutting the
controller's opportunistic mint over to intent-pods + a plugin `Promise` mint
requires **migrating the pure-engine quota-semantics tests** to the intent-pod +
simulated-plugin-mint pattern (as the PLUGIN-2 cutover migrated the normal path
via `seedRunning`) and regenerating the affected golden scenarios. It touches the
quota source-of-truth (`quota-semantics.md`), so it must be done deliberately. The
`Promise` marker is already forgery-protected by the R5/R6 VAP (it gates every
`rq.davidlangworthy.io/*` annotation to the controller SA); add a plugin
**charge cross-check** as defense-in-depth for VAP-off — resolve the carried
`payer-budget/payer-envelope` (the fields `funding.Evaluate` actually charges) and
require the named budget to be owned by the run's own owner and to carry the named
envelope. (A naive `provenance.Owner == run owner` check is **insufficient**: the
lease's `Spec.Owner` is cosmetic — Evaluate never reads it — so it would let a pod
that owns its own run charge a victim's envelope. An adversarial review caught this;
see IMPLEMENTATION-LOG R3 decision #4.)

## Invariant

There is exactly one funding committer (the plugin). A promised start produces a
funded-or-explicitly-Unfunded lease minted by the plugin from authenticated
provenance; the run is reported Running **iff** its pods actually bound. No pod is
ever emitted that the gate is guaranteed to reject.

## Implementation spec (Opus)

- `controllers/run_controller.go:955-982`: delete the `binder.Materialize` mint.
  In the opportunistic branch, emit intent pods (reuse `emitIntentPods`) with each
  pod annotated `lease-reason=Promise` + `payer-owner/budget/envelope` from
  `coverPlan`. Do **not** set Running here; let R2's adoption flip Running when the
  plugin's leases appear (and at correct width).
- `cmd/scheduler/plugin/plugin.go`: in `Permit`, treat a `Promise` pod like
  `isSwapPod` — allow without the gang/funding gate. In `PreBind`, mint from the
  carried provenance (same code path as swap) but set the lease class/annotation
  so `funding.Evaluate` classes it Unfunded until quota returns.
- `pkg/binder/binder.go`: add `AnnotationPromise` (or reuse `lease-reason` value
  `Promise`) to the controller-only set.
- Add `Promise` to the R5/R6 policy's controller-only field list.

## Verification spec (Sonnet)

1. **Envtest — the incoherence is gone.** Drive a reservation activation under
   pure budget shortfall with the plugin modeled; assert either (a) survive: the
   plugin mints an Unfunded `Promise` lease and the run adopts Running at full
   width, or (b) drop: the run stays Reserved, no orphan leases, no false Running.
2. **Unit — re-funding.** With a `Promise`/Unfunded lease present, return quota;
   assert `funding.Evaluate` reclasses it Owned/Shared without a re-mint.
3. **Forgery (ties to R5).** A non-controller pod stamped `lease-reason=Promise`
   is rejected by the policy; assert no lease is minted.
4. **Golden.** Regenerate the golden oracle for the activation scenarios; audit
   the diff is exactly the mint-site move (no funding-class change).

## Additional finding (funding-model review, 2026-07-08)

The current opportunistic mint has a second incoherence beyond the gate refusal:
the lease's `Spec.Slice.Nodes` is baked from `pack`'s placement **before** the
pod is scheduled, but the rendered pod gets only a *soft*
`preferredDuringScheduling` affinity toward that node (`bridge.go:378-380`;
`nodeName` is cleared unconditionally). If the pod ultimately binds elsewhere,
the ledger's node-capacity accounting diverges from physical placement. The
Promise path fixes this automatically — the plugin mints at PreBind from the
**actual** bind node, like every other lease. Verification should assert it: the
Promise lease's `Slice.Nodes` must equal the bound node, not the pack plan.

## Interactions

- **Hard-ordered after R5/R6** (authenticated marker) and **R2** (adopt at width).
- Shares the swap PreBind path; keep the two markers (`Swap`, `Promise`) distinct
  but funnel through one "mint-from-carried-provenance" helper.
