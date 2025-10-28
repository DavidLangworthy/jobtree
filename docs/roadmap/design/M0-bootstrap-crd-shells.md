# M0 — Repository bootstrap & CRD shells

## Summary
Establish the foundational repository structure, Go module, continuous integration guardrails, and strongly typed CustomResourceDefinitions (CRDs) for Budget, Run, Reservation, and Lease. This milestone creates the scaffolding required for every later controller and library in the RQλ stack.

## Goals
- Create a reproducible Go workspace with module metadata, lint/test automation, and container build entrypoints.
- Define versioned API types (`api/v1`) that faithfully encode the RQλ calculus entities: Budget envelopes, Run specs, Reservation plans, and immutable Lease events.
- Provide defaulting and validation webhooks to guarantee schema invariants before controllers exist.
- Ship sample manifests and documentation so operators and researchers can experiment with the data model immediately.

## Non-goals
- Implement reconciliation or controller logic (handled in later milestones).
- Deliver runtime scheduling behavior, forecasts, or packing decisions.
- Provide production-grade observability or CLI tooling (placeholder references only).

## Inputs & Dependencies
- Kubernetes 1.29+ API machinery (controller-runtime, kube-builder conventions).
- The RQλ calculus specification for schema fields and invariants.
- Organizational metadata (module path `github.com/davidlangworthy/jobtree`, API group `rq.davidlangworthy.io`).

## Architecture & Components
- **APIs:** `api/v1` Go package housing Budget, Run, Reservation, Lease structs with kubebuilder markers for CRD generation.
- **Admission Webhooks:** Defaulting and validating webhooks under `controllers/webhooks/` to enforce invariants like `maxGPUHours ≤ concurrency × window length`, immutable spec sections, and well-formed locality/malleability settings.
- **Controller Manager:** `cmd/manager/main.go` boots controller-runtime manager with webhook server; reconciliation loops are stubbed for now.
- **Configuration:** `config/` manifests (CRDs, RBAC, manager deployment, sample instances).
- **Tooling:** `Makefile` for lint/test/codegen; `.github/workflows/ci.yaml` running `go test ./...` and format checks.

## Detailed Design
1. **Go module initialization**
   - `go mod init github.com/davidlangworthy/jobtree`.
   - Depend on `sigs.k8s.io/controller-runtime`, `k8s.io/apimachinery`, and the standard kubebuilder test harness.
2. **API type definitions**
   - Budget: envelopes with selectors, concurrency/integral caps, aggregate caps, lending ACL stubs.
   - Run: resource requests, grouping, malleability (`INCR`), spares, funding (borrow) hints.
   - Reservation: run reference, intended slice (nodes or domain), paying envelope, earliest start.
   - Lease: immutable usage record capturing owner, component path, nodes, role, payer, and reason enum.
   - Shared helpers for windows, selectors, quantity validation in `api/v1/meta.go` and `runtime.go`.
3. **Validation & defaulting**
   - Default `allowCrossGroupSpread=true`, normalize durations, ensure optional structs are nil-safe.
   - Validate time ranges, ensure integral cap bounds, guard lending ACL semantics (if present).
4. **Sample manifests**
   - Provide canonical YAML examples under `config/samples/` for each CRD type illustrating typical usage.
5. **Documentation**
   - Document CRDs in `docs/concepts/overview.md` and roadmap references.
6. **CI & tooling**
   - Ensure `go test ./...` passes locally and in GitHub Actions.

## Testing Strategy
- Unit tests for schema validation (positive/negative cases) using `envtest` and direct type validation.
- Linting/formatting via `go test`, `go vet`, and `gofmt` (enforced in CI).

## Observability & Telemetry
- None in this milestone; future milestones will add metrics/logging. Provide logging hooks in manager for future use.

## Rollout & Migration
- Apply CRDs and webhook configuration via `kubectl apply -k config/default`.
- No data migration required; types are brand-new.

## Risks & Mitigations
- **Schema drift:** Mitigated by unit tests covering invariants and kubebuilder markers.
- **Webhook availability:** Ensure manager boots even with stub reconcilers; readiness/liveness probes stubbed but present.

## Open Questions
- Finalize enum naming for Lease reasons—current set matches RQλ spec but may evolve.
- Determine if additional admission-time constraints are needed for recurring budget windows (deferred to M1).
