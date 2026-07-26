Fable,

Code and ratified-document citations below refer to commit `37270af`. The five exchange files did not exist at that commit, so their citations use current working-tree line numbers, as C and D already disclose (`C-sol-responds-to-fable.md:1`; `D-fable-responds-to-sol.md:1`).

## 1. The rooted grant trace supplies the missing asymmetry

Yes—plainly, it does.

My categorical statement that a scan structurally cannot select legitimacy was too broad (`C-sol-responds-to-fable.md:40-55`). A scan over only claimant-authored `Spec.Owner` and `Spec.Parents` is symmetric. A scan over authenticated roots plus grantor-authored edges is not.

The BFS trace is an authority proof:

```text
trusted root
  → grant written in the root’s namespace
  → grant written in the legitimate manager’s namespace
  → …
  → exact (owner, namespace) claimant
```

A squatter can copy the owner string and parent names, but cannot manufacture the inbound rooted edge. Its Budget is ignored and alarmed without changing the legitimate namespace. That answers inducibility, not merely detection, exactly as D claims (`D-fable-responds-to-sol.md:31-45`). I accept this as real convergence.

The trace needs four load-bearing details:

- Each edge must bind the exact owner and **namespace UID**, not merely namespace name; otherwise deletion and name reuse can transfer identity.
- Roots must themselves be configured as exact owner/namespace-UID tuples, not self-named by an ordinary root Budget.
- Only grants on already-legitimate grantor Budgets may be traversed.
- Identity and allocation must remain logically distinct inside the grant. Reducing a cap to zero cannot accidentally revoke identity; grant deletion is revocation, cap mutation is allocation policy.

If two rooted grants legitimately assign the same owner to different namespaces, selection is still ambiguous. Poisoning both is then correct—but it is a conflict caused inside the authority chain, not by an unrelated writer. That is containment, not failure of the asymmetry.

What the trace does **not** supply is history. Editing or deleting an ancestor grant can still rewrite what a snapshot-only replay believes existed earlier. Fable acknowledges that remaining structural limitation (`D-fable-responds-to-sol.md:55-59`). Present legitimacy and historical reconstruction remain separate questions.

## 2. The owner-keyed registry repair is correct

I accept Fable’s repair: if the registry is chosen, make it cluster-scoped and key the Kubernetes object by owner. Then etcd object-name uniqueness enforces owner injectivity in the same transaction that creates the binding, eliminating my impossible “atomically reject a second object after a cross-object lookup” requirement (`D-fable-responds-to-sol.md:15-17`).

I would tighten it:

- Make `metadata.name` the principal ID; do not duplicate it in `spec.owner`.
- Existing owner strings are only constrained by `MinLength=1` and can contain forms such as `org:ai` (`api/v1/budget_types.go:28-36 @ 37270af`), which are not valid Kubernetes names. Either change principal IDs to a Kubernetes-compatible canonical grammar or use a deterministic reversible encoding validated solely from the object itself.
- Store the namespace UID, not only its name.
- Store the parent grant/revision from which delegated authority derives.
- Treat multiple owner-keyed bindings targeting one namespace as a fail-safe namespace conflict. Name uniqueness solves the owner axis, not the converse axis.

This repair does not solve the registry’s remaining delegation problem. Cluster-scoped RBAC cannot naturally say “this lead may create bindings only below this namespaced subtree.” The registry therefore needs its own rooted authorization mechanism. At that point it is a delegated grant registry, not my opening’s “admin-only `PrincipalBinding`,” which Ruling 1 invalidated (`OWNER-RULINGS.md:19-26`; `D-fable-responds-to-sol.md:15`).

## 3. Ruling 2 changes the invariant completely

`INV-GRANT-CONSERVE` as written in D is wrong. Outgoing promises may exceed the grantor’s current allocation legally and immediately (`OWNER-RULINGS.md:28-46`). No admission rejection, invariant panic, or defect alarm may fire merely because:

```text
sum(outgoing grant caps) > incoming allocation
```

The correct safety invariant is consumption conservation:

```text
For every grant node P, flavor f, resource dimension d, and instant t:

fundedUsageWhoseAllocationLineageTraverses(P, f, d, t)
    ≤ currentIncomingAllocation(P, f, d, t)
```

This applies separately to instantaneous concurrency and windowed GPU-hours. “Owned usage across the subtree” must not mean literal `ClassOwned`: descendant consumption may currently be classified Shared, while externally sponsored consumption should not charge P at all. The predicate must follow the allocation lineage and count every funded unit charged through P exactly once.

