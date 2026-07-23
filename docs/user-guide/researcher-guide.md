# Researcher Guide

Jobtree’s goal is to make **ML researchers extremely productive** while keeping GPU fleets busy
and accountable. This guide walks through the workflows you will use every day—from quick
experiments to multi-thousand GPU trainings that need Reservations, elasticity, and
family-funded borrowing.

> **Tip:** Everything here works with `kubectl runs …` (our dedicated kubectl plugin) or by
> applying YAML manifests with standard `kubectl`. The kubectl plugin keeps the UX minimal
> while still exposing every planner/scheduling decision. By default `kubectl runs` talks to
> your current kube-context like any other kubectl plugin — `submit` really creates the Run
> object. A Run's pods run your own container (`spec.roles[].template`) and request real
> `nvidia.com/gpu`; the controller manager requests their width (creating them as real,
> unscheduled pods, `schedulerName: jobtree`) and forecasts, while the **jobtree scheduler
> plugin** — a kube-scheduler-framework plugin, the sole committer of GPU funding — places each
> pod and mints its Lease at bind time. Pass `--local` (or `--dry-run`) to instead drive an
> in-process, offline `cluster-state.json` simulator that models both the controller's and the
> scheduler plugin's decisions for docs/demos; see
> [docs/cli/kubectl-runs.md](../cli/kubectl-runs.md).

## 1. Required context

* **Budgets** – Your team receives one or more envelopes with a concurrency cap and (optionally)
  `maxGPUHours`. Every Lease is paid for by exactly one envelope.
* **Runs** – Describe the resources you want (`gpuType`, `totalGPUs`), the real workload that
  should run (`spec.roles[]`: a pod `template` carrying your container image/command, `width`
  in pods, and `gpusPerPod`), and optional advanced features: `groupGPUs`, `malleable`, and
  per-group `spares`. A Run with no `roles` still works — it gets a default placeholder
  container — but `roles` is how you run your own code.
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
  roles:
    - name: trainer
      width: 8              # one pod per GPU
      gpusPerPod: 1
      template:
        spec:
          containers:
            - name: workload   # the GPU-target container; jobtree injects nvidia.com/gpu here
              image: ghcr.io/rai-sys/resnet-trainer:2026.06
              command: ["python", "-m", "train", "--config", "resnet-8gpu.yaml"]
  runtime:
    checkpoint: "10m"
```

`width * gpusPerPod` must equal `resources.totalGPUs`. `template` is your ordinary
`PodTemplateSpec` — image, command, env, volumes, resources are all yours; jobtree only
overlays the scheduling-owned fields (`schedulerName`, the `nvidia.com/gpu` request on the
`workload` container, gang labels, and `restartPolicy: Never` so a real `Succeeded` pod is a
trustworthy completion signal).

```bash
kubectl runs submit -f resnet-small.yaml
kubectl runs watch resnet-small
kubectl runs leases resnet-small  # show who paid for each GPULease
```

The controller checks the demand against your team's Budget and requests placement for the 8
GPUs on one NVLink island by creating real, unscheduled pods that run your `resnet-trainer`
container. The jobtree scheduler plugin places and funds them immediately — it schedules each
pod and mints its Lease at bind time. No Reservation is created because the Run fit right away.

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
# EarliestStart: 2025-11-01T14:05:00Z
# Deficit: 48 GPUs
# Confidence: conservative
# Remedies: Reclaim unfunded capacity in scope; Drop spares in scope; Shrink elastic runs by step size; Run fair lottery if deficit remains
```

`EarliestStart` scales with the size of the deficit (a bigger shortfall gets a longer estimate,
not the same fixed window every time); `Remedies` lists only the structural steps the resolver
would actually find something to do for — reclaiming unfunded capacity, dropping spares, or
shrinking elastic runs — plus the fair lottery, which is always the last resort. At
`earliestStart`, the controller resolves the deficit in that order and starts the Run. There is no
`conflictSet` or "kill probability" field — those never existed on any type; the closest real
signal is the resolver's lease closure reason (visible via `kubectl runs explain`) and a `Warning`
event on the affected Run once a cut actually happens.

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

Declare `spares: 1` (or more) per group. Spare concurrency and GPU-hours are tracked as a
separate, visible bucket in Budget/Run status (so you can see how much of your footprint is
spare vs. active) but are **charged at the same rate as active GPU-hours** — there is no billing
discount today. They may host short opportunistic work until you need them. When a failure
occurs:

1. The controller ends the opportunistic Leases with reason `ReclaimedBySpare`.
2. Your active ranks resume on the spare nodes with zero topology changes.
3. The failed node is cordoned for later repair.

## 7. Chaining runs (follow)

To run stages in order — data prep, then training, then evaluation — give each run a `follow` list of
the runs it must wait for. A follower stays in the `Waiting` phase (visible in `kubectl runs explain`)
until **every** run it follows reaches `Completed`, then it enters normal admission.

```bash
kubectl runs submit --file prep.json
kubectl runs submit --file train.json --follow prep
kubectl runs submit --file eval.json  --follow train
```

Notes:

* Runs complete when their workload pods finish; a completed run releases its GPUs and stops charging
  its budget. `kubectl runs complete <run>` is a `--local`-only convenience that marks a run
  finished in the offline simulator; it has no live-cluster equivalent because the CLI does not
  drive completion of a real Run — the controller does, from real pod status.
* If an upstream **fails**, by default the follower waits a grace window (so you can fix and resubmit
  just that stage) and then fails with a clear message — it will not silently hang forever. Set
  `follow.onUpstreamFailure: fail` to fail followers immediately instead, or
  `follow.upstreamFailureGrace` to change the window.
* Follow is same-namespace and all-must-complete (`AND`). There is no branching/parallel combinator
  language — compose workflows from runs and follow edges.

## 8. Waiting on a run from a script

`status.phase` is a human convenience; `status.conditions` is what to script
against, and `kubectl wait` speaks it directly:

```bash
kubectl wait --for=condition=Running   run/train --timeout=30m
kubectl wait --for=condition=Completed run/train --timeout=24h
```

When a run is not starting, the reason is the machine-readable answer, and it is
worth reading before you change anything:

```bash
kubectl get run train -o jsonpath='{.status.conditions[?(@.type=="Admitted")].reason}'
```

`Unfunded` means quota (your envelope cannot cover the width — borrow, shrink, or
wait for a window). `Unschedulable` means capacity (no placement exists — check
flavor and `groupGPUs`). `GangForming` means it is nearly there and assembling.
`FollowWait` means it is not your run's problem at all: an upstream has not
finished. Those are four different next actions, and `Pending` alone does not
tell them apart. `kubectl runs explain <run>` prints the same reason.

See [concepts/runs.md](../concepts/runs.md#status-conditions-and-the-phase-derived-from-them)
for the full condition vocabulary.

## 9. Checklist before you submit

* Define `spec.roles[].template` with your real container image/command; `width * gpusPerPod`
  must equal `resources.totalGPUs`.
* Pick the right `groupGPUs` for your communication pattern.
* Declare `checkpoint` so the system knows when it is safe to requeue.
* Use `malleable` for any job that can tolerate elastic width.
* Add `funding.sponsors` when you expect to borrow.
* Use `follow` to order dependent stages; a follower waits for its upstreams to complete.
* Watch `kubectl runs plan <run>` after submission—Reservations are transparent and auditable.

With these primitives you can scale from laptop-sized experiments to multi-cluster trainings while
keeping GPU usage accountable and well-planned.
