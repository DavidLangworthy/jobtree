# Cluster Administrator Guide

This guide is for platform engineers running Jobtree alongside Kubernetes. It covers
installation, topology labeling, budgeting, observability, and day-two tasks so you can deliver a
first-class experience to researchers.

## 1. Prerequisites

* Kubernetes 1.27+ with GPU nodes registered (NVIDIA or AMD vendor stacks work).
* `kubectl` access plus the ability to install CRDs, Deployments, and RBAC objects.
* Helm 3.12+ or `kustomize` for declarative installs.
* Metrics stack (Prometheus + Grafana) if you want dashboards out of the box.

## 2. Label the topology

Jobtree allocates by flavor and fast-fabric domain. Make the labels explicit:

```bash
kubectl label nodes gpu-a-[01-16] \
  region=us-west cluster=gpu-a fabric.domain=nv-a rack=r01 gpu.flavor=H100-80GB
kubectl label nodes gpu-b-[01-16] \
  region=us-west cluster=gpu-b fabric.domain=nv-b rack=r09 gpu.flavor=H100-80GB
```

* `fabric.domain` describes the fastest local interconnect (e.g., NVSwitch island).
* Use consistent casing so packers can compare strings cheaply.

## 3. Install the controller manager

There is **no Helm repository**. Every release publishes the packaged chart and the
`kubectl-runs` archives as assets on its GitHub release, and pushes the two container
images to GHCR under the same tag. Install the packaged chart:

```bash
VERSION=v0.1.0   # any published release tag
curl -fsSLO "https://github.com/DavidLangworthy/jobtree/releases/download/${VERSION}/gpu-fleet-${VERSION#v}.tgz"
helm install jobtree "gpu-fleet-${VERSION#v}.tgz" \
  --namespace jobtree-system --create-namespace \
  --set scheduler.enabled=true
```

A packaged chart carries the release tag as its `appVersion`, and the image tag defaults
to `appVersion`, so this pulls `ghcr.io/davidlangworthy/jobtree-{controller,scheduler}:$VERSION`
— images that release pushed. Nothing else needs pinning.

Installing from a **source checkout** (`deploy/helm/gpu-fleet`) is for development. The
chart's `appVersion` there is whatever is committed, which is not necessarily a tag that
was ever released, so name the images explicitly:

```bash
helm install jobtree deploy/helm/gpu-fleet \
  --namespace jobtree-system --create-namespace \
  --set image.tag=v0.1.0 \
  --set scheduler.enabled=true
```

`image.tag` sets the tag for every jobtree image; `controller.image.tag` and
`scheduler.image.tag` override it per component, and `controller.image.repository` /
`scheduler.image.repository` repoint them at a private registry (a mirror, or images you
built yourself with `docker build --target manager .` and `--target scheduler .`). For an
air-gapped cluster, mirror both images and set the two repositories.

What gets installed:

* CRDs for Budget, Run, Reservation, and Lease.
* Controller manager Deployment (admission, forecasting/Reservations, lifecycle, webhooks). It
  requests width by creating real, unscheduled workload pods (`schedulerName: jobtree`) — it does
  not place pods or mint Leases itself.
* A second Deployment running the **jobtree scheduler** (`cmd/scheduler`, the out-of-tree
  kube-scheduler-framework binary registering the `jobtree` plugin via a mounted
  `KubeSchedulerConfiguration`). It is the sole committer of GPU funding: it schedules every
  `schedulerName: jobtree` pod and mints the pod's Lease at bind time. Toggle it with
  `--set scheduler.enabled=true` (see `deploy/helm/gpu-fleet/templates/scheduler-deployment.yaml`);
  without it, workload pods created by the controller manager stay unscheduled forever.
* ServiceMonitor + dashboard ConfigMap if Prometheus/Grafana are present.

> Prefer `deploy/kustomize/*` for air-gapped clusters or when you need extra admission policy.

## 4. Seed budgets and families

