# Design brief: model-checking the node-failure and funding-ledger path

*A brief for the engineer who will write the TLA+ specification. Written 2026-07-09 against
the tree at branch `fix/r27-invariant-oracle` (`controllers/run_controller.go` was under
active edit for task #50 while this was researched; citations into that file name functions
rather than trusting line numbers to hold). This document tells you what to build, what not
to build, and why — it contains no TLA+.*

> **Reading this on `main`?** The brief is landed ahead of the work it describes, so that the
> engineer writing the spec is not blocked on a code review. Three artifacts it cites are still
> in flight on [PR #80](https://github.com/DavidLangworthy/jobtree/pull/80) and
> [PR #79](https://github.com/DavidLangworthy/jobtree/pull/79):
>
> | Cited as | Where it is today |
> |---|---|
> | `pkg/invariant` — the four shipped invariants and, crucially for §5, the four **rejected** ones | PR #80 |
> | `docs/project/adversarial-review-playbook.md` — the nine-class defect taxonomy | PR #79 |
> | `docs/project/history-run-phase-writers.md` — one field, seven consecutive defects | PR #80 |
> | `docs/project/reviews/2026-07-09-…/` — the review that confirmed specimen 7 | PR #80 |
>
> §5 (*the invariants that must NOT be written*) is the section that most depends on `pkg/invariant`'s
> package doc. Read that PR before writing §5's witness configs. Everything else stands alone.
>
> Already on `main` and safe to read now: [`../formal-verification-survey.md`](../formal-verification-survey.md)
> and the three existing specs under [`specs/`](../../../specs/).

## Where this sits

Two pieces of prior art precede this brief, and it is downstream of both.

**The survey.** [`../formal-verification-survey.md`](../formal-verification-survey.md)
(on `origin/main`; it merged after this branch forked, so read it there) settled the
landscape questions, and this brief does not relitigate them: there is no upstream
machine-checkable whole-Kubernetes lifecycle spec in any formalism, so upstream's prose
semantics and KEPs are the baseline of record; the serious research efforts are Kivi
(exhaustive SPIN checking of interacting controllers, minimal counterexamples at small
scale), Anvil (proved liveness of real controllers via a TLA embedding in Verus), and a
Real-Time ABS executable model; and a whole-system, god's-eye TLA+ model is legitimate,
with the discipline coming from property-driven abstraction, tiny exhaustive worlds, and
symmetry — not from refusing to model the whole system. All of that is adopted here
without further argument.

**The in-repo specs.** `specs/` holds three working TLA+ specs — `ReservationLifecycle`,
`BudgetConservation`, `QuotaEvaluation` — with `.cfg` files, run by `make spec-check`
(Makefile:191) and, crucially, `make spec-counterexamples` (Makefile:197), where each
historical bug ships a configuration that **must fail**: `ReservationLifecycleBug.cfg`
sets `GuardEnabled = FALSE` and TLC must find the R9 double-bind;
`BudgetConservationRacy.cfg` sets `Serialized = FALSE` and TLC must overspend the
envelope — which is the result that pins `MaxConcurrentReconciles = 1`
(`controllers/kube/reconcilers.go:32`) and is cited in the Bridge's own doc comment
(`controllers/kube/bridge.go:50-52`). The house style, per `specs/README.md`:
*deliberately tiny, tens of lines of state, close enough to the design that it cannot
drift far from reality.* This brief extends that tradition; it does not replace it.

### What this brief adopts from the survey, and where it departs

Adopted: prose semantics as the source of truth for Kubernetes behavior (Pod Lifecycle's
"pods are scheduled once; a failed node's pods are deleted or replaced, never moved" is
exactly jobtree's swap — a new pod and a new lease, never a mutation); Kivi's
find-counterexamples-at-minimum-scale posture; the TLC-first tooling choice; the closing
checklist (fix finite sizes up front, write environment assumptions down explicitly,
symmetry sets, control state separated from payload state, weakest fairness, bounded
abstract time).

Departed, with reasons, on the three questions the survey leaves open for us:

**1. The property-to-state mapping table under-serves this system, and §4 replaces it.**
The survey's table is written for a generic scheduler plugin: its rows are reservation
leaks, queue starvation, QueueingHint liveness, DRA readiness races. Those are real
properties of kube-scheduler, but they are not where this repo's blood is. Nine defect
classes are documented in `docs/project/adversarial-review-playbook.md`, each with a
shipped specimen, and the majority are **ledger** defects: a lease that outlives its
workload, a workload that outlives its lease, a funding class read stale, a terminal
phase that depends on slice iteration order. The survey's table has no row for "an open
lease charges a budget and holds GPUs" — the one fact `AGENTS.md` says generates most of
the defects here — and no notion of the ledger/workload/topology split. Two of its rows
survive contact with this codebase: "no leaked reservation after failure" (already
covered by `ReservationLifecycle` and, on the plugin side, by the future gang-commit spec
sketched in §1.4) and "cordon and node failure never admit new pods to an excluded node"
(handled structurally: the bridge drops unusable nodes at load, `bridge.go:178`). The
rest of the table you need is in §4, where every row carries the historical defect it
would have caught.

**2. Layered abstract/concrete refinement: decline it.** The survey recommends an
abstract spec refined by a concrete one, on the model of the scheduler-refinement paper
and Anvil's layering. For this repo it is the seductive wrong move, for three reasons.
First, there is no mechanized refinement from any spec to the Go code and none is
planned (§7); an internal abstract↔concrete refinement would therefore prove consistency
between two artifacts written by the same hand in the same week from the same
understanding — the design-level analogue of playbook class 8, where code and test agree
because they share the author's misconception. Second, the house already has a working
alternative to one layered edifice: several tiny specs, each pinned to one decided
semantic. `QuotaEvaluation.tla` *is* the abstract funding spec; the spec proposed here
uses a collapsed funded/unfunded derivation whose fidelity to `QuotaEvaluation`'s walk is
argued in a comment, not proven by refinement mapping — a known, documented, cheap gap
rather than an expensive proof about our own consistency. Third, Anvil's layering earns
its cost because it terminates in a proof about the implementation; ours cannot (the
implementation is Go, not Verus). Concession: keep the survey checklist's "explicit
abstraction map, even as comments" — every spec variable's comment names the Go state it
abstracts, as the existing specs already do — so a mechanized refinement remains possible
if the family ever justifies it.

**3. Apalache: scope creep, for now.** At house sizes (§8) the state spaces are in the
10^4–10^6 range; TLC exhausts them in seconds, and the binding constraint is model
fidelity, not state count. The must-fail counterexample tradition leans on TLC's BFS
producing *shortest, legible* traces ("TLC finds the double-bind in a handful of states"
— `specs/README.md:28-30`); Apalache's symbolic traces are less legible and its
temporal-property support is weaker. And it is a second toolchain in CI where today
`make spec-check` curls one jar (Makefile:186-188). This mirrors the repo's
borrow-vs-build rule: the substrate that is already wired in is enough. The one future
exception worth recording: if the GPU-hour *integral* dimension ever enters a spec
(today elided, per `QuotaEvaluation`'s header), the arithmetic-heavy state would suit SMT
better than enumeration — revisit then, not before.

Two more obvious approaches, considered and rejected out loud: **model the whole
kube-scheduler** (rejected — filter/score/queueing mechanics are upstream's semantics to
maintain; we model only the contract the plugin relies on: the scheduler picks a node,
Permit gates the gang, PreBind mints, bind may fail; everything else is nondeterminism)
and **one giant spec** (rejected — beyond state explosion, the must-fail tradition needs
one knob per historical defect, and knobs in a monolith interact; six knobs in one spec
is 64 configurations of which 63 are meaningless). **Verify the Go code directly** is
rejected in §7 where the honest limits are laid out.

---

## 1. Scope and boundary

The proposed deliverable is **one new spec** — working name `NodeFailure.tla` — in
`specs/`, alongside the three that exist, plus a documented follow-up candidate
(§1.4). It models the node-failure/swap/reclaim path of
`controllers/run_controller.go:HandleNodeFailure` and its callees, the fencing decision
of `controllers/kube/reconcilers.go:nodeFailed` (line 437), and the plugin's mint at
PreBind (`cmd/scheduler/plugin/plugin.go:234`) — because every counterexample this brief
demands (§6) lives on that path, and no existing spec touches it.

### 1.1 The one modeling decision that everything else hangs on: atomicity granularity

Every production mutation passes through `Bridge.WithWorld`
(`controllers/kube/bridge.go:101-115`), which holds one mutex, loads a fresh world,
runs one engine entry point, and applies the diff. Engine entry points therefore **are
atomic with respect to each other** in production, and `pkg/invariant`'s steady-tier
invariants are defined to hold exactly "on RETURN of every engine entry point"
(`pkg/invariant/invariant.go:70-74,155`). The spec must adopt the same granularity: the
visible states between top-level actions correspond one-to-one to the states
`invariant.CheckSteady` sees. This is what makes §7's trace-validation bridge cheap, and
it is why the spec's state invariants and the Go oracle's invariants can be *the same
list*.

With one deliberate exception: `HandleNodeFailure` is internally order-sensitive — its
pass-2 loop visits leases in slice order, and one run can be visited twice
(`docs/project/history-run-phase-writers.md`, "Why this function"). The spec therefore
models that one entry point as a **run-to-completion sub-machine**: a `busy` flag
disables all other actions while, one sub-step at a time, an existentially chosen
remaining lease is processed. TLC then explores *every* processing order by construction.
This is the load-bearing reason to build the spec at all, so it gets said plainly: the
LAST-WRITER-WINS defect (playbook class 3) was confirmed in Go by hand-running all 24
orderings of a four-lease failure across 5 process repeats, and an earlier version of
that very test missed the bug because the fixture held one lease too few
(`history-run-phase-writers.md`, Summary; review README, finding 1). In TLA+,
nondeterministic interleaving is the *default semantics* — every ordering is a behavior,
and the fixture size is a swept constant rather than a test author's guess. The class
that took seven specimens and a 24-permutation harness to pin in Go is a free consequence
of `Next` being a disjunction. This is the survey's Kivi argument with a fresh, local,
confirmed specimen attached.

### 1.2 What is modeled, and why (each inclusion argued)

- **Node lifecycle, as a five-valued state**: `Ready`, `Cordoned`, `NotReady`, `Fenced`
  (tainted `node.kubernetes.io/out-of-service`), `Deleted`. Included because the entire
  content of R21 and its amendment (`docs/project/remediation/R21-cordon-not-failure.md`)
  is the difference between these values: cordon and NotReady are *signals*, fencing and
  deletion are *assertions*, and only the assertions license a swap (`nodeFailed`,
  `reconcilers.go:437-444`, which deliberately takes no clock). Cordon must additionally
  remove the node from the *capacity view* without touching its workload — the bridge
  drops unusable nodes at load (`bridge.go:178`), which is why rejected invariant #2
  exists (§5).

- **The machine plane, separate from the API plane.** For each node, the set of
  containers *actually running* (per run/group, per slot) is its own variable, distinct
  from the control plane's pod records. This is the partition requirement and it is not
  optional: every serious bug on this path came from conflating the API server's view
  with the machine's. NotReady means the control plane cannot *hear* the kubelet — a
  partitioned kubelet keeps its containers running and never honors a graceful delete
  (R21 amendment, table of events; `reconcilers.go:409-418`). The model must be able to
  say "the API object is Terminating; the container is running," because that is the
  state in which the pre-amendment code started a second copy of a rank.

- **Pod lifecycle, collapsed to four states**: `Intent` (emitted by the controller,
  unbound), `Bound(node)`, `Terminating` (graceful delete issued; completes only if the
  node's kubelet is reachable), `Gone`. Deletion semantics carry the partition: a
  graceful delete against a `NotReady` node never completes; a force-delete (Pod GC)
  happens only when the node is `Deleted` (`gcOrphaned`) or `Fenced` (`gcTerminating`) —
  the two channels the R21 amendment identifies — and removes the API object *without*
  stopping the machine's container by itself. Included because the swap's safety argument
  is exactly "fencing implies the machine was stopped by something that can actually
  know," and the model must carry that argument as an explicit assumption (§3, A1), not
  as an accident of encoding.

- **The lease ledger**: per lease — owning run, group index, slot set (node × ordinal),
  role `Active | Spare`, open/closed, and closure reason drawn from the enum in
  `docs/concepts/leases.md` §1a (only the reasons an invariant reads: `NodeFailure`,
  `Swap`, `SwapDeclined`, `ReclaimedBySpare`, `RunFailed`). Slots, not machines: a slot
  is `node#ordinal` and "two runs may share a node and never share a slot"
  (`docs/concepts/leases.md:37-38`). R22 is unrepresentable without ordinals, so nodes carry ≥ 2
  GPUs in every config that exercises reclaim.

- **Funding, as a derived operator, never a variable.** Decision 3 of
  `docs/project/quota-semantics.md` is binding: class is a pure function of
  (budgets, open leases, clock) and is never stored. The spec honors this by making
  `Class(lease)` an operator over the ledger state — a collapsed two-class version
  (funded/unfunded) of `QuotaEvaluation`'s ranked greedy fill, one envelope, rank =
  (owner-ness, admission index). Deriving rather than storing is not a simplification;
  it is the semantics, and it has teeth in the model exactly as in the code: closing a
  lease mid-pass can change another lease's class, which is a real, currently-open
  question (`HandleNodeFailure` computes `ev` once before pass 2 — see the exploratory
  config in §6.7).

- **Run status**: phase (`Pending | Running | Failed`, plus `Complete` only if the
  completion action is included), `CheckpointDeadline` as a small counter, and the
  derived base-gang width. Phase is the subject of seven consecutive defects
  (`history-run-phase-writers.md`); it stays in.

- **The plugin's mint, as a separate action.** The controller emits pods; the plugin
  mints exactly one lease per pod at PreBind (`plugin.go:234-304`). Between emission and
  mint a healthy run holds less width than it reports — the `AwaitingMint` window that
  gates `INV-WIDTH-ASSEMBLED` (`invariant.go:80-84,131-133`) and that makes rejected
  invariant #4 false. If the spec fuses emit-and-mint into one step it will verify a
  design *stronger* than the real one, and every trace validated against it (§7) will
  spuriously fail in that window. Model `MintAndBind` as one atomic step (PreBind
  precedes bind; a bound pod always has its lease) but keep it a *distinct* step from
  the controller's pod emission.

### 1.3 What is deliberately NOT modeled, and why (each exclusion argued)

- **kube-scheduler's filter/score/queueing internals, including KEP-4247 QueueingHints.**
  The survey gives these two rows; decline both. No defect in the nine-class taxonomy
  lives there, jobtree's Filter constrains only GPU flavor (`plugin.go:111-125`), and the
  controller does not rely on requeue hints for correctness — parked runs *poll*
  (`pendingRunResync`, `reconcilers.go:39-52`). Node choice is pure nondeterminism in the
  model, bounded by the required affinity of a swap pod (which hard-targets the spare's
  node, `bridge.go:446-448`). If a starvation defect ever appears in the queue path, that
  is a separate tiny spec, written then.

- **DRA, device plugins, and GPU allocatable dynamics.** The device plugin appears in
  this codebase as exactly one integer: node capacity under `nvidia.com/gpu`
  (`bridge.go:32,182-184`). Model GPUs as a per-node constant. KEP-3063/KEP-5007
  machinery guards races jobtree does not have, because funding commitment happens at
  PreBind against CRDs, not against device claims.

- **The resolver's internal phase order and the lottery.** `pkg/resolver/resolver.go`
  is deterministic given a seed, already unit-tested for reproducibility, and its
  decided phase order (unfunded → spares → shrink → lottery, `quota-semantics.md:108`)
  is arithmetic, not concurrency. What the spec keeps from the resolver is only its
  *contract*: funded work is evicted by the resolver alone, and `HandleNodeFailure` may
  reclaim only `Unfunded` conflicts (the R22 ruling, in the long comment inside
  `HandleNodeFailure`'s pass 2). Cost of this exclusion, stated honestly: the confirmed
  half-plane defect in `applyResolution`'s terminal branch (open task #48 — leases
  closed, pods left running; review README, unresolved-high) is *not* rediscoverable by
  this spec. The `PlaneAgreement` invariant (§4.6) is written plane-generically so that
  a later `ResolverCut` action is checked against it for free the day someone adds it.

- **Reservations, follow-dependencies, elastic grow/shrink, budget windows.**
  Reservations have their own spec; the rest have no specimen on the failure path.
  Elasticity enters only as `minRunnableGPUs` — a per-run constant the demote-vs-fail
  branch compares against (`reclaimSquatter`; `applyResolution:1684-1699`).

- **Wall-clock time.** `nodeFailed` taking no clock is a design achievement the model
  should reproduce, not undo. The only timers kept are the checkpoint-grace deadline
  (a 0/1/2 counter decremented by an environment tick; expiry drives `failRun` via
  `Reconcile`, `run_controller.go:163-173`) and nothing else. No heartbeat ages, no
  grace windows, no `LastTransitionTime` — their absence *is* the R21 amendment.

- **Multiple budgets, sponsors, lending, proximity tiers.** `QuotaEvaluation` owns the
  ranking; this spec needs only the funded/unfunded boundary to exist and to move when
  leases close.

### 1.4 The follow-up spec, named but not commissioned: `GangCommit`

The one place the survey's generic table genuinely serves this repo is
Reserve/Unreserve/Permit. The plugin's gang gate has its own retired defect classes —
phantom pending leases leaking into funding decisions (R1), the gang wedging at N−1
after a transient bind failure until `committedCount` was folded into the width check
(R2; `plugin.go:184-204`), permit timeout re-formation — and their shape (concurrent
actors, stale views, a cap) is `BudgetConservation`'s shape, not `NodeFailure`'s. Write
it second, if at all: those fixes shipped with tests and the mechanism is close to a
spec that already exists. It is named here so its exclusion from `NodeFailure` is a
decision, not an oversight; gang *atomicity* as a run-level property (S1 below) is in
`NodeFailure` regardless, via width.

---

## 2. The three planes — the state variables

Most defects here are a state change written to one plane and not another (playbook
class 9). The spec's discipline: **three plane variables, no derived plane stored, and
every correspondence between planes is an invariant to check, never an assumption baked
into a combined data structure.** Concretely:

**Ledger plane.**
- `leases` — a finite function from abstract lease identities to records
  `[run, group, slots ⊆ Node × Ordinal, role ∈ {Active, Spare}, open ∈ BOOLEAN,
  closure ∈ Reasons ∪ {None}]`. Abstracts `v1.Lease` as the invariant oracle already
  projects it (`controllers/invariants.go:snapshotWorld`).
- `envelopeCap` — one constant; `Class(l)` derived per §1.2.
- `phase`, `graceLeft` — per run. Phase is ledger-plane because it is what admission and
  the sweep read; it is the seven-specimen field.

**Workload plane — two variables, and the split is the point.**
- `pods` — the *API server's* record: function from pod identity to
  `[run, group, role, state ∈ {Intent, Bound, Terminating, Gone}, node]`. Abstracts
  `ClusterState.Pods` / the real Pod objects.
- `machine` — the *kubelet's* truth: for each node, the set of `(run, group)` whose
  container is physically executing there, with the slots it occupies. No jobtree
  component reads this variable — that is the model telling the truth about
  observability. Only environment actions (container starts on bind when the node is
  alive; container stops on machine death, on completed graceful delete when the node
  is reachable, on nothing else) and the invariants touch it. "The control plane
  believes X while the machine is doing Y" is expressible as `pods` disagreeing with
  `machine`, and R21's corruption is a `machine` predicate (§4.1).

**Topology plane.**
- `nodes` — function from node to `{Ready, Cordoned, NotReady, Fenced, Deleted}`.
- The *capacity view* is derived, not stored: `usable(n) ≜ nodes[n] = Ready` (matching
  `nodeUsable`, `reconcilers.go:449-468`, which also returns false for fenced). The
  bridge's load dropping unusable nodes (`bridge.go:173-190`) means the engine's
  snapshot is this derived view — and the gap between "absent from the capacity view"
  and "dead" is rejected invariant #2.

Initial states should be **permissive**: `Init` admits any type-correct world satisfying
only the invariants in §4, not merely worlds the model's own actions can produce. The Go
engine's inputs arrive from an API server it does not control — prior versions, races,
manual edits — and the four rejected invariants of §5 are all states the engine must
*tolerate on input*. A permissive `Init` is also what makes the §5 witness
configurations checkable.

---

## 3. Requirements, in prose

Environment assumptions first (the survey's checklist: write them down):

- **A1 — the fencer is honest.** The environment may fence or delete a node only after
  that node's machine plane has actually stopped (machine death precedes fencing). This
  is the trust the design places in "something that can actually know" (R21 amendment:
  CCM instance-termination, operator, fencing agent). It is an *assumption*, not a
  theorem: the model's safety results are conditional on it, and a config that relaxes
  it (a lying fencer) demonstrates — deliberately — that no jobtree-side predicate can
  survive a false fencing assertion. Nothing in jobtree checks it; nothing can.
- **A2 — serialized engine.** Engine entry points do not interleave (the `WithWorld`
  mutex, §1.1). `BudgetConservation` already shows what happens without it.
- **A3 — a NotReady node may stay NotReady forever, and its containers keep running.**
  No fairness on partition healing. A genuinely dead node whose object is never deleted
  and never tainted never swaps: the run stalls. That is the accepted trade (R21
  amendment, "What it costs"), and the model must exhibit the stall rather than "fix" it.

### Safety

- **S1 — gang atomicity.** A fixed-width run reported `Running`, with no pod awaiting
  mint, holds base-gang width ≥ its minimum runnable width. ("Start together or not at
  all"; `INV-WIDTH-ASSEMBLED`, `invariant.go:80-84`.)
- **S2 — sole committer.** Leases enter the ledger through exactly one action, the
  plugin's mint from a bound pod carrying controller-stamped provenance. No controller
  action creates a lease. (In the code: PreBind is the only mint, `plugin.go:234`;
  `HandleNodeFailure` emits a swap *pod* and "mints nothing here" — the comment above
  `emitSwapPod`'s call site.)
- **S3 — monotone closure.** A closed lease never reopens; its slots, ending, and reason
  never change (`INV-CLOSED-MONOTONE`, `invariant.go:86-89,212-249`). In the model this
  is an action property over every step — cheap, and it guards future edits to the
  *model* the way `hack/antifake` guards the code.
- **S4 — fencing before swap, and no duplicate rank.** A swap pod is emitted only for a
  node in `{Fenced, Deleted}`; consequently at no reachable state do two machine-running
  containers exist for one `(run, group)`. The second clause is the outcome and the one
  to check (§4.1); the first is the mechanism.
- **S5 — no overdraft.** The sum of funded widths never exceeds the envelope cap.
  (Primary home: `QuotaEvaluation`. Restated here only because `NodeFailure`'s derived
  `Class` must not break it while leases churn.)
- **S6 — funded work is evicted only by the resolver.** `HandleNodeFailure` may close
  another run's lease only when that lease's *derived class at the moment of closure* is
  `Unfunded`, and only on an exact-slot conflict; any funded conflict declines the swap
  instead (the R22 ruling, in `HandleNodeFailure` pass 2's comment block).
- **S7 — demote, not kill.** A run whose only losses in a pass are reclaims (no stake of
  its own on the failed node) never exits the pass terminal: it ends `Running` (still at
  or above minimum width) or `Pending` (requeued). Quota-semantics R14/Decision 1.
- **S8 — terminal cleanliness.** A terminal run holds no open lease
  (`INV-TERMINAL-PRESENT`). Between top-level actions only — mid-sub-machine it is
  legitimately false, which is exactly why the Go oracle checks it on return only
  (`invariant.go:70-74`) and why §1.1's granularity decision matters.
- **S9 — the phase is a join.** The phase a run ends a `HandleNodeFailure` pass with
  equals the lattice-join (`Running < Pending < Failed`, `runPhaseSeverity`,
  `run_controller.go:1362-1366`) of the per-group verdicts reached during the pass —
  independent of processing order. Checked via a ghost variable that accumulates the
  verdict set (§4.5).

**A contradiction in the record, which S9 forces into the open.** The lattice semantics
(worst verdict wins — `runPhaseTracker`'s design, and the in-flight task-#50 fix that
threads it through `reclaimSquatter`) implies that a run which *both* loses its own
uncovered rank (no grace ⇒ `Failed`) *and* is reclaimed as a squatter (⇒ `Pending`)
deterministically ends `Failed`. But `history-run-phase-writers.md`'s summary and the
review record describe `Pending` as the correct outcome for that composite ("correctly
demoted and requeued") and `Failed` as the kill. Both cannot be normative. The spec must
take a side to state S9, and this brief recommends the lattice (Failed): losing a rank
without coverage or grace kills a fixed gang regardless of funding class — R14's
demote-not-kill governs *reclaim*, not gang death by hardware. S7 is scoped to
reclaim-only victims precisely so it stays true under either reading. **David should
confirm the lattice reading before the spec is written**; if he rules the other way, the
fix on this branch is wrong too, which is worth knowing before it merges.

### Liveness

Kept deliberately thin — the house harness checks invariants with deadlock checking off
(`-deadlock`, Makefile:184), symmetry sets are incompatible with TLC liveness checking,
and every immortal-lease specimen is *catchable as safety* at entry-point granularity
(the closure obligation is discharged within the same atomic pass, so "the sweep ran"
is a postcondition, not an eventually). State two, check them in a dedicated
no-symmetry config or not at all in v1:

- **L1 — fenced nodes drain.** If a node is `Fenced` or `Deleted`, then (under weak
  fairness of the node-failure handler) eventually no open lease names it.
- **L2 — grace resolves.** A run parked with a checkpoint deadline eventually either
  re-admits or fails when the counter expires. (Requires modeling re-admission at least
  as a stub action; mark speculative — no specimen.)

---

## 4. Invariants to check

Format per entry: name; plain-English statement; expected TLA+ shape; **the concrete
historical victim**. An invariant with no victim is flagged speculative.

**4.1 `NoDuplicateRank`** — at most one machine-running container exists per
`(run, group)`. *Shape:* state predicate over `machine`. *Victim:* R21, twice over — the
swap fired on `!nodeUsable`, so a bare `kubectl cordon` started a second live copy of a
training rank (specimens 1–3 commit, `a5c8eef`); and the first fix's own premise,
"NotReady past a 2-minute grace," was equally corrupt because a partitioned kubelet
keeps running its containers (R21 amendment). Note this invariant lives *entirely in the
machine plane* — it is unstatable in a model without §2's plane split, which is the
strongest single argument for that split.

**4.2 `ReclaimIsSlotExactAndUnfunded`** — every lease closed with reason
`ReclaimedBySpare` (a) occupied at least one of the exact `node#ordinal` slots the swap
needed, and (b) derived `Unfunded` at the step that closed it. *Shape:* action property
on the close sub-step. *Victim:* R22 — `leasesOverlap` compared `nodeFromSlot(slot)`,
stripping the ordinal, so a swap for run A closed run B's funded, co-located lease
unconditionally (playbook class 5; the helper survived, compiled, from `32c852c` in
October 2025 until `98b602d`). Clause (b)'s victim is the same specimen's ruling: the
old sweep closed the victim *unconditionally*, funded or not.

**4.3 `FailedNodeFullyHandled`** — when the node-failure sub-machine completes for node
n, no open lease of any role names n. *Shape:* state predicate guarded on
`busy = FALSE`, or equivalently a postcondition action property on the sub-machine's
final step. *Victim:* R25 — the loop `continue`d on `Role == Spare` *before* the
node-match test, so a node holding only a spare matched nothing; the resulting "no
active lease found" error was then string-matched and swallowed by the caller (two bugs
one line apart, playbook class 6 and its swallowed-sentinel corollary). The invariant is
outcome-based on purpose: it catches the *composite* whichever of the two component bugs
recurs, including through a door not yet built.

**4.4 `TerminalHoldsNothing`** — a run in a terminal phase holds no open lease.
*Shape:* state predicate between top-level actions (§1.1). *Victim:* three distinct
doors, which is why it is the highest-value single invariant in the family: the
decline-the-swap path closed the failed active lease and left the run's own spare open
forever, *with a test asserting the leak as correct* ("the spare must not be consumed",
specimens 1–3 commit); `applyResolution`'s terminal branch set `Failed` and never swept,
so a scoped cut could strand an out-of-scope spare (fixed in `98b602d`; the comment in
the `default:` arm calls it "the immortal-lease class, reached by a third door"); and
`failRun`'s indicative-mood comment "it never holds leases at this point" was falsified
by a new caller (playbook class 2).

**4.5 `PhaseIsJoin`** — at sub-machine completion, each affected run's phase equals the
lattice-join of the ghost verdict set accumulated for it during the pass. *Shape:* ghost
variable `verdicts[run]` (a set, appended by every verdict-producing sub-step) plus a
completion-guarded state predicate `phase[r] = Max(verdicts[r])`. *Victim:* playbook
class 3, seven times in one function — most recently `reclaimSquatter` writing
`Pending` directly from inside the pass-2 loop while `failGroupWithoutSpare` wrote
`Failed` through the tracker, so a squatter-and-victim run's terminal fate depended on
the storage order of `c.State.Leases` (confirmed by the R27 review, task #50; fix in
flight on this branch). Formulating order-independence as "final = join of ghost set"
turns a *confluence* property — awkward in any assertion language — into a plain state
predicate that TLC checks across all orderings it already explores. This entry is where
the model checker most visibly out-earns the Go harness: see §1.1.

**4.6 `PlaneAgreement`** — the eviction obligation is discharged in both planes: (a) if
a lease was closed by an eviction reason (`ReclaimedBySpare`, `RunFailed` via the
sweep), then the victim `(run, group)` has no machine-running container on the closed
slots, **except** while the run holds an unexpired checkpoint deadline; (b) a bound
active pod's `(run, group)` has an open lease. *Shape:* state predicate between
top-level actions; the exemption keys on the deadline, never the phase — the playbook's
explicit instruction for the legal half-plane state (`failGroupWithoutSpare` parks the
run and deliberately leaves survivors running to checkpoint). *Victims:* the ledger-only
reclaim — `closeLease(other, "ReclaimedBySpare", now)` and nothing else, the victim's
container still executing on the exact slot `emitSwapPod` targeted one line later
(playbook class 9 specimen, fixed by `reclaimSquatter`); and, in the other direction,
`failRun` releasing every lease while touching no pods. Note honestly: an open sibling
of this class (task #48, resolver terminal branch) is out of this spec's boundary by
§1.3 — the invariant is stated plane-generically so a future resolver action inherits it.

**4.7 `CloseIsStamped`** — a closed lease carries an ending and a reason
(`INV-CLOSE-STAMPED`; funding's `effectiveEnd` bills a half-stamped lease to its start
instant, accruing nothing — `invariant.go:59-63`). *Included with a warning:* in the
spec, closure will almost certainly be one atomic action that sets all three fields, so
this invariant is true *by construction and checks nothing*. Its Go victims (the
hand-rolled closures in `cleanupDeletedRun` and old `applyResolution` — playbook class 7)
are **cloned-obligation** defects, and a design model cannot see clones: the model has
one `Close` action because the design has one closure concept; the code had three
implementations of it. Write the invariant anyway (it is two lines and guards model
edits), but record in the spec's header that class 7 and class 2 are *implementation*
defect classes with no design-level counterpart — the antifake lint
(`hack/antifake/soleclose.go`) is their rail, not TLC. Overstating what the spec covers
is how a green run becomes a false certificate.

**4.8 Speculative (no historical victim — hypotheses, not requirements):**
`SpareCoverageAnnounced` (a run whose last spare died is visibly uncovered — the
`SpareLostToNodeFailure` event exists, but nothing structural depends on it);
`NoLostFence` (a node deleted while the manager is down still drains — known residual
gap, explicitly assigned to R26's auditor rather than any predicate, R21 amendment
"Residual gap"; a spec of the *auditor* would be the place for it); `FreshClassAtClose`
(§6.7's exploration promoted to an invariant, if the exploration shows divergence and
David rules re-derivation is required). Mark all three `\* SPECULATIVE` in the spec if
written at all.

---

## 5. The invariants that must NOT be written

`pkg/invariant`'s package doc records four plausible invariants **rejected during
design because each is false in a state the engine legally produces**
(`invariant.go:26-46`). They are repeated here because the spec author is the next
person who will want to add them, and because a model checker makes them *more*
dangerous, not less. For each: the statement, the legal state that refutes it, and what
would actually happen if it went into a config.

**R-1. "No two open leases hold the same `node#ordinal` slot."** Refuted by: the engine
deliberately tolerates oversubscription and *declines a swap* rather than evict a funded
conflict — that is the R22 ruling itself (choosing between funded runs belongs to the
resolver). TLC, given this invariant, reports the tolerated oversubscription as a
violation with a tidy trace. The trace *looks like a bug report.* The natural "fix" is
to make the model (and then the design) evict the conflicting lease — which rebuilds the
exact corruption R22 fixed, now with a green model check certifying it.

**R-2. "A lease naming a node absent from the cluster is an orphan."** Refuted by: the
bridge drops *unusable* nodes from its snapshot at load (`bridge.go:173-190`), and a
merely cordoned node is unusable — so a healthy, running, correctly-charged lease
routinely names a node the engine cannot see. Enforcing this invariant means closing the
healthy leases of every cordoned node: it is the R21 corruption rebuilt in the ledger
plane. In the model this is why §2 keeps "node exists" and "node is in the capacity
view" as different predicates.

**R-3. "An open Spare lease implies an open Active lease of the same group."** Refuted
by: the spare-only run is an explicitly named, legal state (`Reconcile`'s adoption
block: a run whose only open lease is a leftover spare deliberately does not flip
Running — the comment near `run_controller.go:252-255`), and the plugin mints *per pod*
at PreBind, so a spare's lease can exist before any active's.

**R-4. "A `Running` run holds at least one open active lease."** Refuted by: the swap
window. `HandleNodeFailure` closes the failed active *and* the consumed spare, emits a
swap pod, and sets `Running`; the replacement lease is minted later, by the plugin, at
PreBind. For a single-group run there are zero open active leases at return, and
`Running` is correct (`invariant.go:41-45`; the `AwaitingMint` gate exists for exactly
this). A spec without §1.2's separate mint step would make this invariant *true in the
model and false in the design* — the worst possible divergence, because trace validation
(§7) would then reject correct production behavior.

**Why a false invariant is worse than a missing one.** A missing invariant is a known
gap: it catches nothing and claims nothing. A false invariant produces counterexample
traces with all the authority of exhaustive search behind them, and there are only two
responses to a trace: fix the model, or fix the design. Whoever does not know the legal
state will fix the design — and every one of the four "fixes" above is a shipped
corruption class rebuilt (R-1 → R22's unconditional kill; R-2 → R21's cordon
evacuation; R-3/R-4 → a reaper that closes the swap window's and the spare-first mint's
legal states, i.e. the immortal-lease *cure* becoming a healthy-lease killer). This is
the design-level image of playbook class 8: self-consistent, confidently wrong, and
wearing a green badge. `invariant.go:46` says it in one line — "an invariant that is
wrong is not a weaker safety net. It is a reaper." And there is a second-order cost: the
first spurious red teaches everyone to shrug at red, which retires the whole suite.

**The witness configurations — the house tradition, extended.** The counterexample
configs prove the spec can rediscover the bugs; add four **witness configs** proving the
spec can *represent the legal states* that refute R-1…R-4. Mechanism: same must-fail
machinery — a config `NodeFailureWitnessN.cfg` asserts the *negation* (e.g.
`INVARIANT NoSlotOversubscription`) and TLC **must fail**, exhibiting the legal state as
its "counterexample." These lines go in `spec-counterexamples` beside the bug configs.
What this buys: if a future edit to the spec quietly makes one of these states
unreachable — or someone adds a rejected invariant to the main config — CI goes red
*before* the model starts certifying a reaper. The model is kept honest about what it
tolerates, not only about what it forbids. (R-1's witness needs the permissive `Init` of
§2, since the model's own actions may never *produce* oversubscription — the code
tolerates it as an input, and so must the spec.)

---

## 6. The counterexample configurations

Every entry follows the `ReservationLifecycleBug.cfg` contract: the base config
(`NodeFailure.cfg`, all guards TRUE) must check clean under `spec-check`; each config
below turns exactly one guard OFF and **must fail** under `spec-counterexamples`.
One knob per historical defect, so the knob's name is the defect's name.

**6.1 `NodeFailureR21.cfg` — `RequireFence = FALSE`.** The failure trigger becomes
`¬usable(n)` (the pre-R21 predicate: cordon qualifies). *Expected trace, one sentence:*
cordon n1 while run A's rank runs there, the handler swaps the group onto A's spare on
n2, mint-and-bind starts the replacement — and `NoDuplicateRank` is violated with A's
rank machine-live on both n1 and n2. (A variant worth one extra run, not one extra
config: `RequireFence = FALSE` plus a NotReady node shows the "grace window" draft
fails identically — no timer fixes it, which is the amendment's whole point.)

**6.2 `NodeFailureR22.cfg` — `SlotGranularReclaim = FALSE`.** The conflict test
compares node names (the model's `nodeFromSlot`). *Expected trace:* A's spare holds
n2#0 while funded run B's active lease holds n2#1; n1 fails; the reclaim sees "same
node," closes B's funded lease `ReclaimedBySpare` — violating
`ReclaimIsSlotExactAndUnfunded(a)`. Requires ≥ 2 GPUs on n2, which is why §8 fixes
GPUs-per-node at 2.

**6.3 `NodeFailureR25.cfg` — `SparePassFirst = FALSE`.** Reverts to the single loop
that skips spares before the node match, with the unhandled result swallowed (model the
swallow as: no error surfaces, nothing retries). *Expected trace:* n3 holds only A's
spare; n3 is deleted; the handler matches nothing and completes; `FailedNodeFullyHandled`
is violated — an open, budget-charging lease pointing at a deleted node, forever.

**6.4 `NodeFailureDeclinedSwap.cfg` — `ReleaseSpareOnDecline = FALSE`.** Declining a
swap closes only the failed active. *Expected trace:* n1 fails under A's rank; A's
spare slots conflict exactly with funded B; the swap declines; A (no grace) goes
`Failed`; A's spare lease stays open — `TerminalHoldsNothing` violated. This is the
immortal spare whose Go test asserted the leak as correct, so say it in the config
comment: *the assertion was the bug* (playbook class 8's canonical specimen).

**6.5 `NodeFailureLastWriter.cfg` — `TrackedPhases = FALSE`.** Verdict writers assign
`phase` directly instead of folding through the join. *Expected trace:* unfunded run B
holds a rank on failing n1 *and* squats A's spare slots on n2; TLC picks the ordering
where the reclaim's direct `Pending` lands after the tracker-path's `Failed`, so the
final phase is not the join — `PhaseIsJoin` violated. The config comment must carry the
comparison this brief exists to make: the same defect needed a hand-written 24-ordering
Go test run 5 times to confirm, and an earlier fixture missed it by being one lease too
small; here the orderings are the model's default semantics and the fixture size is a
constant TLC sweeps. Cite `history-run-phase-writers.md` and task #50 from the config.

**6.6 `NodeFailureHalfPlane.cfg` — `EvictBothPlanes = FALSE`.** Reclaim closes the
lease and touches no pod (the pre-`reclaimSquatter` behavior). *Expected trace:* B's
squat lease on n2#0 is closed; B's container keeps machine-running on n2#0; the swap pod
mints and binds onto that exact slot — `PlaneAgreement(a)` violated (and, one step
later, `NoDuplicateRank`'s machine-plane cousin: two containers on one slot). The config
comment should note the direction table from playbook class 9: each half-plane lie is
invisible for a different reason, and this config demonstrates only one direction — the
other direction's live sibling (task #48) is outside this spec's boundary by §1.3.

**6.7 Exploratory, not must-fail: `NodeFailureStaleClass.cfg` — `FreshEvaluation =
FALSE` (matching today's code) vs `TRUE`.** `HandleNodeFailure` derives the funding
evaluation once, before pass 2, then closes leases — so a class read later in the pass
can be stale with respect to closures made earlier in it (flagged low, unadjudicated, in
the R27 review). No confirmed victim, so this is a *question for the model*, not a rail:
run both settings, diff the reachable outcomes, and hand David the answer. If they
diverge on anything that matters, promote to §4.8 and file the finding; if not, record
that the staleness is benign at this world size and say why.

**Wiring:** two lines per config in the Makefile — the clean ones appended to
`spec-check`, the must-fail ones (6.1–6.6 and the four witnesses) to
`spec-counterexamples` with the existing `! $(TLC)` idiom — plus a row in
`specs/README.md`'s table. The counterexample section of that README grows six entries
whose text should name the R-number and the playbook class, as the existing two entries
name R9 and the admission race.

---

## 7. Refinement, and the honest limits

**What a green run proves.** That the *design* — the fencing rule, the slot-granular
funded-aware reclaim, the phase join, the two-plane eviction obligation, at
`WithWorld` granularity, under assumptions A1–A3 — has no reachable state violating §4
within §8's bounds. That is the same class of claim `BudgetConservation` already makes,
and it has already earned its keep once: that spec's result is load-bearing in two
production comments (`reconcilers.go:29-32`, `bridge.go:50-52`).

**What it does not prove.** There is no refinement mapping from the spec to the Go
code, so a green TLC run says nothing about whether `run_controller.go` implements the
checked design. The gap is not hypothetical; this repo has a precise, recent
demonstration of its shape. The R27 review's one *critical* finding (confirmed by hand,
task #49/R28): `reclaimSquatter`'s pod eviction keys on the `LabelGroupIndex` pod label
— and the sole production mint path, `admission.PodLeaseWithRole`
(`pkg/admission/admission.go:205-235`), never stamps that label on leases, while the
production pod path made the group lookup dead — so the both-planes fix was **inert in
production**. The design says "evict the group's pods"; the model would say it too, and
verify it; the code keyed on a label nobody sets. No design-level artifact can see that
defect. The same holds for playbook classes 2 (comment-as-enforcement) and 7 (cloned
obligation) per §4.7, and for the swallowed-sentinel corollary of class 6: they are
properties of *code shape*, and their rails are the AST lints and the invariant oracle,
not TLC. Write this list into the spec's header comment; the most dangerous consumer of
a green check is the one who thinks it covered these.

**The cheapest credible bridge: trace validation.** Do not build a refinement proof;
replay reality against the model. The pieces are unusually cheap here because they
half-exist:

- `pkg/invariant` already projects a full `World` (runs, leases, phases, width facts)
  at the entry and exit of every engine entry point (`snapshotWorld` in
  `controllers/invariants.go`), at exactly the atomicity granularity the spec uses
  (§1.1). Add an opt-in reporter (the `Warn` hook shape already exists,
  `invariant.go:304-311`) that serializes `(site, before, after)` JSONL during `make
  verify`'s envtest and unit runs. ~100 lines of Go, zero production risk (test-only,
  like the oracle's own enablement).
- A small trace-spec (the standard pattern: a TLA+ module whose `Next` reads the next
  record and asserts the pair is a step of `NodeFailure`'s next-state relation, modulo
  the abstraction map). The abstraction map is the honest 20% of the work: concrete
  names must map onto symmetric model values, and any trace exceeding the model's
  constants (a fourth node, a third group) must be *rejected loudly, not clamped
  silently* — a clamped trace validates nothing and reads as green.

What trace validation still would not catch, stated plainly: paths no test exercises
(R25's spare-only node had no test, hence would have produced no trace — the corpus is
the ceiling); production-only phenomena outside the engine (informer staleness is
mostly closed by `WithWorld`'s direct reads, but the plugin's PreBind races the
controller's world loads, and no engine trace sees that); and the R28 class *again* —
the projection is deliberately computed with the same helpers the controller uses
(`controllers/invariants.go`, header comment), which kills drift but means projection
and code can share a misconception. Three honest holes; name them in the harness README
when it is built.

**The costlier alternative, proposed and deferred:** generating conformance tests from
TLC's state graph (`-dump` the graph; drive `ClusterState` through each action sequence
— unusually feasible here because the engine is pure and "`ClusterState` plus a static
clock *is* a simulator," playbook, 'Where to look'). Defer it: it needs an
action-to-engine-call interpreter that is itself a second abstraction map that can
drift, and trace validation exercises the same seam for a tenth the code. Revisit only
if trace validation proves out and the corpus-ceiling hole starts hurting.

---

## 8. Sizing

House calibration is 2 owners / 3 nodes / 5 events, and every counterexample in §6 fits
inside it. Concretely, for the base config:

- **Nodes = {n1, n2, n3}, GPUs per node = 2** (slot space of 6). Three is the minimum
  that separates "the failed node," "the spare's node," and "an uninvolved node" (R25's
  spare-only node must be *only* a spare's); two ordinals per node is the minimum that
  makes R22 expressible at all (co-located ≠ co-slotted) and is fixed by that
  requirement, not by realism.
- **Runs = 3, groups per run ≤ 2**: A (funded, fixed-width, one spare), B (unfunded —
  the squatter/victim), C (funded, no spare — R22's co-located bystander). Two groups on
  one run is required once, by 6.5: the last-writer defect needs two verdict writers
  reaching one `Status.Phase`, which was precisely the case the too-small Go fixture
  missed. Every other scenario runs with the second group's constant set to 1.
- **One envelope, cap 2–4**; funded/unfunded derived per §1.2. One envelope suffices
  because §4's invariants read only the funded/unfunded boundary; the multi-envelope
  ranking is `QuotaEvaluation`'s job.
- **Environment budget ≈ 5 events** per behavior: bound counters on cordon/NotReady/
  machine-death/fence/delete transitions plus one grace tick. Checkpoint grace ∈
  {0, 1}: zero (fail-fast) and one tick (park-then-expire) exercise both
  `failGroupWithoutSpare` branches; nothing needs a longer clock.
- **Leases ≤ 6** (A: 2 active + 1 spare; B: 2; C: 1), pods likewise. The sub-machine's
  order-exploration cost is the factorial of *matching* leases per failure, which stays
  ≤ 4! = 24 sub-orders — the exact number the Go harness had to enumerate by hand.

**Symmetry and views.** Declare `Ordinal` values symmetric, and node identities
symmetric *only if* the failure target is chosen nondeterministically (it is); run
identities are not symmetric in the base config because funding class and spare
ownership distinguish all three — do not force it. Remember TLC refuses symmetry with
liveness: L1/L2, if checked at all, get a dedicated config with symmetry off. Use a
`VIEW` that drops lease identities (identify a lease by
`(run, group, role, slots, open)`), drops closure reasons except the two §4.2/§4.6
read, and drops all message strings — payload, not control state. The harness's
existing flags (`-deadlock`, i.e. deadlock checking off — Makefile:184) mean a
quiesced terminal world is fine, so no artificial stuttering actions are needed.

Sweep upward once (4 nodes, 3 runs, cap 5) as a one-off sanity run before freezing, per
Kivi's incremental-scaling posture; do not put the swept sizes in CI — the tiny world
is the contract, the sweep is due diligence.

---

## 9. What lands in the repo, and open questions

**Deliverables** (for the engineer, in order): `specs/NodeFailure.tla`;
`specs/NodeFailure.cfg` (clean); six must-fail bug configs and four must-fail witness
configs (§5, §6); Makefile wiring into `spec-check`/`spec-counterexamples`; a
`specs/README.md` row and counterexample entries; the spec's header carrying the §7
honest-limits list and the §3 environment assumptions. The trace-validation harness is
*proposed, not commissioned* — it is a separate decision with Go-code cost.

**Open questions for David, in priority order:**

1. **S9's contradiction (§3):** for a run that both loses an uncovered rank (no grace)
   and is reclaimed as a squatter in one pass, is the correct terminal verdict the
   lattice-join (`Failed`, as `runPhaseTracker` and the in-flight task-#50 fix imply)
   or `Pending` (as the history document's and review's prose implies)? The spec cannot
   be written without an answer, and the in-flight fix depends on the same answer.
2. **Boundary of v1 (§1.3):** confirm the resolver's exclusion knowing its cost — the
   spec will not rediscover open task #48 (resolver terminal branch's half-plane).
   Alternative: commission a third tiny spec for `applyResolution`'s settlement after
   this one ships.
3. **§6.7:** is the stale-evaluation exploration wanted in v1, or deferred until the
   review's low-severity finding is adjudicated?
4. **Trace validation (§7):** approve or defer the ~100-line reporter + trace-spec as a
   follow-up task.

---

## 10. Rulings on the open questions

Answered by Claude (Opus) on 2026-07-09, acting on standing instruction to decide and record rather
than block. **David: these are reversible; say so if you disagree.** Each is written so the
engineer can proceed today.

### (1) S9's contradiction — the verdict is `Failed`. The brief is right; the prose was wrong.

A run that both loses an uncovered rank *and* is reclaimed as a squatter in one pass ends **`Failed`**,
deterministically, by the lattice-join.

The distinction the record had collapsed:

- **R14's demote-not-kill governs RECLAMATION.** Unfunded work that lost capacity it never paid for
  should requeue and re-admit when capacity returns. That is `reclaimSquatter`'s `Pending`.
- **It does not govern DESTRUCTION.** A rank that died on a fenced node with no cover kills the gang
  whatever its funding class. That is `failGroupWithoutSpare`'s `Failed`.

A run that suffers both is dead for the reason that has nothing to do with the reclaim. Requeuing it
would be requeuing a corpse.

Two independent confirmations. A skeptic in the review reverted `reclaimSquatter` to its pre-change
form and observed that both orderings then produced `filler.phase=Failed` — **deterministically**. So
`Failed` is what the system did before the defect was introduced, and the lattice restores it.

The consequence for the record: the review lens's phrase *"permanently killed instead of
demoted-and-requeued"*, and the first version of `history-run-phase-writers.md` which repeated it,
were **backwards**. The defect is the **nondeterminism**, not the verdict. Both documents are now
corrected, and the code and its test already agreed on `Failed`.

*This question is the single most valuable thing the TLA+ effort has produced so far, and not one line
of TLA+ has been written. The spec could not state S9 while two documents disagreed about the join's
value. Formal specification found a semantic contradiction in the prose before it found anything in the
code.*

### (2) Boundary of v1 — accept the resolver's exclusion, with one amendment.

Exclude `applyResolution` and `pkg/resolver` from `NodeFailure.tla`. The boundary is right: the
resolver's scope filter, lottery seeding, and per-class ranking would triple the state space to
rediscover defects we already have compiled tests for.

**But do not concede task #48.** The half-plane failure it names — *a terminal run releases every lease
and keeps every container running* — is reachable through `HandleNodeFailure`'s own post-loop sweep
(`closeRunLeases` closes leases; nothing removes pods), not only through `applyResolution`. It is
therefore **in scope** for this spec, and the `PlanesAgree` invariant (§4) should catch it. If it does
not, the invariant is too weak.

A second tiny spec for `applyResolution`'s settlement is worth commissioning **after** this one ships,
not instead of it.

### (3) The stale-`ev` exploration — defer.

Hold it until task #52 adjudicates whether the stale evaluation misclassifies at all. Writing an
exploratory config for a mechanism nobody has confirmed is how a spec acquires a state variable it
never needed. Note it in the spec header as a deliberate omission with the task number, so the next
reader knows it was considered.

### (4) Trace validation — approve as a task, do not build it yet.

Filed. The reasoning in §7 is correct: a green TLC run proves the *design* is sound and says nothing
about the Go code, and the honest bridge is to replay `snapshotWorld`'s projections against the spec's
next-state relation. It is the right ~100 lines.

It is also the second-cheapest thing on the list, and **R28 is the argument for it**: the sole
committer never stamps `LabelGroupIndex`, so a lease-derived group never matched a pod-derived one, and
the eviction that was supposed to free a GPU freed only a row in a ledger. **No design model would ever
have seen that**, because in the model the label is a function of the group. Trace validation is
exactly the instrument that would.

Ship `NodeFailure.tla` first. Then decide.

### A note on what this exercise has already returned

Before any TLA+ exists: one semantic contradiction in the binding documents, found by trying to state
an invariant. That is the case for the whole effort, and it should go in `specs/README.md` when the
spec lands.
