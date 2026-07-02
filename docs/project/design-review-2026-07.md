# Design & implementation review (July 2026)

*Reviewed at commit `16fc4f1` on 2026-07-02. Line references are point-in-time and will drift.*

**Verdict in one paragraph:** the scheduling engine at the core of this project is genuinely good —
cleanly layered, deterministic, race-clean, and well tested (77–85% statement coverage in the core
packages; the full suite passes under the race detector). But the project is currently a simulator
wearing an operator's clothes: the manager binary is a stub, nothing Kubernetes-facing exists, and
two of the headline features (reservation activation and node-failure/spare-swap) are unreachable
from any entrypoint — they only run in tests. Four bugs were confirmed by execution, including a
binder defect that assigns pods more GPUs than their node has, and a CLI that exits silently on
every error.

## What was verified

The review covered all ~9,700 lines of Go plus the docs, Helm/Kustomize bundles, and CI
configuration. `go build`, `go vet`, and `gofmt -l` are clean, and `go test -race -count=1 ./...`
passes everywhere on Go 1.26. Note that with Go 1.22 — the version CI pins — test binaries abort
on current macOS (`missing LC_UUID`); that is a toolchain issue worth fixing regardless, since
Go 1.22 is past end-of-life.

Coverage: controllers 77%, binder 83%, cover 82%, resolver 82%, pack 78%, forecast 75%,
metrics 85%; but `api/v1` 15% and the vendored cobra clone 0%.

## Design assessment

### The architecture is the strong point

The pipeline decomposes cleanly: `pkg/budget` (usage/headroom accounting) → `pkg/cover` (funding
across envelopes with family sharing and lending) → `pkg/topology` / `pkg/pack` (domain-aware
placement) → `pkg/binder` (pods + immutable leases) → `pkg/resolver` (spares → shrink → seeded
lottery) → `pkg/forecast` (reservations). Each layer is a pure function over explicit inputs with
injected clocks, ties broken deterministically, and failures classified by typed reasons that flow
up into user-facing messages. The worked-examples test (`pkg/cover/worked_examples_test.go`)
mirroring the docs is a nice touch, and the hand-rolled Prometheus exposition in
`pkg/metrics/metrics.go` is correct (cumulative histogram buckets and all).

### The central design tension: "Kubernetes-native" is aspirational, not actual

`go.mod` has zero dependencies. There is no controller-runtime, no client-go, no CRD manifests
(only `config/samples/`), and `cmd/manager/main.go` prints "stub" and exits. `ObjectMeta` and
`TypeMeta` are hand-mimicked in `api/v1/meta.go`, the webhook `ValidateCreate`/`ValidateUpdate`
methods are never wired to anything, and the CLI ships a reimplemented cobra
(`cmd/kubectl-runs/internal/cobra/command.go`). The README's roadmap marks M0–M9 complete, but
several definitions-of-done are not met by the artifacts:

- M0 claims "kubectl can list the CRDs, webhooks enforce basic invariants" — there are no CRD
  YAMLs and no webhook server.
- M6 claims e2e failure injection and a `pkg/policy` that does not exist.
- M9 claims Helm is validated in CI — the CI workflow only runs gofmt and `go test`.

The zero-dependency purity is defensible for a design-validation phase, but the docs should say
"simulator" as plainly as `docs/cli/kubectl-runs.md` does, rather than checking the boxes.

### Control-loop gaps — features with no driver

The CLI is the only thing that ever invokes reconciliation, and it never calls
`ActivateReservations` or `HandleNodeFailure` — their only callers are tests. So in the shipped
system, a reservation can be created but can never activate, and the M6 spare-swap path can never
trigger. Relatedly, read-looking commands (`plan`, `watch`, `explain`, `budgets usage`) all
reconcile *and save state* as a side effect, and `reconcileAll`
(`cmd/kubectl-runs/cmd/helpers.go`) iterates a Go map, so when multiple runs compete for capacity,
admission order — and therefore who gets the GPUs — is nondeterministic across invocations. That
undercuts the project's own determinism story. There is also no file locking on the state
snapshot, so a long `watch` loop racing another CLI invocation will silently lose writes.

### GPU-hour caps are dead code at admission

