# M3 — Binder & Leases (immediate starts)

## Summary
Implement the first end-to-end execution path: admit feasible Runs, bind their pods atomically to nodes, and materialize immutable Lease records. This milestone turns the static APIs and planning libraries into a functioning scheduler for runs that can start immediately.

> **Implementation note (current state):** The repository now contains a deterministic binder that produces pod manifests and Leases directly from cover/pack output, plus a Run controller that admits runs against an in-memory cluster state. The Kubernetes integration described below remains the long-term goal.

## Goals
- Integrate cover (M1) and pack (M2) outputs to produce a complete binding plan for each Run submission.
- Create Kubernetes Pods (or StatefulSet equivalents) with pre-assigned `nodeName` values to ensure gang scheduling semantics.
- Emit Lease CRDs capturing each group’s consumption, keeping the immutable ledger aligned with pod lifecycle events.
- Handle Run completion by closing Leases and cleaning up Kubernetes resources.

## Non-goals
- Handling deferred Runs via Reservations (M4).
- Resolving oversubscription via structural cuts or lottery (M5).
- Elastic scaling or spares (M6–M7).

## Inputs & Dependencies
- Cover plan from `pkg/cover` (M1) describing payers per group.
- Placement plan from `pkg/pack` (M2) describing domains and nodes.
- CRD schemas and webhooks (M0).

## Architecture & Components
- **Run controller (`controllers/run_controller.go`):** Watches Run CRDs, coordinates cover+pack, and drives binder actions.
- **Binder library (`pkg/binder`):** Responsible for synthesizing pod specs, ensuring atomicity, and emitting Lease objects.
- **Lease controller (`controllers/lease_controller.go`):** Mirrors pod lifecycle into Lease status (marking `closed` when pods finish) and maintains derived indexes.
- **Work queue:** Ensures idempotent reconciliation when pods crash or pods/leases drift.

## Detailed Design
1. **Admission workflow**
   - On Run create/update, compile Run into groups and request funding from `cover.Plan`.
   - If cover succeeds, request placement from `pack.Plan`.
   - If both succeed, proceed to binding; otherwise defer to M4 (Reservation) logic.
2. **Atomic binding**
   - Generate a pod template per group (e.g., StatefulSet with ordinal labels) and pre-bind nodes using the scheduler `Binder` interface (`client-go` `Bind` call) or NodeName assignment with scheduler bypass.
   - Ensure all pods are created before marking Run as `Active` to maintain gang semantics.
3. **Lease creation**
   - For each bound group, create a Lease CRD with `reason=Start`, `paidByEnvelope`, `compPath`, `slice.nodes`, and `role=Active`/`Spare`.
   - Use server-side apply to guarantee immutability (no updates to spec after creation).
4. **Lifecycle tracking**
   - Watch pod status; when all containers complete successfully, mark corresponding Leases `status.closed=true` with end timestamp.
   - On failure, emit Lease end events with `reason=Fail`; subsequent retries will create new Leases.
5. **Run status**
   - Maintain `Run.status.phase` (`Pending`, `Running`, `Completed`, `Failed`) and reference active Lease IDs for user visibility.
   - Emit Kubernetes Events summarizing admission decisions and completions.
6. **Error handling & retries**
   - Implement exponential backoff for API conflicts (Lease already exists, pods already bound).
   - Detect partial success (some pods pending) and trigger cleanup + retry to maintain atomicity.
7. **Documentation & samples**
   - Provide a walk-through in `docs/user-guide/quickstart.md` showing Run submission and Lease observation.

## Testing Strategy
- Unit tests for binder library covering pod generation, idempotent apply, and Lease creation payloads.
- Envtest integration tests verifying Run controller transitions and Lease lifecycle updates.
- kind-based e2e test: submit Run, observe pods bound to nodes, wait for completion, validate Lease ledger.

## Observability & Telemetry
- Add structured logs for admission decisions (cover plan, pack plan, binding success/failure).
- Emit metrics: `run_admissions_total{result=success|failure}`, `leases_open`, `leases_closed_total`.

## Rollout & Migration
- Rolling deploy of controller manager containing new Run and Lease reconcilers.
- Provide feature flag to disable binder temporarily if regressions appear.

## Risks & Mitigations
- **Partial binding leading to resource leaks:** Mitigate by tracking intermediate state and cleaning up pods on failure before retrying.
- **Lease drift from pod lifecycle:** Use owner references and informers to ensure updates stay in sync; add reconciliation loop that periodically audits pods vs. leases.

## Open Questions
- Should binder use scheduler `Reserve/Permit` extension points for finer control? Default to direct binding now; evaluate integration once scheduler plugin (M5+) is in scope.
- How to handle multi-container pods with heterogeneous resources? Document current assumption (homogeneous GPU containers) and revisit if demand arises.