1. Create a root Budget for the organization (`org:ai`).
2. Define child envelopes for each research group.
3. Add optional `lending` ACLs for teams willing to sponsor others.

```bash
kubectl apply -f config/samples/budgets/budget-west-h100.yaml
kubectl apply -f config/samples/budgets/budget-vision-lending.yaml
```

`kubectl runs budgets usage` shows headroom for every Budget in the namespace, straight from the
`BudgetReconciler`'s last-written `status.usage`/`status.headroom` (it does not recompute funding
client-side, so it can lag the manager's next reconcile by one interval). By default this talks
to the live cluster named by your kubeconfig; pass `--local` only for the offline simulator.

## 5. Validate the workflow end-to-end

* Submit `config/samples/runs/run-128-groups.yaml` and watch Leases appear.
* Submit `config/samples/runs/run-cofunded-128.yaml` to verify sponsor flows.
* Use `config/samples/reservations/reservation-example.yaml` to simulate activation and lottery.

## 6. Observability checklist

* `/metrics` exposes counters and histograms for submissions, admission time, Reservations,
  lottery outcomes, forecast latency (`jobtree_forecast_latency_seconds`), and elastic
  grow/shrink activity (`jobtree_elastic_grows_total`, `jobtree_elastic_shrinks_total`,
  `jobtree_elastic_width_current`).
* Import `deploy/grafana/dashboards/observability.json` into Grafana for ready-made panels:
  - Budget headroom per envelope
  - Reservation backlog + ETA buckets
  - Preemption rate broken down by scope
* The manager emits real Kubernetes Events (`EventRecorder`, not a log-only side channel) at
  admit/reserve/activate/resolver-action/node-failure-swap/complete:

```bash
kubectl get events --field-selector involvedObject.kind=Run
kubectl describe run <run>   # shows the same events inline
```

* The attested lottery/reclaim seed is embedded in the `Reason` of both the `ResolverAction`
  Warning event above and the closed Lease's `status.closureReason` (e.g.
  `RandomPreempt(0xdeadbeef...)`); the controller's structured logger also logs every Event at
  debug level, so grepping logs still works as a fallback:

```bash
kubectl logs deploy/jobtree-controller-manager -n jobtree-system | rg RandomPreempt
```

## 7. Day-two operations

* **Upgrades:** use Helm’s rolling upgrade or apply the new Kustomize overlay; CRDs are backward
  compatible and stored in Git.
* **Quota tuning:** edit Budget envelopes and rely on controller validation to enforce
  `maxGPUHours ≤ concurrency × window`.
* **Cluster growth:** label new nodes with the same topology keys; packers discover them
  automatically.
* **Audits:** query Lease objects (immutable) to reconstruct who used which GPUs at any time.

## 8. Troubleshooting quick hits

| Symptom | Action |
| --- | --- |
| Runs stuck in `Pending` with unscheduled workload pods | `kubectl get pods -l rq.davidlangworthy.io/run=<run>` — if they show `schedulerName: jobtree` and no `nodeName`, confirm the jobtree scheduler Deployment is installed and running (`scheduler.enabled=true`); without it nothing places or funds the pods. Otherwise `kubectl runs plan <run>` for deficit + remedies; verify budgets have headroom. |
| Reservations missing ETAs | `forecast.Plan` is an inline library call inside the `run` reconciler — there is no separate "forecast controller" process to check the liveness of. Check the `run` controller's logs/`jobtree_forecast_latency_seconds` metric instead. |
| Unexpected preemptions | `kubectl get events --field-selector involvedObject.name=<run>` for the `ResolverAction` Warning event (carries the seed and reason); there is no `conflictSet` field — use `kubectl runs explain` for the resolved Lease closure reasons. |
| Borrowing denied | Confirm lending envelopes set `lending.allow=true` and borrowers request ≤ `maxBorrowGPUs`. |

With these steps your cluster is ready for external researchers and internal teams alike.
