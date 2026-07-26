All sources read: the brief; `git show 37270af:docs/project/DECISIONS-NEEDED.md`; `quota-semantics.md`; `R7-tenancy-amendment.md`; `AGENTS.md`; both review archives; and the code at the frozen branch tip `37270af` (`evaluate.go`, `run_controller.go`, `gang.go`, `pkg/invariant`), plus the pinning tests. Here is the recommendation.

---

# Recommendation — P5, P6, P7, P8 (consequence seat)

All file:line citations are against commit `37270af` (tip of the frozen fix branch, where P8's cited code actually lives) unless marked otherwise. The working tree on `main` predates R7 pt2 and does not contain `OwnerOf`.

## The one principle, stated first

These four parks are one question asked four ways: **what is a namespace's funding principal — a live opinion or a time-anchored fact?** Today it is a live opinion: `deriveOwners` (evaluate.go:206-272) computes it from whatever Budgets exist at evaluation time, and that opinion is then applied to all of history (P6), to reservations in one tick (P8), to partial gangs at PreBind (P8-critical), and it can be flipped or laundered by any Budget object anywhere carrying the right free string (P5, P8's DoS). Every reproduction in the record is a consequence of the opinion being live.

The recommendation, in one sentence an operator can hold: **losing your funding principal stops new acquisition immediately and visibly, rewrites nothing that already happened, destroys only un-started promises, and only after the loss has persisted for a grace window W; and no owner string is ever bound in two namespaces.**

Each decision below is that sentence projected onto one surface.

---

## P5 — the interior-tier injectivity exemption

### The rule

**Delete the exemption. Owner injectivity is universal: every owner string is bound in at most one namespace. Any owner — leaf or interior — whose Budgets span two or more namespaces conflicts every namespace it touches (fail-safe to unbound + alarm). A pool lives in exactly one admin namespace.**

The reproduction (`TestInteriorExemptionAdmitsALeafOwnerInTwoNamespaces`, pkg/funding/tenancy_r7_conflicts_test.go:130) shows the exemption is not a property of the owner's *role* but of a cluster-wide OR over every Budget's `Parents` list: one child Budget in an unrelated namespace naming `org:ai` as a Parent makes evaluate.go:239's `isInterior` skip fire, and a leaf owner bound in two tenant namespaces sails through with zero conflicts. That has two consequences, and they are duals:

