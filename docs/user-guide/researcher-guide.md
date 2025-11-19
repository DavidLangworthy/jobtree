# Researcher Guide

Jobtree’s goal is to make **ML researchers extremely productive** while keeping GPU fleets busy
and accountable. This guide walks through the workflows you will use every day—from quick
experiments to multi-thousand GPU trainings that need Reservations, elasticity, and
family-funded borrowing.

> **Tip:** Everything here works with `kubectl runs …` (our dedicated plugin) or by applying
> YAML manifests with standard `kubectl`. The plugin keeps the UX minimal while still exposing
> every planner/binder decision.

## 1. Required context

* **Budgets** – Your team receives one or more envelopes with a concurrency cap and (optionally)
  `maxGPUHours`. Every Lease is paid for by exactly one envelope.
* **Runs** – Describe the resources you want (`gpuType`, `totalGPUs`) and optional advanced
  features: `groupGPUs`, `malleable`, and per-group `spares`.
* **Leases** – Immutable facts of GPU consumption. A Run may have many Leases, each funded by a
  specific envelope (or sponsor when borrowing).
* **Reservations** – Plans created automatically when the system cannot admit a Run immediately.
  You’ll see an ETA plus the deficit and remedies.

## 2. Quick start (8-GPU experiment)

```yaml
apiVersion: rq.davidlangworthy.io/v1
kind: Run
metadata:
  name: resnet-small
  namespace: rai-sys
spec:
  owner: org:ai:rai:sys
  resources:
    gpuType: H100-80GB
    totalGPUs: 8
  runtime:
    checkpoint: "10m"
```

```bash
kubectl runs submit -f resnet-small.yaml
kubectl runs watch resnet-small
kubectl runs leases resnet-small  # show who paid for each Lease
```

The controller covers the demand with your team’s Budget, packs the 8 GPUs on one NVLink island,
and binds the pods immediately. No Reservation is created because the Run fit right away.

## 3. Scaling up (128 GPUs with groups of 32)

```yaml
spec:
  owner: org:ai:rai:sys
  resources:
    gpuType: H100-80GB
    totalGPUs: 128
  locality:
    groupGPUs: 32        # each model-parallel group stays on one island
  runtime:
    checkpoint: "30m"
  malleable:
    minTotalGPUs: 96
    maxTotalGPUs: 160
    stepGPUs: 32
  spares: 1               # per-group spare
```

* The binder fills one fast-fabric domain at a time (`pack-to-empty`).
* If fewer than 128 GPUs are available, the Run starts at 96 and auto-grows as headroom appears
  (`INCR`).
* Spares keep each group resilient: node failures swap onto hot standbys without losing model
  state.

Monitor elasticity:

```bash
kubectl runs watch trainer-128               # desired vs allocated width
kubectl runs shrink trainer-128 --by 32      # voluntary shrink to share capacity
```

## 4. Reservations when the cluster is busy

When cover+pack cannot satisfy the Run now, Jobtree writes a Reservation:

```bash
kubectl runs plan trainer-128
# earliestStart: 2025-11-01T14:05Z
# deficit: 48 GPUs in domain=B
# conflictSet: [run/mm1, run/research-demo]
# remedies: drop spares (32), shrink malleable (16)
```

The Run status shows the ETA, deficit, and kill probability. At `earliestStart`, the controller
resolves the deficit (drop spares → shrink → fair lottery) and starts the Run. You get advance
warning when you are in someone else’s conflict set.

## 5. Borrowing GPUs to finish early

If your family hierarchy is out of quota, you can list sponsors:

```yaml
spec:
  funding:
    allowBorrow: true
    maxBorrowGPUs: 32
    sponsors:
      - org:ai:mm:vision
```

* Cover tries your own envelopes first, then siblings/parents, then sponsors that permit lending.
* Borrow caps are enforced per Run and per lending envelope.
* `kubectl runs budgets usage` shows the split between owned and borrowed Leases.

## 6. Productive spares and opportunistic fill

Declare `spares: 1` (or more) per group. They are accounted at a discount and may host short
opportunistic work until you need them. When a failure occurs:

1. The controller ends the opportunistic Leases with reason `ReclaimedBySpare`.
2. Your active ranks resume on the spare nodes with zero topology changes.
3. The failed node is cordoned for later repair.

## 7. Checklist before you submit

* Pick the right `groupGPUs` for your communication pattern.
* Declare `checkpoint` so the system knows when it is safe to requeue.
* Use `malleable` for any job that can tolerate elastic width.
* Add `funding.sponsors` when you expect to borrow.
* Watch `kubectl runs plan <run>` after submission—Reservations are transparent and auditable.

With these primitives you can scale from laptop-sized experiments to multi-cluster trainings while
keeping GPU usage accountable and well-planned.
