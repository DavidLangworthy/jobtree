# jobtree

[![Docs](https://img.shields.io/badge/docs-readthedocs-blue)](https://jobtree.readthedocs.io)

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

## Testing

- `go test ./...` — unit tests; no external dependencies.
- `make envtest` — integration tests against a real `kube-apiserver` (no kubelet): real reconcilers, real webhooks, real watches. See `controllers/kube/scenario_test.go`.
- `make antifake` — the anti-fake lint gates (`hack/antifake/`): fail the build if a `*_test.go` hand-sets a workload Pod's terminal `.Status.Phase` without a documented, ratcheted exception, or if an `api/v1` field ships with zero readers outside `api/v1`.
- `make e2e` — the kind e2e harness: a real cluster, a real built-and-loaded manager image, the real Helm chart (with real webhook certs), and a real kubelet. Requires `kind`, `docker`, and `helm` on `PATH` (`make kind-up`/`make kind-down` drive the cluster directly). See [`docs/project/testing-and-simulation.md`](docs/project/testing-and-simulation.md) for exactly what this tier proves and what it still cannot: there is no real workload container yet (`RunSpec` has no image/command field), so the completion/follow e2e cases in `test/e2e/` are documented, deliberate skips until that lands.

## Roadmap snapshot

The full roadmap lives in [`docs/roadmap/milestones.md`](docs/roadmap/milestones.md). A quick summary of the current state:

- [x] **M0 – Repository bootstrap & CRD shells**
- [x] **M1 – Budget accounting engine**
- [x] **M2 – Topology discovery & group-aware packing**
- [x] **M3 – Binder & Leases (runs that can start immediately)**
- [x] **M4 – Reservations & forecasting**
- [x] **M5 – Oversubscription resolver**
- [x] **M6 – Failure handling & hot spares** — spare swaps run on node-failure events; the end-to-end fault-injection suite is still pending ([R28](docs/project/remediation-plan.md))
- [x] **M7 – Elastic runs (INCR) & voluntary shrink**
- [x] **M8 – Co-funded runs (borrowing)**
- [x] **M9 – Observability, CLI polish, packaging** — the Helm chart provisions webhook serving and scoped RBAC, and CI renders and asserts it
- [ ] **M10 – Multi-cluster aggregate caps (stretch)**

## Repository layout (planned)

The long-term directory structure is documented in [`docs/architecture/directory-tree.md`](docs/architecture/directory-tree.md). As milestones land, the repository will grow toward that shape.

## Documentation

- Hosted site: [jobtree.readthedocs.io](https://jobtree.readthedocs.io) (MkDocs + Material).
- Preview locally: `pip install -r docs/requirements.txt && mkdocs serve`.
- Maintainer and authorship: [`MAINTAINERS.md`](MAINTAINERS.md)
- Security reporting (no email needed): [`SECURITY.md`](SECURITY.md)
- **Licence:** none. The source is public to read and discuss; [no rights are granted](LICENSE).
- First-class readiness checklist: [`docs/project/first-class-readiness.md`](docs/project/first-class-readiness.md)
- Read the Docs implementation notes: [`docs/website/readthedocs.md`](docs/website/readthedocs.md)
- Cluster visualization guide: [`docs/visualizations/cluster-allocation.md`](docs/visualizations/cluster-allocation.md)
- Researcher budget UX spec: [`docs/product/researcher-budget-ux.md`](docs/product/researcher-budget-ux.md)
