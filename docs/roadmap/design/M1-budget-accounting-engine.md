# M1 — Budget accounting engine

## Summary
Introduce the controllers and libraries that transform static Budget CRDs into live concurrency and GPU-hour accounting. This milestone computes per-envelope headroom, enforces aggregate caps, and implements the family-sharing and lending rules necessary for cover decisions in later milestones.

## Goals
- Track point-in-time GPU usage (`u_e(t)`) and integral usage (`U_e(t)`) for every budget envelope.
- Implement the cover algorithm that walks the ownership DAG (child → siblings → parent → cross-location → cousins) with optional sponsor lending.
- Surface status conditions and Prometheus metrics that describe remaining concurrency and GPU-hours.
- Provide a reusable library for future controllers (Run, Reservation) to request funding plans.

## Non-goals
- Binding workloads or making placement decisions (handled starting M3).
- Handling Reservations or oversubscription remedies (M4–M5).
- Multi-cluster coordination (M10).

## Inputs & Dependencies
- CRD schemas delivered in M0.
- RQλ calculus rules for envelope validity and cover order.
- Access to Lease stream (for consumption) once M3 exists; for M1, synthetic tests rely on fixtures.

## Architecture & Components
- **Controller:** `controllers/budget_controller.go` reconciles Budget objects, updates status, and publishes metrics.
- **Library:** `pkg/cover` implements the cover solver, including lending ACL enforcement.
- **Stores:** Derived indexes keyed by owner, flavor, location, and window to accelerate lookups.
- **Metrics:** Per-envelope gauges (`budget_concurrency_used`, `budget_gpu_hours_used`), counters for lending usage, and alerts for approaching integral limits.

## Detailed Design
1. **State indexing**
   - Build in-memory structures keyed by `(owner, flavor, location scope)` capturing active leases (initially from status or tests).
   - Support sliding-window integration for `maxGPUHours` via trapezoidal integration over Lease events.
2. **Family sharing traversal**
   - Compute preference order at reconciliation time using the owner DAG from Budget `parents` fields.
   - Maintain per-location views to guarantee location-first semantics.
3. **Lending ACLs**
   - Budget envelopes optionally expose `lending.allow`, `lending.to[]`, `maxConcurrency`, `maxGPUHours`.
   - Track lent usage separately to honor sub-caps.
4. **Aggregate caps**
   - Evaluate aggregate caps by flavor across envelope sets; update status conditions when usage approaches thresholds.
5. **Cover API**
   - `cover.Plan(request)` accepts Run demand (flavor, count, location hints) and returns an ordered list of `(envelope, quantity)`.
   - Failures include machine-readable reasons (e.g., `NoEnvelope`, `ConcurrencyCapExceeded`, `IntegralCapExceeded`, `ACLDenied`).
6. **Status & events**
   - Populate `Budget.status.headroom` (per envelope) and `Budget.status.aggregateHeadroom`.
   - Emit Kubernetes Events when envelopes exhaust concurrency or integral caps.
7. **Documentation**
   - Extend `docs/concepts/budgets.md` with lending, aggregate caps, and status semantics.

## Testing Strategy
- Unit tests in `pkg/cover` using table-driven cases (family ordering, lending ACLs, window edge cases, aggregate cap saturation).
- Envtest reconciliation tests validating status updates and metric emission.
- Property-based tests for integral calculations to ensure `maxGPUHours` never underflows.

## Observability & Telemetry
- Expose metrics via controller-runtime manager `/metrics` endpoint.
- Add structured logs for cover failures, lending denials, and aggregate cap breaches.

## Rollout & Migration
- Deploy updated controller via Helm/Kustomize; no data migration, but CRDs gain new status fields (backward-compatible).
- Provide runbook for interpreting new status conditions and metrics.

## Risks & Mitigations
- **Time-series precision:** Mitigate by using monotonic cumulative GPU-hour counters derived from Lease events; document sampling frequency.
- **ACL misconfiguration:** Validate ACL patterns on admission (M0 webhooks) and during reconciliation (warnings when unused).

## Open Questions
- Should integral usage integrate on wall-clock or job runtime (paused pods)? Plan: use Lease intervals (wall-clock); revisit if checkpoint semantics demand adjustments.
- Do we need burst credits for short spikes? Defer until utilization data is available.
