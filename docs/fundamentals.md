# Fundamentals: RQΛ calculus

This page describes the quota- and topology-aware calculus that Jobtree’s controllers implement. It is intentionally operational and event-sourced so that every scheduling decision is auditable from the ledger. The calculus matches the CRDs and controller semantics in this repository: budgets/envelopes fund work, runs compile to job DAGs with optional elasticity and spares, reservations plan future slices, and leases record immutable consumption facts.

## 0. Informal one-liner
RQΛ is a small operational calculus for quota-aware, topology-aware, auditable execution on GPU fleets. It defines validity for budgets/runs/reservations/leases, covers funding first then packing, binds immediately or plans reservations, resolves oversubscription by structural cuts then a lottery, and records all changes as immutable events in a ledger.

## 1. Entities and notation
- **Time**: continuous wall time \(t \in \mathbb{R}\); intervals \(W = [\text{start}, \text{end})\) with duration \(|W|\) hours.
- **Owners (teams)**: \(o \in O\) with an optional parent DAG \(O \rightharpoonup O^*\) for family sharing.
- **Nodes**: \(n \in N\) with labels \(\text{labels}(n)\) (cluster, region, fast-fabric domain) and per-node GPU counts per flavor \(f \in F\).
- **Selectors**: predicates \(\sigma\) over nodes (true iff a node matches \(\sigma\)).
- **Runs**: user-facing specs compiled into internal job DAGs; groups stay within a fast-fabric domain, but runs may span domains unless a component demands a single domain.

## 2. Syntax (data)
### Budget envelopes
\[
e ::= \langle \text{id}, \; \text{owner}=o, \; \text{flavor}=f, \; \text{selector}=\sigma, \; \text{window}=W, \\
\phantom{e ::=}\; \text{concurrency}=C, \; \text{maxGPUHours}=B?\,(B \le C\cdot|W|), \; \text{lending}=\langle\text{allow}, \text{toACL}, \text{lendC}?, \text{lendB}?\rangle? \rangle
\]

- Budgets \(B(o)\) are finite sets of envelopes; parents encode family sharing; the parent DAG is acyclic.
- **Aggregate caps** optionally bound a collection of envelopes: \(A = \langle f, E \subseteq \{e\}, \text{maxC}, \text{maxB}? \rangle\).

### Runs (surface → compiled)
- **Surface (researcher)**: \(\langle o, f, \text{totalGPUs}=G, \text{groupGPUs}=g?, \text{allowCrossGroupSpread}=\text{bool}=\text{true}, \text{malleable}=\{\text{minTotalGPUs}, \text{maxTotalGPUs}, \text{stepGPUs}\}?, \text{spares}=s?, \text{checkpoint}=\Delta? \rangle\).
- **Compiled job DAG (controller)**: \(D = \langle k, f, \text{perRank}, \text{Group}(g)?, \text{fault}=\{\text{spares}=s, \text{replace}=\text{SameDomain}|\text{Restart}\}, \text{time}=\{\text{checkpoint}=\Delta?\}\rangle\) and combinators \(\text{SHARD}, \text{INCR}, \text{AND}, \text{SEQ}\).
- **Default compilation**: if \(g\) is present, emit \(\text{SHARD}(m, D\{k=g\})\) with \(m = \lceil G/g \rceil\). If \(g\) is absent, treat it as a soft grouping hint.

### Reservations
\[\langle \text{runRef}=R, \text{intendedSlice}=\text{Slice}, \text{payingEnvelope}=e, \text{earliestStart}=t_0 \rangle\]

### Leases (immutable facts)
\[
\ell ::= \langle \text{runRef}=R, \text{compPath}=\pi, \text{nodes}=S \subseteq N, \text{role}=\text{Active}|\text{Spare}|\text{Borrowed}, \text{paidBy}=e, \text{interval}=[t_s, t_e), \text{reason} \rangle
\]

Reasons include \(\text{Start}\), \(\text{End}\), \(\text{Swap}\), \(\text{Shrink}\), \(\text{RandomPreempt}(\rho)\), \(\text{ReclaimedBySpare}\), and \(\text{Fail}\).

### Ledger state
The global state \(\Sigma\) is a finite sequence of immutable events (budget updates, reservation creation/activation, lease starts/ends, failures). “Live” cluster state is a pure function of \(\Sigma\).

