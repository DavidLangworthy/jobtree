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

## Milestone status

- ✅ **M0 – Repository bootstrap & CRD shells:** Implemented strongly typed CRD definitions, defaulting/validation logic, and CI that enforces formatting and runs unit tests.
- ⏳ **M1+ – Budget accounting, placement, binder, reservations, oversubscription, spares, elasticity, co-funding, observability:** Not yet started in this repository snapshot.

This milestone provides strongly typed CRD definitions, defaulting and validation logic, and continuous integration that enforces formatting and runs unit tests.
