codex
# Recommendation

Treat P5–P8 as one contract:

1. Funding identity must come from an authenticated, versioned principal binding—not from scanning mutable Budgets.
2. Every owner, including a pool, gets one canonical namespace.
3. Binding and topology changes apply prospectively. Elapsed accounting never changes silently.
4. Loss of current funding blocks new admissions but does not immediately destroy reservations or running work.
5. Previously authorized gangs may finish assembling from durable provenance; incomplete assemblies are unwound after a finite grace period.
6. Encode all of that in concrete runtime invariants, backed by a bounded SMT two-state check.

All code citations below refer to the requested `37270af` snapshot. That code is on the parked R7 branch, not merged into the current `main` ancestry.

## P5 — when an owner binding is trustworthy

### Recommended operator rule

A namespace’s funding owner should come from one admin-only, versioned `PrincipalBinding`, not from `Budget.Spec.Owner`.

The binding must name:

- namespace UID;
- owner ID;
- kind: `principal` or `pool`;
- effective time and immutable revision.

Every owner—including an interior pool—has exactly one active canonical namespace. Budgets in that namespace may spend for that owner; mismatching Budgets are rejected or ignored and alarmed. A foreign Budget mentioning the same owner has no effect on the legitimate namespace.

This replaces the present algorithm, which collects owner strings from every Budget and treats any string appearing in `Parents` as globally interior (`pkg/funding/evaluate.go:206-221`). That single interior occurrence suppresses collision detection everywhere (`pkg/funding/evaluate.go:237-250`), producing P5’s mixed leaf/interior hole.

It also removes an impossible security decision from the evaluator. Given two namespaces whose Budgets claim the same free-form owner, the current symmetric input contains no authenticated fact from which it can determine which namespace is legitimate. Poisoning both is safe for charging but enables remote denial of service; choosing one by timestamp or lexical order would merely make the attacker race for canonical status.

### Complete enumeration

| Observed state | Binding outcome |
|---|---|
| No `PrincipalBinding` | Namespace is unbound; no new admission or mint. |
| One active principal binding | Runnable namespace; this owner pays. |
| One active pool binding | Non-runnable administrative pool namespace; Runs are rejected/alarmed. |
| Multiple bindings for one namespace | Invalid control-plane state; no new funding there, but no other namespace is affected. |
| Same owner requested for a second namespace | Reject the second binding atomically; the established binding remains valid. |
| Matching Budget | Eligible funding object. |
| Zero Budgets | Principal identity remains valid, but no envelope currently funds new work. |
| Budget with mismatching owner | Reject/ignore and alarm; it cannot alter the binding. |
| Foreign Budget naming the victim owner or parent | No transition in the victim namespace. |
| Any duration | Binding remains as above until an authorized binding revision changes it; Budget churn alone never changes identity. |

### Executable invariant

`INV-BINDING-BIJECTION`

For every active binding set \(B\):

```text
∀ namespaceUID: count(B where namespaceUID = n) ≤ 1
∀ owner:        count(B where owner = o) ≤ 1
∀ eligible Budget b:
    binding(b.namespaceUID).owner = b.spec.owner
∀ Run r:
    binding(r.namespaceUID).kind = principal
OwnerOf(namespace) depends only on its authenticated binding revision.
```

Additionally:

```text
Changing any Budget outside namespace N cannot change OwnerOf(N).
```

The current invariant projection cannot express this yet: `invariant.World` contains only Runs and Leases (`pkg/invariant/invariant.go:229-233`). P7 must extend it with binding and funding projections.

### Ratified support and amendment

Supported by:

- Isolation is already defined by authenticated namespace identity, while owner strings are the family naming axis (`docs/project/remediation/R7-tenancy-amendment.md:71-80`).
- A principal is already specified as having exactly one namespace (`docs/project/remediation/R7-tenancy-amendment.md:64-69`).
- Cross-namespace funding must follow edges asserted by someone other than the spender (`docs/project/remediation/R7-tenancy-amendment.md:270-281`).

Explicit amendments required:

- Replace Budget-derived `ownerOf(namespace)` in §4 (`docs/project/remediation/R7-tenancy-amendment.md:116-138`).
- Remove the interior-tier injectivity exemption (`docs/project/remediation/R7-tenancy-amendment.md:152-170`, `603-610`).
- Amend the earlier rejection of a separate binding registry. P5 demonstrates that Budget contents cannot simultaneously serve as quota objects, the identity registry, and a non-inducible trust boundary.

### Trade-off

The project gives up distributing one logical pool’s Budgets across several namespaces. Operators must put all Budgets for a pool in its canonical admin namespace.

In return, injectivity becomes unconditional, “leaf versus interior” cannot change merely because somebody mentions an owner in `Parents`, and a foreign Budget cannot invalidate a victim.

