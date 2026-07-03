# Fundamentals: RQΛ calculus

This page describes the quota- and topology-aware calculus that Jobtree's
controllers implement. The model is operational: Budgets, Runs,
Reservations, and Leases are the durable facts; status fields are derived
caches for humans, dashboards, and metrics.

The current implementation follows this calculus with the known gaps called
out at the end of this page and tracked in
[`docs/project/remediation-plan.md`](project/remediation-plan.md).

## 0. Informal one-liner

RQΛ is a small operational calculus for auditable GPU scheduling. It evaluates
quota from immutable lease facts, packs work onto topology-aware slices, binds
now or records a reservation, and resolves contention by reclaiming unfunded
work before touching funded work.

## 1. Entities and notation

- **Time**: continuous wall time \(t \in \mathbb{R}\). Budget windows are
  intervals \(W = [\text{start}, \text{end})\). The accounting horizon
  \(P\) defaults to 24 hours and is configurable by the manager.
- **Owners**: teams \(o \in O\), with an optional parent DAG for family
  sharing.
- **Nodes**: \(n \in N\), with labels such as region, cluster, and
  fast-fabric domain, and GPU capacity per flavor \(f \in F\).
- **Selectors**: predicates \(\sigma\) over node labels.
- **Runs**: user-facing work requests that compile into one or more placement
  groups. A group stays within one fast-fabric domain; a run may span domains
  unless its locality requires otherwise.

## 2. Syntax (data)

### Budget envelopes

\[
e ::= \langle \text{id}, \text{owner}=o, \text{flavor}=f,
\text{selector}=\sigma, \text{window}=W?,
\text{concurrency}=C, \text{maxGPUHours}=B?,
\text{sharing}\in\{\text{family},\text{none}\},
\text{preActivation}?, \text{lending}? \rangle
\]

- Budgets \(B(o)\) are finite sets of envelopes. Envelope names are unique
  within one Budget; leases therefore record both `paidByBudget` and
  `paidByEnvelope`.
- Parent edges on Budgets define the family graph. Family sharing needs no
  lending policy, but an envelope can opt out with `sharing: none`.
- Lending policy applies only to sponsors/non-family borrowers and may set
  ACL, concurrency, and GPU-hour caps.
- Aggregate caps optionally bound a set of envelopes inside one Budget:
  \(A = \langle f, E, \text{maxC}?, \text{maxB}? \rangle\).

### Runs

A Run surface spec is:

\[
\langle o, f, \text{totalGPUs}=G, \text{groupGPUs}=g?,
\text{allowCrossGroupSpread}, \text{malleable}?,
\text{spares}=s?, \text{funding}? \rangle
\]

Malleability supplies `minTotalGPUs`, `maxTotalGPUs`, `stepGPUs`, and an
optional `desiredTotalGPUs`. Funding supplies `allowBorrow`,
`maxBorrowGPUs`, and sponsor owner names.

### Reservations

\[
\langle \text{runRef}=R, \text{intendedSlice}, \text{payingEnvelope},
\text{earliestStart}=t_0 \rangle
\]

The reservation records an intended slice and an envelope name. At activation
the current Budget set resolves that name to a concrete payer or fails if no
usable envelope remains.

### Leases

\[
\ell ::= \langle \text{runRef}=R, \text{nodes}=S,
\text{role}\in\{\text{Active},\text{Spare}\},
\text{paidByBudget}, \text{paidByEnvelope},
\text{interval}=[t_s,t_e), \text{reason} \rangle
\]

Lease role is only a slice fact: active work or held spare. Funding class is
not stored on the Lease. It is derived from budgets, leases, runs, and the
clock as one of:

- **Owned**: funded by the run owner's own envelope.
- **Shared**: funded by family excess.
- **Borrowed**: funded by a sponsor through lending policy.
- **Unfunded**: backed by no current quota; hours are metered separately and
  do not charge envelope caps.

