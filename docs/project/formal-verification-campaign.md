# Formal verification campaign: common-sense behavior first

**Starting point:** `main` at `d736ea4` (PR #127 merged).

**Time box:** one to two days of model construction, executable calibration,
counterexample search, and bounded checking. Solver throughput is not the goal.
The goal is to find implementation and design defects before they become quiet,
long-lived ledger or capacity corruption.

## Objective

Jobtree should behave the way a careful operator and tenant would reasonably
expect, especially through failures and configuration changes:

- work that is running and billed has a real workload holding the GPUs;
- work that is gone, terminal, or released cannot bill or hold capacity forever;
- capacity cannot be handed out twice;
- a funding or topology edit cannot rewrite already-spent history;
- one tenant cannot change another tenant's funding identity, history, or safety
  without an authenticated delegation path;
- a reservation, gang, or recovery attempt either progresses, blocks visibly
  with a cause, or unwinds within a stated bound;
- losing funding demotes work; it does not silently destroy healthy work;
- every destructive action is tied to a real arriving demand or physical
  failure, not merely to an accounting deficit;
- status, metrics, conditions, leases, pods, and controller decisions agree
  about the same physical and financial reality.

TLA+, TLC, Apalache, SMT, fuzzing, executable probes, and static review are means
to establish those claims. A clean solver result over an inadequate model is a
failure.

## The adequacy rule

Before trusting a model, challenge the model itself.

1. Build a matrix from every historical defect in
   `docs/project/adversarial-review-playbook.md`, the invariant-oracle history,
   and the recent R7/P5-P8 findings to:
   - a modeled state variable and transition;
   - an invariant that should fail on the defective behavior;
   - an executable Go specimen or trace; and
   - a reasoned out-of-scope entry when the model cannot express it.
2. A known defect that the model cannot reproduce is not "covered". It is a
   named abstraction gap that must be closed or accepted explicitly.
3. Add negative controls before positive proof claims. Break the load-bearing
   transition or predicate and confirm the intended rail goes red.
4. Look for vacuity: empty domains, impossible `Init` states, caller-selected
   exceptions, zero timestamps, fairness assumptions that force progress, and
   types that exclude the real failure.
5. Calibrate every important abstract counterexample against the Go engine or a
   real controller trace. If the trace shape cannot happen, fix the model. If it
   can happen and Go does not match the desired result, file the implementation
   or design defect.

The campaign report must state both:

- what was proved at which finite bounds; and
- which common-sense claims remain unmodeled or unconnected to production.

## Reuse from `DavidLangworthy/tla-k8s`

The sibling repo is cloned at `/workspaces/tla-k8s` from branch
`codex/codespaces-ci-security`.

Useful assets:

- a clean separation between failure-rich safety and stable/failure liveness;
- observed-vs-actual node, GPU, cordon, and link state;
- scheduler Reserve/Permit/Bind cleanup and stale-cache transitions;
- pod generation/recreation and single-bind history;
- explicit run records, timeout summaries, and incremental finite-size configs;
- a Codespaces pattern with Java 21, TLC, tmux, and negative controls.

Do not import it as a claimed jobtree model. It has no Run, Reservation,
GPULease, funding replay, derived class, gang width, spare role, settlement,
two-plane ledger/workload state, or sole-committer/sole-closer boundary. Its
generic invariants therefore cannot catch most of jobtree's historical defects.
Use transitions and tooling selectively, then map them to jobtree concepts.

The prior exploratory run is also a warning about scale without focus:
`safety-1p2n-gen0` generated about 115 million states in 651 seconds and still
had about 4.6 million queued. Prefer property-driven worlds and minimal
counterexamples to simply enlarging every cardinality.

## Highest-value model surfaces

### 1. Lease/workload/capacity lifecycle

This is the first model, because an open lease bills and holds GPUs forever.
Represent at least:

- the scheduler plugin as the only lease minter at PreBind;
- the controller closer, including every terminal and failure path;
- lease facts, closure stamps, pods, physical slots, run phase/conditions;
- gang minimum width, partial mint windows, spares, swap intent, and checkpoint
  grace;
- node failure versus cordon, stale observation, and declined swap;
- the ledger and workload planes moving together.

Check common-sense safety and bounded-progress properties corresponding to the
runtime oracle plus its documented rejected/reaper invariants. Never strengthen
the invariant into a reaper merely to simplify the state space.

### 2. Funding history and prospective mutations

Use Apalache/SMT for bounded two-state mutation claims and TLC where a trace is
material.

Canonical target:

`INV-ACCRUAL-PREFIX-IMMUTABLE`

For an effective-dated mutation at `tm`, no hour tuple ending at or before `tm`
may change payer, class, envelope/window epoch, or conserved accounting fields.
Recorded adjustments and explicit window rotations are separate operation
types, not Boolean escape hatches.

Current-main counterexamples must remain executable and visible:

- a namespace conflict turns 32 spent GPU-hours into 0;
- moving `Start` erases prior charges;
- reducing `MaxGPUHours` rewrites/clamps prior charges;
- an incorrect settlement key carries old-window history into a renewed window.

Reuse the `LedgerCompactionAccounting` substrate. Distinguish the desired
persisted-history model from pt2 production compaction and from the current
live-snapshot replay. Do not claim pt2b correspondence where code does not exist.

### 3. Authenticated ownership, grants, and conservation

Model these together over a bounded delegation graph:

- `INV-GRANT-LOCAL`;
- `INV-OWNER-INJECTIVE`;
- `INV-OWNED-IS-LOCAL`; and
- instantaneous `INV-SUBTREE-CONSERVE`.

Measure depth 1 through 4 if feasible and report the exact bound. Current
`Parents` entries are not authenticated grant traces; the interior-tier
exemption and foreign-Budget denial are expected counterexamples. Do not select
grantor-side `Budget.Spec.Grants` versus a cluster registry without an owner
decision. The graph input can validate shared properties while that location is
settled.

Windowed-hours subtree conservation is not specified. Resolve its semantics
before encoding it.

### 4. Reservations and gang progress

Model the states that repeatedly produced mirror-gate defects:

- Pending, BlockedFunding, activated, released, and unwound;
- durable block cause/onset with no stale countdown or backlog gauge;
- partial gangs allowed to complete only to recorded width;
- below-minimum GPU holders unwound at policy deadline `U`;
- every string/enum mirror that decides whether a blocked object is revisited.

### 5. Resolver/reaper consequences

SMT cannot decide whether a legal state may be destroyed; legality is an input.
Use executable simulation and adversarial consequences for:

- unfunded-first reclaim;
- demand-driven rather than deficit-driven destruction;
- checkpoint and swap grace;
- lottery identity/weight publication matching actual selection; and
- no funded victim closed merely because accounting or identity changed.

## Tool split

- TLC: interleavings, failure traces, liveness, deadlines, controller progress,
  and shortest readable counterexamples.
- Apalache/SMT: pure bounded two-state funding/grant mutations and arithmetic
  conservation.
- Go tests/fuzzing: model-to-implementation correspondence and differential
  properties against `funding.Evaluate`.
- Static/adversarial review: missing transitions, false legality assumptions,
  reaper consequences, and design contradictions.
- KWOK/kind when a Kubernetes event ordering or apiserver behavior must
  calibrate the model.

Do not launch the repository's full milestone adversarial-review workflow
without owner scheduling. This campaign may use focused reviews and Fable design
analysis, but the expensive archived harness remains a separate decision.

## Compute strategy

Start on the 4-core/16-GB Codespace:

- ordinary Apalache heap: 5.5 GB;
- opt-in stateful heap: 10 GB;
- TLC workers: automatic, with isolated metadata directories;
- every long run in tmux with wall time, peak RSS, bound, outcome, and last
  progress recorded.

The prior direct universal seeded-fold encoding exhausted 10 GB and was killed
near the VM limit at 12.5 GB. Do not repeat it unchanged. First improve or bound
the encoding. If a valuable, non-vacuous check still needs more than roughly
10-12 GB heap, report the exact command and measured failure. Provision a
32-64-GB cloud runner and resume there; do not weaken the property to fit the
Codespace.

## Design clarity

Formalization is allowed to stop on an ambiguous design. Record:

- the smallest concrete scenario with two plausible outcomes;
- which current source or decision records conflict or stay silent;
- the invariant each choice enables or falsifies; and
- the operational/tenant consequence.

Ask the owner to decide before encoding. Once decided, update the binding
decision record first, then the model and implementation. A solver should not
become the accidental author of product semantics.

## Deliverables

1. Historical-defect/model-coverage matrix.
2. Common-sense invariant catalog, with legal-state witnesses for subtle rules.
3. Focused TLA+/TLC modules and must-fail configs.
4. Bounded Apalache/SMT modules and depth/cost matrix.
5. Executable Go conformance and differential rails.
6. Counterexample and design-finding report against an exact commit.
7. CI split between cheap continuous rails and opt-in large checks.
8. Explicit residual blind spots and the next highest-value experiment.

Every substantive claim names the reviewed SHA, model bounds, tool/version,
runtime, and whether it is a design theorem, a production-conformance result, or
only a counterexample in an abstraction.