The operator bears the namespace-consolidation cost; the cluster gains a comprehensible security boundary.

### Owner’s residual question

Must one pool be allowed to occupy several admin namespaces?

- **No:** one canonical namespace gives simple, unconditional injectivity.
- **Yes:** preserve distributed pools, but require a separate authenticated pool registry and explicit membership; the current “named as a Parent somewhere” exemption is not sufficient.

## P6 — whether principal loss rewrites accrued history

### Recommended operator rule

Binding and family changes take effect at their recorded effective time. They never rewrite earlier GPU-hours.

For a lease open across a transition:

```text
[start, change)   → classified using the old binding/topology
[change, recovery) → Unfunded if no valid principal or relationship exists
[recovery, end)   → classified using the repaired/new binding/topology
```

Removing a conflict must not retroactively bill the conflicted interval. Adding one must not refund the earlier funded interval.

The current replay does the opposite because it derives one current owner before replay (`pkg/funding/evaluate.go:314-333`) and uses that same `OwnerOf(run.Namespace)` at every replay instant (`pkg/funding/evaluate.go:675-708`). Since `ConsumedGPUHours` is accumulated only for currently funded classifications (`pkg/funding/evaluate.go:975-1005`), changing the live mapping can erase earlier consumption and restore headroom.

Implementing the recommended rule requires immutable binding/topology epochs. Creation timestamps alone can identify some conflict onsets but cannot reconstruct a conflict that was later deleted.

### Complete enumeration

| Lease state and transition | Outcome |
|---|---|
| No lease yet | No historical charge exists. New admission follows current binding. |
| Closed lease | All attribution before its end remains unchanged. |
| Open lease, binding stays valid | Existing replay and accrual continue normally. |
| Open lease, binding becomes unavailable | Prefix remains charged as originally classified; suffix becomes Unfunded. |
| Open lease, family/lending relationship changes | Prefix retains its prior class and payer; suffix uses the new relationship. |
| Binding repairs to the same owner | Only the post-repair suffix re-funds. |
| Binding changes to another owner | Earlier attribution stays with the payer that covered it; only later time uses the new principal. |
| Clock advances without mutation | Previous prefix is unchanged; only the new suffix accrues. |
| Budget window is explicitly rotated | Existing window semantics may release out-of-window hours; this is a named exception, not a topology mutation. |
| Explicit refund/correction | Allowed only as a separate immutable adjustment event, never as an incidental replay side effect. |

### Executable invariant

`INV-ACCRUAL-PREFIX-IMMUTABLE`

For any legal transition effective at \(t\):

```text
Attribution(after, interval < t) = Attribution(before, interval < t)
```

where attribution is keyed by:

```text
lease UID × payer envelope UID × funding class
```

Also:

```text
sum(class hours for a lease interval) = elapsed lease GPU-hours
```

and, for topology-only changes within an unchanged envelope window:

```text
previously consumed funded hours cannot disappear, move payer, or change class.
```

Explicit window rotations and recorded adjustment events are excluded by type, not by ad hoc caller choice.

### Ratified support and amendment

Supported by the stronger portions of quota semantics:

- Leases are immutable consumption facts (`docs/project/quota-semantics.md:64-69`).
- Immutable facts plus deterministic evaluation should make a past instant auditable (`docs/project/quota-semantics.md:90-91`).
- Unfunded work coasts and can recover without resubmission (`docs/project/quota-semantics.md:27-44`).

Required amendments:

- Extend Decision 3’s input from `(budgets, leases, clock)` to include immutable binding/topology epochs (`docs/project/quota-semantics.md:64-69`).
- Clarify R7’s “pre-existing leases reclassify Unfunded and coast” as prospective from the binding transition, not retroactive over the whole lease (`docs/project/remediation/R7-tenancy-amendment.md:122-134`, `588-592`).
- Preserve the explicitly documented current-window behavior separately; the code currently says moving the window releases earlier hours (`pkg/funding/evaluate.go:104-108`).

### Trade-off

This gives up the simplicity of replaying all history under today’s topology. It requires durable transition history and makes compaction/settlement carry epoch attribution.

Operators and storage bear that complexity; tenants and budget owners gain a ledger whose past does not oscillate when an admin fixes configuration.

### Owner’s residual question

May an administrator correct history?

- **Never:** past attribution is final and maximally auditable.
- **Only through an explicit adjustment event:** corrections are possible, but reports must show both the original charge and the adjustment.

Silent replay-based correction should not remain an option.

## P7 — machine-checking the contract

### Recommended rule

Use both layers:

1. A concrete `pkg/invariant` oracle on every evaluation/controller transition.
2. A bounded SMT check over pairs of legal states and admissible mutations.

