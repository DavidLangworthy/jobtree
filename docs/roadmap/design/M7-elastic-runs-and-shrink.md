# M7 — Elastic runs (INCR) & voluntary shrink

## Summary
Extend the scheduler to support malleable Runs that can expand or contract between `minTotalGPUs` and `maxTotalGPUs` in increments of `stepGPUs`. Provide voluntary shrink tools so researchers can trade resources for queue priority, while keeping leases and budgeting consistent.

## Goals
- Interpret `Run.spec.malleable` to manage desired width and enforce bounds.
- Grow runs opportunistically when capacity appears, honoring group cohesion and lending policies.
- Shrink runs automatically during structural cuts (M5) and voluntarily via CLI/API requests.
- Update funding plans and Lease sets atomically as width changes.

## Non-goals
- Multi-cluster elasticity (M10).
- Advanced autotuning policies (e.g., dynamic step sizes based on throughput).

## Inputs & Dependencies
- Binder and Lease logic (M3) for creating/ending leases.
- Resolver (M5) for mandatory shrink events.
- Budget accounting (M1) to recalculate debit when width changes, including borrowed envelopes (M8).

## Architecture & Components
- **Run controller elasticity loop:** Reconciles running malleable Runs, computes gaps between `desired` and `allocated`, invokes growth or shrink helpers, and records width/pending state in status.
- **Binder enhancements:** Accepts a `GroupIndexOffset` and `LeaseReason` so new groups receive monotonic indices and leases log `Grow` events.
- **Run API updates:** `Run.spec.malleable.desiredTotalGPUs` persists user intent (defaults to `maxTotalGPUs`); `Run.status.width` captures `min`, `max`, `desired`, `allocated`, and any pending operation.
- **Voluntary shrink trigger:** Users edit `spec.malleable.desiredTotalGPUs` (CLI shortcut to follow) and the controller releases highest-index groups, prioritising borrowed leases.

## Detailed Design
1. **Desired width computation**
   - Maintain `Run.spec.malleable.desiredTotalGPUs` (default `maxTotalGPUs`).
   - When capacity available, attempt to grow by `stepGPUs` up to desired; when voluntary shrink requested, reduce desired accordingly.
2. **Growth procedure**
   - Invoke cover to secure funding for additional groups; respect lending limits.
   - Use pack to assign new groups to domains (prefer existing domains to minimize spread).
   - Bind new pods and create leases with `reason=Grow` (or `Start` for new groups) and update Run status.
3. **Shrink procedure**
   - For voluntary shrink: mark target width, select groups to end (least recently added or per policy), drain pods gracefully, end leases with `reason=Shrink`.
   - For structural shrink (resolver-driven): integrate with M5 to ensure consistent ordering; Run controller performs actual lease termination and updates status.
4. **Borrowed capacity handling**
   - Track payer for each group; when shrinking, release borrowed leases first to return capacity to sponsors.
   - Update Budget status to reflect reduced usage and repay GPU-hour accounting.
5. **User interface**
   - Today the desired width is updated by editing `spec.malleable.desiredTotalGPUs`; a `kubectl runs shrink` helper will wrap this in a later milestone.
   - Controller status surfaces `Run.status.width` (`min`, `max`, `desired`, `allocated`, `pending`) and `Run.status.message` (e.g. `grew to 128 GPUs`).
6. **Documentation**
   - Expand `docs/user-guide/elastic-runs.md` with workflows, tradeoffs, and interaction with Reservations.

## Testing Strategy
- Unit tests: `pkg/binder` (offset/reason), `controllers/run_controller` (growth + voluntary shrink, failure paths).
- Repository test suite: `go test ./...` (ensures API validation, binder logic, controller flows).
- Worked examples updated with elasticity scenarios for manual validation and future e2e automation.

## Observability & Telemetry
- Metrics: `elastic_grows_total`, `elastic_shrinks_total`, `elastic_width_current` (gauge).
- Logs capturing width changes, voluntary requests, and payer adjustments.

## Rollout & Migration
- Deploy updated Run controller and CLI plugin.
- Communicate to researchers about new fields and guardrails (`maxBorrowGPUs`, etc.).

## Risks & Mitigations
- **Thrashing (grow/shrink oscillation):** Add hysteresis or cooldown timers between adjustments; expose config.
- **Starvation of fixed jobs:** Monitor fairness metrics and adjust lending/priority policies if elastic runs dominate.

## Open Questions
- Should voluntary shrink provide incentives (reduced charge)? Await finance/product decision.
- Do we need automatic elasticity based on job progress? Potential future enhancement after instrumentation.