`cover.Request.ExpectedDuration` is only ever set in tests; every controller-built request leaves
it zero, and all the `MaxGPUHours` checks in `headroomForEnvelope` (`pkg/cover/cover.go`) are
guarded by `expectedHours > 0`. So envelope, aggregate, and lending GPU-hour limits constrain
nothing in the real path — a run admits happily into an envelope with zero GPU-hour headroom.
Concurrency caps do work. Since Runs have no duration/completion concept at all (nothing ever
transitions to `Completed`, leases never end naturally), the integral-budget half of the design
currently exists only in status reporting.

### Family sharing vs. lending is semantically inconsistent

By design (`docs/concepts/budgets.md`), siblings/parents/cousins are consumed *before* sponsors,
with no lending policy gate — meaning the lending ACL machinery only governs strangers, while any
family member can drain an envelope unconditionally. Perhaps intended, but the reporting is then
inconsistent: family-funded leases get `Role: Active` (not `Borrowed`; see `buildLease` in
`pkg/binder/binder.go`), so they bypass lending sub-caps in budget accounting
(`pkg/budget/usage.go`) — yet `summarizeRunFunding` (`controllers/run_controller.go`) classifies
anything paid by a different owner as "borrowed" in Run status. The same GPUs are "borrowed" in
one ledger and not in the other.

## Confirmed bugs (reproduced by execution)

### 1. Binder assigns pods across node boundaries — wrong node, impossible GPU counts

When a funding segment boundary does not align with a node allocation chunk, `Materialize`
(`pkg/binder/binder.go`) slices the flattened GPU list by segment, and `buildPod` assumes "all
slots belong to the same node allocation chunk by construction" — which is false. A reproduction
with one group of 4 GPUs on two 2-GPU nodes and a cover plan of 3 owned + 1 borrowed produced:

```text
name=train-g00-active-node-1   node=node-1  gpus=3   <- node-1 only has 2 GPUs
name=train-g00-borrowed-node-2 node=node-2  gpus=1
```

Any co-funded or multi-envelope run whose segment sizes do not align with node capacities is
affected. With the segment order reversed it also produces two pods with the *same name*.

### 2. Lease name collisions

Lease names (`buildLease` in `pkg/binder/binder.go`) are `run-gNN-<envelopeName>-<UnixNano>`.
`Now` is fixed for the whole materialization and envelope names are only unique *within* a budget,
so the same reproduction produced two leases both named `train-g00-west-1767225600000000000`
(the owner's "west" envelope and the sponsor's "west" envelope). In anything name-keyed —
including a real API server — the second create fails.

### 3. The CLI swallows every error

`cmd/kubectl-runs/main.go` does `os.Exit(1)` without printing, and the root command sets
`SilenceErrors`. Verified: `kubectl-runs ... watch missing-run` exits 1 with zero output. Every
failure mode — bad flags, missing run, invalid manifest — is indistinguishable silence.

### 4. The docs' own example does not parse

The stdlib-`flag`-based cobra clone requires flags before positional arguments, and
`--watch-count` is a root persistent flag. The documented
`kubectl runs --state cluster.yaml watch train-128 --watch-count 3`
(`docs/cli/kubectl-runs.md`) fails (verified) — directly below the doc's own note explaining the
constraint.

## High-confidence issues from code reading

- **Double-bind on reservation activation.** `activateReservation`
  (`controllers/run_controller.go`) never checks the run's phase, and the direct-bind path in
  `Reconcile` clears neither `Status.PendingReservation` nor the stored Reservation. A run that
  gets a reservation, then binds directly on a later reconcile, leaves a live Pending reservation
  that would materialize a *second* set of pods/leases on activation. Unreachable today only
  because nothing calls `ActivateReservations`; wire that in without fixing this and it is a
  double-spend.
- **Resolver preempts for budget shortfalls.** In `activateReservation`, if the failure was
  *cover* (budget) rather than capacity, `computeDeficit` can return 0 and the code then forces
  `deficit = totalNeeded` and runs the lottery — killing other tenants' groups to free physical
  capacity that was never the problem. Preemption cannot create budget headroom for the requester.
- **Discarded actions still counted in metrics.** `Resolve` (`pkg/resolver/resolver.go`)
  increments `IncResolverAction` for spares/shrinks as it plans, but if the subsequent lottery
  errors, the whole `Result` is discarded — metrics report actions that never happened. Count
  after application, not during planning.
