# R3 — Reconcile the opportunistic-activation fork with the cutover

**Priority:** P0 · **Design:** complete (Fable), **one product decision for David** · **Next:** Opus implements, Sonnet verifies
**Depends on:** R5/R6 (its pods must carry trusted provenance) and R2 (correct adoption width).

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

### Decision for David (flagged)

Does the promised-but-unfunded opportunistic start **survive** (recommended:
yes, via the authenticated `Promise` path) or is it **dropped** (keep the run
Reserved until funded)? Everything else in this spec assumes "survive"; the
"drop" variant is strictly smaller (delete the mint, keep the run Reserved).

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

## Interactions

- **Hard-ordered after R5/R6** (authenticated marker) and **R2** (adopt at width).
- Shares the swap PreBind path; keep the two markers (`Swap`, `Promise`) distinct
  but funnel through one "mint-from-carried-provenance" helper.
