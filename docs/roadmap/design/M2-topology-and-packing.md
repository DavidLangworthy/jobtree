# M2 — Topology discovery & group-aware packing

## Summary
Build the topology awareness required to place Run groups on fast-fabric domains, respecting group cohesion, pack-to-empty heuristics, and cross-domain minimization. This milestone produces a deterministic placement plan without yet binding workloads.

## Goals
- Discover cluster topology (regions, clusters, fabric domains, racks, GPU flavors) from node labels.
- Represent fast-fabric domains as placement units with available GPU capacity and spare slots.
- Implement the `pack` algorithm that fills domains to empty before spilling, while meeting `groupGPUs` constraints and minimizing cross-domain cuts.
- Expose placement plans that downstream controllers (M3+) can consume.

## Non-goals
- Creating or binding Kubernetes pods (handled in M3).
- Handling Reservations or conflict resolution (M4–M5).
- Accounting for GPU-hour budgets (M1 already covers usage).

## Inputs & Dependencies
- Node inventory with consistent labels (`region`, `cluster`, `fabric.domain`, `gpu.flavor`, optional `rack`).
- Run specs referencing GPU flavor and optional `groupGPUs`, `allowCrossGroupSpread`, `spares`.
- Budget selectors (to ensure placement respects envelope location filters).

## Architecture & Components
- **Topology cache:** `pkg/topology` builds typed indexes for nodes and domains.
- **Packing engine:** `pkg/pack` exposes deterministic functions to allocate Run groups to domains and nodes.
- **Fragmentation analyzer:** Optional helper to evaluate residual capacity for forecasting.
- **Integration:** `pkg/pack` consumes cover output to ensure selected envelopes align with placement scope.

## Detailed Design
1. **Topology ingestion**
   - Watch `Node` objects and group them by `(region, cluster, fabric.domain)` into domain structs containing ordered node lists and GPU counts.
   - Track per-domain health (cordoned/tainted nodes excluded) and spare availability.
2. **Group modeling**
   - Compile Run spec into groups (`groupGPUs` or default chunk size 1) with metadata: desired spare count, malleability step, labels.
   - Represent spares as shadow slots attached to each group.
3. **Pack-to-empty algorithm**
   - Sort domains by available capacity (descending) within the target selector (flavor, location).
   - For each group:
     - Find a domain with enough contiguous GPUs; prefer domains already partially filled by the same Run to minimize spread.
     - If multiple domains fit, choose the one with least residual waste.
   - Record the node assignments for each group and track residual capacity per domain.
4. **Cross-domain policy**
   - When `allowCrossGroupSpread=false`, ensure all groups land within a single domain; fail placement if impossible.
   - When true (default), allow groups to span domains but keep each group coherent.
5. **Spare placement**
   - Allocate spare slots within the same domain as the active group when possible; otherwise choose adjacent domain with lowest latency (rack-aware tie-breaker).
6. **Output representation**
   - Produce a placement plan containing: domain assignments, node lists per group, spare location, fragmentation metrics.
   - Provide deterministic hashing (`plan.Hash()`) for auditing and Reservation referencing.
7. **Documentation**
   - Document topology assumptions and algorithm heuristics in `docs/design/calculus.md` and a new packing subsection.

## Testing Strategy
- Unit tests covering single-domain, multi-domain, and fragmented topologies.
- Property tests ensuring invariants (group cohesion, spare co-location) hold for randomized node sets.
- Benchmark tests verifying deterministic ordering and acceptable runtime for large clusters (10k+ GPUs).

## Observability & Telemetry
- Add debug logging for placement decisions (domain selected, residual capacity) and surface metrics for pack success/failure counts.

## Rollout & Migration
- Enable topology controller as part of manager; ensure it caches nodes before Run controller relies on it.
- Provide operator runbook on required node labels and validation script (e.g., `hack/labeler.sh`).

## Risks & Mitigations
- **Label inconsistency:** Provide admission checks and validation scripts; fail fast with clear errors.
- **Fragmentation leading to failures:** Document fallback strategies (Reservation) and expose fragmentation metrics for capacity planning.

## Open Questions
- Do we need rack-level anti-affinity beyond fabric domains? Collect feedback after initial deployments.
- Should packer support weighted fabrics (e.g., NVLink vs. PCIe) explicitly? Potential enhancement once metrics indicate need.
