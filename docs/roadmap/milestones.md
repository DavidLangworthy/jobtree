# Milestone roadmap

!!! warning "Read this before the checkboxes below"

    **A ticked box on this page means "scope built and unit-tested." It does not
    mean correct, and it does not mean production-ready.**

    A twelve-perspective, code-grounded audit in July 2026
    (`docs/project/design-vs-implementation-audit.md`) found that the newly
    load-bearing parts of the system — the scheduler plugin that is the sole
    committer of GPU funding — carried the repository's worst and least-audited
    failure semantics. Several milestones below were ticked while defects in the
    behaviour they claim were still present. Some still are.

    **The authoritative status is the remediation board**, in the repository
    under `docs/project/remediation/README.md`, sized and sequenced in
    `docs/project/remediation/SIZING.md`. Known-open defects are tracked there,
    including ones that can corrupt a running job.

    This page is kept as a record of what was *scoped and constructed*, milestone
    by milestone, with its false claims corrected rather than deleted. Where a
    milestone's stated outcome is not yet true, it now says so.

The project was organized into progressive milestones. Each entry outlines scope,
definition of done, validation, and artifacts.

- [x] **M0 — Repository bootstrap & CRD shells**
  - **Scope:** Establish the Go module, initial CI, and strongly typed CRDs for Budget, Run, Reservation, and Lease with defaulting/validation logic and sample manifests.
  - **Definition of done:** `kubectl` can list the CRDs, webhooks enforce basic invariants, and CI runs `go test ./...` and `gofmt`.
  - **Validation:** envtest coverage for schema validation; CI status badge green.
  - **Artifacts delivered:** `api/v1` type definitions, sample manifests under `config/samples/`, bootstrap README instructions.
  - **Design doc:** [docs/roadmap/design/M0-bootstrap-crd-shells.md](design/M0-bootstrap-crd-shells.md)

- [x] **M1 — Budget accounting engine**
  - **Scope:** Implement concurrency and GPU-hour accounting per envelope, aggregate caps, and lending ACLs; expose headroom metrics.
  - **Definition of done:** Budget controller reconciles live Budget objects, updating status/metrics; cover library returns funding plans respecting family sharing and lending limits.
  - **Validation:** Unit tests cover headroom math, aggregate caps, lending guardrails; controller tests validate status and metrics.
  - **Artifacts delivered:** `pkg/cover`, `controllers/budget_controller.go`, documentation under `docs/concepts/budgets.md`. *(Correction: `pkg/budget` no longer exists. The accounting engine was replaced by `pkg/funding` — one pure `Evaluate` that derives the four funding classes from leases, budgets and the clock, and stores nothing. See `docs/project/quota-semantics.md`.)*
  - **Design doc:** [docs/roadmap/design/M1-budget-accounting-engine.md](design/M1-budget-accounting-engine.md)

- [x] **M2 — Topology discovery & group-aware packing**
  - **Scope:** Build topology indexes from node labels and pack groups to fast-fabric domains using pack-to-empty heuristics.
  - **Definition of done:** Packing library produces deterministic placement plans honoring `groupGPUs` across heterogeneous clusters.
  - **Validation:** Unit tests over synthetic topologies; fuzzing for fragmentation scenarios.
  - **Artifacts delivered:** `pkg/topology`, `pkg/pack`, placement documentation in `docs/concepts/runs.md` and worked example extensions.
  - **Design doc:** [docs/roadmap/design/M2-topology-and-packing.md](design/M2-topology-and-packing.md)

- [x] **M3 — Binder & Leases (runs that can start immediately)**
  - **Scope:** Materialize pod manifests for feasible runs, emit Lease objects, and stitch cover/pack outputs into a runnable plan.
  - **Definition of done:** Deterministic binder splits funding segments across node allocations, and the run controller admits a pending run using the in-memory cluster state while updating Run status. *(Superseded: the run controller no longer commits funding. The scheduler plugin is the **sole committer** — it gang-gates at `Permit` and mints one `Lease` per pod at `PreBind`. `pkg/binder.Materialize` survives only for the legacy path and the parity tests.)*
  - **Validation:** Unit tests for binder split/validation logic and controller admission over a synthetic topology.
  - **Artifacts delivered:** `pkg/binder`, `controllers/run_controller.go`, `docs/user-guide/quickstart.md` updates summarizing the immediate-start path.
  - **Design doc:** [docs/roadmap/design/M3-binder-and-leases.md](design/M3-binder-and-leases.md)

