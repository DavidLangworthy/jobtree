# NodeFailure spec

`specs/NodeFailure.tla` is a design-level model of the node-failure / spare-swap / reclaim seam in jobtree.

## Purpose

This spec exists to catch the bug classes that kept recurring in production on exactly this path:

- swapping on a signal that does not prove machine death
- reclaiming by coarse node identity instead of exact slot identity
- leaking a spare on a declined swap
- order-dependent phase writes during one failure sweep
- fixing only the ledger plane while the workload keeps running

## Match to implementation

This is closer than a whitepaper and looser than a refinement proof.

It mirrors the real seam in three places:

- the reconciler's fenced/deleted failure trigger in `controllers/kube/reconcilers.go:345-455`
- `HandleNodeFailure`'s pass-1 spare cleanup, pass-2 active handling, and post-loop failed-run sweep in `controllers/run_controller.go:1186-1452`
- the scheduler plugin's later PreBind mint of the swap lease in `cmd/scheduler/plugin/plugin.go:244-315`

The main abstractions are intentional:

- a fixed tiny universe of runs, groups, nodes, and slots
- one slot per lease identity rather than arbitrary slice sets
- a collapsed funding class (`Funded` / `Unfunded`) instead of the full owned/shared/borrowed/unfunded derivation
- a collapsed pod lifecycle (`Intent`, `Bound`, `Gone`) instead of the full API-level lifecycle

## What TLC proves here

The clean config checks the intended current design:

- `NodeFailure.cfg`

The bug configs reintroduce one defect class at a time and must fail:

- `NodeFailureR21.cfg` -> `NoDuplicateRank`
- `NodeFailureR22.cfg` -> `ReclaimIsSlotExactAndUnfunded`
- `NodeFailureR25.cfg` -> `FailedNodeFullyHandled`
- `NodeFailureDeclinedSwap.cfg` -> `TerminalHoldsNothing`
- `NodeFailureLastWriter.cfg` -> `PhaseIsJoin`
- `NodeFailureHalfPlane.cfg` -> `PlaneAgreement`

TLC does reproduce the historical bug classes. It does not, by itself, prove a new implementation bug in Go.

The main open modeling question left on purpose is the "stale class evaluation" issue: the Go code computes funding once before pass 2, while this model re-derives on the current state. That deserves a separate exploratory knob if we want to answer it mechanically.

## Why this is useful

This seam is where order sensitivity and cross-plane invariants matter. TLC earns its keep here because it explores every lease-processing order by construction, instead of depending on a hand-written permutation harness in Go tests.

It is not a substitute for envtest, e2e, or the antifake rails. API wiring, informer behavior, duplicate writers, and other implementation-only defects are outside this model's reach.

## Customer promise covered

This model covers a narrow but safety-critical part of the product promise:

- node-failure swaps do not duplicate ranks
- swaps do not steal funded neighbors
- failed runs do not strand immortal leases
- the ledger/workload planes stay consistent enough for the lease trail to stay auditable

It does not model:

- pack quality
- reservations or ETA correctness
- elastic grow/shrink
- follow/completion
- full GPU-hour arithmetic
- multi-cluster behavior

## Kubernetes semantics imported

This is not "a Kubernetes spec". It imports only the thin slice of Kubernetes semantics that this design relies on:

- cordoned and NotReady are signals, not proof of machine death
- deletion / out-of-service fencing is the only safe swap trigger
- pods are replaced by new pods and new leases, never moved in place
- bind-time minting matters because there is a real swap window between pod emission and lease creation

Queueing, scoring, DRA/device-plugin details, informer behavior, and most pod lifecycle detail are intentionally out of scope.

## How it runs

This spec is intentionally not in the global `make verify` gate.

Use the dedicated targets:

- `make node-failure-spec-check`
- `make node-failure-spec-counterexamples`

CI runs those targets only when the relevant implementation seam or the spec itself changes, and publishes this note as a PDF artifact.
