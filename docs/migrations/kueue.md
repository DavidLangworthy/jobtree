# Jobtree for Kueue Users

Kueue popularized quota-aware scheduling on Kubernetes. Jobtree keeps the Kubernetes-native
experience while adding topology-aware packing, immutable Leases, and deterministic conflict
resolution. This page maps Kueue constructs to Jobtree so you can migrate confidently.

## 1. Concept mapping

| Kueue concept | Jobtree equivalent |
| --- | --- |
| ClusterQueue | Budget envelope or aggregate cap |
| LocalQueue + Workload | Namespace + Run CRD |
| ResourceFlavor | Node selector (`gpu.flavor`, `region`, `fabric.domain`) |
| Admission checks | Cover phase (family sharing + lending ACLs) |
| Borrowing across ClusterQueues | Sponsors/lending envelopes |
| Preemption policy | Structural cuts → fair lottery (scope = flavor + location) |
| Usage reports | Lease ledger (immutable; owner/envelope scoped) |

## 2. Submitting work

Kueue workload:

```yaml
apiVersion: kueue.x-k8s.io/v1beta1
kind: Job
spec:
  queueName: rai-gpu
  podSets:
    - name: trainer
      count: 32
      template: …
```

Jobtree Run:

```yaml
apiVersion: rq.davidlangworthy.io/v1
kind: Run
metadata:
  name: trainer
spec:
  owner: org:ai:rai:sys
  resources:
    gpuType: H100-80GB
    totalGPUs: 32
  locality:
    groupGPUs: 16
```

Jobtree handles the PodTemplate generation automatically based on the Run (binder materializes
pod specs per group).

## 3. Budgeting vs ClusterQueues

* ClusterQueues are global buckets. In Jobtree, **Budgets** encode both location selectors and
  concurrency/integral caps, and they live in a DAG for family sharing.
* Aggregate caps act like a ClusterQueue that spans envelopes; enforcement happens in the Budget
  controller.
* Borrowing is explicit: envelopes opt into lending, and Runs list sponsors plus guardrails.

## 4. Reservations & fairness

Kueue keeps workloads `Pending` until capacity frees up. Jobtree instead creates a Reservation with
an ETA, intended slice, and deficit forecast:

```bash
kubectl runs plan mm3
# earliestStart: 14:05
# intendedSlice: domain=B (nodes b01-b08)
# deficit: 60 GPUs
# remedies: shrink RAI-SYS (16), lottery conflict set [mm1]
```

At `earliestStart`, the resolver performs deterministic cuts followed by a public lottery with a
seed that you can verify later.

## 5. Topology awareness

Kueue’s ResourceFlavors pick labels, but the scheduler does not reason about island packing. Jobtree
packs group-by-group within a fast-fabric domain (`pack-to-empty`):

* Each `groupGPUs` chunk stays within one NVSwitch island.
* The binder fills one domain fully before spilling to the next, minimizing cross-island traffic.
* Spares are scheduled alongside active ranks, so failure recovery is deterministic.

## 6. Operational differences

| Topic | Kueue | Jobtree |
| --- | --- | --- |
| Auditing | Workload status | Lease CRD (immutable interval, payer, nodes) |
| Elasticity | Limited (requeue with new size) | Built-in `malleable` width + voluntary shrink |
| CLI | kubectl + `kueuectl` | `kubectl runs` plugin (plan/watch/explain/budgets) |
| Forecasting | Queue length estimates | Reservation ETA, deficit, kill probability, remedies |

## 7. Migration checklist

1. Convert ResourceFlavors into node labels if not already present (Jobtree reuses them).
2. Translate ClusterQueues into Budgets. Each queue becomes one or more envelopes with selectors,
   concurrency, `maxGPUHours`, and optional aggregate caps.
3. Update workloads to Run CRDs. Most data (GPU type/count, checkpoint, elasticity) maps 1:1.
4. Roll out the `kubectl runs` plugin to your users—they can inspect Reservations and lotteries.
5. Mirror your existing Prometheus/Grafana integration to Jobtree’s `/metrics` and dashboards.

Once migrated, researchers get clearer forecasts, auditable Leases, and topology-aware binding
without leaving Kubernetes.
