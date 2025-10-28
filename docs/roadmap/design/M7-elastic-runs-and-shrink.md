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
- **Elasticity manager (`pkg/binder/elastic`):** Determines new desired width, orchestrates add/remove group operations.
- **Run controller updates:** Monitor capacity signals and voluntary shrink requests, reconciling desired vs. actual width.
- **CLI integration:** `kubectl runs shrink` and optional `kubectl runs grow` commands hitting subresources or CRD fields.
- **Status tracking:** `Run.status.width` capturing `desired`, `allocated`, `min`, `max`, and pending operations.

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
   - CLI `kubectl runs shrink train-128 --by 16` updates desired width; controller acknowledges with status condition `VoluntaryShrinkInProgress` until completion.
   - Provide events summarizing growth/shrink decisions, including reason and payer changes.
6. **Documentation**
   - Expand `docs/user-guide/elastic-runs.md` with workflows, tradeoffs, and interaction with Reservations.

## Testing Strategy
- Unit tests for elasticity manager (growth/shrink decision logic, payer ordering).
- Envtest verifying Run status transitions and voluntary shrink subresource.
- e2e scenario: Run starts at min width, grows to max as capacity appears, voluntarily shrinks, and survives resolver-mandated shrink.

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
