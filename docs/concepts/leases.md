# Leases

A **Lease** is the immutable fact that a Run consumed a slice of the cluster for a period of time.
Every controller, report, and audit folds over Leases to reconstruct history.

## 1. Schema recap

```yaml
apiVersion: rq.davidlangworthy.io/v1
kind: Lease
metadata:
  name: lease-abc-0007
spec:
  owner: org:ai:rai:sys
  runRef:
    name: train-128
  compPath: ["SHARD[1]", "D"]
  slice:
    nodes: [n17-04]
    role: Active        # or Spare/Borrowed
  interval:
    start: "2025-11-01T00:00:10Z"
  paidByEnvelope: west-h100
  reason: Start         # Start | Swap | Shrink | RandomPreempt | ReclaimedBySpare | Fail
status:
  closed: true
  endTime: "2025-11-01T03:33:29Z"
```

Key rules:

* A Lease never changes once written. Ending it appends `status.endTime` and `closed=true`.
* Each Lease lists exactly one payer (`paidByEnvelope`). Budgets are debited from there.
* `slice.role` communicates how the GPU was used:
  - `Active`: contributes to the Run’s desired width.
  - `Spare`: hot standby reserved for failover.
  - `Borrowed`: opportunistic workload temporarily occupying a spare.
* `reason` tells you why this Lease exists (start, swap, shrink, lottery, etc.).

## 2. Derived views

Controllers maintain indices on top of Leases:

* **Active index:** Leases with `status.closed=false` per node.
* **Usage index:** owner/envelope summaries for `u_e(t)` (concurrency) and `U_e(t)` (GPU-hours).
* **Run status:** aggregated width, payer split, and lifecycle events used by `kubectl runs watch`.

## 3. Auditing workflows

* “Who used GPU `n17-04` last night?” → `kubectl get leases --field-selector spec.slice.nodes=n17-04`.
* “How many GPU-hours did org:ai:mm:vision spend?” → integrate Lease durations where
  `paidByEnvelope` matches the sponsor.
* “Why was my job preempted?” → find the Lease that ended with reason `RandomPreempt(seed)` and
  correlate with the Reservation activation log (seed + conflict set).

## 4. Failure and spare swaps

When a node fails, spares take over without changing the Run topology:

1. Spare Lease ends with reason `Swap`.
2. Active Lease starts on the spare node with reason `Swap` and the same payer.
3. Opportunistic Leases running on that spare end with reason `ReclaimedBySpare`.

Every step is recorded so SREs can replay the sequence later.

## 5. Best practices

* Treat the Lease set as the source of truth—do not rely on pod logs alone.
* Retain Leases for as long as your compliance program requires (they are lightweight CR objects).
* Emit events or metrics derived from Leases rather than maintaining separate mutable state.

Leases keep the ledger honest and make it possible to answer “who ran where, when, and why?” years
after the fact.
