# Design-level specs (TLA+)

Three deliberately tiny TLA+ specifications, per the scoping in
`docs/project/testing-and-simulation.md` ("Design-level model checking — a gate
for the Kubernetes port") and the decided semantics in
`docs/project/quota-semantics.md`. Each is tens of lines of state, close enough
to the design that it cannot drift far from reality.

| Spec | Models | Checked invariants |
| ---- | ------ | ------------------ |
| `ReservationLifecycle` | plan / direct-bind / activate racing for one run | pods materialize at most once; no Pending reservation for a Running run (invariant 8) |
| `BudgetConservation` | reconcilers admitting against one envelope from possibly-stale lease snapshots | the concurrency cap is never overspent |
| `QuotaEvaluation` | the ranked greedy fill from `quota-semantics.md` Decision 3 | no overdraft; an owner's funding never depends on borrower claims (owner recall) |

## Running

```bash
make spec-check            # all three specs must check clean (the port gate)
make spec-counterexamples  # the historical bugs, demonstrated: these MUST fail
```

`spec-check` downloads `tla2tools.jar` into `specs/.cache/` on first use and
needs a Java runtime.

## The counterexample configurations

- `ReservationLifecycleBug.cfg` sets `GuardEnabled = FALSE` — the state of the
  code before R9. TLC finds the double-bind (a run that reserves, direct-binds,
  and then re-materializes on the activation tick) in a handful of states,
  exactly as the design review predicted a model checker would.
- `BudgetConservationRacy.cfg` sets `Serialized = FALSE` — concurrent
  admissions deciding from the same stale snapshot overspend the envelope.
  This is the result that pins the manager's Run reconciler to a single
  admission worker (`MaxConcurrentReconciles = 1`); revisit the spec before
  relaxing that.

## What is deliberately out of scope

The GPU-hour integral is a second budget dimension filled by the same walk as
concurrency; modeling one dimension covers the walk. Demotion/promotion
message protocols are not modeled because, per Decision 3, they do not exist —
classification is a derived function, so staleness bugs in it are
unrepresentable. The stability property ("equal claims never reshuffle") is
structural: a claim's class depends only on claims ranked above it, and ranks
are `(tier, admission index)`, which mutations never reorder for survivors.
