# R20 — Plugin emits Events so `kubectl runs explain` sees scheduling refusals

**Priority:** P5 · **Design:** complete (Fable — event taxonomy below) · **Next:** Opus implements, Sonnet verifies

## Problem (evidence)

The scheduler plugin — the component that actually makes the placement/funding
decision — emits **no Events of its own**. A gang stuck in `Permit` (forming, or
rejected as unfundable) is invisible to the Run and to `kubectl runs explain`; a
researcher must know to inspect pod-level `FailedScheduling` events. The controller
now emits good Events, but the plugin's decisions are dark
(`cmd/scheduler/plugin/plugin.go` — no recorder).

## Root cause

The plugin was built to commit funding, not to narrate; it has no event recorder
wired and `explain` reads only controller/Run-level signal.

## Design decision

Give the plugin an EventRecorder and emit a small, fixed vocabulary of Events **on
the Pod** (and mirror the load-bearing ones onto the Run so `explain` surfaces
them without pod spelunking):

- `GangForming` (Normal) — waiting for siblings, `(k/N)`.
- `GangUnfundable` (Warning) — the funding gate refused, with the deficit reason
  from `decide` (the researcher's "why isn't it starting" answer).
- `GangUnplaceable` (Warning) — **distinct from unfundable**: `admission.Feasible`
  failed at the `pack` step (physical capacity/topology), not the `cover` step
  (quota). Today `decide` collapses both into one string and Permit labels every
  rejection "not fundable" (`gang.go:168-176`, `plugin.go:198-206`) — misleading
  for anyone debugging overcommit (funding-model review, 2026-07-08). Preserve
  the typed `pack.PlanError` vs `cover.PlanError` distinction through `decide`
  and emit the matching event.
- `FlavorMismatch` (Warning) — Filter rejected all nodes of the wrong flavor.
- `GangTimeout` (Warning) — Permit timed out; the gang will re-form.
- `LeaseMinted` (Normal) — PreBind committed the lease (payer envelope), the
  positive audit signal.

`explain` (in `cmd/kubectl-runs`) should aggregate the Run-mirrored plugin Events
alongside the controller's so one command answers "why isn't my run starting?" for
both controller- and plugin-side causes.

## Invariant

Every plugin scheduling decision that changes or blocks a run's progress produces a
Run-visible Event; `kubectl runs explain` answers "why isn't it starting?" for
plugin-side causes without the user inspecting pods.

## Implementation spec (Opus)

- `cmd/scheduler/plugin/plugin.go`/`gang.go`: build an EventRecorder in `New`
  (broadcaster on the plugin's client); emit the vocabulary above at the Permit/
  Filter/PreBind decision points. Mirror `GangUnfundable`/`GangTimeout` to the Run
  object.
- `cmd/kubectl-runs/cmd/explain.go`: include plugin-sourced Run Events.

## Verification spec (Sonnet)

1. **Envtest/live.** Submit an unfundable gang; assert a `GangUnfundable` Event on
   the Run with the deficit reason; assert `runs explain` shows it.
2. **Happy path.** A funded gang emits `GangForming`→`LeaseMinted`.
3. **Timeout.** Force a Permit timeout; assert `GangTimeout` and that the gang
   re-forms (ties to R2).

## Interactions

- **R2** (`GangTimeout`/re-form), **R23** (`explain`/observability CLI) — coordinate
  the CLI aggregation once.
