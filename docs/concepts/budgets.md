# Budgets, envelopes, and lending

Milestone M1 introduces the live accounting layer for Budget resources. Budgets define
time- and location-scoped envelopes that cap concurrent GPU usage and cumulative GPU
hours. The controller now tracks real usage from Lease objects, publishes headroom in
status, and exposes metrics for observability.

## Headroom semantics

Each envelope reports:

- **Concurrency headroom** – `spec.concurrency - activeLeaseGPUs`
- **GPU-hour headroom** – `(spec.maxGPUHours - cumulativeLeaseGPUHours)` when a limit is
  configured. GPU-hours are computed from Lease start/end times and multiplied by the
  number of GPUs in the slice.

Aggregate caps (named groups of envelopes) expose the same headroom fields. Status now
includes both per-envelope and per-aggregate headroom along with a timestamp marking
when the snapshot was computed.

## Metrics

The controller records per-envelope usage snapshots—current concurrency, cumulative
GPU-hours, and borrowed consumption. These values power dashboards and alerts for
approaching concurrency or integral limits and provide inputs for future Prometheus
exporters.

## Lending and ACLs

Envelopes may include a `lending` policy. When present and `allow: true`, the cover
planner can allocate GPUs to other owners (borrowers) if they appear in the `to` ACL.
Sub-caps `maxConcurrency` and `maxGPUHours` restrict how much capacity can be lent at
any moment. Borrowed capacity is tracked separately to respect these limits.

The cover solver now walks the family graph in location-first order:

1. Run owner in the requested location.
2. Siblings (same parent) in the same location.
3. Parents in the same location.
4. Owner, siblings, and parents in other locations.
5. Cousins (children of aunts/uncles) in the same location, then other locations.
6. Optional sponsors, if the run opts into borrowing.

This ordering ensures family sharing happens before cross-location borrowing.

## Failure modes

Planning can fail with actionable reasons:

- `InvalidRequest` – missing owner/flavor or non-positive quantity.
- `NoEnvelope` – no envelope matches the flavor/location.
- `InsufficientCapacity` – envelopes exist but lack headroom or violate aggregate caps.
- `ACLDenied` – borrowing was requested but the lending ACL blocks the owner.

These reasons surface in controller logs and CLI tooling as we build out later
milestones.