## 3. Well-formedness (typing judgments)
- **Envelopes**: \(B? \le C\cdot|W|\); selector total; flavor valid.
- **Aggregate caps**: \(\text{maxB}? \le \text{maxC} \cdot \sum_{e\in E}|W_e|\) (loose upper bound).
- **Budgets**: all envelopes well formed; parent DAG acyclic.
- **Runs**: \(m \ge 1\) for shards; \(\text{min} \ge 0\), \(\text{max} \ge \text{min}\), \(\text{step} \ge 1\) for malleable; \(\text{spares} \ge 0\).
- **Reservations**: selector(e) matches intendedSlice; \(W_e\) covers \(\text{earliestStart}\).
- **Leases**: selector matches nodes; pointwise and integral bounds hold for \(e\) (using \(C\) and \(B\)); exclusivity invariant—at most one active lease per node at any \(t\).

## 4. Cover (who pays)
Cover resolves funding before placement. It prefers close family and location matches, then considers sponsors; there are no numeric priorities.

1. Candidate order (location-first): owner’s envelopes in location, then siblings via parent’s unused capacity, then parent, then repeat for other locations, then sponsors (lending.allow with ACL match).
2. Feasibility per envelope \(e\):
   - Window: \(t \in W_e\) (or earliestStart \(\in W_e\) for reservations).
   - Pointwise: \(\text{active}(e,t) + \Delta k \le C_e\).
   - Integral: \(\text{usedGPUHours}(e) + \Delta k \cdot \mathbb{E}[\text{duration}] \le B_e\) where \(B_e = \text{maxGPUHours}(e)\) if set, else \(C_e \cdot |W_e|\).
   - Aggregate caps remain feasible.
3. Output: a partitioning \(\varphi\) of the demand; each lease is paid by exactly one envelope. Borrowed and owned leases are indistinguishable once funded.

## 5. Pack (where to run)
Packer decisions are topology-aware but quiet by default.
- Fill one fast-fabric domain at a time with whole-node groups; each group of \(g\) GPUs stays local to a domain.
- Runs may span domains unless a component declares a hard single-domain constraint.
- Minimize inter-domain cut as a soft objective.
- If no full fixed bundle fits now, return \(\text{None}\) so the planner can create a reservation. Malleable \(\text{INCR}\) components may admit partial placements.