Reasons include `Start`, `Grow`, `Shrink`, `Swap`, `NodeFailure`,
`ReclaimedBySpare`, `ReclaimUnfunded(seed)`, and `RandomPreempt(seed)`.

## 3. Well-formedness

- **Envelopes**: name, flavor, selector, and positive concurrency are
  required. If both start and end are set, end must be after start and
  `maxGPUHours <= concurrency * windowHours`.
- **Aggregate caps**: each referenced envelope must exist in the Budget; cap
  names are unique; cap limits are non-negative or positive as appropriate.
- **Budgets**: envelope names are unique within the Budget.
- **Runs**: GPU demand and malleability fields are positive and internally
  consistent; `maxBorrowGPUs` is positive when set.
- **Reservations**: point at a Run and a non-empty intended slice or domain.
- **Leases**: specs are immutable consumption facts. The controller uses open
  leases to derive node usage; at most one active lease should occupy a GPU
  slot at a time.

## 4. Funding Evaluation

Funding is a pure function:

\[
\text{Eval}(B, L, R, t, P) \rightarrow
\{\text{class}(\ell), \text{envelope usage}, \text{run usage}\}
\]

It is implemented in `pkg/funding`. No control path reads funding class back
from status.

Evaluation groups live leases into claims by `(paidByBudget, paidByEnvelope,
runRef)`. For each envelope:

1. Sponsor claims that satisfy lending policy are contractual carve-outs,
   bounded by the lending caps.
2. Owner/family claims fill by rank: envelope owner first, then children,
   siblings, and cousins; ties use admission time and run key.
3. Claims that do not fit are Unfunded. Fixed-width claims are all-or-nothing;
   malleable claims can be partly funded lease-by-lease, lowest group first.

Only Owned, Shared, and Borrowed width and GPU-hours charge envelope and
aggregate caps. Unfunded work continues to run while physical capacity exists,
accumulates unfunded GPU-hours, and may re-fund automatically when quota
returns.

## 5. Cover (who may pay for new work)

Cover is the admission-side view of the same evaluation. It asks how much
width a prospective claim could receive if ranked into the current facts.

The search order is:

1. the run owner's envelopes,
2. parent envelopes,
3. sibling envelopes,
4. cousin envelopes,
5. sponsor envelopes, only when `funding.allowBorrow` is true.

Each family tier tries same-location envelopes before cross-location
envelopes. Sponsor use must satisfy lending policy and `maxBorrowGPUs`.

Admission lookahead checks `width * P` against remaining GPU-hours, so normal
admission does not create work that is born Unfunded. A reservation activation
is different: if the system already promised the run and physical capacity is
available, it can start against a real envelope as Unfunded and later re-fund
by arithmetic.

## 6. Pack (where to run)

Packing is topology-aware:

- Groups are kept within one fast-fabric domain.
- Runs may span domains unless locality forbids it.
- Spares are placed per group when requested.
- If a fixed request cannot pack now, the controller plans a reservation.
- Malleable runs grow in `stepGPUs` increments when Pack and Cover both allow.

## 7. Operational Semantics

