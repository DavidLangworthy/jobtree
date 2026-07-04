# Fundamentals: RQΛ calculus

This page describes the quota- and topology-aware calculus that Jobtree’s controllers implement. It is intentionally operational so that every scheduling decision is auditable from immutable lease facts: budgets/envelopes fund work, runs lower into placement groups with optional elasticity and spares, reservations plan future slices, and leases record immutable consumption facts whose funding class is *derived*, never stored.

The calculus is the theory. The controllers realize it with imperative, in-place updates to Kubernetes CRDs rather than a literal append-only event log; the two are intended to be observably equivalent (a property that can be model-checked), so the event-sourced vocabulary below (\(\Sigma\), append) is the model, and the CRD objects are the implementation of that model.

## 0. Informal one-liner

RQΛ is a small operational calculus for quota-aware, topology-aware, auditable execution on GPU fleets. It defines validity for budgets/runs/reservations/leases; covers funding first (classifying each slice as funded — owned/shared/borrowed — or unfunded) then packing; binds immediately or plans reservations; keeps over-quota work running as unfunded and reclaims it first under contention by a fixed ranking, breaking ties with an attested lottery; and treats live state as a pure function of budgets, leases, and the clock.

## 1. Entities and notation

- **Time**: continuous wall time \(t \in \mathbb{R}\); intervals \(W = [\text{start}, \text{end})\) with duration \(|W|\) hours.
- **Period**: a cluster-configured accounting horizon \(P\) (default 24h). It is the admission look-ahead and the evaluation/reporting cadence — **not** a lease lifetime; leases stay open-ended.
- **Owners (teams)**: \(o \in O\) with an optional parent DAG \(O \rightharpoonup O^*\) for family sharing.
- **Nodes**: \(n \in N\) with labels \(\text{labels}(n)\) (cluster, region, fast-fabric domain) and per-node GPU counts per flavor \(f \in F\). GPUs are addressed as slots \(n\#i\).
- **Selectors**: predicates \(\sigma\) over nodes (true iff a node matches \(\sigma\)).
- **Runs**: user-facing specs lowered into placement groups; each group stays within a fast-fabric domain, but a run may span domains unless it demands a single domain.

## 2. Syntax (data)

### Budget envelopes

$$
e ::= \langle \text{id},\; \text{owner}=o,\; \text{flavor}=f,\; \text{selector}=\sigma,\; \text{window}=W?,\; \text{concurrency}=C,\; \text{maxGPUHours}=B?,\; \text{sharing}\in\{\text{family},\text{none}\}?,\; \text{preActivation}?,\; \text{lending}=\langle\text{allow},\text{toACL},\text{lendC}?,\text{lendB}?\rangle? \rangle
$$

- The **window** is optional: an envelope is open-ended by default. A closed window does not make the envelope ill-formed — work simply coasts as unfunded until a window opens (see §4).
- **sharing** controls family access to this envelope’s excess: absent or `family` allows it, `none` opts out (the owner’s own use and sponsor lending are unaffected).
- **preActivation** \(= \langle\text{allowReservations},\text{allowAdmission}\rangle\) may admit or reserve work *before* the window opens; such work evaluates unfunded until the window opens, then re-funds by arithmetic.
- Budgets \(B(o)\) are finite sets of envelopes plus a **parents** list (the family DAG, acyclic) and optional **aggregate caps** \(A = \langle f,\; E \subseteq \{e\},\; \text{maxC}?,\; \text{maxB}? \rangle\) that bound a collection of envelopes (both bounds optional).

### Runs (surface → lowered)

$$
\langle o,\; f,\; \text{totalGPUs}=G,\; \text{groupGPUs}=g?,\; \text{allowCrossGroupSpread}=\text{true},\; \text{malleable}=\{\text{min},\text{max},\text{step},\text{desired}?\}?,\; \text{spares}=s?,\; \text{funding}=\langle\text{allowBorrow},\text{maxBorrowGPUs}?,\text{sponsors}\rangle?,\; \text{follow}=\langle\text{after},\text{onUpstreamFailure},\text{grace}\rangle?,\; \text{checkpoint}=\Delta? \rangle
$$

- **Lowering**: if \(g\) is present the run becomes \(m = \lceil G/g \rceil\) placement groups of \(g\) GPUs (write this \(\text{SHARD}(m)\)); if \(g\) is absent it is a soft grouping hint. A **malleable** run carries an elastic width in \([\text{min},\text{max}]\) stepped by \(\text{step}\) toward \(\text{desired}\) (default \(\text{max}\)); write this \(\text{INCR}\).
- **funding** carries the run’s borrowing intent: `allowBorrow` and a `sponsors` list gate the sponsor path in §4; family sharing needs none of this.
- **follow** joins runs into a workflow (a "job forest"): the run is not admitted until every run in `after` (same namespace) reaches `Completed`. This is a minimal dependency edge, not a combinator engine — `AND` is the conjunction over `after`, and there is no `SEQ`/`SHARD` composition language (§10).
- `SHARD`/`INCR` are descriptive names for grouping and elasticity, not a combinator engine; general DAG composition across components is **not yet built** (§10). `checkpoint` *is* wired — as a bounded safe-requeue window on an unspared node failure, not restore of in-process model state (§10).

### Reservations

$$
\langle \text{runRef}=R,\; \text{intendedSlice}=\text{Slice},\; \text{payingEnvelope}=e,\; \text{earliestStart}=t_0 \rangle
$$

### Leases (immutable facts)

$$
\ell ::= \langle \text{runRef}=R,\; \text{nodes}=S \subseteq \{n\#i\},\; \text{role}\in\{\text{Active},\text{Spare}\},\; \text{paidBy}=e,\; \text{interval}=[t_s, t_e),\; \text{reason} \rangle
$$

- **role** is a fact about the slice — `Active` work or a held `Spare` — and is the *only* role stamped on a lease.
- Reasons include \(\text{Start}\), \(\text{End}\), \(\text{Swap}\), \(\text{Shrink}\), \(\text{NodeFailure}\), \(\text{ReclaimedBySpare}\), \(\text{ReclaimUnfunded}(\rho)\), and \(\text{RandomPreempt}(\rho)\).

### Funding class (derived, never stored)

A lease’s **class** is not a field. It is a pure function of the payer facts against current quota, recomputed by whoever needs it:

$$
\text{class}(\ell) \in \{\text{owned},\; \text{shared},\; \text{borrowed},\; \text{unfunded}\}
$$

| class | backing | counts against | reclaim exposure |
| ----- | ------- | -------------- | ---------------- |
| **owned** | requester’s own envelope | that envelope’s \(C\) and \(B\) | general resolver phases only |
| **shared** | a family envelope’s excess | the lender envelope’s usage (visible to the lender) | owner recall re-ranks it unfunded |
| **borrowed** | a sponsor via a lending policy | the lender envelope + the lending caps | contractual — never unilaterally recalled |
| **unfunded** | nothing (metered separately) | nothing (visibility only) | first cut, by lottery, on demand |

### Ledger state (theory)

The model state \(\Sigma\) is a finite sequence of immutable events; “live” cluster state is a pure function of \(\Sigma\). In the implementation there is no separate event log: leases are the immutable facts (spec immutable, closed by a status event), and Run/Budget/Reservation *status* are non-authoritative caches reconciled in place. The classification of any slice is a pure function of \((\text{budgets}, \text{leases}, \text{clock})\), which is the auditable core the ledger framing describes.

## 3. Well-formedness (typing judgments)

- **Envelopes**: if windowed, \(B? \le C\cdot|W|\); if open-ended, \(B? \ge 0\). Selector total; flavor valid; `sharing` \(\in \{\text{family},\text{none}\}\) when set.
- **Aggregate caps**: `maxC`, `maxB` are both optional; the loose relation \(\text{maxB} \le \text{maxC}\cdot\sum_{e\in E}|W_e|\) is *informative*, enforced (if at all) at cover time, not by the validator.
- **Budgets**: all envelopes well formed; parent DAG acyclic.
- **Runs**: \(m \ge 1\) for groups; \(\text{min} \ge 1\), \(\text{max} \ge \text{min}\), \(\text{step} \ge 1\), \(\text{desired}\in[\text{min},\text{max}]\) aligned to \(\text{step}\) for malleable; \(\text{spares} \ge 0\).
- **Cross-object judgments** — selector matches the slice, pointwise/integral bounds hold for the paying envelope, a reservation’s window covers `earliestStart`, and slot exclusivity — are enforced at admission by Cover/Pack/resolver, **not** by the per-object CRD webhooks (a per-object validator cannot see the payer envelope or the other leases). The webhooks check field-level validity only.
- **Exclusivity**: at most one active lease per **GPU slot** \(n\#i\) at any \(t\). A node is deliberately shared across leases, groups, and runs at distinct ordinals.

## 4. Cover (who pays)

Cover resolves funding before placement, and “funding” is **not** pass/fail. Every requested GPU gets a lease; each lease is *classified* funded (owned/shared/borrowed) or unfunded (opportunistic). Funded consumption never overdrafts an envelope; unfunded consumption is metered in a separate, visible bucket and charges nothing.

**Proximity order.** Cover looks for capacity in one order, shared with the ranking below: the owner’s own envelopes, then **parents**, then **siblings**, then **cousins**, and — only if the run opts in (`funding.allowBorrow` with a matching sponsor) — **sponsors** last. Within each tier a same-location pass precedes a cross-location pass.

**The ranking (normative core).** Per envelope, order all claims by

1. tier — owner \(<\) child \(<\) sibling \(<\) cousin \(<\) sponsor,
2. then admission time, then run name (a deterministic tiebreak);

then greedy-fill against the envelope’s concurrency \(C\) and remaining integral. Claims that fit are **funded** (owned/shared/borrowed per relationship); the remainder — including any claim with no covering quota at all — are **unfunded**. This is a *ranking*, not a user-set priority: there is no knob to raise a run’s standing.

**Owner recall.** An owner’s claim outranks every family borrower on its envelope. When the owner needs headroom currently used by family, the family (shared) claim simply re-ranks to unfunded — there is no demotion event, and recall frees nothing physically; eviction happens later only if the GPUs are actually needed (§6).

**Feasibility per envelope \(e\)** (evaluated at the claim’s rank):

- **Window**: \(t \in W_e\) (or `earliestStart` \(\in W_e\) for reservations), unless `preActivation` admits early — then the claim is unfunded until the window opens.
- **Pointwise**: funded width \(\le C_e\).
- **Integral (admission look-ahead)**: the remaining integral covers \(\text{width} \times P\), where \(P\) is the accounting period. This prevents admitting work that would be *born* opportunistic; it replaces any notion of an expected job duration.
- **Aggregate caps** remain feasible.

**Metering and recovery.** Funded hours charge the envelope, clamped at its cap so funded consumption never overdrafts; unfunded hours accrue in the separate bucket. A funded claim whose envelope integral or concurrency no longer covers it is re-evaluated **unfunded** — it keeps its GPUs and keeps running (demote-not-kill), and re-funds automatically when quota returns (a reopened window, freed headroom). Budget-window expiry therefore no longer implies death.

**Family vs sponsor.** These are two distinct mechanisms:

- **Family (class shared)**: parent/sibling/cousin excess, drawn with **no** lending policy and **no** ACL. It is bounded only by the lender envelope’s own \(C\)/\(B\)/aggregate caps via the ranking, and is **recallable** by the owner. An envelope may opt out entirely with `sharing: none`.
- **Sponsor (class borrowed)**: a non-family lender, reached only when the run sets `funding.allowBorrow` and the lender’s `lending` policy permits (ACL match, `lendC`/`lendB` caps). Borrowed capacity is **contractual** — the lender pre-consented, so it is never unilaterally recalled.

Output: a classification of the demand; each lease is paid by exactly one envelope and carries a derived class. A funded admission that cannot pack **first reclaims unfunded capacity** (fleet-wide, since packing has not yet chosen a domain) before falling back to a reservation (§6).

## 5. Pack (where to run)

Packer decisions are topology-aware but quiet by default.

- Fill one fast-fabric domain at a time; each group of \(g\) GPUs stays within a single domain, packed at **GPU granularity** across that domain’s nodes (a node may be shared across groups and runs — allocation is per-slot, not whole-node).
- Runs may span domains unless a component declares a hard single-domain constraint. Minimize inter-domain cut as a soft objective.
- Pack is **all-or-nothing** on the width it is handed: if the requested width does not fit, return \(\text{None}\) so the planner can reclaim or reserve. Partial width is produced elsewhere — by partial **funding** in Cover (fund as much width as quota affords, lowest group first) and by incremental **grow** after admission (§6) — not inside Pack.

## 6. Operational semantics (small-step)

We write \(\Sigma \longrightarrow \Sigma'\) for a transition; in the implementation each transition is an in-place CRD update rather than an appended event. Rules are deterministic except for a published lottery seed under contention.

### Follow gate (before admission)

A run with `follow` dependencies is held in **Waiting** — outside admission entirely, so it never covers, packs, or reserves — until every upstream completes. This rule runs before Cover/Pack.

$$
\frac{\forall R' \in \text{after}(J):\; \text{phase}(R') = \text{Completed}}{\Sigma \longrightarrow \Sigma \;[\,J \mapsto \text{eligible}\,]} \quad \text{(Follow-Ready)}
\qquad
\frac{\exists R' \in \text{after}(J):\; \text{phase}(R') \notin \{\text{Completed},\text{Failed}\}}{\Sigma \longrightarrow \Sigma \;[\,J \mapsto \text{Waiting}\,]} \quad \text{(Follow-Wait)}
$$

If an upstream **fails** (or is deleted), `onUpstreamFailure` decides: `wait` (default) keeps \(J\) Waiting for a grace window — so a researcher can fix and resubmit just that stage — then fails \(J\); `fail` fails \(J\) at once. A follow cycle (or a permanently missing upstream past grace) fails \(J\) honestly rather than deadlocking it. Only **Follow-Ready** lets a run reach the admission rules below.

### Admission

$$
\frac{\text{Cover}(o, J, t) = \varphi \quad\; \text{Pack}(S, J) = \text{slice}}{\Sigma \longrightarrow \Sigma \cup \{\text{Start}(\text{Leases}(J, \text{slice}, \varphi))\}} \quad \text{(Bind-Now)}
$$

Some of those leases may be unfunded; admission still binds them.

$$
\frac{\text{Cover}(o, J, t) = \varphi \quad\; \text{Pack}(S, J) = \text{None} \quad\; \text{reclaimUnfunded restores a fit}}{\Sigma \longrightarrow \Sigma \cup \{\text{End(unfunded)}\} \cup \{\text{Start}(\text{Leases}(J, \text{slice}, \varphi))\}} \quad \text{(Reclaim-then-Bind)}
$$

A funded admission that fails to pack first clears the physical deficit from **unfunded** leases only (never funded work, and only when the demand is itself fundable). Only if reclaim frees nothing does it plan a reservation:

$$
\frac{\text{Cover}(o, J, t_0) = \varphi \quad\; \text{Pack}(S, J) = \text{None} \quad\; \text{reclaim frees nothing}}{\Sigma \longrightarrow \Sigma \cup \{\text{Create}(\text{Reservation}\langle J, \text{intendedSlice}, \text{payer}\in\varphi, t_0\rangle)\}} \quad \text{(Plan-Later)}
$$

### Reservation activation (at \(t = \text{earliestStart}\))

The shortfall splits along two axes — physical capacity and budget:

- **No deficit**: append Start(Leases); mark the reservation released.
- **Pure budget shortfall** (physical capacity is available, quota is short): the reserved run **starts anyway**, classified by the evaluation (typically unfunded), and re-funds when quota returns. No work is cut. This is R7 sharpened — a budget shortfall never lotteries funded work.
- **Physical-capacity deficit backing fundable demand**: apply the **consolidated structural cuts** deterministically, in order: **reclaim unfunded → drop spares in scope → shrink malleable runs → lottery**. Shrink releases whole placement groups down to `minTotalGPUs`. If a deficit remains, run the lottery:
  - Build conflict set \(C\) = leases in scope whose removal restores feasibility.
  - Seed \(\rho = H(\text{source}, t)\) where `source` is the reservation id (activation) or the run key (admission reclaim); \(\rho\) is attested on each closed lease’s reason (\(\text{RandomPreempt}(\rho)\), \(\text{ReclaimUnfunded}(\rho)\)).
  - Repeat until the deficit clears: pick an owner uniformly from owners(\(C\)); pick one token from that owner; append \(\text{End}(\ell, \text{RandomPreempt}(\rho))\).
  - Bind the reservation slice and mark released.
- **Physical deficit whose demand is itself unfundable**: park — cut nobody, wait for capacity.

### Failure and spares

- **Swap-from-spare**: if node \(n\) fails inside a bundle that has a spare in the same domain, end the failed lease (reason \(\text{NodeFailure}\)), reclaim any unfunded filler, and start the spare as active (reason \(\text{Swap}\) or \(\text{ReclaimedBySpare}\)). The promoted lease inherits the spare’s payer facts, so the derivation re-classifies it automatically.
- **No in-domain spare**: end the affected lease. If `spec.runtime.checkpoint` is a positive duration, the run parks **Pending** with `status.checkpointDeadline = now + checkpoint` and re-enters normal admission (bind directly, or reserve-and-wait) until that deadline; otherwise, or once the deadline passes without recovering capacity, the run is marked **Failed**. This is a bounded *safe-requeue* window, not checkpoint-restore of in-process model state (§10) — the workload container itself carries no state to restore.

### Elasticity (INCR)

- **Grow**: when Pack yields additional groups and Cover funds them, start new leases toward `desiredTotalGPUs` in `stepGPUs` increments up to `maxTotalGPUs`.
- **Shrink**: append \(\text{End}(\ell, \text{Shrink})\) for whole placement groups (the smallest schedulable unit). A *voluntary* shrink targets `desiredTotalGPUs` (with `minTotalGPUs` as the floor); the resolver's *structural* shrink under contention cuts to `minTotalGPUs`. A malleable run may also be *partially funded* — funded up to what quota affords (lowest group first), the remainder unfunded.

### Opportunistic (unfunded) execution

Idle capacity is always usable. Any run may run on it as **unfunded** — an over-quota run, a run whose window closed, or one with no covering quota are the same class. Unfunded work keeps its GPUs and keeps running until funded demand actually needs the capacity, at which point it is the first cut (the reclaim order above), and it re-funds automatically when quota returns. Spares are a distinct *role* (held capacity for swaps), reclaimed in the same consolidated order.

## 7. Properties (informal theorems)

- **Safety / exclusivity**: no two active leases overlap a GPU slot; selectors + per-slot accounting enforce this by construction (nodes are shared at distinct ordinals).
- **Budget compliance**: for every envelope \(e\) and time \(t\), **funded** pointwise concurrency and integral GPU-hour bounds hold with no overdraft; excess demand is admitted **unfunded** and metered separately, never charged. This holds because admission is **serialized** — one evaluation at a time behind a single world lock (see `BudgetConservation.tla`) — so concurrent admissions cannot overspend from stale snapshots.
- **Reservation soundness**: activation makes the reserved slice available at or after `earliestStart` — via consolidated cuts for a capacity deficit, or an opportunistic (unfunded) start for a budget deficit.
- **Ranking, not priorities**: there is **no user-set priority** — no high/med/low, no numeric level to game. Survival is decided by a fixed, deterministic ranking (proximity to the paying owner, then recency, then name) that classifies funded vs unfunded; the attested uniform lottery only breaks ties *within* a rank class. Reclassification is arithmetic, not a message.
- **Family-first vs sponsor**: family (shared) draws parent/sibling/cousin excess with no consent and is recallable; sponsor (borrowed) requires the lender’s lending policy (ACL + caps) and is contractual. Both obey the lender envelope’s \(C\)/\(B\) and aggregate caps.
- **Automatic recovery**: classification is a continuous function of \((\text{budgets}, \text{leases}, \text{clock})\); a running claim demotes to unfunded or re-funds as quota, windows, and integrals change, with no demotion/promotion event.
- **Auditability**: leases are immutable consumption facts; live classification is a pure function of budgets, leases, and the clock; status is a non-authoritative cache, enabling reproducible postmortems and proofs over traces.

## 8. Worked example (derivation)

Run: owner RAI, totalGPUs=128, groupGPUs=64, allowCrossGroupSpread=true. Supply: Domain A has 72 free GPUs, Domain B has 48 free GPUs. Budget: envelope `west-h100` for RAI with concurrency headroom ≥ 128 and an integral covering \(128 \times P\).

1. **Lower**: \(J = \text{SHARD}(2)\) of 64-GPU groups.
2. **Cover**: all 128 fund from `west-h100` — location matches, and the claim passes the pointwise and \(\text{width}\times P\) integral look-ahead, so both groups class **owned** (no unfunded, no borrowing here).
3. **Pack** attempts cohesive groups: \(\text{group}_1=64\) on A; \(\text{group}_2=64\) cannot fit on B (only 48 free) and may not split. Result: \(\text{Pack} = \text{None}\).
4. **Reclaim / Plan-Later**: if unfunded work on B could free 16 GPUs it is reclaimed and the run binds now; otherwise a reservation targets A plus future headroom on B at \(t_0 + \Delta\).
5. **Activation** with a 16-GPU physical deficit on B: the consolidated cuts run in order — reclaim unfunded, drop spares, shrink malleable (none here) — and only then the lottery within scope frees 16 GPUs; the binder places \(\text{group}_1=64\) on A and \(\text{group}_2=64\) on B, each single-domain. (A *budget*-only shortfall would instead start the run unfunded rather than cut anyone.)
6. **Growth variant**: as \(\text{INCR}(\text{min}=64, \text{max}=128, \text{step}=16)\), the binder starts 64 on A at \(t_0\), grows toward `desired` across A→B as capacity appears, and may reach 128, honoring the same cover + pack rules.

## 9. Mapping to CRDs and controller behavior

- **Budget ↔ envelopes**: selector, concurrency, optional window (`start`/`end`), optional `maxGPUHours`, `sharing`, `preActivation`, `lending` ACLs, and aggregate caps — plus `Budget.spec.parents`, the family DAG from which the whole proximity/sharing/recall order derives.
- **Run ↔ surface spec**: `totalGPUs`, `groupGPUs`, `allowCrossGroupSpread`, malleable `{min,max,step,desired}`, `spares`, and `funding = {allowBorrow, maxBorrowGPUs, sponsors}`; `checkpoint` bounds the safe-requeue window on an unspared node failure (§ Failure and spares). `Run.status.funding` reports the derived four-class breakdown with per-lender attribution.
- **Reservation ↔ CRD**: `runRef`, `intendedSlice`, `payingEnvelope`, `earliestStart` (spec immutable). Status evolves **Pending → Released** (reason `Activated`), or **Pending → Failed** for a run that vanished or cannot be kept.
- **Lease ↔ CRD/event**: `runRef`, nodes (GPU slots), `role` \(\in \{\text{Active},\text{Spare}\}\), `paidByEnvelope`, `interval.start`, `reason` (immutable once recorded; closed with End events). Funding class is **derived**, not a stored field.
- **Conflict resolution**: the consolidated cut order (unfunded → spares → shrink → lottery) and the published seed match the controller’s deterministic path; the ranking (not a priority) decides who is cut first.
- **Auditability**: dashboards and CLI derive live state and classification from budgets, leases, and the clock, mirroring the calculus.

## 10. Not yet built (reserved / roadmap)

Some vocabulary above names concepts the surface accepts but the controllers do not yet act on. They are kept in the calculus as intent, not as implemented behavior:

- **General DAG composition** (`SEQ`/`SHARD` combinators across multi-component runs) and a lease `compPath` provenance field — today a run lowers directly to groups; the `compPath` field exists but is unpopulated. The common ordering case this was meant to serve is now delivered by `follow` (§2, §6): runs joined by dependency edges, conjunction over `after`. A full combinator language remains out of scope.
- **Checkpoint-driven restart of in-process model state** — `Run.spec.runtime.checkpoint` now bounds a real *safe-requeue window* (a node failure without in-domain spare coverage parks the run Pending, retrying admission, until the checkpoint deadline; only then does it fail). What remains not-yet-built is restoring the *training process's own state* — the workload container carries none to restore, so this is a scheduling-level grace period, not a checkpoint/restore mechanism.
- **AutoRenew auto-extension** of open-ended envelopes — `Budget.spec.autoRenew` is read: `BudgetStatus.pendingRenewals` lists envelopes whose window closes within `notifyBefore`. It does not itself extend `end` (window rotation stays an explicit operator action); window-reopen re-funding already falls out of the evaluation arithmetic once an operator does rotate it.

The calculus above is deliberately small: one funding evaluation (Cover, classifying rather than gating), one placement function (Pack), immutable lease facts, and a deterministic, ranking-first pathway for contention. It is sufficient to analyze work-conservation, borrowing fairness, and reservation soundness while matching the implementation and CRD vocabulary.
