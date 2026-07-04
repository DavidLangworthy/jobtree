# Reservations guide

Reservations represent the plan for a Run that cannot start immediately. The run controller
forecasts *when* a Run is expected to start, *why* it is waiting, and *what* remedies the resolver
will actually try when the activation window opens — computed from live cluster/budget state, not
a fixed constant.

## When reservations are created

The run controller attempts immediate admission (cover → pack → bind). If any of the following
conditions hold, a Reservation is created instead of binding pods:

- **Insufficient topology capacity:** the packer cannot find enough free GPUs in the required domains.
- **Budget window not yet open:** the requested envelope forbids admission until its `start` time but allows reservations.
- **Budget concurrency/GPU-hour headroom exhausted:** an envelope exists but has no room for the requested width.

The Reservation spec is immutable and captures:

```yaml
spec:
  runRef:            # namespace/name of the Run
  intendedSlice:     # domain labels + optional node list (when a pack plan existed)
  payingEnvelope:    # envelope that will fund the activation
  earliestStart:     # timestamp the system will target for activation
```

## Status fields

Reservation status includes:

- `state`: `Pending` until it activates or is released/superseded/failed. A due reservation
  transitions to `Released` (reason `Activated`) once the run controller successfully admits the
  run — including via the resolver's deficit resolution (unfunded reclaim → spares → shrink →
  fair lottery) — or to `Failed` if the run it references is gone or has no envelope left to fund
  it.
- `reason`: human-readable summary (e.g. "budget window opens at 2024-02-01T12:00Z").
- `forecast`: deficit metadata
  - `deficitGPUs`: how many GPUs must be freed before activation, computed from live free-GPU
    counts and funding headroom (`pkg/forecast.estimateDeficit`) — not a placeholder.
  - `scope`: labels describing the affected domain (region/cluster/fabric).
  - `remedies`: only the structural steps the resolver would actually find something to do for —
    reclaiming unfunded capacity, dropping spares, or shrinking elastic runs — each included only
    when that kind of capacity/run genuinely exists right now, plus the fair lottery, which is
    always the last resort.
  - `confidence`: `window-aligned` when the estimate is tied to a budget window, `conservative`
    otherwise.
- `countdownSeconds`: seconds until `earliestStart` when the value is in the future.

`earliestStart` itself is data-driven: it is a base lead plus an increment proportional to the
size of the deficit, so a 4-GPU shortfall and a 400-GPU shortfall are not given the same ETA.

Run status mirrors key data:

```yaml
status:
  phase: Pending
  pendingReservation: train-128-res-1700000000
  earliestStart: 2024-02-01T12:00:10Z
  message: "reservation train-128-res-1700000000 scheduled for 2024-02-01T12:00:10Z (deficit 64 GPUs)"
```

## CLI workflow

```bash
kubectl runs plan train-128
kubectl get reservations
kubectl describe reservation train-128-res-1700000000
kubectl get events --field-selector involvedObject.name=train-128   # Reserved/Activated events
```

The worked examples in [`docs/examples/worked-examples.md`](../examples/worked-examples.md)
highlight how reservations appear in audit trails for both capacity shortfall and future-dated
windows.

## Observability

- `jobtree_reservations_backlog_seconds{reservation="<ns>/<name>",flavor="..."}` tracks the live
  countdown for each pending reservation individually (not collapsed by flavor) and is cleared
  once the reservation activates or is released.
- `jobtree_forecast_latency_seconds{flavor="..."}` times the forecast computation itself (an
  inline call inside the run reconciler — there is no separate "forecast controller" process).

## What's next

- A richer, per-envelope "why is my deficit N" breakdown (today `reason`/`remedies` are
  cluster/flavor-scoped, not fully location-scoped) — see `pkg/forecast.computeRemedies`.
- External notifications on countdown milestones.
