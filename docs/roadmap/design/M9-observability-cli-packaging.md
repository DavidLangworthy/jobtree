# M9 — Observability, CLI polish, packaging

## Summary
Deliver the operational tooling that makes the scheduler usable at scale: comprehensive metrics and dashboards, a polished `kubectl runs` plugin, and production-ready Helm/Kustomize distributions. This milestone completes the user and operator experience layer.

## Goals
- Expose Prometheus metrics covering admission latency, reservation backlogs, resolver activity, borrowing, and spare usage.
- Provide Grafana dashboards for SREs and program managers with drill-down views.
- Ship a full-featured `kubectl runs` plugin (with krew packaging) covering submission, planning, watch, explain, budgets, sponsors, shrink, etc.
- Harden deployment artifacts (Helm chart, Kustomize overlays) with configuration for HA, TLS, and webhook cert management.

## Non-goals
- Changing core scheduling semantics (handled in earlier milestones).
- Multi-cluster coordination (M10 is optional stretch).

## Inputs & Dependencies
- Metrics hooks from previous milestones (controllers, resolver, budget accounting).
- CLI command stubs introduced alongside earlier milestones.
- Deployment manifests from M0 (to be expanded).

## Architecture & Components
- **Metrics exporters:** Extend controllers to register Prometheus collectors; integrate with controller-runtime metrics registry.
- **Dashboards:** `deploy/grafana/` or JSONNet definitions capturing key charts (queue depth, deficits, preemption seed audits, borrowed GPU usage).
- **CLI plugin (`cmd/kubectl-runs`):** Cobra-based CLI with subcommands for submit, plan, watch, explain, shrink, budgets, sponsors, leases, who-pays, resolve.
- **Packaging:** Helm chart with templated values for feature flags, metrics service monitors, webhook cert manager; Kustomize overlays for dev/stage/prod.

## Detailed Design
1. **Metrics inventory**
   - Define metric names, types, and labels in a spec table (append to `docs/architecture/metrics.md`).
   - Ensure each controller exports latency histograms, success/failure counters, and gauges for active workloads/budgets.
2. **Dashboard content**
   - Build Grafana dashboards covering: admission pipeline, reservation backlog, resolver outcomes, borrowing, spares, elasticity, errors, and forecasts.
   - Provide templating by cluster/owner; include panels linking to CLI commands for deeper inspection.
3. **CLI UX**
   - Implement human-friendly table and watch output, colorized status, and JSON/YAML output modes for scripting.
   - Support interactive explanations (fetch resolver outcomes, lottery seeds, reason codes).
   - Provide completions (`bash`, `zsh`, `fish`) documented in `docs/cli/completions.md`.
   - Package plugin via krew manifest under `plugins/krew/runs.yaml`.
4. **Deployment hardening**
   - Helm chart: parameterize replicas, resources, service accounts, RBAC, TLS secrets, Notifier integration endpoints.
   - Provide example values for single-cluster and multi-cluster (M10 prep) deployments.
   - Add GitHub Actions release workflow to build/push container images and publish Helm chart + krew manifest.
5. **Documentation**
   - Update `README.md` quickstart to use Helm install and `kubectl runs` CLI flows.
   - Add operator runbooks in `docs/operator-guide/` for metrics, dashboards, upgrades, certificate rotation.

## Testing Strategy
- Unit tests for CLI commands (golden snapshots of table/JSON output).
- Integration tests verifying metrics endpoints expose expected series (using `promtool` or custom scrapes).
- Helm chart lint (`helm lint`), template tests, and kind-based smoke deployment pipeline.

## Observability & Telemetry
- Already part of goal; ensure metrics are scraped in CI environment (kind cluster with Prometheus).
- Provide alerting rules (PrometheusRule manifests) for SLA violations (reservation delay, high preemption rate, missing heartbeat).

## Rollout & Migration
- Publish Helm chart/krew plugin to internal registries; document upgrade procedures.
- Coordinate with SRE to install dashboards and alert rules.

## Risks & Mitigations
- **Metrics cardinality explosion:** Standardize label sets; cap per-run labels via sampling; document best practices.
- **CLI drift from API changes:** Add integration tests that run CLI against envtest cluster; fail CI if output changes unexpectedly.

## Open Questions
- Should we offer a hosted UI beyond CLI? Consider future milestone once CLI adoption measured.
- Do we need SLO dashboards segmented by business unit? Determine based on stakeholder feedback.
