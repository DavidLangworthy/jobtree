# Reservations guide

Reservations represent the plan for a Run that cannot start immediately. The M4 implementation introduces lightweight
forecasting so researchers can see *when* their job is expected to start, *why* it is waiting, and *what* remedies will
be applied when the activation window opens.

## When reservations are created

The run controller now attempts immediate admission (cover → pack → bind). If any of the following conditions hold,
a Reservation is created instead of binding pods:

- **Insufficient topology capacity:** the packer cannot find enough free GPUs in the required domains.
- **Budget window not yet open:** the requested envelope forbids admission until its `start` time but allows reservations.
- **Budget concurrency exhausted:** an envelope exists but has zero concurrency headroom.

The Reservation spec is immutable and captures:

```yaml
spec:
  runRef:            # namespace/name of the Run
  intendedSlice:     # domain labels + optional node list (when a pack plan existed)
  payingEnvelope:    # envelope that will fund the activation
  earliestStart:     # timestamp the system will target for activation
```

## Status fields

Reservation status now includes:

- `state`: currently `Pending` until activation work lands in later milestones.
- `reason`: human-readable summary (e.g. "budget window opens at 2024-02-01T12:00Z").
- `forecast`: deficit metadata
  - `deficitGPUs`: how many GPUs must be freed before activation.
  - `scope`: labels describing the affected domain (region/cluster/fabric).
  - `remedies`: the structural steps that will be attempted (drop spares, shrink elastic runs, lottery).
  - `confidence`: `window-aligned` when the estimate is tied to a budget window, `conservative` otherwise.
- `countdownSeconds`: seconds until `earliestStart` when the value is in the future.

Run status mirrors key data:

```yaml
status:
  phase: Pending
  pendingReservation: train-128-res-1700000000
  earliestStart: 2024-02-01T12:00:10Z
  message: "reservation train-128-res-1700000000 scheduled for 2024-02-01T12:00:10Z (deficit 64 GPUs)"
```

## CLI workflow

The dedicated `kubectl runs plan` command will arrive in a later milestone. For now you can inspect Reservations
directly:

```bash
kubectl get reservations
kubectl describe reservation train-128-res-1700000000
```

The worked examples in [`docs/examples/worked-examples.md`](../examples/worked-examples.md) now highlight how
reservations appear in audit trails for both capacity shortfall and future-dated windows.

## What’s next

- Activation-time deficit resolution (drop spares → shrink → lottery) lands in M5.
- Countdown refreshers, external notifications, and Reservation state transitions will arrive with a dedicated
  reservation controller and notifier service.
