# Oversubscription resolver

Milestone M5 introduces the resolver that restores feasibility when a reservation activates or a budget tightens. The resolver executes in three phases: drop spares, shrink malleable runs, and run an attested lottery. This document captures the implementation details that now live in the repository.

## Structural cuts

1. **Spare reclamation** — The resolver walks all active leases in the conflict scope (flavor + location) and immediately ends any `role=Spare` leases. These actions are emitted with the `DropSpare` reason and free capacity before touching production work.

2. **INCR shrink** — For runs that declared malleability, the resolver looks at whole groups (identified by `rq.davidlangworthy.io/group-index`) and removes the highest-index groups first while respecting `minTotalGPUs`. Shrink actions set the lease status `closureReason` to `Shrink`, allowing downstream reporting to separate deterministic shrinkage from random preemption.

Both structural steps update the in-memory ledger immediately so subsequent headroom and snapshot calculations include the freed GPUs.

## Lottery execution

When structural cuts are insufficient, the resolver builds a conflict set of remaining groups per owner and performs a two-stage draw:

1. Uniformly select an owner from the remaining participants.
2. Uniformly select one of the owner’s eligible groups. Groups that would violate `minTotalGPUs` are skipped.

The lottery is seeded deterministically using the reservation name and activation timestamp. Results are encoded as `RandomPreempt(<seed>)` in the lease status, and the seed itself is returned to the controller so operators can surface it in events or CLI explanations.

## Controller integration

- `controllers/run_controller.go` now exposes `ActivateReservations`, which activates due reservations and invokes the resolver before binding.
- Resolved leases are marked closed with end timestamps and `closureReason`. Pod manifests belonging to the affected group are removed from the in-memory state to simulate eviction.
- Runs that lose all leases transition to `Failed`, whereas survivors retain `Running` with a “shrunk by resolver” message.

## Testing

- `pkg/resolver/resolver_test.go` covers spare drops, INCR shrink ordering, and lottery determinism.
- `controllers/run_controller_test.go` contains an end-to-end activation that shrinks an elastic run and preempts a fixed run before admitting the reserved workload.

## Follow-up hooks

The resolver result structure carries the lottery seed so future milestones (M9) can publish attested outcomes via the notifier or CLI. When CLI support arrives, the `kubectl runs explain` command will use this data to display the conflict set and winners.
