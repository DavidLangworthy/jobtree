# What to check with an SMT solver — and what not to

Note requested by David, 2026-07-26. Scope only; not a schedule and not a commitment to build.

The gate order the recommendation argues for is **rails first, differential property test second, SMT
last** (`FINAL-RECOMMENDATION.md` §P7). This note says what the solver is *for* when its turn comes,
so the decision is about cost rather than about scope.

## The test for "is this an SMT question"

Three properties together. Missing any one means a different tool is cheaper or more honest.

1. **Two-state.** The claim is `∀ state S, ∀ single admissible mutation m: P(S, m(S))`. No
   interleavings, no fairness, no traces. A claim about *executions* is TLA+'s job, and the repo
   already has specs for those (`specs/`).
2. **Pure function of inputs.** The thing under judgment is a fold over `(budgets, grants, leases,
   clock)`, not a controller loop. `Evaluate` qualifies; anything reading informer state does not.
3. **Decidable encoding at bounded size.** Quantifier-free plus bounded reachability. Unbounded
   ancestry is where this stops being cheap.

## In scope — highest value first

### 1. `INV-ACCRUAL-PREFIX-IMMUTABLE` — the canonical target

*For any admissible mutation `m` with effective time `t_m`, attribution of every hour tuple ending at
or before `t_m` is identical before and after.*

The best fit in the whole design: two-state, over a pure fold, and it is the invariant that owner
Ruling 6 ("no one can take back quota that has been spent") makes **total** rather than scoped. Also
the one where a solver beats testing outright, because the interesting witnesses are *adversarial
combinations* of mutation time, window boundary and lease interval — exactly what a generator finds
by luck and a solver finds by construction.

**Encode:** leases as intervals with a payer key; envelopes as (window, cap) pairs; the mutation as a
delta on the funding inputs with an effective time. **Ask for:** a counterexample where a
pre-`t_m` hour changes payer or class.

**Two excluded cases must be excluded by TYPE, not by the caller's choice**, or the property is
vacuous: explicit window rotation (ratified release-on-renewal) and recorded adjustment events.

**Precondition, and it is a real one:** a funding object with no effective time must be rejected as
input to a historical claim, not treated as eternal. The recommendation found A's zero-timestamp
convention made A's own invariant pass vacuously on the very fixture cited as its falsifier
(`FINAL-RECOMMENDATION.md` §P6). A solver will happily prove a vacuous theorem.

### 2. `INV-GRANT-LOCAL` — non-inducibility, the security property

*A mutation touching only funding objects outside `chain(ns)` leaves `OwnerOf(ns)`, ns's conflict set
and ns's loss onset unchanged.*

This is the property that closes the reproduced cross-tenant DoS, so it is worth machine-checking
rather than testing by example: the value of "no such state exists" is much higher here than "these
17 states are fine". Two-state and pure.

**Cost lives here, and it is the whole cost question.** The grant trace is reachability over the
delegation DAG. Bounded depth is decidable and fast; unbounded is a fixpoint and the cheap win
evaporates. **So this interacts directly with the generations work:** every generation added to the
family/grant model raises the depth bound the solver must carry. Decide the maximum delegation depth
as a modelling parameter *before* encoding, and record it — an SMT result is only a result at the
depth it was checked, and a proof at depth 4 says nothing about depth 5.

### 3. `INV-OWNER-INJECTIVE` — cheap, do it in the same encoding

Set-cardinality over the same graph the trace already needs. Nearly free once (2) is encoded, and it
catches the P5 exemption class the reproduction found.

### 4. `INV-SUBTREE-CONSERVE` — worth it, but not first

*Funded consumption whose payer's grant lineage traverses `P` never exceeds `P`'s incoming allocation,
per flavour, per dimension.*

Arithmetic plus reachability, so encodable in principle. Two reasons it is not the first target
despite being the biggest gap:

- **It is currently FALSE** (`FINAL-RECOMMENDATION.md` §3.3 — nothing aggregates descendant Budgets).
  Proving a false property finds a counterexample immediately, which a two-line unit test also does
  and more cheaply. A solver earns its keep *after* the mechanism exists, checking that no exotic
  combination slips past it.
- **The windowed-hours dimension is not yet specified.** An ancestor's GPU-hour cap and a
  descendant's are integrals over possibly different windows, and "traverses `P`" is undefined when
  `P`'s window opened after the descendant's consumption began. The recommendation flags this as
  unresolved design, not unresolved encoding. **Do not encode an underspecified predicate** — that is
  precisely how P7's own warning about a "confidently wrong oracle" comes true.

The **instantaneous concurrency** dimension is well defined today and could be checked alone. That is
a reasonable first slice.

## Out of scope — and why, so this is not relitigated

- **Anything about the lottery.** `INV-ODDS-PUBLISHED-MATCH-DRAWN` is an equality between two
  computations — a differential test executes both and is strictly better than modelling either. And
  fairness of a weighting is a *statistical* claim over many draws, which SMT does not express.
- **Durations and deadlines** (`U`, blocked-age escalation). Temporal, about executions. TLA+ or a
  simulation, not a solver.
- **Reaper questions** — "would this repair destroy something legal". A judgement about which states
  are legal, i.e. the thing a solver takes as *input*. This is the consequence seat's job and cannot
  be delegated to a tool.
- **Anything reading informer or apiserver state.** Not a pure function; fails test (2).
- **`INV-BLOCKED-VISIBLE` and the other steady/transition rails.** Checkable on every engine entry
  point through the hook that already exists (`pkg/invariant`), continuously and for free. A solver
  would be slower, later, and no stronger.

## Before spending anything

1. **Measure the `Graph.Tier` ancestry encoding at the chosen depth bound.** Sol's own reason for
   deferring SMT was that this repo has no solver-cost data. That is still true.
2. **Fix the effective-time precondition first** (see 1) or the headline theorem is vacuous.
3. **Answer P6/Ruling 6's remaining mechanical question** — the settled-accrual key needs a window
   epoch (`FINAL-RECOMMENDATION.md` §3.8c) — because the solver would otherwise be checking a
   property the storage layer breaks.
4. **Fix the depth bound as an explicit modelling parameter** and state it in any result. See (2).
