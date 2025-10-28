# M5 — Oversubscription resolver

## Summary
Introduce the mechanisms that restore feasibility when Reservations activate or budgets tighten: deterministic structural cuts (drop spares, shrink malleable runs) followed by the attested two-stage lottery. The resolver enforces fairness while keeping auditability guarantees.

## Goals
- Compute conflict scopes (flavor + location [+ aggregate cap]) when deficits appear.
- End spare Leases and shrink INCR runs deterministically before invoking randomness.
- Execute a two-stage lottery (owner → lease token) using deterministic seeds, publishing the conflict set and outcome for audit.
- Integrate with Reservation activation and manual rebalancing workflows.

## Non-goals
- Implementing failure-driven spare swaps (M6) or voluntary shrink CLI (M7).
- Multi-cluster coordination (M10).

## Inputs & Dependencies
- Active Lease ledger from M3 (needed to enumerate candidates).
- Reservation activation logic from M4 to trigger resolver when deficits persist.
- Budget status from M1 to detect overages.

## Architecture & Components
- **Resolver library (`pkg/resolver`):** Encapsulates structural cuts, token generation, lottery execution, and audit emission.
- **Controller hooks:** Run/Reservation controllers invoke resolver before binding when deficits exist.
- **Attestation service:** Extend `cmd/notifier` or a new component to publish seed, conflict set, and winners to durable storage.

## Detailed Design
1. **Conflict scope derivation**
   - Compute the minimal scope covering the deficit: flavor, location tuple (region/cluster/domain), plus aggregate cap identifier when applicable.
   - Gather candidate leases within scope along with metadata (role, owner, component path, bundle membership, spare flag, INCR status).
2. **Structural cuts**
   - End spare leases first (`role=Spare`). Update ledger with `reason=RandomPreempt`? (No, use `reason=Swap`/`ReclaimedBySpare` depending on context) and adjust Run status.
   - For malleable Runs (`INCR`), compute shrink amount respecting `minTotalGPUs` and `stepGPUs`. End the corresponding leases with `reason=Shrink`.
   - Recompute deficit after each action.
3. **Lottery preparation**
   - If deficit remains, construct tokens: each owner receives tokens equal to the number of AND bundles (gangs) or INCR steps eligible for removal.
   - Generate deterministic seed (e.g., `SHA256(reservationUID || timestamp || entropy)`), store in Reservation/Run event log.
4. **Lottery execution**
   - Stage 1: uniform random owner selection using seed and token counts.
   - Stage 2: within owner, uniform selection of lease token (bundles count as 1 token) using same seed + draw index.
   - End selected lease(s) with `reason=RandomPreempt{seed}` until deficit ≤ 0.
   - Update ledger and notify affected Runs.
5. **Audit trail**
   - Persist conflict set, tokenization, seed, draws, and resulting lease IDs in a ConfigMap/CRD (`ResolverOutcome`) for transparency.
   - Expose via CLI (`kubectl runs explain`) and Notifier messages.
6. **Integration**
   - Reservation activation path: run resolver before binding; if resolver succeeds, proceed to binder (M3). If not (deficit persists), mark Reservation `Blocked`.
   - Manual trigger: administrators can invoke resolver for maintenance windows (optional CLI subcommand `kubectl runs resolve --scope ...`).
7. **Documentation**
   - Expand `docs/design/oversubscription.md` with algorithms, token examples, and CLI walkthroughs.

## Testing Strategy
- Unit tests for structural cuts (spares, INCR shrink) ensuring invariants (no below-min shrink, concurrency updated).
- Lottery determinism tests: given seed and conflict set, outcome is reproducible.
- e2e scenario: Reservation activation causing deficit, resolver executes, affected runs receive preemption events.

## Observability & Telemetry
- Metrics: `resolver_invocations_total`, `resolver_spares_dropped_total`, `resolver_shrinks_total`, `resolver_lottery_draws_total`.
- Logs include conflict scope, deficit, seed, and winners.

## Rollout & Migration
- Deploy resolver library and integrate with existing controllers; ensure feature flags allow staged rollout.
- Backfill documentation and runbooks for operations teams handling preemption incidents.

## Risks & Mitigations
- **Perceived unfairness:** Provide transparent audit logs and CLI explanations; ensure RNG seed published before outcomes applied.
- **Race conditions with concurrent activations:** Serialize resolver execution per scope using leader election or distributed locks.

## Open Questions
- Should we allow owners to nominate preferred sacrifice leases (e.g., opportunistic jobs) before lottery? Consider optional hinting API in future milestone.
- How to handle leases spanning multiple aggregates simultaneously? Define precedence (aggregate cap → location) and document.
