# jobtree

Kubernetes-native gang scheduling of job forests under time-scoped organizational budgets.

## Getting started

1. Install the custom resource definitions (CRDs) once they are published (coming in a later milestone).
2. Build the controller manager:

   ```bash
   make test
   go build ./cmd/manager
   ```

3. Explore the API using the samples in `config/samples/`.

## Roadmap snapshot

The full roadmap lives in [`docs/roadmap/milestones.md`](docs/roadmap/milestones.md). A quick summary of the current state:

- [x] **M0 – Repository bootstrap & CRD shells**
- [x] **M1 – Budget accounting engine**
- [ ] **M2 – Topology discovery & group-aware packing**
- [ ] **M3 – Binder & Leases (runs that can start immediately)**
- [ ] **M4 – Reservations & forecasting**
- [ ] **M5 – Oversubscription resolver**
- [ ] **M6 – Failure handling & hot spares**
- [ ] **M7 – Elastic runs (INCR) & voluntary shrink**
- [ ] **M8 – Co-funded runs (borrowing)**
- [ ] **M9 – Observability, CLI polish, packaging**
- [ ] **M10 – Multi-cluster aggregate caps (stretch)**

## Repository layout (planned)

The long-term directory structure is documented in [`docs/architecture/directory-tree.md`](docs/architecture/directory-tree.md). As milestones land, the repository will grow toward that shape.
