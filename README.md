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

This milestone provides strongly-typed CRD definitions, defaulting and validation logic, and continuous integration that enforces formatting and runs unit tests.