- **One bad reservation blocks all.** `ActivateReservations` returns on the first error, so a
  single reservation referencing a deleted run stalls every other due reservation. Iteration order
  over the map is also nondeterministic.
- **Group-index ordering breaks at 10.** The resolver's shrink pass sorts group indices as strings
  descending, so group "9" is cut before "10" — contradicting the documented "highest-index groups
  first" once a run has ≥10 groups (elastic growth makes that reachable).
- **Three `namespacedKey` implementations disagree.** The controllers version yields `"/name"` for
  an empty namespace, the resolver version yields `"name"`, and the CLI version defaults to
  `"default/name"`. Consistent today only because everything happens to use `default`; an
  empty-namespace `RunRef` makes the resolver silently skip leases (its run lookup misses),
  exempting them from preemption.
- **Binder can panic instead of erroring** (`segments[0]` on an exhausted slice in `Materialize`)
  when a cover plan covers the first group exactly but more groups remain — reachable only via
  caller bugs, but this is exactly where an error is wanted, not a panic.
- **Validation gaps in `api/v1`:** duplicate envelope names within a budget are not rejected (they
  silently collapse in `BuildBudgetState`'s map, and a same-named envelope in *another* budget of
  the same owner double-counts the same lease); `AggregateCap.Envelopes` references are not
  checked against real envelope names; `Run.Default()` is never called anywhere (each consumer
  re-implements the defaulting).

## Packaging, docs, CI

- **Helm RBAC is wildcard cluster-admin** (`deploy/helm/gpu-fleet/templates/rbac.yaml`) — `*`/`*`/`*`
  on a ClusterRole, for a deployment whose container is a stub binary. If the chart ships, scope
  it to the CRDs plus the nodes/pods access it needs.
- The krew manifest (`plugins/krew/runs.yaml`) points at `cmd/kubectl-runs/kubectl-runs`, a path
  nothing builds (the release workflow outputs `dist/kubectl-runs`), covers only linux/amd64, and
  references no downloadable archive.
- CI pins Go 1.22 (end-of-life, and broken on current macOS as demonstrated); `helm lint` runs
  only in the tag-triggered release workflow despite M9 claiming CI validation.
- The state file convention is JSON content with a `.yaml` default filename
  (`cluster-state.yaml`, parsed by `json.Unmarshal`) — documented, but needlessly confusing;
  either accept YAML or default to `.json`.

## Recommendations, in order

*Each finding below is tracked as a numbered work item with concrete steps in the remediation
plan (`remediation-plan.md`).*

1. **Fix the binder** — split segment slices on node-chunk boundaries (walk chunks and segments as
   two cursors), and make lease names collision-proof (include the budget name and an index; drop
   the reliance on `UnixNano`). This is the one confirmed data-corruption-class bug in the engine.
2. **Print CLI errors** in `main.go` — one line, disproportionate UX payoff. Fix the docs example
   (or teach the flag parser to interleave flags and positionals).
3. **Decide what the project is for the next milestone.** Either wire the real Kubernetes layer
   (controller-runtime, CRDs, webhooks — replacing the mimicked types and cobra clone with the
   real dependencies) or re-badge the roadmap checkboxes to reflect the simulator honestly. The
   engine's purity makes the eventual port straightforward; the hand-rolled scaffolding is the
   part that will not survive contact with real Kubernetes anyway.
4. **Close the control-loop gaps before wiring:** call `ActivateReservations` from the reconcile
   path, add the run-phase guard and reservation cleanup, plumb `ExpectedDuration` (or make an
   explicit "indefinite runs bypass GPU-hour caps" decision), and reconcile the
   family-sharing/borrowed-reporting semantics.
5. Smaller cleanups: numeric group-index sort, a single shared `namespacedKey`, resolver
   metrics-after-apply, per-reservation error isolation, deterministic `reconcileAll` ordering,
   state-file locking, RBAC scoping, CI Go bump plus `helm lint`.

The companion document, *Testing & simulation plan* (`testing-and-simulation.md`), lays out the
test environments that would have caught most of these findings mechanically and describes the
path from single-machine simulation to live systems testing.