SMT should encode P5/P6/P8 semantics, not merely reproduce current `Evaluate`. The useful assertion is prefix immutability, not “charges never decrease”: explicit window rotation already permits decreases.

### Complete mutation coverage

The oracle must classify every mutation:

| Mutation | Required property |
|---|---|
| Add/delete/update unrelated foreign Budget | Cannot alter victim binding or attribution. |
| Add/delete eligible local Budget | May affect future funding; not identity or elapsed attribution. |
| Binding enters draining/unbound/rebound | Time-sliced prospective effect. |
| Family re-parent | Prospective class change only. |
| Lending ACL change | Prospective Borrowed eligibility only. |
| Clock advance | Old prefix stable; new suffix accrues. |
| Window rotation | Checked against the separate explicit window rule. |
| Lease spec mutation/reopen | Illegal under existing lease monotonicity. |
| Reservation/principal-loss timer advance | Must satisfy P8’s bounded lifecycle. |

Existing invariants check concrete steady and transition states (`pkg/invariant/invariant.go:235-371`), but none currently projects Budgets, bindings, reservations, or accrual (`pkg/invariant/invariant.go:163-233`). That is the required extension.

### Executable invariants

The principal P7 predicate is P6’s:

`INV-ACCRUAL-PREFIX-IMMUTABLE`

The solver should also prove:

- `INV-BINDING-BIJECTION`
- `INV-PRINCIPAL-LOSS-LIFECYCLE` from P8
- Existing conservation: every elapsed GPU-hour is in exactly one class.

