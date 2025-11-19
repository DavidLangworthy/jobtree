# M4 — Reservations & forecasting

## Summary
Add the ability to plan for runs that cannot start immediately by storing intended slices, protecting future windows, and communicating forecasts. Reservations allow deterministic activation at `earliestStart` while keeping the cluster productive through safe backfill.

## Goals
- Generate Reservation objects when cover/pack cannot satisfy a Run right now but feasibility exists at/after a calculable time.
- Record intended slices (nodes or domain references) and paying envelopes for later activation.
- Enforce backfill guardrails so opportunistic workloads vacate before `earliestStart`.
- Publish user-facing forecasts: deficit size, remedies, countdowns, and probability-of-cut estimates.

## Non-goals
- Resolving activation-time deficits (handled in M5).
- Elastic scaling and spares (M6–M7).
- Multi-cluster aggregation (M10).

## Inputs & Dependencies
- Active Run controller and binder (M3) for immediate starts.
- Cover and pack libraries (M1–M2) to compute intended slice and payer plan.
- Budget accounting for future window validation (M1).

## Architecture & Components
- **Run controller enhancements (`controllers/run_controller.go`):** When admission fails, synthesize Reservation objects with immutable specs, update Run status with `pendingReservation` and `earliestStart`, and persist the reservation in cluster state.
- **Planner module (`pkg/forecast`):** Calculates earliest feasible start, deficit size, confidence label, and recommended remedies given pack/cover errors and budget state.
- **Topology helpers (`pkg/topology`):** Provide total free GPU counts and the largest domain to seed forecasts when no pack plan exists.
- **(Future)** Reservation controller / notifier: still planned for later milestones to drive activation, countdown refresh, and external notifications.

## Detailed Design
1. **Reservation creation**
   - When Run admission fails, compute earliest feasible time by replaying cover against Budget availability (including window start times) and pack availability (domain fragmentation forecasts).
   - Create Reservation with immutable spec (runRef, intendedSlice, payingEnvelope, earliestStart) and status `Pending`.
2. **Backfill safety**
   - Label nodes in intended slice with Reservation metadata.
   - Admission of opportunistic workloads must ensure they finish before `earliestStart`; otherwise they are marked for eviction when activation approaches.
3. **Forecast generation**
   - Calculate deficit (GPUs missing) scoped by flavor + location.
   - Determine remedies: drop spares, shrink specific INCR runs, expect lottery probability (based on active leases and `stepGPUs`).
   - Publish summary via CLI (`kubectl runs plan`), Notifier, and `Reservation.status.forecast`.
4. **Activation preparation**
   - `Reservation.status.countdown` updated periodically (controller requeue) as `earliestStart` approaches.
   - If prerequisites change (e.g., budgets revoked), mark Reservation `Blocked` and raise alerts.
5. **Activation trigger**
   - At `earliestStart`, re-run cover/pack. If feasible without cuts, bind immediately; else hand off to resolver (M5).
   - Upon successful binding, mark Reservation `Active` then immediately `Released` (kept for audit).
6. **Cancellation / re-plan**
   - If Run specification changes or owner cancels, mark Reservation `Canceled` with reason.
   - Requeue Run for new admission cycle.
7. **Documentation**
   - Update `docs/user-guide/quickstart.md` and add `docs/user-guide/reservations.md` describing lifecycle and CLI usage.

## Testing Strategy
- Unit tests for planner algorithms (earliestStart calculation, deficit computation) in `pkg/forecast` and snapshot helpers in `pkg/topology`.
- Controller tests covering both capacity deficits and future budget windows to ensure reservations appear with expected metadata.
- Repository-wide `go test ./...` executed in CI.

## Observability & Telemetry
- Reservation status now exposes `forecast` (deficit, scope, remedies, confidence) and `countdownSeconds` for UI consumption.
- Metrics and external notifier wiring remain open work for a later milestone.

## Rollout & Migration
- Deploy Reservation controller and Notifier components.
- Update Helm values to toggle Notifier integrations (Slack/webhook endpoints).

## Risks & Mitigations
- **Forecast inaccuracy:** Use conservative estimates derived from actual Lease data; include confidence interval in status.
- **Backfill starvation:** Document policies to prioritize small, short jobs and allow manual override.

## Open Questions
- How should forecasts account for uncertain failure events (node flaps)? Consider probabilistic adjustments once failure data is available.
- Should we allow user-specified `latestStart` deadlines to auto-cancel Reservations? Gather feedback before extending schema.