- **The theft leg (P5's confirmed repro):** a dual-role owner mints non-recallable Owned across a namespace boundary — the exact S-1 hazard the injectivity invariant exists to close.
- **The laundering leg (this couples P5 to P8):** the same `Parents` entry that enables the theft *suppresses* the `ConflictLeafOwnerSpansNamespaces` detection that P8's attack depends on. Whoever can write one Budget anywhere chooses, per owner string, between "silent cross-tenant charge" and "victim namespace conflicted." The exemption is not a narrow allowance; it is a switch the victim does not hold.

The amendment's stated premise for the exemption — *"nothing ever classes Owned against a pool, and pools are admin-written at both ends"* (R7-tenancy-amendment.md:165-166) — is proven false by execution for the dual-role case, and the engine **cannot** repair it with a narrower predicate, because the actual hazard condition is "owner derivable in namespace A with an envelope in namespace B," and *every* spanning owner is derivable in each of its namespaces (`OwnerOf(pool-ns)` returns the pool tier; the derivation cannot see RBAC runnability). The only spanning configuration the exemption protects is one the design itself says not to build (§4: "R18 recommends one namespace per tier as posture"; §5 describes THE pool namespace, singular, at 199-201). Deleting the exemption costs the model nothing it advertises: single-namespace pools, `Parents` naming higher tiers directly, cross-team sponsor edges all survive untouched.

### Completeness

For every owner string o, let N(o) = namespaces holding a Budget with `Spec.Owner: o`, and I(o) = whether any Budget names o in `Parents`:

| |N(o)| | I(o) | Outcome |
|---|---|---|
| 0 | true | No binding; R26 alarm 1 (parent tier with no Budget — a typo). Unchanged. |
| 0 | false | String unused. No cell. |
| 1 | any | Bound normally. Unchanged. |
| ≥2 | false | Conflict, all namespaces of N(o) unbound + alarmed. Unchanged (today's leaf rule). |
| ≥2 | true | **Today: exempt. Proposed: conflict, identical to the row above.** |

No cell is undefined; the last row is the entire change.

### INV

- **INV-OWNED-IS-LOCAL** (steady-tier, the safety property itself): for every open lease l with derived class Owned, `l.Spec.PaidByBudgetNamespace == l.Spec.RunRef.Namespace`. This is the wallet ruling made executable, it is checkable at every engine entry point today (the World projection gains the two namespaces and the derived class), and it catches *any* future road to cross-namespace Owned, not just this one.
- **INV-OWNER-INJECTIVE** (Evaluate postcondition): ∀ o: |N(o)| ≥ 2 ⟹ (∀ ns ∈ N(o): `OwnerOf(ns) == ""` ∧ a `BindingConflict` naming ns is recorded). No interior escape hatch in the predicate.

### Ratified text

Supports: §3's namespace↔principal bijection claim, the wallet ruling, the accountability principle, C-4's own ruling that two same-owner leaf Budgets are "a detected, alarmed admin error, not a supported merge." **Amends, explicitly:** §4's "Interior tiers are exempt" sentence and §5/C-4's "interior tiers may span admin namespaces" — I am proposing to strike the allowance because its stated premise was falsified by an executed reproduction, and the strike implements the amendment's intent (one home per tier) rather than overriding it. `TestInteriorExemptionAdmitsALeafOwnerInTwoNamespaces` flips from pin to regression test.

### Trade-away

A genuinely-intended multi-namespace pool stops funding its members' Shared claims (they demote to Unfunded and coast — nothing dies) until the admin consolidates it into one namespace. That cost lands on a configuration no production install has (amendment §9: there is none), and the clean-break rule schedules it. In exchange, the worst *inducible* outcome under a violated RBAC precondition downgrades from silent cross-tenant theft to loud denial — fail toward denial, away from theft.

### Residue for David

**Must a pool's Budgets ever legitimately span namespaces?** (a) No → everything above; the exemption is deleted. (b) Yes → the exemption survives only for *pure*-interior owners and the dual-role case still conflicts; the theft hole stays closed for the reproduced shape but a runnable namespace deriving a spanning pool owner remains RBAC-guarded territory, permanently. If you cannot name the cluster that needs (b), take (a).

---

## P6 — does the fail-safe reach backwards through the replay?

### The rule

**Forward only. The namespace→owner binding used to class a replay segment is the binding in effect during that segment, derived from the Budgets whose `CreationTimestamp` precedes it. Hours already accrued keep the class they earned; a binding change affects the next hour, never the last one.** Envelope *spec* (windows, caps) remains current-spec — Decision 1's "a reopened budget window re-funds" arithmetic is untouched.

Mechanically: `deriveOwners` becomes time-indexed (`OwnerOf(ns, t)`, derived from Budgets with `CreationTimestamp ≤ t`; zero timestamps count as "always existed" so hand-built fixtures and the golden stay bit-identical); Budget creation instants join `eventTimes`; the two live-derivation sites — evaluate.go:707 (`cl.tier = ev.Graph.Tier(acct.Owner, ev.OwnerOf(run.Namespace))`) and :798 (the lending borrower) — take the segment's t. Callers at Now are unchanged.

Why this is the right line and not vagueness about "fairness": the two axes are different kinds of statement. *Who paid for hour h* is a fact of hour h — the isolation axis, the thing the API server authenticates. *What the payer's budget affords* is admin policy, deliberately current (Decision 1 ratifies release-on-renewal). The pinned behaviour (`TestConflictRetroactivelyErasesAccruedHours`, tenancy_r7_conflicts_test.go:170) mixes them: an attacker-placeable object (P8's squatter Budget) erases 32 already-burned GPU-hours and hands the envelope 32 hours of already-spent headroom — an inducible ledger rewrite, in both directions (removing the Budget restores it). Anything consulting `RemainingGPUHours` — sponsors, aggregate caps — plans against capacity that was spent. And quota-semantics.md:90-91 promises "the classification at any past instant is recomputable"; under backward reach it is not — the past instant's answer changes when a Budget appears. Forward-only *implements* Decision 3's audit-by-replay sentence.

### Completeness

Cause of binding change × hours relative to it:

| | hours before the change | hours during | hours after resolution |
|---|---|---|---|
| Budget **added** creating multi-owner or leaf-span conflict | **keep earned class (changed — today they erase)** | Unfunded (fail-safe, unchanged) | bound again, forward (unchanged) |
| All Budgets **deleted** (GitOps window) | account object gone → hours invisible (unchanged — see honesty note) | Unfunded via `acct == nil` (evaluate.go:694-698) | restored on re-apply (unchanged) |
| Conflicting Budget **deleted** (resolution) | n/a | **retroactively re-billed as bound — unchanged and imperfect**: the replay cannot see an object that no longer exists (the second half of the pinned test, 4/0/12) | forward normal |
| Legitimate **rebind** A→B | A keeps hours earned under A; B pays only from B's creation (**changed** — today B is back-billed for history it never covered) | — | — |
| In-place **edit** of `Budget.Spec.Owner` | still current-spec (no history exists for spec fields) — documented limitation; R18 should say "rebind by delete+create, never edit owner in place" | | |

Every cell is defined. Two cells are honestly imperfect (deletion blindness): the ledger has no memory of deleted objects, and the *complete* fix is the R4 pt2b settlement store (parked P3), which persists settled accrual and makes history durable against deletion. Forward-only anchoring closes every *inducible* rewrite — an attacker adds Budgets, only an admin deletes the victim's — and that is the safety-relevant half.

### INV

**INV-ACCRUAL-ANCHORED**: ∀ Input S, ∀ Budget b with `CreationTimestamp = T ≤ Now`: `Evaluate(S, Now=T)` and `Evaluate(S ∪ {b}, Now=T)` agree on every `EnvelopeAccount.{ConsumedGPUHours, HoursByClass}`, every `RunAccount.GPUHours`, and every `LenderHours`. In words: **adding a Budget never changes any hour already on the books at the instant it was added.** Today this predicate is false (the pinned test's 32→0 at identical Now falsifies it); under the rule it holds by construction. It is directly executable as a differential check against the real `Evaluate` — see P7.

### Ratified text

Supports: Decision 3 (pure deterministic function of facts — `CreationTimestamp` is a fact; audit by replay), C-2's "pre-existing leases reclassify Unfunded and coast" (which carries no time qualifier — this adds one, forward), Decision 1's no-overdraft (backward erasure hands out spent headroom, the moral converse of overdraft). **Amends:** the `ConsumedGPUHours` doc-comment doctrine ("History is evaluated under the current spec", evaluate.go:95-98 region) is *scoped*, in writing: current-spec for envelope spec fields, anchored for the namespace→owner binding. Decision 1's renewal semantics are explicitly preserved.

### Trade-away

The replay gains event times (one per Budget creation) — bounded, cheap. The engine's one-sentence simplicity ("everything under current spec") becomes two sentences. Both pinned tests flip to assertions of the new semantics. The people who bear the residual cost are admins who *delete* Budgets expecting history to survive — until P3 lands, it does not, and R18 must say so.

### Residue for David

**When a namespace is legitimately rebound from team A to team B, A's books keep the hours already burned and B pays only from the switch. Accept?** (a) History stays with the payer-at-the-time — recommended; ledgers never rewrite; the oracle in P7 is sound. (b) History follows the run — today's behaviour generalized; every envelope's ledger is revisable by admin action forever, and no anchoring invariant is statable. This is the values choice; everything in P6/P7 downstream of it.

---

## P7 — which monotonicity property does the oracle enforce?

### The rule

**Candidate (2), scoped exactly to what P6 rules: hours already elapsed never change classification or attribution under a change to the Budget SET.** Not candidate (1) — refunds and window renewal legitimately decrease charges, so "charges never decrease" is false on purpose. Not candidate (3) alone — conservation is implied by the scoped (2) on pre-change intervals, and (3) as a standalone would bless a rewrite that "moves" a charge, which P6 just forbade. The re-parenting sub-question is answered by P6(a): history stays with the envelope that paid at the time.

The invariant IS **INV-ACCRUAL-ANCHORED** as stated under P6. P6's ruling and P7's oracle are the same sentence — which is why P6 had to be answered first, and why this recommendation refuses to give them different answers.

### Gate form — where I differ with the SMT framing

Build the per-PR gate as a **differential property test against the real `Evaluate`** first, not an SMT encoding: generate random (budgets, leases, graph) states plus a random Budget addition at a random T, and assert the INV-ACCRUAL-ANCHORED equality. Reasons of consequence: (i) the P7 entry's own warning is that an oracle built on the wrong invariant is *confidently* wrong — a hand-encoded SMT model of `Evaluate` adds a second way to be confidently wrong (model-vs-code divergence) that a differential test structurally cannot have, because it executes the code under judgment; (ii) this repo's own epistemic rule is "prefer a compiled, running reproduction over an argument" (AGENTS.md); (iii) the P7 entry already identifies where the SMT cost hides (`Graph.Tier` ancestry; unbounded reachability drags toward fixpoints). The SMT encoding is worth adding later only if the bounded-owner-depth encoding is cheap and the property test has ever demonstrably missed a state class. The property test lands with P6's implementation; nothing waits.

### Completeness / ratified text / trade-away

Inherited from P6 (the oracle is its encoding). Supports Decision 3's determinism. Trade-away: a property test samples rather than proves; the sampled space must include the shapes the reproductions used (conflict onset mid-lease, leaf-span, interior-exempt-turned-conflict) as fixed seeds, or it is decorative — and per AGENTS.md it must be mutation-verified against the anchoring line itself.

### Residue for David

**Is per-PR solver-grade proof on this path worth the `Graph.Tier` encoding now, or does the executed differential gate suffice until the settlement store (P3) changes the ledger's shape anyway?** (a) Property gate now, SMT never/later — recommended. (b) SMT now — buys ∀-quantification, costs the encoding and a second model to keep honest, and P3 landing would force a re-encode.

---

## P8 — may losing the principal DESTROY, and may a foreign party induce it?

### The rule

Three clauses, each closing one executed reproduction:

1. **Destruction requires durability.** A reservation whose namespace derives no owner is HELD, visibly — `State: Pending`, a reason naming the binding, `Status.UnboundSince` stamped (persisted, restart-safe), backlog gauge refreshed every tick — and transitions to Failed **only after the condition has persisted continuously for W** (an owner-set number, precedent R4 pt1b's staleness bound / P2). A single bound observation resets the clock. At ≥ W: `failReservationTerminally` (run_controller.go:1521-1526), gauge cleared, cause + onset recorded on the object, and any emitted-but-unminted intent pods withdrawn (or they re-emit forever — the P8 CRITICAL's tail). Recovery after repair stays autonomous, as the judge's probe demonstrated.
2. **Only promises are destructible, never work and never history.** Binding loss closes no lease, kills no pod, and (per P6) reclassifies no elapsed hour. Running work coasts Unfunded and is exposed to demand-driven reclaim exactly as quota-semantics.md Decision 1 already prescribes — with the honesty note evaluate.go:280-286 already states: coasting means "not billed and not closed by the engine," not "left alone."
3. **The unit of "fresh" at the mint is the gang, not the pod.** PreBind's refusal for an unbound namespace (gang.go:755-758, `derived == ""`) applies **only when the gang holds zero minted leases**. A gang with ≥1 open minted lease may complete its remaining ranks — including a replacement rank after node failure — and the resulting leases class Unfunded and coast. This is C-2's own dichotomy (fresh = refused; pre-existing = coasts) applied at the granularity where work actually exists: a half-minted gang is pre-existing work, and stranding it produces the panel's executed worst case — leases open 20 hours, `Unfunded: 40.03 GPU-hours` climbing, pods re-emitted forever, capacity held by ranks that can never run. Refusing completion protects nothing (the completed leases bill nobody — gang.go:752-754 says so itself) and wedges everything.

Why W and not the cause-split (candidate 2, "terminal only for zero Budgets"): the zero-Budget cell is the *routine* one — `kubectl delete && apply`, helm recreate, GitOps prune-before-apply — so exempting only the inducible reasons still destroys on ordinary operations, one tick into a one-minute resync window, against a scheduler that runs for months. Why not authenticate `Budget.Spec.Owner` (candidate 3): it is the client-backed webhook + registry machinery the ratified design rejected three separate times, and it does nothing for the zero-Budget cell either. Why not keep bare terminal (candidate 4, live by accident): its RBAC gate is real for the hostile Budget but irrelevant to GitOps, and P5's universal injectivity makes conflicts *easier* to induce (that is the point — loud denial instead of silent theft), so a one-tick trigger becomes strictly more reachable. W is the only candidate that answers both executed reproductions at once: the 2026-07-24 panel proved holding-forever defective (immortal, gauge frozen at 1020); the fix-diff panel proved terminal-at-one-tick defective (inducible + routine destruction). Terminal-after-W is not a compromise between them — it is the statement that both panels were measuring *durability*, and neither endpoint has any.

The 3am sentence: *"Losing your funding principal never kills running work and never rewrites your bill. New funding stops immediately and the reservation says why, with a timestamp. If nobody fixes the binding within W, the queued reservation fails — running work still doesn't die — and it recovers by itself once the binding is repaired."*

And the structural half of the P8 HIGH ("the guard sits behind two earlier returns"): the rule is stated on the reservation *lifecycle*, not on one code path. Every due reservation, every tick, ends in exactly one of {activated, released, held-with-refreshed-reason-and-gauge, failed-terminally-with-cause}. The current second door — run_controller.go:1168-1171, which clears the gauge and returns nil, leaving a Pending reservation invisible — and the first door at :1122 are both cells in that enumeration, not exemptions from it. The unbound check runs before any branch that can suppress state/gauge maintenance; the adoption/top-up branches then follow under clause 3.

### Completeness

(cause) × (what the run holds) × (duration):

| Cause \ holdings | nothing minted (reservation only / promise pods out) | partial gang (0 < k < n leases) | full gang / running |
|---|---|---|---|
| zero Budgets (routine) | < W: hold visibly; PreBind refuses fresh mint; ≥ W: reservation Failed, intent pods withdrawn | leases coast Unfunded (class unchanged for elapsed hours, P6); completion mints allowed; if its reservation is still Pending, same W clock | leases coast Unfunded; replacement ranks allowed; nothing terminal ever |
| multi-owner (local admin error) | same | same | same |
| leaf-span (foreign-inducible, post-P5 always detected) | same, plus the conflict record names both namespaces for the alarm | same | same |

Uniform across causes by design — the operator learns one rule, and the inducible column is distinguished by *detection* (alarm naming the foreign namespace), not by different destruction semantics. No cell undefined; no cell destroys work; only one cell destroys anything, and it requires W of persistence.

### Trust boundary (non-inducibility, named)

Principals: tenants (create-Run in own namespace only), admins (Budget/Lease write), controller SA (reservation + run status), scheduler plugin (sole lease committer). Then: bound→unbound is causable **only by a Budget write** — by posture only admins; a Budget-writer anywhere can induce it for any namespace, and post-P5 that induction is always recorded as a `BindingConflict` naming both namespaces. **No principal, admin included, can cause:** a cross-namespace Owned charge (INV-OWNED-IS-LOCAL), reclassification of elapsed hours via Budget creation (INV-ACCRUAL-ANCHORED), closure of any lease via binding loss (INV-BINDING-CLOSES-NOTHING), or reservation destruction in under W (INV-RESERVATION-GRACE). A hostile Budget-writer's maximum is W-delayed, alarmed denial of *new* funding — and per §6c a hostile Budget-writer is already total quota compromise, so this adds no new trust.

### INV

- **INV-RESERVATION-GRACE** (transition): reservation Pending→Failed with a binding cause ⟹ `after.Status.UnboundSince ≠ nil ∧ now − UnboundSince ≥ W`.
- **INV-UNBOUND-VISIBLE** (steady): `OwnerOf(ns) == ""` ∧ a due Pending reservation in ns ⟹ that reservation carries `UnboundSince` and a reason naming the binding (the anti-second-door rail: held is never invisible).
- **INV-BINDING-CLOSES-NOTHING** (transition): across any engine entry point in which some namespace's derived owner flips bound→unbound, the set of open leases in that namespace is unchanged, and no `ClosureReason` in the vocabulary names a funding/binding cause.
- **INV-UNBOUND-MINT-COMPLETES** (transition): every lease in `after` absent from `before` whose run-namespace derives "" belongs to a run that already held ≥1 open lease in `before`.

The World projection in pkg/invariant needs Budgets (or the derived `OwnerOf`/conflict facts) and reservations added; that is an extension of the existing pattern (invariant.go:229-233), not a new mechanism.

### Ratified text

Supports: Decision 1 (demote-not-kill — clause 2 is that decision verbatim; clause 1 extends its spirit from work to promises with a durability bound); C-2's fresh/pre-existing dichotomy (clause 3 applies it at gang granularity); R4 pt1b's precedent that a safety-defining number is an owner number. **Amends, explicitly:** R7-tenancy-amendment.md:126-131 and the §14 C-2 restatement, where "the reservation path fails terminally" becomes "fails terminally once the loss has persisted for W." That sentence was ratified before two executed reproductions showed one-tick terminality fires on routine GitOps and is foreign-inducible; the judge's `fixIsReaper=false` was established for a durable admin typo and — as P8's own correction says — does not cover the transient or hostile cases. Both prior verdicts are honored: not immortal (fails at W, gauge never freezes), not hair-trigger (survives every window shorter than W). Also to be scheduled with this: wiring R26 to `Conflicts()` — evaluate.go:176-182 says in its own words that NOTHING consumes the conflict records yet, and a grace-then-terminal rule whose only signal is a run-status message is half a rule.

### Trade-away

A reservation on a genuinely dead namespace holds its reserved capacity for W before failing — that cost is borne by whoever queues behind it, and it is the price of never destroying on a transient. A sustained hostile Budget still denies a namespace new funding for as long as it stands — loud, attributed, and gated by an RBAC violation that is already classified as total compromise. One new persisted field (`Reservation.Status.UnboundSince`) and one owner number.

### Residue for David — one sharp question

**W = how long, and is it uniform across the three causes?** Options: **15m** — covers every GitOps/helm window and controller restarts; a sleeping admin's typo still destroys reservations overnight (work never dies regardless). **1h** — covers most human repair; holds reserved capacity up to an hour on dead namespaces. **24h** — nothing is destroyed inside a working day; a day of capacity holdback per dead namespace, borne by other tenants. And uniform-vs-split: uniform (recommended — one rule at 3am) or never-terminal for the foreign-inducible leaf-span cause only (stronger anti-DoS, but re-opens an immortal-reservation cell for exactly the cause an attacker controls, which is backwards). Everything else in P8 is invariant under this choice.

---

## Interactions (why these four are one decision)

P5 (universal injectivity) makes binding conflicts strictly easier to induce — that is deliberate, trading silent theft for loud denial — which is only tolerable because P8 makes denial non-destructive under W and P6 makes it non-retroactive. P6's ruling *is* P7's invariant; they cannot be answered differently without building the confidently-wrong oracle P7 warns about. P8's clause 2 depends on P6: "work coasts" is only safe if coasting cannot erase the hours the work already paid for. And the whole stack rests where the amendment already put it: RBAC keeps Budget writes admin-only, R26's alarms (once actually wired) make violations visible, and the INVs make the rest un-driftable.

## Where I am uncertain, plainly

1. **Nothing here is executed by me.** The reproductions I rely on are the panels'; my own claims about creation-time anchoring are traced, not run. Specific unexecuted risks: interaction of anchored derivation with settlement compaction (`settlementSafe`/`PriorAccrual` — `SettleAccrual` calls `Evaluate` recursively, so it should inherit anchoring, but the no-straddle proof in `specs/LedgerCompaction.tla` should be re-checked); and golden-oracle bit-identity under the zero-CreationTimestamp = "always existed" rule.
2. **Clause 3's predicate granularity** ("≥1 open minted lease of the same run" vs "same gang/cohort") needs care against `gangProvenance` reconstruction and grow cohorts (gang.go:398-516). The reaper-veto record supports leniency on this path, but the exact boundary should be settled by a compiled probe, not by this document.
3. **W's floor.** I assert W must exceed the GitOps prune-to-apply window and the activation tick; I have not measured either in this deployment shape. Clamp W ≥ 4× the activation interval regardless of the owner's number, R26-style.
4. **P6's deletion blindness** is real and I have not found a way to close it without the P3 settlement store; if David rejects P3 permanently, the "hours during a resolved conflict re-bill retroactively" cell (pinned as 4/0/12) survives my recommendation, and the INV must exempt deletions or it will fail honestly.