We write \(\Sigma \longrightarrow \Sigma'\) for a controller step over the
durable facts. The Kubernetes implementation materializes these steps as CRD
spec/status updates and Pods.

### Admission

If Pack finds a slice and Cover funds it, bind immediately:

\[
\frac{\text{Pack}(R,t)=S \quad \text{Cover}(R,S,t)=\varphi}
{\Sigma \longrightarrow \Sigma \cup \{\text{Start}(\text{Leases}(R,S,\varphi))\}}
\]

If Pack fails for capacity, a fundable admission first reclaims Unfunded work
only, then retries. If it still cannot pack, the controller creates a
Reservation. If Cover fails, the run parks or reserves rather than starting
opportunistically.

### Reservation activation

At or after `earliestStart`:

- If the referenced Run is already Running, Completed, or Failed, release the
  pending Reservation without materializing anything.
- If physical capacity is available and Cover succeeds, start leases and mark
  the Reservation Released.
- If physical capacity is available but Cover fails, start against the
  intended owner envelope as Unfunded when such an envelope still exists.
- If capacity is short but the demand is not currently fundable, wait; cutting
  other work cannot create quota.
- If capacity is short and the demand is fundable, resolve the capacity
  deficit in the order below, then bind.

### Resolver

The consolidated reclaim order is:

1. reclaim entirely Unfunded groups by seeded lottery within the class,
2. drop spares,
3. shrink malleable runs down to `minTotalGPUs`,
4. use the general seeded lottery over remaining groups.

The direct admission path stops after step 1; funded-work preemption is
reservation-activation-only.

### Elasticity

- **Grow**: when desired width exceeds allocated width, grow by at most one
  step if Pack and Cover allow.
- **Shrink**: close whole groups down to the desired width, preferring groups
  with non-owned derived funding before owned groups, then higher group
  indices.

### Failure and spares

On a node failure, the controller closes affected active leases. If a spare
lease for the same run and group exists, it closes that spare with `Swap`,
reclaims overlapping filler leases with `ReclaimedBySpare`, and opens a new
active lease on the spare nodes while preserving the payer fields. If no spare
exists, the run fails.

## 8. Properties

- **Derived funding**: class is recomputed from facts; status is never an
  authority.
- **No funded overdraft**: funded width and funded GPU-hours do not exceed
  envelope, aggregate, or lending caps. Unfunded consumption is visible but
  not charged against caps.
- **Owner recall**: an owner's claim on its own envelope does not depend on
  family borrowers. Lower-ranked family claims can re-evaluate as Unfunded.
- **Stable ties**: equal-tier claims use admission time and run key, so
  survivors do not reshuffle between evaluations.
- **Reservation safety**: a run's pending Reservation is released when the run
  binds directly or is already terminal, preventing double materialization.
- **Auditability**: leases and closure reasons are enough to replay funding
  class and usage for a past instant, modulo the Budget specs being replayed.

## 9. Worked Example

Run `train`: owner `org:rai`, `totalGPUs=96`, `groupGPUs=64`. Domain A has 72
free H100s; Domain B has 48. `org:rai` has a matching envelope with enough
concurrency and at least `96 * P` remaining GPU-hours.

1. Pack can place one 64-GPU group on A but cannot place the second group on
   B, so direct bind cannot complete.
2. Cover can fund the run, so the controller creates a Reservation targeting
   the intended domain.
3. At activation, if B is still short by 16 GPUs, the resolver first reclaims
   Unfunded work in scope, then spares, then malleable groups, then the
   general lottery if needed.
4. Once the slice is available, binder starts Active leases for the two
   groups. Funding status is derived from those leases at the current clock.
5. If the envelope later exhausts its GPU-hour cap, the run keeps running as
   Unfunded. If a new budget window opens, the same leases can evaluate as
   Owned again without rewriting them.

## 10. Mapping to CRDs and Controllers

- **Budget**: envelopes, parent graph, sharing mode, lending policy,
  aggregate caps, and derived usage/headroom status.
- **Run**: requested width, locality, malleability, spares, sponsor hints, and
  derived width/funding status.
- **Reservation**: future slice promise with lifecycle status.
- **Lease**: immutable consumption fact with Active/Spare role and payer
  fields; closure is recorded in status.
- **Manager**: serializes engine access through the Kubernetes bridge, uses
  fresh API reads, drives Runs, Reservations, Budgets, and node failures, and
  serves validation/defaulting webhooks.

## 11. Known Implementation Gaps

The calculus above is the implementation target, but these known gaps remain
tracked in the remediation plan:

- **R26**: node-failure swap is still per-lease and can fail multi-lease
  groups produced by funding-segment splits.
- **R27**: resolver accounting can double-count spares that are dropped before
  shrinking the same group.
- **R28**: Kubernetes bridge apply is not atomic; some partial API failures
  still need repair/fault-injection coverage.
