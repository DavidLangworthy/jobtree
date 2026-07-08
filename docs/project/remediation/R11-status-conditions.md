# R11 — Adopt `status.conditions` (metav1.Condition)

**Priority:** P3 · **Design:** complete (Fable) · **Next:** Opus implements, Sonnet verifies

## Problem (evidence)

There is not a single `metav1.Condition` in the API. Status is freeform phase
strings (`RunStatus.Phase`, `Message`) whose canonical values live in the
*controllers* package, not the API package, and are unenumerated in the CRD schema.
Tools cannot reliably wait on or reason about state
(`api/v1/run_types.go:171-190`; phase constants defined in `controllers/`).

## Root cause

Status was modeled as a single phase string early and never migrated to the
standard conditions pattern.

## Design decision

Add `status.conditions []metav1.Condition` to every CRD that has meaningful state
(Run, Reservation, Lease, Budget), keeping `Phase` as a derived convenience for
`kubectl get` printer columns. Define a **fixed condition taxonomy** in the API
package (this is the Fable deliverable — the vocabulary):

**Run conditions** (type / notable reasons):
- `Admitted` — `Funded` / `Reserved` / `Unfunded` / `Waiting`
- `Scheduled` — `GangBound` / `GangForming` / `Unschedulable` / `Degraded` (R2 partial width)
- `Running` — `AllPodsRunning` / `PartialWidth`
- `Completed` — `AllSucceeded`
- `Failed` — `WorkloadFailed` (R8) / `NodeFailureNoSpare` / `UpstreamFailed` (follow) / `NoEnvelope`
- `Blocked` — `FollowWait` / `CheckpointGrace`

**Reservation:** `Forecast` (`EarliestStartKnown`/`Unforecastable`), `Activated`.
**Lease:** `Active` / `Closed` (reason = the existing `ClosureReason` values).
**Budget:** `Healthy` / `Overcommitted`.

Move the phase-string constants into `api/v1` and derive `Phase` from conditions so
the two never disagree. Use `meta.SetStatusCondition` semantics
(observedGeneration, lastTransitionTime).

## Invariant

Every state a run can be in is expressible as a set of conditions with a stable
type + reason vocabulary defined in the API package; `Phase` is a pure function of
conditions.

## Implementation spec (Opus)

- `api/v1/*_types.go`: add `Conditions []metav1.Condition` (with the standard
  patchStrategy/patchMergeKey markers) to each status; move phase constants here;
  add a `derivePhase(conditions)` helper.
- `controllers/run_controller.go` + reconcilers: replace direct `Phase`/`Message`
  writes with `meta.SetStatusCondition` calls at each decision point; derive Phase.
- Wire the R2 `Degraded` and R8 `Failed` reasons here rather than as ad-hoc strings.
- Regenerate CRDs; keep printer columns pointed at derived `Phase`.

## Verification spec (Sonnet)

1. **Envtest.** Drive each lifecycle transition; assert the expected condition
   type/reason/status and that `Phase` matches `derivePhase`.
2. **kubectl wait.** Assert `kubectl wait --for=condition=Completed run/<x>` works.
3. **Golden.** Regenerate; the conditions are additive — confirm no funding-class
   change.

## Interactions

- **R2** (`Degraded`) and **R8** (`Failed` reasons) should emit through this
  taxonomy; land R11's vocabulary first or in the same change so they don't invent
  ad-hoc strings.