- [x] **M4 — Reservations & forecasting**
  - **Scope:** Store intended slices for deferred runs, protect windows from unsafe backfill, and generate user-facing forecasts/remedies.
  - **Definition of done:** Runs lacking immediate capacity gain Reservations with concrete earliest start timestamps and deficit summaries exposed on Run/Reservation status.
  - **Validation:** Unit tests for the forecast planner and topology helpers; controller tests proving reservations appear for both capacity shortfall and future budget windows; `go test ./...` in CI.
  - **Artifacts delivered:** `pkg/forecast`, reservation planning in `controllers/run_controller.go`, updated worked examples, and a user guide in `docs/user-guide/reservations.md`.
  - **Design doc:** [docs/roadmap/design/M4-reservations-and-forecasting.md](design/M4-reservations-and-forecasting.md)

- [x] **M5 — Oversubscription resolver**
  - **Scope:** Implement structural cuts (spares then INCR shrink) followed by the attested two-stage lottery to resolve deficits.
  - **Definition of done:** Reservation activation under deficit resolves deterministically and publishes seed/conflict set for audit.
  - **Validation:** Resolver unit tests cover spares, shrink ordering, and deterministic lotteries; controller test exercises reservation activation that preempts/shrinks existing runs before binding.
  - **Artifacts delivered:** `pkg/resolver`, resolver integration in `controllers/run_controller.go`, enhanced lease status fields, updated worked examples, and documentation in `docs/architecture/oversubscription.md`.
  - **Design doc:** [docs/roadmap/design/M5-oversubscription-resolver.md](design/M5-oversubscription-resolver.md)

- [x] **M6 — Failure handling & hot spares** *(partial — see gap)*
  - **Scope:** Support per-group spares, opportunistic filler workloads, and deterministic spare swaps on node failure.
  - **Definition of done:** Runs configured with spares survive node failures without losing world-size; opportunistic tenants are reclaimed cleanly. *Partially met:* the node reconciler drives `HandleNodeFailure` on node watch events (unit + envtest coverage). **Not met, and unsafe today:** a `kubectl cordon` is misread as a node failure and triggers a destructive swap while the original pod keeps running — two live copies of the same rank (**R21**); the reclaim sweep closes co-located runs' leases at node rather than slot granularity (**R22**); deleting a spare-only node leaks an immortal, budget-charging lease (**R25**). A failed workload pod is never noticed at all (**R8**). All four are open.
  - **Validation:** Unit tests around spare accounting and controller/envtest swap coverage. **Gap:** the end-to-end fault-injection suite this milestone originally claimed does not exist — the Bridge apply is not atomic and partial API failures can strand states (tracked as **R28** in `docs/project/remediation-plan.md`).
  - **Artifacts delivered:** Spare-handling logic in `controllers/run_controller.go` and the node watch in `controllers/kube/reconcilers.go`. There is no `pkg/policy` package — the spare/opportunistic logic lives in the run controller; the earlier reference was aspirational. Docs in `docs/user-guide/spares-and-fill.md`.
  - **Design doc:** [docs/roadmap/design/M6-failure-and-spares.md](design/M6-failure-and-spares.md)

- [x] **M7 — Elastic runs (INCR) & voluntary shrink**
  - **Scope:** Enable malleable runs to scale within `[minTotalGPUs, maxTotalGPUs]` using `stepGPUs`, including voluntary shrink via the Run spec.
  - **Definition of done:** Elastic runs expand when headroom exists, shrink deterministically when desired width drops, and record width/pending state in status.
  - **Validation:** Controller tests covering growth and voluntary shrink, binder unit tests for group index offsets, and repository-wide `go test ./...`.
  - **Artifacts delivered:** Elasticity logic in `controllers/run_controller.go`, updated `pkg/binder` lease materialisation, Run status width tracking, new examples/tests, and docs in `docs/user-guide/elastic-runs.md`.
  - **Design doc:** [docs/roadmap/design/M7-elastic-runs-and-shrink.md](design/M7-elastic-runs-and-shrink.md)

