# M10 — Multi-cluster aggregate caps (stretch)

## Summary
Implement coordinated enforcement of aggregate budget caps across multiple clusters while retaining per-cluster binding. This optional stretch milestone introduces a central controller that consumes Lease streams from each cluster and orchestrates re-plans when global caps are threatened.

## Goals
- Ingest Lease events from all participating clusters into a central aggregator.
- Enforce aggregate caps defined in Budget CRDs (`aggregateCaps[]`) spanning envelopes across clusters.
- Trigger coordinated remedies (drop spares, shrink INCR, lotteries) in affected clusters without violating per-cluster invariants.
- Provide cross-cluster dashboards and alerts for global budget utilization.

## Non-goals
- Cross-cluster gang scheduling (runs still bind within a single cluster).
- Federated Kubernetes installation (assume independent clusters with connectivity to control plane APIs).

## Inputs & Dependencies
- All prior milestones (M0–M9) providing per-cluster functionality.
- Streaming or periodic replication of Lease records (e.g., via gRPC, HTTP SSE, or message bus).
- Secure service mesh or network connectivity between clusters and central aggregator.

## Architecture & Components
- **Aggregator controller (`cmd/aggregator`):** Consumes Lease streams, maintains global usage per envelope and aggregate cap.
- **Global state store:** Could be etcd, PostgreSQL, or CRD in a management cluster storing aggregated usage and directives.
- **Re-plan orchestrator:** Issues coordination requests to per-cluster controllers (via CRD `GlobalDirective` or API calls) instructing them to run resolver with specified scope.
- **Authentication/authorization:** mTLS between clusters; RBAC ensuring central controller can observe but not mutate unrelated resources.

## Detailed Design
1. **Lease ingestion**
   - Each cluster deploys an exporter that tails Lease events and sends them to aggregator (compressed JSON or protobuf).
   - Aggregator normalizes timestamps and updates global counters (`globalConcurrency`, `globalGPUHours`).
2. **Aggregate cap evaluation**
   - For each defined aggregate cap, sum usage across member envelopes (possibly across clusters) and compare against `maxConcurrency`/`maxGPUHours`.
   - When approaching threshold, emit warnings; when exceeding, initiate coordinated remedy.
3. **Coordinated remedy protocol**
   - Aggregator selects affected clusters and issues directives (e.g., `ResolveScope{flavor, location, deficit, seed}`) through a CRD or webhook.
   - Cluster-local controllers execute resolver (M5) within specified scope using provided seed to maintain global fairness.
   - Aggregator collects outcomes (leases ended) and updates global state.
4. **Conflict handling**
   - Ensure directives carry idempotent IDs; clusters report success/failure.
   - Retry or escalate (human intervention) if clusters fail to comply in time.
5. **Observability & audit**
   - Maintain audit log of directives, seeds, outcomes, and resulting usage in aggregator store.
   - Provide dashboards and CLI `kubectl runs global` commands to inspect aggregate utilization and directives.
6. **Failure scenarios**
   - If aggregator unreachable, clusters continue local enforcement but raise alerts for potential global drift.
   - Implement reconciliation loop to reconcile state once connectivity restored.
7. **Documentation**
   - Expand `docs/design/calculus.md` with multi-cluster semantics and aggregator protocol.
   - Provide operator guide for deploying aggregator, configuring connectivity, and interpreting directives.

## Testing Strategy
- Unit tests for aggregator’s cap evaluation logic and directive issuance.
- Integration tests using two kind clusters, simulated Lease streams, and directives verifying coordinated resolver execution.
- Chaos testing: drop aggregator connectivity, verify eventual reconciliation.

## Observability & Telemetry
- Metrics: `global_cap_usage`, `aggregate_directives_issued_total`, `aggregate_directives_failed_total`.
- Logs for ingestion errors, directive handling, and reconciliation outcomes.

## Rollout & Migration
- Deploy aggregator in management cluster; configure clusters with exporter and trust bundles.
- Roll out gradually (monitor-only mode before enforcement) to validate telemetry accuracy.

## Risks & Mitigations
- **Latency between clusters causing stale decisions:** Use bounded staleness windows and require confirmation before new high-impact runs are admitted.
- **Security exposure:** Enforce mTLS, least-privilege RBAC, and audit trails for cross-cluster commands.

## Open Questions
- Should aggregate enforcement support priority tiers (e.g., research vs. product)? Requires policy definition beyond current scope.
- Is eventual consistency acceptable, or do we need synchronous admission control? Evaluate based on utilization volatility.
