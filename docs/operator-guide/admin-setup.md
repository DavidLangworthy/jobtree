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

```bash
helm repo add jobtree https://davidlangworthy.github.io/jobtree
helm install jobtree deploy/helm/gpu-fleet \
  --namespace jobtree-system --create-namespace \
  --set image.tag=$(git rev-parse --short HEAD)
```

What gets installed:

* CRDs for Budget, Run, Reservation, and Lease.
* Controller manager Deployment (scheduler/binder + webhooks).
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

`kubectl runs budgets usage --owner org:ai:rai:sys` shows headroom in real time.

## 5. Validate the workflow end-to-end

* Submit `config/samples/runs/run-128-groups.yaml` and watch Leases appear.
* Submit `config/samples/runs/run-cofunded-128.yaml` to verify sponsor flows.
* Use `config/samples/reservations/reservation-example.yaml` to simulate activation and lottery.

## 6. Observability checklist

* `/metrics` exposes counters and histograms for submissions, admission time, Reservations, and
  lottery outcomes.
* Import `deploy/grafana/dashboards/observability.json` into Grafana for ready-made panels:
  - Budget headroom per envelope
  - Reservation backlog + ETA buckets
  - Preemption rate broken down by scope
* Tail controller logs for attested seeds:

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
| Runs stuck in `Pending` | `kubectl runs plan <run>` for deficit + remedies; verify budgets have headroom. |
| Reservations missing ETAs | Ensure `forecast` controller is running; check metrics `jobtree_forecast_latency_seconds`. |
| Unexpected preemptions | Review `resolver` logs for the seed + conflict set; use `kubectl runs explain`. |
| Borrowing denied | Confirm lending envelopes set `lending.allow=true` and borrowers request ≤ `maxBorrowGPUs`. |

With these steps your cluster is ready for external researchers and internal teams alike.