- [x] **M8 — Co-funded runs (borrowing)**
  - **Scope:** Enforce lending ACLs, borrower guardrails, sponsor ordering, and expose per-run funding splits so usage is attributable across teams.
  - **Definition of done:** Runs start with a mix of owned and borrowed leases; `Run.status.funding` reports the split and budgets record borrowed GPU-hours.
  - **Validation:** Unit tests for lending limits/ACLs, controller tests covering co-funded admission and borrow-limit reservations, forecast tests for borrow-limit messaging.
  - **Artifacts delivered:** Lending enhancements in `pkg/cover`, funding summaries in `controllers/run_controller.go`, new docs (`docs/user-guide/cofunded-runs.md`), worked example updates, and lending samples under `config/samples/`.
  - **Design doc:** [docs/roadmap/design/M8-cofunded-runs.md](design/M8-cofunded-runs.md)

- [x] **M9 — Observability, CLI polish, packaging**
  - **Scope:** Deliver Prometheus metrics, Grafana dashboards, a user-friendly `kubectl runs` plugin, and Helm/Kustomize bundles. *(Correction: the bundles are **not** production-ready. The release pipeline builds no container images at all, so the chart points at tags that were never pushed (**R15**); the `ServiceMonitor`'s selector matched no Service, so nothing was scraped (**R16**, fixed 2026-07-09); and the production overlay ran three manager replicas with no leader-election key in `values.yaml` (**R17**, fixed 2026-07-09). The release pipeline still builds no images.)*
  - **Definition of done:** Metrics exported via `pkg/metrics`, dashboards packaged with Helm, CLI covers submit/plan/watch/explain/budgets/sponsors/shrink/leases/completions, and Helm/Kustomize templates deploy the stack.
  - **Validation:** CLI golden tests under `cmd/kubectl-runs/cmd/root_test.go`; `go test ./...` executes metrics assertions; the Helm chart is linted **and** rendered with `helm template` in CI, which asserts scoped RBAC (no wildcards), the webhook serving configuration, and health probes (R22/R29). The release workflow cross-builds the CLI and produces an installable krew manifest with per-archive checksums (R23).
  - **Artifacts delivered:** `pkg/metrics`, CLI under `cmd/kubectl-runs`, Helm chart in `deploy/helm/gpu-fleet` (now provisions webhook certs/Service/configurations and probes so the deployed manager admits objects), Kustomize overlays in `deploy/kustomize/`, Grafana dashboards in `deploy/grafana/`, Prometheus rules in `deploy/prometheus/`, krew manifest in `plugins/krew/`, docs in `docs/architecture/metrics.md`, `docs/cli/kubectl-runs.md`, and `docs/operator-guide/observability.md`. *Packaging gaps (wildcard RBAC, unbuildable krew manifest, chart that could not serve webhooks) closed by R22/R23/R29.* The elasticity metrics this entry originally left as "will follow" (`elastic_grows_total`/`elastic_shrinks_total`/`elastic_width_current`) now exist and are emitted from `growRun`/`shrinkRun`'s real success points, asserted via `metrics.Snapshot()` — M9 no longer contradicts `elastic-runs.md`.
  - **Design doc:** [docs/roadmap/design/M9-observability-cli-packaging.md](design/M9-observability-cli-packaging.md)

- [ ] **M10 — Multi-cluster aggregate caps (stretch)**
  - **Scope:** Enforce aggregate caps across clusters via a central reconciler consuming Lease streams and orchestrating coordinated re-plans.
  - **Definition of done:** Aggregate cap breaches trigger coordinated remedies across clusters without violating per-cluster invariants.
  - **Validation:** Multi-cluster e2e across two kind clusters; unit tests for cap math and coordination logic.
  - **Artifacts expected:** Cross-cluster controller components, documentation in `docs/design/calculus.md`, ops guide for multi-cluster setup.
  - **Design doc:** [docs/roadmap/design/M10-multicluster-aggregate-caps.md](design/M10-multicluster-aggregate-caps.md)