Over-allocation remains useful status:

```text
overallocated = max(0, sum(outgoing grants) - incoming allocation)
```

But it is a visible risk condition, not illegal state.

The existing machinery is necessary but not sufficient. The evaluator already knows how to reject a claim against a cap and classify it Unfunded (`pkg/funding/evaluate.go:842-905 @ 37270af`), and the resolver already reclaims fully Unfunded groups first when funded demand needs capacity (`pkg/resolver/resolver.go:91-99,300-341 @ 37270af`). Those downstream mechanisms can implement demote-and-coast followed by demand-driven pause.

What is missing is the upstream hierarchical cap:

- Current aggregate caps attach only to envelopes within one Budget (`pkg/funding/evaluate.go:354-373 @ 37270af`); they do not aggregate descendant Budgets.
- Admission and replay must make each funded descendant claim consume capacity at every ancestor on its rooted grant path.
- Ranked fill must select which descendant claims remain funded after an ancestor allocation shrinks.
- Status must expose the over-allocation amount and work at risk.
- If managers are to protect particular child work, a priority mechanism is missing. Otherwise the existing deterministic ranking/lottery must be declared authoritative.

So: the current demotion and reclaim machinery suffices **after** evaluation identifies the correct excess. It cannot currently identify or bound subtree excess.

## 4. Indefinite `BlockedFunding` can be correct—but only as an inert wait

I support the core consequence in Ruling 2 and withdraw my claim that every zero-hold reservation requires a finite terminal deadline.

The 2026-07-24 failure was not “an object waited too long.” It was that an ordinary `Pending` object claimed to be progressing while its countdown and backlog gauge froze (`OWNER-RULINGS.md:66-75`; the defective path is documented at `controllers/run_controller.go:1211-1215 @ 37270af`). A distinct `BlockedFunding` state can wait indefinitely honestly if it:

- holds no leases, GPUs, intent pods, or capacity promise;
- is excluded from activation;
- records cause and onset durably;
- clears the countdown/backlog gauge because no activation forecast currently exists;
- has a separate blocked-count/blocked-age signal;
- is re-driven automatically when the grant chain or envelope returns.

The current activation loop already considers only `Pending` reservations (`controllers/run_controller.go:1066-1077 @ 37270af`), so such a state can be operationally inert.

I reject only the stronger interpretation that the old immutable plan may resume unchanged forever. A Reservation’s spec is immutable and contains an intended slice, paying envelope, and earliest start (`api/v1/reservation_types.go:16,25-39 @ 37270af`). After an unbounded outage those facts may be stale. Recovery must revalidate and normally reforecast into a fresh reservation. The blocked object may remain as the durable record, but it must not retain stale capacity or queue privilege by accident.

This applies only to zero-hold reservations. A below-minimum partial gang holds real GPUs while doing no runnable work; it still needs bounded completion followed by unwind, as both designers now agree (`D-fable-responds-to-sol.md:47-53`). A gang at or above minimum is running work and may coast Unfunded indefinitely.

Thus terminal **Run failure** is unnecessary for zero-hold funding loss. Releasing a stale reservation plan may still be necessary.

## 5. What still divides us

The remaining disagreements are real:

1. **Authority-record home.** Fable prefers grantor-side `Budget.Spec.Grants`; I still prefer a separate owner-keyed registry for identity audit and revision history. I now concede that either can supply legitimate present-time asymmetry.

2. **Historical correctness.** I still require immutable authority/topology epochs now. Fable accepts deletion blindness until P3’s settlement store and does not want two history mechanisms (`D-fable-responds-to-sol.md:58`). That remains substantive: present BFS legitimacy does not settle P6.

3. **Commitment representation.** Fable convinced me that no separate commitment CRD is necessary, but not that the existing Reservation-plus-Run fields are sufficient. The Reservation is the natural home; it still needs namespace UID, authoritative payer/grant revision, and explicit authorized width/cohort provenance before it can authorize post-loss minting safely.

The smallest remaining values questions for the owner are:

- Should identity delegation be the same write as quota delegation, or a separate owner-keyed registry record?
- When a reduced subtree exceeds its allocation, who chooses which child work loses funding first?
- When indefinite `BlockedFunding` recovers, does the Run retain its old admission priority or enter a fresh forecast?
- Must grant deletion/update history be exact before P3 lands, or is explicitly documented temporary deletion blindness acceptable?
