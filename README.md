# jobtree

Kubernetes-native gang scheduling of job forests under time-scoped organizational budgets.

## Getting started

1. Run unit tests and build the controller binaries:

   ```bash
   go test ./...
   go build ./cmd/manager
   go build -o kubectl-runs ./cmd/kubectl-runs
   ```

2. Explore the API using the samples in `config/samples/` and the simulated CLI (`kubectl runs --state cluster.yaml plan <run>`).

3. Deploy the observability stack with Helm:

   ```bash
   helm install jobtree ./deploy/helm/gpu-fleet --namespace jobtree-system --create-namespace
   ```

   For overlays, use the Kustomize bundles under `deploy/kustomize/`.

## Roadmap snapshot

The full roadmap lives in [`docs/roadmap/milestones.md`](docs/roadmap/milestones.md). A quick summary of the current state:

- [x] **M0 – Repository bootstrap & CRD shells**
- [x] **M1 – Budget accounting engine**
- [x] **M2 – Topology discovery & group-aware packing**
- [x] **M3 – Binder & Leases (runs that can start immediately)**
- [x] **M4 – Reservations & forecasting**
- [x] **M5 – Oversubscription resolver**
- [x] **M6 – Failure handling & hot spares**
- [x] **M7 – Elastic runs (INCR) & voluntary shrink**
- [x] **M8 – Co-funded runs (borrowing)**
- [x] **M9 – Observability, CLI polish, packaging**
- [ ] **M10 – Multi-cluster aggregate caps (stretch)**

## Repository layout (planned)

The long-term directory structure is documented in [`docs/architecture/directory-tree.md`](docs/architecture/directory-tree.md). As milestones land, the repository will grow toward that shape.

## Project governance & docs

- Maintainers and escalation paths: [`MAINTAINERS.md`](MAINTAINERS.md)
- First-class readiness checklist: [`docs/project/first-class-readiness.md`](docs/project/first-class-readiness.md)
- Read the Docs rollout plan: [`docs/website/readthedocs.md`](docs/website/readthedocs.md)
- Cluster visualization guide: [`docs/visualizations/cluster-allocation.md`](docs/visualizations/cluster-allocation.md)
- Researcher budget UX spec: [`docs/product/researcher-budget-ux.md`](docs/product/researcher-budget-ux.md)
