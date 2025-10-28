# M6 — Failure handling & hot spares

## Summary
Enable resilient execution by provisioning per-group spares, running opportunistic filler work on those spares, and performing deterministic swaps when nodes fail. This milestone ensures failures do not interrupt fixed-size gangs while preserving auditability of spare usage.

## Goals
- Extend Run admission to allocate spare leases per group when requested.
- Allow opportunistic jobs to temporarily consume spare capacity, knowing it may be reclaimed instantly.
- Detect node failures and swap active workloads onto spare nodes without altering world size.
- Record all spare activations and reclamations in the Lease ledger with precise reasons.

## Non-goals
- Elastic scaling of Run size (M7).
- Advanced repair strategies across clusters (M10).

## Inputs & Dependencies
- Binder and Lease machinery from M3.
- Resolver (M5) to handle deficits post-failure if spares are exhausted.
- Budget accounting (M1) to reflect spare usage (usually discounted or separate policy).

## Architecture & Components
- **Run controller enhancements:** Recognize `spec.spares` and allocate additional spare pods/leases in the same domain.
- **Spare policy module (`pkg/policy/spares`):** Manage spare accounting, opportunistic borrowing rules, and reclamation sequencing.
- **Failure detector:** Integrate with Kubernetes Node events (NotReady/Unknown) and optional health probes to trigger swaps.
- **Opportunistic scheduler integration:** Tag spare nodes for preemptible workloads with appropriate tolerations/taints.

## Detailed Design
1. **Spare allocation**
   - During admission, allocate `spares` extra GPU slots per group in the same domain when possible; fall back to closest domain if necessary.
   - Create spare pods marked with `role=Spare` and matching Lease records (paid by same envelope or discounted payer per policy).
2. **Opportunistic fill**
   - Label spare pods/nodes to allow preemptible workloads (e.g., `rq.davidlangworthy.io/opportunistic=true`).
   - Implement a filler controller (optional) or document how teams schedule opportunistic jobs with taints/tolerations.
   - Filler leases reference their own payer but note `role=Borrowed` in spec.
3. **Failure detection**
   - Watch Node conditions; when a node fails, identify affected Run group and available spare.
   - If spare exists in same domain: cordon failed node, end spare Lease with `reason=Swap`, start new active Lease for original group on spare node, requeue filler to end with `reason=ReclaimedBySpare`.
   - If no spare available: trigger Run failure handling (checkpoint -> requeue) or defer to resolver.
4. **Accounting**
   - Update Budget usage to reflect spare activation/deactivation (spares may be discounted or counted separately per policy).
   - Maintain metrics for spare utilization, swap frequency, and filler preemptions.
5. **User experience**
   - Update Run status with spare readiness (e.g., `status.spares[domain]=available/consumed`).
   - CLI `kubectl runs watch` shows spare events and filler reclaims.
6. **Documentation**
   - Expand `docs/user-guide/spares-and-fill.md` with configuration, best practices, and failure walkthrough.

## Testing Strategy
- Unit tests for spare allocation logic and swap sequencing.
- Envtest simulation of node failure events leading to swap.
- e2e scenario: Run with spares + opportunistic filler → inject node failure → verify active workload continues, filler preempted, leases updated.

## Implementation Notes (Completed)
- Pack planner now allocates `sparesPerGroup` alongside active placements and exposes `TotalSpares` so the cover planner can fund them explicitly.
- Binder emits spare pods and leases (role = `Spare`) with labels that allow later reconciliation and ledger accounting.
- Budget usage tracks spare concurrency/GPU-hours to support future discounting policy.
- Run controller provisions spare leases during admission, synthesises reservations with spare capacity, and exposes `HandleNodeFailure` to perform deterministic swaps that:
  - close the failed lease with reason `NodeFailure`,
  - reclaim opportunistic borrowers with `ReclaimedBySpare`,
  - close the spare lease with reason `Swap`, and
  - mint a new active lease on the spare nodes while refreshing pod manifests.
- Controller tests cover the swap path, ensuring borrowed work is reclaimed and status messaging remains accurate.

## Observability & Telemetry
- Metrics: `spares_allocated_total`, `spares_active`, `spares_swaps_total`, `filler_preemptions_total`.
- Logs capture node failures, swap decisions, and filler eviction reasoning.

## Rollout & Migration
- Deploy updated Run controller and optional filler controller.
- Coordinate with platform team to label nodes for opportunistic use and update admission policies.

## Risks & Mitigations
- **Spare exhaustion:** Document fallback to reservation/lottery and encourage sizing guidance.
- **Unreliable failure signals:** Use multiple signal sources (Node condition, device plugin health) and configurable debounce timers.

## Open Questions
- Should spare usage be billed at full rate or discounted? Policy decision pending finance alignment.
- Do we need multi-spare tiers (warm vs. cold)? Collect data post-launch.