For transition \(S \rightarrow S'\) effective at \(t\):

```text
legal(S) ∧ admissibleMutation(S,S',t)
⇒ prefixAttribution(S,t) = prefixAttribution(S',t)
```

For unrelated namespace mutation:

```text
affectedNamespace(m) ≠ N
⇒ OwnerOf_S(N) = OwnerOf_S'(N)
```

### Ratified support

- The repository already treats invariant violations as illegal state and panics under tests (`pkg/invariant/invariant.go:14-20`, `441-473`).
- The existing model-checking guidance says to model evaluation semantics rather than nonexistent demotion protocols (`docs/project/quota-semantics.md:124-126`).

No ratified semantic text needs amendment beyond P5/P6; P7 encodes their ruling.

### Trade-off

A bounded owner DAG and finite lease set are not a mathematical proof for arbitrary cluster size. They are, however, a strong per-PR counterexample oracle. Increasing bounds raises CI cost sharply, particularly for graph ancestry.

### Owner’s residual question

What bound must the per-PR solver cover?

- **Small bug-finding bound:** fast CI, excellent at structural counterexamples, not representative of maximum production depth.
- **Configured production maximum:** stronger assurance, potentially material CI cost.

I am uncertain what bound is affordable because the repository contains no measured solver-cost data for the proposed binding-epoch model.

## P8 — loss of current funding principal

### Recommended operator rule

Do not use a single “hold or terminate” rule. Distinguish:

- identity;
- current quota/envelope availability;
- a previously authorized commitment;
- work already running.

Let `G` be a finite, nonzero principal-loss grace period.

A durable commitment must identify Run UID, namespace UID, binding revision, payer envelope UID/revision, authorized width, and expiry. Promise/swap/top-up minting validates that commitment—not the current global Budget scan. The current Promise path instead recomputes current ownership and refuses whenever it derives empty (`cmd/scheduler/plugin/gang.go:738-758`), which is what strands a partially minted gang.

### Complete cause × held-state × duration matrix

| Cause | Holds nothing / pending reservation | Authorized pods or partial gang | Fully running gang |
|---|---|---|---|
| Foreign Budget, hostile owner string, unrelated namespace conflict | No victim transition; reject/alarm the offending object. Duration irrelevant. | Continue from existing commitment. | Unaffected. |
| Temporary zero Budgets, GitOps delete/apply, mismatching local Budget, uncertain/stale read | For `< G`, mark reservation `BlockedFunding` with `blockedSince`; no new mint and no live countdown gauge. At `≥ G`, expire/release that reservation, clear its metrics, and automatically reforecast the Run after recovery. | For `< G`, permit only completion of the already-authorized width. At `≥ G`, if still below minimum width, close all partial leases, delete its pods, release the reservation, and requeue. If it reached minimum by the deadline, use the running rule. | Continue as Unfunded from onset. No growth, but replacement within the existing width is allowed from durable provenance. Reclaim only when funded demand actually needs capacity. |
| Authorized binding enters `Draining` | Same bounded behavior, but the onset is an explicit admin event rather than inferred absence. | Same commitment completion/unwind rule. | Continue Unfunded; no fresh growth. Binding removal waits until pending/partial commitments are resolved. |
| Binding recovers before `G` | Resume the same reservation from `BlockedFunding`. | Complete using current funding if available; retain original commitment identity. | Future accrual re-funds; the outage interval remains Unfunded. |
| Binding recovers after `G` | Old reservation remains expired; Run automatically receives a fresh forecast/reservation. | Old partial assembly remains unwound; start a fresh admission. | Future accrual re-funds automatically. |
| Run or namespace is explicitly deleted | Cancel reservations and close leases immediately through normal deletion cleanup; this is deletion of the workload, not an inferred funding-loss action. | Same. | Same. |

This covers the early-return holes in the current controller. A partial gang is topped up and returned before the empty-owner guard (`controllers/run_controller.go:1122-1145`); emitted active pods also return before it (`controllers/run_controller.go:1163-1170`); only a zero-hold activation reaches the immediate terminal guard (`controllers/run_controller.go:1179-1225`). One condition therefore currently has three different outcomes depending on how far admission happened to progress.

The proposed rule makes that distinction intentional:

- no commitment: block, then expire;
- valid commitment: finish within bounds, then unwind;
- running work: coast Unfunded.

A missing payer already evaluates Unfunded rather than charging some other envelope (`pkg/funding/evaluate.go:692-698`), so honoring an authenticated pre-loss commitment need not create a cross-tenant charge.

### Executable invariant

`INV-PRINCIPAL-LOSS-LIFECYCLE`

For a namespace whose funding availability was lost at \(t_0\):

```text
1. No mint after t0 unless:
   commitment.createdAt < t0
   ∧ commitment.namespaceUID = run.namespaceUID
   ∧ mintedWidth ≤ commitment.authorizedWidth.

2. Pending reservation with no commitment:
   now - t0 < G  ⇒ state = BlockedFunding
   now - t0 ≥ G ⇒ state ∉ {Pending, BlockedFunding}
                    ∧ countdown absent ∧ backlog gauge absent.

3. Partial committed gang:
   now - t0 ≥ G ∧ runnableWidth < minimum
   ⇒ no open leases, no pods, no pending reservation.

4. Fully running gang:
   principal loss alone must not close its leases;
   its post-t0 class is Unfunded unless valid backing exists.

5. An object outside namespace N cannot start or reset N’s loss timer.

6. Recovery never reclassifies the interval [t0, recovery).
```

Existing `INV-TERMINAL-PRESENT` correctly prohibits terminal Runs retaining leases (`pkg/invariant/invariant.go:65-74`, `278-289`), but P8 needs the complementary bounded rule for nonterminal partial gangs and reservations.

### Ratified support and amendment

Supported by quota semantics:

- Exhaustion demotes rather than kills (`docs/project/quota-semantics.md:19-30`).
- Unfunded work is reclaimed only on actual demand (`docs/project/quota-semantics.md:31-39`, `108-111`).
- Recovery is automatic (`docs/project/quota-semantics.md:38-44`).

Required R7 amendment:

- Replace immediate terminal failure for every unbound fresh reservation (`docs/project/remediation/R7-tenancy-amendment.md:122-134`, `588-592`) with the bounded `BlockedFunding → ExpiredFunding → automatic reforecast` lifecycle.
- Clarify that a previously authorized gang completion or replacement is continuation of an existing commitment, not a fresh admission.
- Remove Budget churn and foreign owner strings from the set of events capable of changing the principal binding.

### Trade-off

During `G`, a blocked reservation or partial gang can retain some capacity, delaying other users. After `G`, an incomplete gang loses its partial work and must restart. Running unfunded work may continue indefinitely on idle capacity, but that is already the ratified quota principle; funded demand can reclaim it.

The partial gang bears the restart cost. Other tenants receive a firm upper bound on stranded capacity.

### Owner’s residual question

What is `G`?

- **Short:** fast capacity recovery, but normal GitOps/informer disruptions can unwind partially formed gangs.
- **Long:** safer operationally, but partial commitments can hold GPUs longer.
- **Zero:** recreates the demonstrated destructive GitOps behavior.
- **Infinite:** recreates the demonstrated immortal reservation/partial-gang behavior.

I cannot recommend a number without measured Budget replacement and informer-convergence times. The semantics require only that it be finite, nonzero, visible in status, and configured cluster-wide.

## Final uncertainty

I am confident about the semantic shape: authenticated binding, prospective accounting, durable commitments, and a finite bounded lifecycle are jointly necessary to eliminate both reproduced endpoints.

I am less certain about the storage representation. A cluster-scoped binding CRD with immutable epochs is the cleanest model I see, but a Namespace-backed binding plus an append-only history object could satisfy the same predicates. What cannot satisfy them is the current snapshot-only Budget scan: it has neither authenticated canonical ownership nor enough history to reconstruct a repaired conflict interval.
tokens used
250,313
# Recommendation

Treat P5–P8 as one contract:

1. Funding identity must come from an authenticated, versioned principal binding—not from scanning mutable Budgets.
2. Every owner, including a pool, gets one canonical namespace.
3. Binding and topology changes apply prospectively. Elapsed accounting never changes silently.
4. Loss of current funding blocks new admissions but does not immediately destroy reservations or running work.
5. Previously authorized gangs may finish assembling from durable provenance; incomplete assemblies are unwound after a finite grace period.
6. Encode all of that in concrete runtime invariants, backed by a bounded SMT two-state check.

All code citations below refer to the requested `37270af` snapshot. That code is on the parked R7 branch, not merged into the current `main` ancestry.

## P5 — when an owner binding is trustworthy

### Recommended operator rule

A namespace’s funding owner should come from one admin-only, versioned `PrincipalBinding`, not from `Budget.Spec.Owner`.

The binding must name:

- namespace UID;
- owner ID;
- kind: `principal` or `pool`;
- effective time and immutable revision.

Every owner—including an interior pool—has exactly one active canonical namespace. Budgets in that namespace may spend for that owner; mismatching Budgets are rejected or ignored and alarmed. A foreign Budget mentioning the same owner has no effect on the legitimate namespace.

This replaces the present algorithm, which collects owner strings from every Budget and treats any string appearing in `Parents` as globally interior (`pkg/funding/evaluate.go:206-221`). That single interior occurrence suppresses collision detection everywhere (`pkg/funding/evaluate.go:237-250`), producing P5’s mixed leaf/interior hole.

It also removes an impossible security decision from the evaluator. Given two namespaces whose Budgets claim the same free-form owner, the current symmetric input contains no authenticated fact from which it can determine which namespace is legitimate. Poisoning both is safe for charging but enables remote denial of service; choosing one by timestamp or lexical order would merely make the attacker race for canonical status.

### Complete enumeration

| Observed state | Binding outcome |
|---|---|
| No `PrincipalBinding` | Namespace is unbound; no new admission or mint. |
| One active principal binding | Runnable namespace; this owner pays. |
| One active pool binding | Non-runnable administrative pool namespace; Runs are rejected/alarmed. |
| Multiple bindings for one namespace | Invalid control-plane state; no new funding there, but no other namespace is affected. |
| Same owner requested for a second namespace | Reject the second binding atomically; the established binding remains valid. |
| Matching Budget | Eligible funding object. |
| Zero Budgets | Principal identity remains valid, but no envelope currently funds new work. |
| Budget with mismatching owner | Reject/ignore and alarm; it cannot alter the binding. |
| Foreign Budget naming the victim owner or parent | No transition in the victim namespace. |
| Any duration | Binding remains as above until an authorized binding revision changes it; Budget churn alone never changes identity. |

### Executable invariant

`INV-BINDING-BIJECTION`

For every active binding set \(B\):

```text
∀ namespaceUID: count(B where namespaceUID = n) ≤ 1
∀ owner:        count(B where owner = o) ≤ 1
∀ eligible Budget b:
    binding(b.namespaceUID).owner = b.spec.owner
∀ Run r:
    binding(r.namespaceUID).kind = principal
OwnerOf(namespace) depends only on its authenticated binding revision.
```

Additionally:

```text
Changing any Budget outside namespace N cannot change OwnerOf(N).
```

The current invariant projection cannot express this yet: `invariant.World` contains only Runs and Leases (`pkg/invariant/invariant.go:229-233`). P7 must extend it with binding and funding projections.

### Ratified support and amendment

Supported by:

- Isolation is already defined by authenticated namespace identity, while owner strings are the family naming axis (`docs/project/remediation/R7-tenancy-amendment.md:71-80`).
- A principal is already specified as having exactly one namespace (`docs/project/remediation/R7-tenancy-amendment.md:64-69`).
- Cross-namespace funding must follow edges asserted by someone other than the spender (`docs/project/remediation/R7-tenancy-amendment.md:270-281`).

Explicit amendments required:

- Replace Budget-derived `ownerOf(namespace)` in §4 (`docs/project/remediation/R7-tenancy-amendment.md:116-138`).
- Remove the interior-tier injectivity exemption (`docs/project/remediation/R7-tenancy-amendment.md:152-170`, `603-610`).
- Amend the earlier rejection of a separate binding registry. P5 demonstrates that Budget contents cannot simultaneously serve as quota objects, the identity registry, and a non-inducible trust boundary.

### Trade-off

The project gives up distributing one logical pool’s Budgets across several namespaces. Operators must put all Budgets for a pool in its canonical admin namespace.

In return, injectivity becomes unconditional, “leaf versus interior” cannot change merely because somebody mentions an owner in `Parents`, and a foreign Budget cannot invalidate a victim.

The operator bears the namespace-consolidation cost; the cluster gains a comprehensible security boundary.

### Owner’s residual question

Must one pool be allowed to occupy several admin namespaces?

- **No:** one canonical namespace gives simple, unconditional injectivity.
- **Yes:** preserve distributed pools, but require a separate authenticated pool registry and explicit membership; the current “named as a Parent somewhere” exemption is not sufficient.

## P6 — whether principal loss rewrites accrued history

### Recommended operator rule

Binding and family changes take effect at their recorded effective time. They never rewrite earlier GPU-hours.

For a lease open across a transition:

```text
[start, change)   → classified using the old binding/topology
[change, recovery) → Unfunded if no valid principal or relationship exists
[recovery, end)   → classified using the repaired/new binding/topology
```

Removing a conflict must not retroactively bill the conflicted interval. Adding one must not refund the earlier funded interval.

The current replay does the opposite because it derives one current owner before replay (`pkg/funding/evaluate.go:314-333`) and uses that same `OwnerOf(run.Namespace)` at every replay instant (`pkg/funding/evaluate.go:675-708`). Since `ConsumedGPUHours` is accumulated only for currently funded classifications (`pkg/funding/evaluate.go:975-1005`), changing the live mapping can erase earlier consumption and restore headroom.

Implementing the recommended rule requires immutable binding/topology epochs. Creation timestamps alone can identify some conflict onsets but cannot reconstruct a conflict that was later deleted.

### Complete enumeration

| Lease state and transition | Outcome |
|---|---|
| No lease yet | No historical charge exists. New admission follows current binding. |
| Closed lease | All attribution before its end remains unchanged. |
| Open lease, binding stays valid | Existing replay and accrual continue normally. |
| Open lease, binding becomes unavailable | Prefix remains charged as originally classified; suffix becomes Unfunded. |
| Open lease, family/lending relationship changes | Prefix retains its prior class and payer; suffix uses the new relationship. |
| Binding repairs to the same owner | Only the post-repair suffix re-funds. |
| Binding changes to another owner | Earlier attribution stays with the payer that covered it; only later time uses the new principal. |
| Clock advances without mutation | Previous prefix is unchanged; only the new suffix accrues. |
| Budget window is explicitly rotated | Existing window semantics may release out-of-window hours; this is a named exception, not a topology mutation. |
| Explicit refund/correction | Allowed only as a separate immutable adjustment event, never as an incidental replay side effect. |

### Executable invariant

`INV-ACCRUAL-PREFIX-IMMUTABLE`

For any legal transition effective at \(t\):

```text
Attribution(after, interval < t) = Attribution(before, interval < t)
```

where attribution is keyed by:

```text
lease UID × payer envelope UID × funding class
```

Also:

```text
sum(class hours for a lease interval) = elapsed lease GPU-hours
```

and, for topology-only changes within an unchanged envelope window:

```text
previously consumed funded hours cannot disappear, move payer, or change class.
```

Explicit window rotations and recorded adjustment events are excluded by type, not by ad hoc caller choice.

### Ratified support and amendment

Supported by the stronger portions of quota semantics:

- Leases are immutable consumption facts (`docs/project/quota-semantics.md:64-69`).
- Immutable facts plus deterministic evaluation should make a past instant auditable (`docs/project/quota-semantics.md:90-91`).
- Unfunded work coasts and can recover without resubmission (`docs/project/quota-semantics.md:27-44`).

Required amendments:

- Extend Decision 3’s input from `(budgets, leases, clock)` to include immutable binding/topology epochs (`docs/project/quota-semantics.md:64-69`).
- Clarify R7’s “pre-existing leases reclassify Unfunded and coast” as prospective from the binding transition, not retroactive over the whole lease (`docs/project/remediation/R7-tenancy-amendment.md:122-134`, `588-592`).
- Preserve the explicitly documented current-window behavior separately; the code currently says moving the window releases earlier hours (`pkg/funding/evaluate.go:104-108`).

### Trade-off

This gives up the simplicity of replaying all history under today’s topology. It requires durable transition history and makes compaction/settlement carry epoch attribution.

Operators and storage bear that complexity; tenants and budget owners gain a ledger whose past does not oscillate when an admin fixes configuration.

### Owner’s residual question

May an administrator correct history?

- **Never:** past attribution is final and maximally auditable.
- **Only through an explicit adjustment event:** corrections are possible, but reports must show both the original charge and the adjustment.

Silent replay-based correction should not remain an option.

## P7 — machine-checking the contract

### Recommended rule

Use both layers:

1. A concrete `pkg/invariant` oracle on every evaluation/controller transition.
2. A bounded SMT check over pairs of legal states and admissible mutations.

SMT should encode P5/P6/P8 semantics, not merely reproduce current `Evaluate`. The useful assertion is prefix immutability, not “charges never decrease”: explicit window rotation already permits decreases.

### Complete mutation coverage

The oracle must classify every mutation:

| Mutation | Required property |
|---|---|
| Add/delete/update unrelated foreign Budget | Cannot alter victim binding or attribution. |
| Add/delete eligible local Budget | May affect future funding; not identity or elapsed attribution. |
| Binding enters draining/unbound/rebound | Time-sliced prospective effect. |
| Family re-parent | Prospective class change only. |
| Lending ACL change | Prospective Borrowed eligibility only. |
| Clock advance | Old prefix stable; new suffix accrues. |
| Window rotation | Checked against the separate explicit window rule. |
| Lease spec mutation/reopen | Illegal under existing lease monotonicity. |
| Reservation/principal-loss timer advance | Must satisfy P8’s bounded lifecycle. |

Existing invariants check concrete steady and transition states (`pkg/invariant/invariant.go:235-371`), but none currently projects Budgets, bindings, reservations, or accrual (`pkg/invariant/invariant.go:163-233`). That is the required extension.

### Executable invariants

The principal P7 predicate is P6’s:

`INV-ACCRUAL-PREFIX-IMMUTABLE`

The solver should also prove:

- `INV-BINDING-BIJECTION`
- `INV-PRINCIPAL-LOSS-LIFECYCLE` from P8
- Existing conservation: every elapsed GPU-hour is in exactly one class.

For transition \(S \rightarrow S'\) effective at \(t\):

```text
legal(S) ∧ admissibleMutation(S,S',t)
⇒ prefixAttribution(S,t) = prefixAttribution(S',t)
```

For unrelated namespace mutation:

```text
affectedNamespace(m) ≠ N
⇒ OwnerOf_S(N) = OwnerOf_S'(N)
```

### Ratified support

- The repository already treats invariant violations as illegal state and panics under tests (`pkg/invariant/invariant.go:14-20`, `441-473`).
- The existing model-checking guidance says to model evaluation semantics rather than nonexistent demotion protocols (`docs/project/quota-semantics.md:124-126`).

No ratified semantic text needs amendment beyond P5/P6; P7 encodes their ruling.

### Trade-off

A bounded owner DAG and finite lease set are not a mathematical proof for arbitrary cluster size. They are, however, a strong per-PR counterexample oracle. Increasing bounds raises CI cost sharply, particularly for graph ancestry.

### Owner’s residual question

What bound must the per-PR solver cover?

- **Small bug-finding bound:** fast CI, excellent at structural counterexamples, not representative of maximum production depth.
- **Configured production maximum:** stronger assurance, potentially material CI cost.

I am uncertain what bound is affordable because the repository contains no measured solver-cost data for the proposed binding-epoch model.

## P8 — loss of current funding principal

### Recommended operator rule

Do not use a single “hold or terminate” rule. Distinguish:

- identity;
- current quota/envelope availability;
- a previously authorized commitment;
- work already running.

Let `G` be a finite, nonzero principal-loss grace period.

A durable commitment must identify Run UID, namespace UID, binding revision, payer envelope UID/revision, authorized width, and expiry. Promise/swap/top-up minting validates that commitment—not the current global Budget scan. The current Promise path instead recomputes current ownership and refuses whenever it derives empty (`cmd/scheduler/plugin/gang.go:738-758`), which is what strands a partially minted gang.

### Complete cause × held-state × duration matrix

| Cause | Holds nothing / pending reservation | Authorized pods or partial gang | Fully running gang |
|---|---|---|---|
| Foreign Budget, hostile owner string, unrelated namespace conflict | No victim transition; reject/alarm the offending object. Duration irrelevant. | Continue from existing commitment. | Unaffected. |
| Temporary zero Budgets, GitOps delete/apply, mismatching local Budget, uncertain/stale read | For `< G`, mark reservation `BlockedFunding` with `blockedSince`; no new mint and no live countdown gauge. At `≥ G`, expire/release that reservation, clear its metrics, and automatically reforecast the Run after recovery. | For `< G`, permit only completion of the already-authorized width. At `≥ G`, if still below minimum width, close all partial leases, delete its pods, release the reservation, and requeue. If it reached minimum by the deadline, use the running rule. | Continue as Unfunded from onset. No growth, but replacement within the existing width is allowed from durable provenance. Reclaim only when funded demand actually needs capacity. |
| Authorized binding enters `Draining` | Same bounded behavior, but the onset is an explicit admin event rather than inferred absence. | Same commitment completion/unwind rule. | Continue Unfunded; no fresh growth. Binding removal waits until pending/partial commitments are resolved. |
| Binding recovers before `G` | Resume the same reservation from `BlockedFunding`. | Complete using current funding if available; retain original commitment identity. | Future accrual re-funds; the outage interval remains Unfunded. |
| Binding recovers after `G` | Old reservation remains expired; Run automatically receives a fresh forecast/reservation. | Old partial assembly remains unwound; start a fresh admission. | Future accrual re-funds automatically. |
| Run or namespace is explicitly deleted | Cancel reservations and close leases immediately through normal deletion cleanup; this is deletion of the workload, not an inferred funding-loss action. | Same. | Same. |

This covers the early-return holes in the current controller. A partial gang is topped up and returned before the empty-owner guard (`controllers/run_controller.go:1122-1145`); emitted active pods also return before it (`controllers/run_controller.go:1163-1170`); only a zero-hold activation reaches the immediate terminal guard (`controllers/run_controller.go:1179-1225`). One condition therefore currently has three different outcomes depending on how far admission happened to progress.

The proposed rule makes that distinction intentional:

- no commitment: block, then expire;
- valid commitment: finish within bounds, then unwind;
- running work: coast Unfunded.

A missing payer already evaluates Unfunded rather than charging some other envelope (`pkg/funding/evaluate.go:692-698`), so honoring an authenticated pre-loss commitment need not create a cross-tenant charge.

### Executable invariant

`INV-PRINCIPAL-LOSS-LIFECYCLE`

For a namespace whose funding availability was lost at \(t_0\):

```text
1. No mint after t0 unless:
   commitment.createdAt < t0
   ∧ commitment.namespaceUID = run.namespaceUID
   ∧ mintedWidth ≤ commitment.authorizedWidth.

2. Pending reservation with no commitment:
   now - t0 < G  ⇒ state = BlockedFunding
   now - t0 ≥ G ⇒ state ∉ {Pending, BlockedFunding}
                    ∧ countdown absent ∧ backlog gauge absent.

3. Partial committed gang:
   now - t0 ≥ G ∧ runnableWidth < minimum
   ⇒ no open leases, no pods, no pending reservation.

4. Fully running gang:
   principal loss alone must not close its leases;
   its post-t0 class is Unfunded unless valid backing exists.

5. An object outside namespace N cannot start or reset N’s loss timer.

6. Recovery never reclassifies the interval [t0, recovery).
```

Existing `INV-TERMINAL-PRESENT` correctly prohibits terminal Runs retaining leases (`pkg/invariant/invariant.go:65-74`, `278-289`), but P8 needs the complementary bounded rule for nonterminal partial gangs and reservations.

### Ratified support and amendment

Supported by quota semantics:

- Exhaustion demotes rather than kills (`docs/project/quota-semantics.md:19-30`).
- Unfunded work is reclaimed only on actual demand (`docs/project/quota-semantics.md:31-39`, `108-111`).
- Recovery is automatic (`docs/project/quota-semantics.md:38-44`).

Required R7 amendment:

- Replace immediate terminal failure for every unbound fresh reservation (`docs/project/remediation/R7-tenancy-amendment.md:122-134`, `588-592`) with the bounded `BlockedFunding → ExpiredFunding → automatic reforecast` lifecycle.
- Clarify that a previously authorized gang completion or replacement is continuation of an existing commitment, not a fresh admission.
- Remove Budget churn and foreign owner strings from the set of events capable of changing the principal binding.

### Trade-off

During `G`, a blocked reservation or partial gang can retain some capacity, delaying other users. After `G`, an incomplete gang loses its partial work and must restart. Running unfunded work may continue indefinitely on idle capacity, but that is already the ratified quota principle; funded demand can reclaim it.

The partial gang bears the restart cost. Other tenants receive a firm upper bound on stranded capacity.

### Owner’s residual question

What is `G`?

- **Short:** fast capacity recovery, but normal GitOps/informer disruptions can unwind partially formed gangs.
- **Long:** safer operationally, but partial commitments can hold GPUs longer.
- **Zero:** recreates the demonstrated destructive GitOps behavior.
- **Infinite:** recreates the demonstrated immortal reservation/partial-gang behavior.

I cannot recommend a number without measured Budget replacement and informer-convergence times. The semantics require only that it be finite, nonzero, visible in status, and configured cluster-wide.

## Final uncertainty

I am confident about the semantic shape: authenticated binding, prospective accounting, durable commitments, and a finite bounded lifecycle are jointly necessary to eliminate both reproduced endpoints.

I am less certain about the storage representation. A cluster-scoped binding CRD with immutable epochs is the cleanest model I see, but a Namespace-backed binding plus an append-only history object could satisfy the same predicates. What cannot satisfy them is the current snapshot-only Budget scan: it has neither authenticated canonical ownership nor enough history to reconstruct a repaired conflict interval.
