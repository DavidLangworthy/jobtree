# Elastic runs (INCR) and voluntary shrink

Elastic runs let a training job adjust its width between `minTotalGPUs` and
`maxTotalGPUs` in deterministic steps. This milestone wires the controller,
binder, and API so Runs can grow when headroom appears and shrink when users
reduce their desired footprint.

## Spec fields

```yaml
spec:
  resources:
    gpuType: H100-80GB
    totalGPUs: 96           # initial width (must align with step)
  malleable:
    minTotalGPUs: 96        # lower bound for the run
    maxTotalGPUs: 160       # upper bound
    stepGPUs: 32            # grow/shrink in 32 GPU increments
    desiredTotalGPUs: 160   # optional; defaults to maxTotalGPUs
```

Key points:

- `totalGPUs` still defines the initial allocation and must satisfy the
  malleability constraints (`min ≤ total ≤ max` and `(total - min) % step = 0`).
- `desiredTotalGPUs` captures user intent. If omitted, it defaults to
  `maxTotalGPUs` during webhook defaulting.
- Changing `desiredTotalGPUs` after the Run is running drives voluntary shrink
  (lower value) or additional growth (higher value).

## Status surface area

Elastic Runs now report width information directly in status:

```yaml
status:
  phase: Running
  message: "grew to 128 GPUs"
  width:
    min: 96
    max: 160
    desired: 160
    allocated: 128
    pending: "Grow to 160"
```

`allocated` counts active (and borrowed) GPUs only—spares do not inflate the
number. When the controller is still working toward the desired width, the
`pending` field calls it out.

## Growth workflow

1. The controller observes `desired > allocated`.
2. It plans an additional `stepGPUs` worth of work (subject to topology and
   budget headroom).
3. `pkg/binder` materialises the new pods and leases using the next group index
   and tags the leases with `reason: Grow`.
4. Status updates to show the new allocation and, if more growth is pending,
   keeps a reminder in `status.width.pending`.

## Voluntary shrink

1. Edit the Run:

   ```bash
   kubectl patch run default/train --type merge \
     -p '{"spec":{"malleable":{"desiredTotalGPUs":96}}}'
   ```

2. On the next reconcile the controller selects the highest-index groups,
   prioritising borrowed capacity, and closes their leases with
   `closureReason=Shrink`.
3. Pods for the removed groups disappear from the in-memory state and status
   reflects the new width.

## Interaction with the resolver

Structural shrink (triggered by the oversubscription resolver) still happens
first when Reservations activate. The new width tracking means the controller
can reconcile back to the desired width after the deficit clears, and reporting
shows deterministic shrink versus lottery outcomes.

## What’s next

- A `kubectl runs shrink` helper will wrap the `desiredTotalGPUs` patch.
- Metrics (`elastic_grows_total`, `elastic_shrinks_total`,
  `elastic_width_current`) will follow in M9.
