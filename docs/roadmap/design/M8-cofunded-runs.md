# M8 — Co-funded runs (borrowing)

## Summary
Enable runs to draw GPU capacity from multiple teams by enforcing lending ACLs, borrower guardrails, and sponsor preferences. The milestone formalizes the policy knobs introduced earlier and ensures accounting, scheduling, and observability treat co-funded usage correctly.

## Goals
- Honor `Budget.spec.envelopes[].lending` policies (allow list, concurrency/integral sub-caps) during cover decisions.
- Support `Run.spec.funding` hints (`allowBorrow`, `maxBorrowGPUs`, `sponsors[]`) and ensure borrower guardrails are enforced.
- Attribute leases to the correct paying envelopes even as runs grow/shrink (M7 interaction).
- Surface reporting/CLI views showing per-run and per-owner cost splits.

## Non-goals
- Introducing new budgeting constructs beyond lending ACLs (aggregate caps already exist).
- Preferential preemption rules for borrowed vs. owned capacity (same resolver behavior).

## Inputs & Dependencies
- Budget accounting engine (M1) with lending data structures.
- Elasticity support (M7) to ensure growth/shrink operations account for borrowed slots first.
- CLI/plugin foundation (M9) for user interactions (initial commands can land here as skeletons).

## Architecture & Components
- **Cover enhancements:** Extend `pkg/cover` to evaluate sponsor order after family sharing, decrementing lending sub-caps.
- **Run controller updates:** Respect borrower guardrails when requesting cover plans; fail admission if borrowing would exceed `maxBorrowGPUs`.
- **Status & metrics:** Record `Run.status.funding` (owned vs. borrowed GPUs) and expose metrics `borrowed_gpus_current`, `borrowed_gpu_hours_total`.
- **CLI additions:** `kubectl runs sponsors` to list/add/remove sponsors; `kubectl runs who-pays` to show split.

## Detailed Design
1. **Lending policy evaluation**
   - Each envelope with `lending.allow=true` advertises ACL entries (glob syntax) and optional sub-caps.
   - Cover traversal order: owner family (same location), extended family (other locations), sponsors (per Run hint), parent sponsors, finally cousins allowed by ACL.
   - When selecting a lending envelope, ensure both concurrency and integral lending caps remain positive; decrement provisional usage in plan.
2. **Borrower guardrails**
   - `Run.spec.funding.maxBorrowGPUs` limits borrowed concurrency per Run; enforce during cover planning.
   - `allowBorrow=false` forces cover to fail when owner capacity insufficient, triggering Reservation instead of borrowing.
   - Sponsors list acts as preferred lenders; if empty, only family sharing applies.
3. **Lease attribution**
   - Each Lease retains `paidByEnvelope`; when runs shrink, release borrowed leases first.
   - Maintain history in ledger to support chargeback and reporting.
4. **Status & reporting**
   - `Run.status.funding` contains counts for `owned`, `borrowed`, `sponsors[]` with GPU/hour usage to date.
   - Metrics aggregated per owner and sponsor; feed into dashboards.
   - CLI displays split for live and historical runs.
5. **Documentation**
   - Update `docs/user-guide/cofunded-runs.md` with setup steps (budget ACL, run spec), sample scenarios, and troubleshooting.

## Testing Strategy
- Unit tests for cover planning with various DAG structures, ACL combinations, and guardrails.
- Envtest verifying Run controller rejects over-borrow requests and updates status correctly.
- e2e scenario: Run requiring borrowed GPUs starts, metrics/CLI show split, shrink releases borrowed capacity first.

## Observability & Telemetry
- Metrics: `borrowing_attempts_total`, `borrowing_denied_total`, `borrowed_gpus_current`, `borrowed_gpu_hours_total`.
- Logs for borrowing decisions (envelope selected, reason) and guardrail violations.

## Rollout & Migration
- Update budgets with lending sections and communicate policy to owners.
- Roll out controller/CLI changes; ensure existing runs without funding block continue unaffected (defaults maintain previous behavior).

## Risks & Mitigations
- **Lending exhaustion leading to surprise failures:** Provide alerts when lending sub-caps near limits; allow sponsors to observe usage via dashboards.
- **Complex policy debugging:** Add `cover --explain` CLI output showing envelope traversal and reasons for denial.

## Open Questions
- Should borrowed capacity incur premium pricing? Defer to finance/product alignment.
- Do we allow reciprocal borrowing loops (A lends to B while borrowing from B)? Document and optionally restrict via ACL validation.