## 6. Operational semantics (small-step, event-sourced)
We write \(\Sigma \longrightarrow \Sigma'\) to mean “append events to the ledger.” Rules are deterministic except for a published lottery seed in oversubscription.

### Admission
\[
\frac{\text{Cover}(o, J, t) = \varphi \quad \text{Pack}(S, J) = \text{slice}}{\Sigma \longrightarrow \Sigma \cup \{\text{Start}(\text{Leases}(J, \text{slice}, \varphi))\}}\;\text{[Bind-Now]}
\]
\[
\frac{\text{Cover}(o, J, t_0) = \varphi \quad \text{Pack}(S, J) = \text{None} \quad \text{choose intendedSlice},\; \text{earliestStart} \ge t_0}{\Sigma \longrightarrow \Sigma \cup \{\text{Create}(\text{Reservation}\langle J, \text{intendedSlice}, \text{payer} \in \varphi, \text{earliestStart}\rangle)\}}\;\text{[Plan-Later]}
\]

### Reservation activation (at \(t = \text{earliestStart}\))
- If \(\text{deficit}(\text{scope}, t) = 0\), append Activate(Res) then Start(Leases); mark the reservation released.
- Otherwise, apply **structural cuts** deterministically: drop spares in scope; shrink \(\text{INCR}\) components by \(\text{stepGPUs}\) down to \(\text{minTotalGPUs}\). If deficit remains, run the lottery:
  - Build conflict set \(C\) = leases in scope whose removal restores feasibility.
  - Seed \(\rho = H(\text{scope}, \text{Res.id}, t)\) (published).
  - Repeat until deficit ≤ 0: pick an owner uniformly from owners(\(C\)); pick one token from that owner (an AND bundle counts as one); append \(\text{End}(\ell, \text{RandomPreempt}(\rho))\); reduce deficit.
  - Bind the reservation slice and mark released.

### Failure and spares
- **Swap-from-spare**: if node \(n\) fails inside a bundle that has a spare in the same domain, end the failed lease (reason=Fail), reclaim any filler, and start the spare as active (reason=Swap or ReclaimedBySpare).
- **Abort-and-requeue**: if no spare exists in-domain, end affected leases and optionally create a reservation to restart at the next checkpoint.

### Elasticity (INCR)
- **Grow**: when Pack yields additional groups and Cover is feasible, start new leases up to \(\text{maxTotalGPUs}\) in \(\text{stepGPUs}\) increments.
- **Shrink**: append \(\text{End}(\ell, \text{Shrink})\) events down to \(\text{minTotalGPUs}\).

### Opportunistic fill on spares
If a spare lease exists for owner \(o\) on nodes \(S\) and another run \(R'\) can use \(k \le |S|\) GPUs, end the spare on \(S_{\text{part}}\) and start a lease for \(R'\) (paid by its envelope). Reclaim via the same spare-swap rule on failure.

## 7. Properties (informal theorems)
- **Safety / exclusivity**: no two active leases overlap a node; selectors + exclusivity invariants enforce this.
- **Budget compliance**: for every envelope \(e\) and time \(t\), pointwise concurrency and integral GPU-hour bounds hold (admission checks + ledger accumulation).
- **Reservation soundness**: backfill plus activation (cuts + lottery) make the reserved slice available at or after \(\text{earliestStart}\).
- **Family-first borrowing**: Cover order prefers siblings/parent in the same location before sponsors; all borrowing obeys envelope caps \((C, B)\) and aggregate caps.
- **No priorities**: survival decisions depend only on feasibility, structural cuts, and the attested uniform lottery seed—no numeric priorities influence outcomes.
- **Auditability**: \(\Sigma\) is append-only; active state is a pure function of events with explicit reasons, enabling reproducible postmortems and proofs over traces.

## 8. Worked example (derivation)
Run: owner RAI, totalGPUs=96, groupGPUs=64, allowCrossGroupSpread=true. Supply: Domain A has 72 free GPUs, Domain B has 48 free GPUs. Budget: envelope `west-h100` for RAI with concurrency headroom ≥ 96 now.

1. **Compile**: \(J = \text{SHARD}(2, D\{k=64\})\).
2. **Cover**: \(\varphi\) pays all 96 from `west-h100` (location match; headroom passes pointwise/integral tests).
3. **Pack** attempts cohesive groups: \(\text{group}_1=64\) on A; \(\text{group}_2=64\) cannot fit on B (only 48 free) and may not split. Result: \(\text{Pack} = \text{None}\).
4. **Plan-Later**: create a reservation targeting A plus future headroom on B at \(t_0 + \Delta\).
5. **Activation**: if deficit on B is 16 at activation, structural cuts shrink only INCR components (not present here). Remaining deficit triggers lottery within scope; released leases free 16 GPUs on B; binder then places \(\text{group}_1=64\) on A and \(\text{group}_2=64\) on B. Cohesion is preserved because each group is single-domain.
6. **Growth variant**: if the run were \(\text{INCR}(\text{min}=64, \text{max}=128, \text{step}=16)\), at \(t_0\) the binder would start 64 on A, later grow to 96 across A→B as capacity appears, and potentially to 128 while honoring the same cover + pack rules.

## 9. Mapping to CRDs and controller behavior
- **Budget ↔ envelopes**: selector, concurrency window, optional maxGPUHours, lending ACLs, aggregate caps.
- **Run ↔ surface spec**: totalGPUs, groupGPUs, allowCrossGroupSpread, malleable bounds, spares, checkpoint hints.
- **Reservation ↔ CRD**: runRef, intendedSlice, payingEnvelope, earliestStart (spec immutable; status evolves from Create → Activate → Released).
- **Lease ↔ CRD/event**: runRef, nodes, role, paidByEnvelope, interval.start, reason (immutable once recorded; closed with End events).
- **Conflict resolution**: structural cuts and the published lottery seed match the controller’s deterministic preemption path; there are no numeric priorities.
- **Auditability**: dashboards and CLI derive live state from the ledger \(\Sigma\), mirroring the calculus view.

The calculus above is deliberately small: one funding function (Cover), one placement function (Pack), immutable ledger events, and a deterministic pathway for contention. It is sufficient to analyze work-conservation, borrowing fairness, and reservation soundness, while matching the implementation and CRD vocabulary.
