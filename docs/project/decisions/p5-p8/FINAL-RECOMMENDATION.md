# Final recommendation — P5, P6, P7, P8

**For:** David (owner). **Written by:** the consequence reader, after both designers filed and after Owner Rulings 1–4.
**Status:** recommendation. Every rule below is a proposal for your ratification, not a decision taken.

All code and ratified-doc citations are against commit **`37270af`** and were **re-verified against that commit for this document**. Exchange papers (`A`–`E`, `OWNER-RULINGS.md`) are cited by working-tree line; Sol's `B` is duplicated in its file and I cite the first copy (lines 1–375).

## Citations I checked and found wrong

Inherited nothing. Corrections, so the record stops carrying them:

| Claim | Cited | Actual at `37270af` |
|---|---|---|
| `ConsumedGPUHours` "History is evaluated under the current spec" doc comment | A:95 (`evaluate.go:95-98`) | **`evaluate.go:104-108`**. Sol's citation (`104-108`, B:179) is exact; A's is off by nine lines. |
| `TestResolveShrinksBeforeLottery` | Ruling 3 (`resolver_test.go:64`) | **`resolver_test.go:65`** |
| "both ends of every family edge are written by the same accountable party" | D:43 (`R7:288-289`) | **`R7:287-288`** |
| `Reservation.Spec.PayingEnvelope` | `R7:372-374` cites `reservation_types.go:27` | **`reservation_types.go:37`** — and see §3.7: R7 §7 specified scoping this field in the pt1 sweep and **it was never done**. |
| `pkg/invariant` Check entry point | Sol B:253 (`invariant.go:441-473`) | straddles two decls. `Check` = **`466-474`**; `Panic` = **`445-461`**; `var Report Reporter = Panic` = **`381`**. |
| steady+transition predicates | Sol B:223 (`invariant.go:235-371`) | **`235-372`** (one line short). |
| `lendingAllows` | `R7:144`, `R7:326` (`evaluate.go:905-918/928`) | **`evaluate.go:1085-1117`**. R7's numbers are pre-pt2 and stale. |
| P6/P7 park entries | `DECISIONS-NEEDED.md` (`evaluate.go:661`) | **`evaluate.go:707`** (written against `0b77fbe`). |

Everything else I repeat below I verified: `deriveOwners` at `evaluate.go:206-272` with the `isInterior` skip at `:239` and `interior[parent]` collection at `:220-222`; the live-derivation sites at `:707` and `:798`; `acct == nil` → Unfunded at `:694-698`; the "COAST IS NOT A PROMISE OF SAFETY" note at `:280-286`; "NOTHING CONSUMES THESE YET" at `:176-182`; the three activation doors at `run_controller.go:1122`/`:1145`, `:1168-1171`, `:1216-1226`; `failReservationTerminally` at `:1521-1526`; `minRunnableGPUs` at `:2732-2737`; grow-lease segregation at `:2804-2816`; `gang.go:755-758` and its own "it bills nobody" comment at `:752-754`; `budget_types.go:30-31,36`; `reservation_types.go:16,33-39`; `rbac.yaml:19-24` bound at `:43-54`; `tenancy_r7_conflicts_test.go:130` and `:170` (including the `4/0/12` assertion at `:270-273`); `World` = Runs+Leases only at `invariant.go:229-233`.

---

## 0. What changed after the designers filed — read this first

The exchange is not a level playing field and the document must not be read as one:

- **D (Fable) was written against Ruling 1 only.**
- **E (Sol) was written against Rulings 1 and 2** (it cites `OWNER-RULINGS.md:19-26`, `:28-46`, `:66-75`).
- **Neither designer saw Rulings 3, 4, 5 or 6.**

Five things therefore have no designer input, and are mine:

1. **Ruling 4 dissolves the repeated-draw trap.** Verified independently: `Resolve` returns on non-positive `Deficit` (`resolver.go:39,74-75`), and its only two production callers are `run_controller.go:458` (admission) and `:1332` (activation). No timer reclaims. §3.1 records this as a hypothesis checked and **disconfirmed**, plus what remains.
2. **P8 was already decided in ratified text on 2026-07-04, for two of its three cells** — and the whole oscillation is what happens when a new case is not connected to an existing rule. §3.2 states exactly how far that reaches, including where it does *not*.
3. **The largest gap in this whole exercise is that Ruling 2's remedy cannot currently fire at all.** §3.3. Sol saw the missing machinery (E §3); neither designer saw that its absence inverts who bears the cost.
4. **Ruling 5 shows P6 as both designers framed it is only half the surface.** They answered it on the *owner* axis. The *window* axis has the same hazard with a legal actor, and neither touched it. §2/P6 now carries both axes and its own enumeration; §3.8 records what I found there that no ruling states.
5. **Ruling 6 changes my P6 recommendation and resolves one of the two surviving disagreements.** It makes `INV-ACCRUAL-PREFIX-IMMUTABLE` **total** over spec mutations, which **removes Fable's identity-vs-policy split** — a contribution of A's that I had adopted and now withdraw. And it settles disagreement 2 in a specific direction: history is required (Sol's premise) but the mechanism is the already-parked P3 (Fable's refusal to buy two systems). §4 therefore lists **one** unresolved disagreement, not two.

**A note on precedence, because it bears on §5.** The coordinator instructed me to place Ruling 5's window-axis question in the owner's residue unanswered. Ruling 6 answers it — *"they stay charged"*, `OWNER-RULINGS.md:226-227`, and explicitly *"Remove it from the owner's list"* (`:270-271`). The owner outranks the instruction, so it is recorded in §2/P6 as decided and does **not** appear in §5. What appears in §5 instead is the question Ruling 6 opens: **is P3 scheduled?**

---

## 1. True consensus versus coincidence

### 1a. Convergences that are concessions to evidence — the strongest items in the record

Each of these was argued *against* by the designer who then accepted it, with the other's text cited.

**(i) Authority must be asserted by someone other than the claimant. Two different routes to one answer — the strongest single result of the exchange.**
Sol argued it from authentication first principles (B:19-32): a free-form `Spec.Owner` cannot be an identity registry. A argued the *opposite* in its opening, then reached the same place from the other end: it enumerated every asymmetry a Budget scan could use and rejected each on its own merits — creation order (squat-first *awards* family position rather than merely denying, D:27), incumbency-by-accrual (makes identity a function of usage, D:28), namespace locality (no asymmetry exists, D:29) — and concluded there was none. A's concession is unusually complete: *"on the central factual question — can self-naming `Budget.Spec.Owner` serve as the identity source — Sol was right and my position as filed was wrong"* (D:45). Verified: `Spec.Owner` is `MinLength=1` free text (`budget_types.go:30-31`) and `Spec.Parents` is child-asserted with no existence or consent check (`budget_types.go:36`; `funding.go:60-73`). **Two routes, one destination. This is real evidence, not one argument twice.**

**(ii) The rooted grant trace supplies the missing asymmetry — a concession against a stated categorical.**
Sol had asserted that a scan *structurally cannot* select legitimacy (C:40-44). A proposed grantor-side grants with a BFS from a configured root set (D:33-36). Sol withdrew the categorical in the first sentence of its reply: *"My categorical statement that a scan structurally cannot select legitimacy was too broad"* (E:9). A withdrawal of a universal claim under a counterexample is the strongest form of agreement available.

**(iii) The owner-keyed registry repair — offered by the *opponent* of the registry.**
A, while arguing against Sol's mechanism, handed Sol the fix for its one transactional hole ("reject the second binding atomically", B:42, requires the cross-object admission `R7:135-136` says webhooks lack): make the CRD cluster-scoped and key it by owner, so etcd name uniqueness enforces owner injectivity in the same transaction (D:17). Sol accepted and tightened it (E:36-44). Concessions run in both directions here.

**(iv) The min-runnable-width boundary, and the C-2 carve-out. Mutual, and therefore jointly proposed.**
Sol charged that A's clause 3 left partial leases open indefinitely (C:71-85). A verified the charge against the code and conceded (*"On an idle cluster that is an immortal-class defect wearing different clothes. Conceded."* D:49), adopting Sol's boundary — `minRunnableGPUs` (`run_controller.go:2732-2737`). Sol had already conceded that its own rule needed the same amendment to R7 C-2 (C:101). **Neither can claim this one; both had to move.**

**(v) Differential property test before SMT.**
A's argument (A:115-125): a hand-encoded SMT model adds a second way to be confidently wrong that a differential test structurally cannot have, because it executes the code under judgment. Sol had required both layers immediately (B:196-205) and withdrew, citing A's argument *and* its own admission that the repo has no solver-cost data (B:262-269): *"That is a real change of position"* (C:14).

**(vi) The miscount, corrected. This is the failure mode the brief warned about, and it happened.**
A wrote that the ratified design rejected Sol's mechanism "three separate times" (A:139). Sol corrected it (C:59-69); A withdrew (D:17). I verified: R7 rejected a **client-backed Budget webhook** (`R7:135-137`, `303-306`), a **Namespace label as a second source of truth** (`R7:136-137`), and a **Run-creation VAP with its own registry** (`R7:250-252`). None is a canonical-identity CRD. Sol's correction stands.

**(vii) The §6c "no new trust" argument is dead** — conceded by A in all three places it used it (D:13), after Ruling 1. Verified: the only RBAC the chart ships grants `budgets` to the controller ServiceAccount (`rbac.yaml:19-24`, bound `:43-54`); the amendment says of itself *"It is posture, not code"* (`R7:399`). Not a concession to the other designer — a concession to evidence about the chart.

### 1b. Convergences that are one argument counted twice

- **Delete the interior-tier injectivity exemption.** Both reason from the identical fact: `cover.NewInventory` buckets envelopes by owner cluster-wide with no namespace term (`cover.go:84-91`), so a leaf owner in two namespaces mints Owned across the boundary (A:25-30; B:30). Same evidence, same inference — **weight it as one argument.** It happens to be well-supported anyway: the exemption's stated premise (*"nothing ever classes Owned against a pool"*, `R7:165-166`) is falsified by an executed reproduction pinned at `tenancy_r7_conflicts_test.go:130`, and the reaper-veto lens ruled the narrowing is not a reaper. So the *conclusion* is strong; the *agreement* adds little.
- **Visibility rails.** A's `INV-UNBOUND-VISIBLE`; Sol adopted it (C:16). One-sided adoption, same route (the frozen gauge). One argument.
- **"Running work must never be closed by binding loss."** Both. Same route: `quota-semantics.md` Decision 1. This is not a convergence at all — **it is two designers quoting ratified text.** Zero independent weight, and §3.2 is about why nobody noticed that.
- **Forward-only accounting (P6).** Partially independent. A's route is a semantic-category argument (who paid for hour *h* is a fact of hour *h*; what the payer's budget affords is deliberately current policy). Sol's is an audit-property argument from `quota-semantics.md:90-91`. They share the anchor citation but not the reasoning. Modest additional weight.

### 1c. The convergence I distrust — and it is the most consequential thing in this section

**Both designers assumed the load-bearing decision in P8 is a number (A's `W`, Sol's `G`), and neither tested whether the cell that needs it holds anything.**

It does not. The cell A and Sol both size their number against — a zero-hold pending reservation — by construction holds no leases, no GPUs, and no capacity promise. Terminality there protects nothing and frees nothing. The cell that *does* hold GPUs while producing nothing (the below-minimum partial gang) needs a deadline, and already has a neighbouring mechanism with the same shape (§3.6). And the two executed reproductions the numbers were sized against — foreign inducibility and the GitOps window — are both closed by P5's trace, not by any duration.

They converged. On the answer to the wrong question. That is exactly the failure mode a two-designer panel is supposed to catch and did not, because they were decorrelated by *vendor* and not by *framing* — the brief handed both the same four candidate rules, three of which contain a number.

### 1d. Where their final filings still disagree, and the record should show it

Sol withdrew "every zero-hold reservation requires a finite terminal deadline" (E:89) after Ruling 2. **A did not follow** — `W` survives in A's last filing, because A never saw Ruling 2. So on P8's central cell the two ended *further apart* than the middle of the exchange, and Sol's later-informed position is the one this recommendation adopts. Reading the exchange as "converged substantially" is right about six items and wrong about this one.

### 1e. One position vindicated by a later ruling, one overtaken — a category the exchange could not produce

Neither of these is a concession between designers. Both are the owner's later rulings landing on filed positions, which is stronger evidence than agreement because the ruling was written without reference to the argument.

**Sol's per-dimension conservation predicate is vindicated, and it looked pedantic when filed.** Sol insisted the conservation predicate hold *"separately to instantaneous concurrency and windowed GPU-hours"* (E:65). Ruling 5 then named the director's two levers independently — dial down the count, or move the window — and **verified: they are literally the two dimensions Sol separated.** `BudgetEnvelope.Concurrency int32` at `budget_types.go:63` is the count axis; `Start`/`End *metav1.Time` at `:66-67` are the window axis; `MaxGPUHours *int64` at `:65` is the integral. A conservation rule written over one number would have been wrong for half the reductions a director can perform. **Credit to Sol.** A's `INV-GRANT-CONSERVE` (D:41) was single-dimensional and Ruling 2 had already killed it for a different reason; Ruling 5 shows it was wrong twice over.

**A's identity-versus-policy split is overtaken, and I withdraw my adoption of it.** A's P6 rested on a genuine distinction: *who paid for hour h* is a fact of hour h (anchored), while *what the payer's budget affords* is deliberately current admin policy (current-spec). It is a good argument and I adopted it. **Ruling 6 rejects the asymmetry outright** — *"there is no policy exemption: owner binding, grant topology, concurrency, and window are all prospective"* (`OWNER-RULINGS.md:232-236`). So the split is not wrong about the *categories*; it is overruled about the *consequence*. The owner's reason is better than the split's: a fact of the hour and a policy in force during the hour are both **facts about that hour once it has elapsed**, and only the ledger's inability to remember them made the second look revisable. §2/P6 is rewritten accordingly, and §3.8 states what that costs.

---

## 2. The recommendation

One sentence, for an operator: **Losing your funding principal is a funding shortfall, and jobtree already decided what a shortfall does — nothing dies, nothing is billed, nothing is resubmitted. Identity comes from a chain of grants rooted in a configured admin, so nobody outside your chain can take it away. The only thing ever unwound is an assembly that holds GPUs and cannot run.**

Prerequisite for every INV below: **`invariant.World` carries `Runs` and `Leases` only (`invariant.go:229-233`).** Extending the projection with Budgets/grants, derived `OwnerOf`/conflicts, reservations, and per-envelope accrual is the single gating task; the existing `Check` hook (`invariant.go:466-474`, `Report = Panic` at `:381`) and the three deferred entry points (`run_controller.go:147-148`, `:1050-1051`, `:1566-1567`) then carry all eight.

---

### P5 — when is an owner binding trustworthy at all?

**Rule (operator-actionable).**

1. A **deploy-time root set** names exact `(owner, namespaceUID)` tuples. Nothing self-nominates into it.
2. A Budget is **legitimate** iff its `(owner, namespaceUID)` is in the root set, **or** some already-legitimate Budget carries a grant naming exactly that `(owner, namespaceUID)`.
3. **Only legitimate Budgets bind a namespace.** An illegitimate Budget is ignored for identity and alarmed. It changes nothing, anywhere.
4. **Injectivity is universal — no interior-tier exemption.** If two *legitimate* grants bind one owner in two namespaces, both namespaces fail safe to unbound and both are alarmed. That residue is a within-chain error by someone who holds authority over that owner — no longer foreign-inducible.
5. **A namespace with a live inbound grant but zero local Budgets is BOUND** — known principal, no envelope. This is what kills the GitOps cell for identity, and it is grantor-side, so it survives recreation of the leaf's Budgets.

Sol's four load-bearing details (E:25-28) are adopted verbatim as part of the rule: bind **namespace UID**, not name; roots are configured tuples, never self-named; only already-legitimate grantors are traversed; **identity and allocation are logically distinct inside the grant** — reducing a cap to zero is allocation policy, deleting the grant is revocation.

**INV.**
- **`INV-OWNER-TRACES-TO-ROOT`** (Evaluate postcondition, D:39): `OwnerOf(ns) == o ≠ ""` ⟹ a grant chain exists from a root-set Budget to `(o, nsUID)`, every link on a legitimate Budget.
- **`INV-GRANT-LOCAL`** (transition, D:40): a mutation touching only funding objects outside `chain(ns)` leaves `OwnerOf(ns)`, ns's conflict set, **and ns's loss-onset timestamp** unchanged. This is Sol's inducibility rail (B:68) made true for a scan.
- **`INV-OWNER-INJECTIVE`** (postcondition, A:49): `∀o: |{ns legitimately bound to o}| ≥ 2` ⟹ every such ns derives `""` and a `BindingConflict` names it. No interior escape in the predicate.
- **`INV-OWNED-IS-LOCAL`** (steady, A:48; adopted by Sol at C:7): every open lease classed Owned has `PaidByBudgetNamespace == RunRef.Namespace`. The end-to-end rail that catches *any* future road to cross-namespace Owned.

**Enumeration — no state undefined.** For owner `o`, namespace `ns`:

| inbound rooted grant to `(o,ns)` | local Budget claiming `o` in `ns` | other namespaces legitimately bound to `o` | outcome |
|---|---|---|---|
| `ns` in root set | — | — | bound by configuration |
| yes | yes | 0 | bound, funded normally |
| yes | **no** | 0 | **bound; no envelope.** Fresh admission fails for lack of *capacity*, not identity. Running work coasts. (the GitOps cell, now benign) |
| yes | any | ≥1 | all colliding namespaces derive `""`; `BindingConflict` on each; alarm names the grantor(s) |
| **no** | yes | — | Budget illegitimate: ignored for identity, `ns` unbound, alarm "unrooted Budget" (**the squatter cell — no victim transition anywhere**) |
| no | no | — | `ns` unbound. No conflict record: nothing was claimed |
| grant exists but grantor illegitimate | any | — | edge not traversed ⟹ identical to the two rows above |
| two distinct owners legitimately granted into one `ns` | — | — | `ns` derives `""`, `ConflictMultipleOwners`. (E:44 is right that owner-name keying solves the owner axis only; this converse axis needs its own fail-safe either way) |

**Trust boundary, named.** `bound → unbound` for namespace `N` is causable only by a write to a funding object **inside `chain(N)`** — your grantors can affect you, which is delegation working, and is precisely "contained to what they were granted" (Ruling 1). No principal, root admin included, can cause: a cross-namespace Owned charge (`INV-OWNED-IS-LOCAL`), an identity change outside its own subtree (`INV-GRANT-LOCAL`), or reclassification of elapsed hours (P6).

**What is traded away.** A genuinely multi-namespace pool stops funding its members' Shared claims until consolidated — a configuration no install has (`R7:419`). And every principal in the chain can now break its own descendants' funding, deliberately: that is the delegation Ruling 1 asked for, not a leak.

---

### P6 — does the fail-safe reach backwards through the replay?

**P6 has two axes, and both designers answered only one.** The *owner* axis is what the panel reproduced (a squatter Budget erases 32 burned hours). The *window* axis is Ruling 5's finding: a director delaying an envelope's `Start` produces **the same observable by a legal action**. Ruling 6 rules one answer for both, and it is stronger than either designer proposed.

**Rule (Ruling 6, `OWNER-RULINGS.md:215-244`).** **Forward only, on every axis. Spent quota cannot be taken back.** The owner binding, the grant topology, the envelope's concurrency, and the envelope's window used to classify a replay segment are the ones in effect *during* that segment. Hours already accrued keep their class, their payer, and their charge. Every spec change takes effect from the moment of the edit. A director may move a window freely; the work goes Unfunded from the edit and coasts, at risk, reclaimed only on demand (`quota-semantics.md:27-34`) and re-funded automatically when the window reopens (`:38-39`) — but **nothing she does un-spends an hour already burned.**

**INV — `INV-ACCRUAL-PREFIX-IMMUTABLE`** (Sol's name, B:139; A's `INV-ACCRUAL-ANCHORED` states the same property only for *additions*), now **total over spec mutations** rather than scoped to the identity axis:

> For any mutation `m` of the funding inputs — object creation, deletion, or in-place spec edit — occurring at `t_m`, and any `Now ≥ t_m`:
> `Attribution(Evaluate(S, Now))` and `Attribution(Evaluate(m(S), Now))` agree on every `(leaseUID × payerEnvelopeKey × class)` hour tuple attributed to intervals ending at or before `t_m`.

Quantified over grants and over envelope spec fields, not just over Budget existence. The only remaining exclusion is a **recorded adjustment event** (Sol's, B:135) — an explicit, auditable correction, never an incidental replay side effect. **Release-on-renewal is not an exception and does not need one:** each hour is attributed to the window in force during it, so old hours belong to the old window and a reopened window starts with a fresh integral. The ratified arithmetic (`evaluate.go:104-108`) produces the same answer by a different route — clamping to *the current* window — and it is the route, not the answer, that Ruling 6 changes.

**Enumeration — the window axis, which no designer covered.** Verified mechanism: `windowActive` (`evaluate.go:634-642`) is tested against `acct.Spec` — the **current** spec — at every replay instant, and it has exactly one caller (`:760`), where a closed window marks *every* claim on that envelope Unfunded (`:760-765`). `accrue` then adds to `ConsumedGPUHours` only for non-Unfunded classes (`:988-998`). So today the window axis is fully current-spec, and the erasure is total rather than partial.

| Director's edit | Hours already accrued and funded | From the edit forward | Status |
|---|---|---|---|
| **Dial down `Concurrency`** (`:63`) | unaffected — the concurrency gate is per-instant (`admit`, `:848`) and never revisits a charged hour | excess demotes to Unfunded; reclaimed only on demand | **already ratified** — Decision 1 names it: "or concurrency, via a higher-ranked claim" (`quota-semantics.md:27`) |
| **Accelerate `End`** (`:67`) | unaffected — hours before the new `End` are still inside the window | work Unfunded from the new `End`; re-funds when a window reopens | **already ratified** (`quota-semantics.md:41-43`) |
| **Delay `Start`** (`:66`) | **today: erased.** `windowActive` false for every historical segment ⟹ all hours reclass Unfunded ⟹ `ConsumedGPUHours` drops to **zero**, and already-spent headroom reappears in full | work Unfunded until the new `Start`; fresh admission also refused unless `PreActivation.AllowAdmission` (`cover.go:261-272`, nil ⟹ refuse) | **Ruling 6: hours stay charged.** Requires P3 (§3.8) |
| **Reduce `MaxGPUHours`** (`:65`) below the already-spent total | **today: erased above the new cap.** `accrue` clamps the charge to the remaining room (`:993-997`), so an envelope that burned 40 and is reduced to 10 reads `ConsumedGPUHours = 10`; the other 30 spent hours move to the Unfunded bucket | claims demote once `RemainingGPUHours()` hits zero (`:124-133`, gate at `:851-853`) | **Ruling 6 covers it; Ruling 5's table does not** — see §3.8(b) |
| **Reopen / rotate the window** | attributed to the window in force at the time; the new window starts fresh | re-funds automatically | ratified, and preserved by Ruling 6 via per-window attribution |
| **In-place edit of the owner or a grant target** | anchored, per the identity axis | forward | **not computable from the snapshot** (§3.8a) — same defect class as delaying `Start` |
| **Delete** a funding object | not recoverable from the current input | forward | the `4/0/12` cell (`tenancy_r7_conflicts_test.go:270-273`) — closed by P3, not by anchoring |

Every cell defined. **Three cells — delay `Start`, reduce `MaxGPUHours`, delete — are stated correctly by Ruling 6 and enforceable by nothing today.** That is not a hole in the rule; it is a dependency, and §3.8 names it.

**My own finding, against A: A's zero-timestamp convention neuters A's own invariant on its own witness.** A proposes that Budgets with zero `CreationTimestamp` "count as always existed" so fixtures stay bit-identical (A:71). The pinning fixture has **no timestamps** (`tenancy_r7_conflicts_test.go:214-217`: `other` is constructed with `ObjectMeta{Name, Namespace}` only). Under A's convention the conflicting Budget always existed, so `0 consumed / 40 remaining` is the *correct* answer and the headline invariant passes vacuously on the very test A cites as its falsifier. The fix is not the convention but the quantifier: a funding object with no effective time is **not admissible input to a historical claim**, and the property generator must reject it rather than treat it as eternal. This is the P7 entry's "confidently wrong oracle" arriving through the fixture rather than through the model.

**Enumeration — the owner axis**, which is what both designers answered. Cause of binding/topology change × interval:

| | hours before the change | hours during | hours after resolution |
|---|---|---|---|
| grant/Budget **added**, creating a conflict | keep earned class and payer (**changed** — today they erase, pinned 32→0) | Unfunded (unchanged) | bound again, forward |
| chain intact, all local Budgets **deleted** | account object gone ⟹ hours invisible (**unchanged; honestly imperfect** — closed by P3) | Unfunded via `acct == nil` (`evaluate.go:694-698`) | restored on re-apply |
| conflicting object **deleted** (resolution) | n/a | **retroactively re-billed** (`4/0/12`); the replay cannot see an object that is gone — **closed by P3, not by anchoring** | forward normal |
| legitimate **re-grant** A→B | A keeps hours earned under A; B pays only from the grant's effective time (**changed**) | — | — |
| **in-place edit** of a grant's owner/target | anchored under Ruling 6; **not computable from the snapshot** (§3.8a), so P3-dependent | | |
| envelope **window rotation** | see the window-axis table above | — | — |

Every cell defined. **Under Ruling 6 the imperfect cells are no longer "honestly imperfect semantics" — they are correct semantics with a missing mechanism**, and the mechanism is named: P3. That is a better position to be in, and it is a worse position to *ship* from, because a stated invariant with no enforcement is exactly what `AGENTS.md` calls comment-as-enforcement. §5/R2 is the question.

---

### P7 — how would any of it be machine-checked?

**Rule.** Candidate **(2)** — hours already elapsed never change classification or attribution — and under Ruling 6 it is **total over spec mutations**, not scoped to one axis. Not (1): "charges never decrease" is still false on purpose, because a recorded adjustment event may decrease a charge deliberately. Note what Ruling 6 changes here — (1) was previously false *also* because window renewal released hours, and it no longer does; the gap between (1) and (2) narrows to explicit corrections alone. Not (3) alone — conservation is implied by (2) on pre-change intervals, and (3) standing alone would bless a rewrite that *moves* a charge, which P6 forbids. The re-parenting sub-question is answered by P6: history stays with the envelope that paid at the time.

**Gate, in three layers, in this order.** I differ from both designers here, and it is a simplification:

1. **Rails at the door, now.** Five of the eight INVs in this document (`INV-OWNER-TRACES-TO-ROOT`, `INV-GRANT-LOCAL`, `INV-OWNER-INJECTIVE`, `INV-OWNED-IS-LOCAL`, `INV-BLOCKED-VISIBLE`) are steady or transition predicates checkable on every engine entry point through the hook that already exists (`invariant.go:466-474`). They are cheaper than any property test, land sooner, and do not need P6 answered. Both designers filed P7 as "the oracle for P6"; most of the checking value is not in P6's oracle.
2. **A differential property test against the real `Evaluate`** for `INV-ACCRUAL-PREFIX-IMMUTABLE` — Sol's concession (C:9-14) to A's argument. The **generator** is the load-bearing part, not the assertion, and this repo has a demonstrated failure mode for exactly that: `37270af`'s own commit message records a recovery test that passed with the repair deleted. Requirements: the generator must emit **non-zero effective times** (else vacuous, per §P6); fixed seeds must include all five reproduced shapes — conflict onset mid-lease, leaf-span, interior-exempt-turned-conflict, **unrooted squatter, within-chain double grant**; and it must be mutation-verified against the anchoring line itself, per `AGENTS.md:171-173`.
3. **Bounded SMT: not now.** Only after (2) has demonstrably missed a state class *and* the `Graph.Tier` ancestry encoding cost has been measured. Sol's own admission that the repo has no solver-cost data (B:262-269) is the reason.

**INV.** As stated in P6. **`INV-CLOSED-MONOTONE` (`invariant.go:117`, enforced `346-369`) is the only existing transition-tier invariant** — every transition predicate here is the second of its kind, so the pattern exists but is thin.

---

### P8 — may losing the funding principal DESTROY, and may a foreign party induce it?

**The answer to the second half is: no, and it is answered by P5, not by a number.** Under `INV-GRANT-LOCAL` a foreign Budget cannot move `OwnerOf(victim)`; under P5's fifth clause a GitOps window that deletes and recreates a namespace's Budgets is not an identity event at all. Both executed reproductions close there. **`W` and `G` were sized against threats that P5 removes, and this recommendation drops them.**

**Clause 1 — running work: already ratified; no new rule, no number, no decision.**
A gang at or above `minRunnableGPUs` (`run_controller.go:2732-2737`) coasts **Unfunded** from the onset of the loss. Nothing terminal, ever. Reclaimed only when funded demand actually arrives, first in the order, by the attested lottery. Recovery is automatic. This is `quota-semantics.md:27-30`, `:31-34`, `:38-39`, `:108`, `:128-129` — i.e. Rulings 2, 3 and 4 restated. Both designers already agree (A clause 2; Sol row 4). Mechanically the code already does it: an unbound namespace derives `""`, `Tier(owner, "")` is `tierNone` (`funding.go:132-150`), `cl.sponsored` is set (`evaluate.go:708`), and the empty-borrower guard refuses the sponsor pass (`evaluate.go:1095-1097`) ⟹ `ClassUnfunded`. **Downstream, principal loss and envelope exhaustion are indistinguishable** — `groupEntirelyUnfunded` (`resolver.go:364-377`) reads the class and does not ask why.

**Clause 2 — zero-hold reservation: `BlockedFunding`, indefinite, inert, honest. Not terminal.**
The 2026-07-24 defect was **invisibility, not waiting**: an ordinary `Pending` object claiming progress while its countdown and backlog gauge froze at `{H100-80GB 1020}`. `failReservationTerminally`'s own comment says so (`run_controller.go:1512-1519`). Sol's conditions (E:91-98) are adopted: holds no leases, GPUs, pods or capacity promise; excluded from activation (the loop already considers only `Pending` — `run_controller.go:1066-1068`, so this is inert for free); records **cause and onset** durably; **clears** the countdown and backlog gauges, because no activation forecast exists; carries its own blocked-count and blocked-age signals; re-driven automatically when the chain or envelope returns.
*Why not terminal-after-W/G:* nothing is protected by the transition. This cell holds nothing — that is its definition. Terminality's only products are a `Failed` object and, in the shipped code, an operator message telling a human to resubmit (§3.4).
*The stale-plan half is real but narrower than Sol claims.* Verified: `IntendedSlice.Nodes` is written at forecast (`run_controller.go:2162`) and **never read on the activation path** — activation reads only `.Domain` (`:1234`, `:1289`) and re-plans against the current snapshot each time (`:1344`). `PayingEnvelope` is already only a *preference* with a same-flavour fallback (`:1483-1495`). So: **resume the same reservation; re-forecast only if the recomputed plan differs materially.** No new object, no new CRD.

**Clause 3 — the below-minimum partial gang: the one cell that needs a new rule, and the one number.**
It holds real GPUs and produces nothing, so `quota-semantics.md:27-30` ("it keeps its GPUs and keeps running") is *factually false* of it. This is the genuine gap, and both designers converged on it from opposite directions (§1a-iv). Rule: while unbound, a gang below `minRunnableGPUs` may **complete to its recorded width** from durable provenance, including replacement ranks after node failure; if still below minimum after deadline **U**, **unwind whole** — leases closed by normal release, pods deleted, reservation released, run requeued. PreBind must stop refusing on `derived == ""` for a gang that already holds ≥1 minted lease (`gang.go:755-758`) — the CRITICAL reproduction, and `gang.go:752-754` already concedes the completed leases bill nobody.
**My finding: the mechanism already exists in a neighbouring form, so clause 3 needs no new machinery.** `checkpointGrace` → `CheckpointDeadline` → `RunStateCheckpointExpired` (`run_controller.go:944-949`, `:1867-1878`, enforced `:228-233`) is exactly "below minimum runnable width, bounded, then terminal", and the condition vocabulary already exists (`api/v1/conditions.go`, incl. `RunConditionBlocked` at `:148`). **But it must not be reused as-is:** `checkpointGrace` reads `run.Spec.Runtime.Checkpoint.Duration` — **tenant-declared, defaulting to zero**. Tenant-declared means a tenant can hold GPUs indefinitely; zero-default means immediate destruction. **U must be cluster policy with a floor.** That is a substantive correction to the obvious reuse.

**Enumeration — (cause) × (what the run holds) × (duration).** Post-P5 causes:

| Cause | H1: nothing minted (reservation / intent-or-promise pods out) | H2: partial gang below `minRunnableGPUs` | H3: gang at or above minimum (running work) |
|---|---|---|---|
| **Unrooted (squatter) Budget anywhere** | **no transition. Duration irrelevant.** Alarm the object | no transition | no transition |
| **Whole grant chain lost / grant revoked** | `BlockedFunding`, cause + onset, gauges cleared, indefinite, auto-redriven | complete to recorded width; at `U` if still below min, **unwind** | coast Unfunded; replacements allowed; **never terminal** |
| **Chain intact, zero local Budgets** (GitOps, helm recreate) | same (cause = `NoEnvelope`) — identity is intact, capacity is absent | same | same |
| **Chain intact, envelope exhausted or window closed** | **activates opportunistically and starts Unfunded — the existing ratified path** (`run_controller.go:1372-1381`) | same | same |
| **Within-chain double grant of one owner** | same as chain-lost; cause names the grantor(s) | same | same |
| **Two owners granted into one namespace** | same; cause names both grants | same | same |
| **Run or namespace explicitly deleted** | normal deletion cleanup — deletion of the workload, not funding loss | same | same |

**Duration appears in exactly one cell (H2 × `U`).** Cause appears in exactly one place: whether H1 *blocks* or *admits opportunistically*, and the discriminator is **"does a payer envelope of the run's flavour exist"** (`opportunisticCoverPlan`, `run_controller.go:1472-1501`), not "who caused it". That is better than A's "uniform across causes by design" (which glosses this difference) and better than Sol's split on inducibility (which P5 has now removed).

**INV.**
- **`INV-BINDING-CLOSES-NOTHING`** (transition, A:165): across any entry point in which some namespace's derived owner flips bound→unbound, the set of open leases in that namespace is unchanged, and no `ClosureReason` in the vocabulary names a funding or binding cause.
- **`INV-BLOCKED-VISIBLE`** (steady; A's `INV-UNBOUND-VISIBLE` with Sol's inertness conditions): a due reservation whose namespace derives no owner is in state `BlockedFunding` with a durable cause and onset, **and** its countdown and backlog gauge are absent, **and** it holds no lease and no pod. The anti-second-door rail: blocked is never invisible and never holds anything.
- **`INV-UNBOUND-MINT-COMPLETES`** (transition, A:166): every lease in `after` absent from `before` whose run-namespace derives `""` belongs to a run that already held ≥1 open lease in `before`, and the run's total minted width does not exceed its recorded width.
- **`INV-BELOW-MIN-BOUNDED`** (transition): a run whose runnable width has been below `minRunnableGPUs` continuously for ≥ `U` holds no open lease, no pod and no pending reservation.

---

## 3. What neither designer covered

### 3.1 The repeated-draw trap: hypothesis checked, DISCONFIRMED

**Recorded as the owner's hypothesis at `OWNER-RULINGS.md:117-123` ("if the draw re-runs every reconcile tick…"). I checked it and it is false, and the record should not carry a phantom defect forward.**

Evidence, verified independently before Ruling 4 landed: `Resolve` returns immediately on non-positive `Deficit` (`resolver.go:74-75`); its only production callers are `reclaimForAdmission` (`run_controller.go:458`, `OnlyUnfunded: true`) and reservation activation (`:1332`); the internal escalation is already `reclaimUnfunded` (`:94`) → `shrinkMalleable` (`:131`) → lottery (`:139`), matching Ruling 4's ordering; and there is no timer anywhere that reclaims. **Nothing is drawn on a clock.** Ruling 4 is therefore a ratification of existing behaviour, not a change to it.

Three real findings survive in the same code, on their own evidence:

**(a) There is no survivor memory across contention events, and the seed makes that structural.** `computeSeed` hashes `fmt.Sprintf("%s|%d", source, now.UnixNano())` (`resolver.go:611-615`). Two draws at different instants have independent seeds even with an identical `SeedSource`, and `candidateSet` is rebuilt by `gatherCandidates` each call, so `grp.Marked` is per-call. So `N` successive arriving funded claims are `N` independent draws over the same survivors. This **is** the answer to Ruling 3's burst question: **independent draws is what is built today.** One-shot-per-burst is buildable but needs a burst boundary the code does not have. The seed *is* persisted — it goes into the closure reason, `RandomPreempt(%s)` at `resolver.go:599` — so the attestation exists; what does not exist is any record that a group *survived*.

**(b) A weighting already exists, and Ruling 3 records the weighting as undecided.** Both `reclaimUnfunded` (`:316-340`) and `runLottery` (`:563-606`) draw an **owner uniformly** (`rng.Intn(len(owners))`) and then a **token uniformly within that owner**. So the de-facto rule is: *uniform across tenants, then uniform across that tenant's placement groups.* Three consequences: per-GPU it is strongly non-uniform (a 1-GPU group and a 512-GPU group are equally likely); **the unit is the placement group, not the job**, so a multi-group run's survival is a product over its groups and "per job odds" must name the unit; and the owner layer already implements the fairness Ruling 3's "proportional to the owner's share" candidate would need, and `ownerOf`'s own comment argues at length (`resolver.go:471-494`) that collapsing it is identity coarsening with a directly unfair consequence. **Whatever you choose is a ratification of, or a change to, existing behaviour — not a greenfield choice.**

**(c) Under Ruling 4, an over-allocated subtree's excess work is *funded*, so it is not first in line.** See §3.3 — this is the cost inversion.

### 3.2 P8 was already decided on 2026-07-04 — how far that reaches, and where it stops

**Proposed by the coordinator; I checked it; it substantially holds, with one correction that matters.**

Verified ratified text: `quota-semantics.md:19` is titled *"exhaustion demotes rather than kills"*; `:27-30` is demote-and-coast; `:31-34` is *"reclaimed only on demand, and unluckily… selected by the attested lottery"*; `:38-39` is *"Recovery is automatic… Nothing to resubmit, nothing to approve"*; `:108-109` is the consolidated reclaim order; `:128-129` is *"preemption of funded work remains capacity-only; budget shortfall now manifests as opportunistic classification, never as a lottery over funded runs."* **Ruling 2 is `:27`. Ruling 3's "unluckily / attested lottery" is `:31-32`. Ruling 4 is `:31` plus `:128`.** The owner's "we forgot somehow" is accurate.

And there is an authority hierarchy that settles the conflict cleanly: **`AGENTS.md:176-177` names `docs/project/quota-semantics.md` and the concept docs as binding.** `R7-tenancy-amendment.md` is a remediation design document. So where `R7:126-127` ("the reservation path fails terminally") and `quota-semantics.md:27`/`:128` conflict, **quota-semantics wins on the standing rule's own terms.** That is load-bearing for P8 and neither designer used it.

**How far it reaches — my honest reading, which is narrower than the hypothesis in one place:**

- **Clause 1 (running work): governed completely and dispositively.** No new rule needed. Both designers' running-work clauses are quotations of ratified text.
- **Clause 2 (zero-hold reservation): the ratified text establishes the *direction* but does not by itself forbid terminal.** A reservation is not a "running job", so `:27` does not literally reach it. And `:38-39`'s "nothing to resubmit" is satisfiable by terminal-plus-*automatic* re-forecast — which is what the shipped code's own comment claims it does (`run_controller.go:1200-1206`). So the decisive arguments against terminality here are the executed cross-tenant DoS, P5 closing inducibility, and the fact that the transition protects nothing — **not** "Decision 1 says so".
- **Clause 3 (below-minimum partial gang): not reached at all.** `:27`'s "it keeps its GPUs and keeps running" is factually false of an assembly that holds GPUs and runs nothing. This is the one genuine amendment.

**Therefore: neither designer quietly diverged from ratified text.** Neither proposes terminating running work — A forbids it explicitly (A:136), Sol forbids it explicitly (B:328-330). Their terminality is confined to the cell Decision 1 does not decide and the cell it does not reach. **The thing that contradicts ratified text is the code committed on the branch**, blessed by `R7:126-127`. That is the amendment (§8), and it is the option "in force by accident rather than by decision".

### 3.3 THE LOAD-BEARING GAP: Ruling 2's remedy cannot currently fire, and the cost lands on the wrong people

This is the most important finding in the document. Sol saw half of it (E:77-85); nobody saw the consequence.

**Verified: nothing aggregates descendant Budgets.** In `Evaluate`, `byName` is allocated *fresh inside the per-Budget loop* (`evaluate.go:339`) and populated only from that Budget's own envelopes; the aggregate-cap attach at `:354-373` resolves members against it and silently `continue`s on a miss (`:359-361`). `admit` (`:847-878`) checks the envelope's own concurrency, the envelope's own integral, the lending caps, and `acct.aggregates` — nothing else. There is no traversal of `FamilyGraph` anywhere in the cap machinery. API validation agrees (`budget_types.go:194-215`, `220-247`), and so do the concept docs (`docs/concepts/budgets.md:27-29`).

**Consequence, worked through.** Director reduces manager `org:a` from 120 to 100. Leads `org:a:l1` and `org:a:l2` each hold their own Budget with a 60-GPU envelope and each runs 60 GPUs. Each lead's run derives its own owner, resolves its own envelope at `TierOwner`, classes **Owned**, and fits. Total funded: **120**. The manager's envelope was never consulted. So:

- **Nothing demotes. Nothing is Unfunded. There is no deficit, no lottery, no pause.** Ruling 2's "jobs in the reduced subtree are at risk of pause" does not happen — not later, not ever.
- **Worse: the excess is *funded*, so under Ruling 4 it is NOT first in line.** It sits in the general lottery at equal standing with a compliant tenant's Owned work (`runLottery` builds tokens from every run regardless of class, `resolver.go:536-552`). **The cost of one manager's over-allocation is borne by every other tenant.** That is the exact inversion of the cost allocation Ruling 2 states.
- **The "you're over by" counter Ruling 3 asks for cannot be computed** by anything that exists.

**And Ruling 1's delegation depth exceeds the family graph's reach, so the one configuration where conservation *does* work cannot express it.** Verified at `funding.go:132-150`: `Tier` returns owner / child / sibling / cousin and otherwise `tierNone`. **There is no grandparent tier.** `Tier("org:a", "org:a:l1:r1")` is `tierNone` — a researcher cannot draw the manager's pool. `R7:230-241` says this in its own words ("there is no grandparent, uncle, or sibling-*team* reach"). So the nominal-envelope pass-through pattern (`R7:216-228`), under which an ancestor's concurrency *does* bind the subtree and Ruling 2 works exactly as written, **bounds one level only**. Ruling 1's four-level chain cannot be expressed that way without flattening `Parents` and destroying the lead as an accounting level.

**Where Ruling 3's lever does and does not work — the precise statement.** Reducing a *lead's* Budget **does** work: it shrinks the envelope the lead's own work charges, and `admit` (`:848`) demotes the excess immediately. So the manager's re-granting lever is real. What is missing is the automatic consequence when she does *not* pull it: the director's reduction does not propagate, nothing tells her how much to pass down, nothing enforces that she does, and if she does nothing, nothing happens — the excess runs, funded, at other tenants' expense.

**The predicate that would fix it, with my refinement.** Sol's shape is right (E:56-65) and its warning about sponsored consumption is load-bearing. Verified why: `accrue` keys the charged envelope off the **lease's `PaidBy*` fields** (`evaluate.go:984`) and charges *that* envelope (`:998`) — so a Borrowed lease charges the **lender**, never the borrower. A predicate that summed "all consumption by runs in P's subtree" would therefore double-charge. The resolution is one line:

> **Key subtree conservation on the paying envelope's position in the grant tree, not on the consuming Run's namespace.**
> `INV-SUBTREE-CONSERVE`: for every legitimate Budget `P`, flavour `f`, dimension `d` (instantaneous concurrency and windowed GPU-hours, separately), and instant `t`: the sum of **funded** consumption whose *paying envelope's* grant lineage traverses `P` ≤ `P`'s current incoming allocation for `(f,d)` at `t`. Each funded unit counted exactly once, at every ancestor on its payer's lineage.

That satisfies all three of Sol's requirements at once: it follows the allocation lineage; it charges each unit once (a lead's run drawing the manager's pool is paid by the manager's envelope, charged at the manager and *not* at the lead); and externally sponsored consumption never charges `P`, because the payer is the lender's envelope, whose lineage traverses the lender's ancestors.

**And note honestly what building it costs:** greedy fill stops being "against the envelope's concurrency and remaining integral" (`quota-semantics.md:71-81`, *"the normative core of this document"*) and becomes fill against a **path** of caps. That is an amendment to the normative core, and it must be written rather than slipped in. **This is the largest piece of unscheduled work in this recommendation, and it is independent of both surviving disagreements.**

`overallocated = max(0, Σ outgoing grants − incoming allocation)` remains **visible status and never illegal state** — Sol's formulation (E:67-73) is correct and follows directly from Ruling 2.

### 3.4 The shipped operator message contradicts ratified text verbatim

`run_controller.go:1216-1226` sets the run `Unfunded` with:

> `"namespace %q has no funding principal: it has no Budget, or its Budgets name more than one owner. An administrator must fix the namespace→owner binding and resubmit."`

**"and resubmit" directly contradicts `quota-semantics.md:38-39`: "Nothing to resubmit, nothing to approve."** It also contradicts the code's own comment thirty lines above, which claims recovery is autonomous (`:1200-1206`). This is simultaneously a ratified-text violation and a human-test failure: the sentence a tenant reads instructs them to do the one thing the design promises they never have to do. Neither designer caught it.

### 3.5 Loans — the brief's item 3(b), answered

**There is no loan mechanism.** `git grep -i loan` at `37270af` returns nothing; same for `iou`, `repay`, `debt`. "Loans" means sponsor lending: `LendingPolicy{Allow, To, MaxConcurrency, MaxGPUHours}` (`budget_types.go:82-90`) attached **per-envelope** (`:73`), evaluated by `lendingAllows` (`evaluate.go:1085-1117`). Five findings, none covered by either designer:

**(a) A "loan" between two researchers under the same manager is not a loan.** Verified against `Cousins` (`funding.go:111-126`): researchers under different leads of the same manager are **cousins**. `Tier` returns `TierCousin`, `classForTier` returns **`Shared`** (`funding.go:166-172`) — never `Borrowed`. So Ruling 1's "researchers can horse trade with loans if needed" is, for same-manager researchers, **free, unconsented, recallable family sharing that consults no `LendingPolicy` at all** (`quota-semantics.md:51-55`: "Within the family, no gates"). Only researchers in *different managers'* subtrees are `tierNone`, and only they need — and get — a contractual loan.

**(b) The loan the parties think they made is not the loan the ledger records.** `cover.Segment.Borrowed` is set when `ph.sponsor && acct.Owner != req.Owner` (`cover.go:177`), but `funding.ClassBorrowed` requires `tier == tierNone` (`evaluate.go:708`, `:886-888`). **Name a family member as a sponsor and cover marks the segment Borrowed while the replay classes the lease Shared** — subject to owner recall instead of the contractual carve-out.

**(c) `quota-semantics.md:56-60`'s contractual guarantee is currently false, and Ruling 1 makes it worse.** The text says borrowed capacity *"is not subject to unilateral recall"*. But the replay re-evaluates eligibility on **every fill** — `eligible := …Lending.Allow && lendingAllows(policy, OwnerOf(ns))` (`evaluate.go:797-798`) — so a lender who flips `Allow: false` or narrows `To` demotes a live loan to Unfunded with a Budget edit. Under R7's dead premise the lender was an admin recalling from themselves; under Ruling 1 **the lender is an ordinary user**, and unilateral revocation is exactly what the text forbids. Either amend the text or honour the policy revision in force at the mint.

**(d) Lending has no per-borrower fairness.** Caps are per-envelope with no borrower dimension (`budget_types.go:87,89`), and sponsors fill in admission order (`evaluate.go:780`). One greedy borrower can consume the whole lending cap. `R7:408-411` accepts the analogous intra-family unfairness explicitly; nothing accepts it for lending.

**(e) `pkg/funding` never reads the borrower's opt-in.** `AllowBorrow`, `Sponsors` and `MaxBorrowGPUs` appear only in `pkg/cover` (`cover.go:35-37,121,180,189,224`). `MaxBorrowGPUs` is enforced at admission (`cover.go:189-199`) and nowhere in the replay.

**Does the loan machinery interact correctly with a rooted grant trace and subtree conservation?** With the payer-keyed predicate of §3.3, **yes and cleanly** — a loan crosses the grant tree sideways, and keying on the payer's lineage means it charges the lender's ancestors and not the borrower's, which is what Sol demanded (E:65) and what the code already does at `evaluate.go:984,998`. With the naive spender-keyed predicate, **no** — it double-charges. Sol's warning was correct and it is now closed by a one-line specification.

### 3.6 The partial-gang deadline needs no new machinery, but must not reuse the existing field

Covered in P8 clause 3. Restated here because it is a find, not a design choice: `checkpointGrace`/`CheckpointDeadline`/`RunStateCheckpointExpired` (`run_controller.go:944-949`, `1867-1878`, `228-233`) is already "below minimum runnable width → bounded window → terminal", with the condition vocabulary already built. **But the number is tenant-declared and defaults to zero** — so as-is it offers a tenant both the immortal-gang defect and the destructive one. `U` must be cluster policy with a floor.

### 3.7 Smaller finds, each real

1. **Every alarm in every proposal here is currently a no-op.** `evaluate.go:176-182` says in its own words that nothing consumes `Conflicts()` and that `Conflicts()` has no production caller. The entire P5 detection story — R26 alarms 2, 3, 4 — is unwired. A flagged this; I am raising its priority, because both designers' fail-safes are "unbound + alarm" and half of that does not exist.
2. **`Reservation.Spec.PayingEnvelope` is still a bare envelope name** (`reservation_types.go:37`) with no budget and no namespace. `R7:372-374` specified scoping it in the pt1 sweep; it did not happen. **Both designers propose the Reservation as the durable commitment record, and it cannot currently name which Budget in which namespace pays.** If clause 3's completion-within-recorded-width is to be authorized off the Reservation, this must land first.
3. **`ReservationStatus` has no onset field** (`reservation_types.go:54-70`): `Conditions, State, Reason, ActivatedAt, ReleasedAt, CanceledAt, CountdownSeconds, Forecast`. Both A's `UnboundSince` and Sol's `blockedSince` need a status addition. And `State` is an unvalidated free string — the vocabulary is unenforced, so `BlockedFunding` costs nothing to add and nothing prevents typos either.
4. **A quota reduction's granularity is the run, not the GPU.** `fillClaim` is all-or-nothing for fixed-width claims (`evaluate.go:889-896`) and lease-by-lease only for malleable ones (`:898-905`). Shave 20 GPUs off a lead running one 60-GPU fixed job and **all 60** demote to Unfunded, not 20. This belongs in the human test and in the runbook.
5. **Recall does not cross a grant edge.** `quota-semantics.md:51-55`'s "you can never be locked out of your own budget" works by `TierOwner` outranking family tiers *on the owner's own envelope*. Where a grantee holds a real allocation, the grantor's envelope is never consulted, so there is no recall to exercise — the only lever is re-granting, and per §3.3 its automatic half is missing. Recall is strong within an envelope and absent across a grant.
6. **Decision 2's no-consent rationale is dead and its replacement is already proposed.** `R7:287-288` justifies unilateral `Parents` edges because "both ends of every family edge are written by the same accountable party". Under Ruling 1 grantor and grantee are different parties. D's resolution — `Spec.Parents` becomes derived-from or validated-against grants, and the grant *is* the consent — is correct and should be adopted with P5.

### 3.8 The window axis — what Ruling 6 depends on, and three things no ruling states

Ruling 6's rule is right and I have no argument against it. What follows is what it costs, verified, and where I think it is under-specified.

**(a) Ruling 6's "catch" is verified, and it is worse than one axis.** Ruling 6 observes that forward-only is not computable from the snapshot on the window axis, because changing `Start` is an in-place edit and nothing records what a spec field was before, or when it changed. **Verified: `BudgetStatus` (`budget_types.go:107-126`) carries `Conditions, ObservedGeneration, Headroom, AggregateHeadroom, Usage, UpdatedAt, PendingRenewals` — no prior-spec record of any kind.** `ObservedGeneration` records *that* generation N was seen, never what generation N−1 contained. And the same defect reaches the **identity** axis the moment `Spec.Owner` or a grant target is edited in place rather than delete-and-recreated — A conceded that limitation (A:85) and proposed a runbook rule against in-place edits, which is posture, not code. So P3 is load-bearing for *both* axes, not just the window.

**(b) Ruling 5's table row for `MaxGPUHours` is misleading read on its own, and Ruling 6 rescues it.** Ruling 5 files "accelerate `End` / reduce `MaxGPUHours`" together as **already ratified**. That is right for the forward consequence and **wrong for history**: accelerating `End` leaves earlier hours inside the window and touches nothing, whereas reducing `MaxGPUHours` below the already-spent total **retroactively un-charges the difference**. Verified: `accrue` clamps each charge to the remaining room (`evaluate.go:993-997`), so an envelope that burned 40 GPU-hours and is then reduced to 10 reads `ConsumedGPUHours = 10` and moves the other 30 into the Unfunded bucket. Same class of erasure as delaying `Start`, and the same magnitude. Ruling 6's "every axis" covers it; **Ruling 5's table should not be quoted without Ruling 6 beside it**, or a reader will conclude the integral axis needs no attention.

**(c) `EnvelopeKey` cannot key settled accrual under Ruling 6 — a concrete requirement on P3 that no ruling states.** Ruling 6 asserts that release-on-renewal survives because "each hour is attributed to the window in effect during it". For that to hold, settled accrual must be keyed **per window epoch**. **Verified: it is not.** `Input.PriorAccrual` is `map[EnvelopeKey]SettledAccrual` (`evaluate.go:50`), `SettledAccrual` is `{ConsumedGPUHours, HoursByClass}` (`:59-62`), and `EnvelopeKey` is `{Namespace, Budget, Envelope}` (`funding.go:174-180`) — no window discriminator. Seeded as-is (`evaluate.go:388-400`), pre-rotation hours would be carried straight into the new window's integral and **release-on-renewal would break** — the exact property Ruling 6 claims it preserves. **P3's key needs a window epoch.** This is the sort of thing that is cheap now and expensive after the store is built.

**(d) `PreActivationPolicy` already exists and already governs the pre-`Start` regime — and no designer or ruling mentions it.** `BudgetEnvelope.PreActivation` (`budget_types.go:72`) carries `{AllowReservations, AllowAdmission}` (`:77-80`). Verified readers: `cover.go:261-272` (`windowAllowsAdmission` — before `Start`, `AllowAdmission` decides, and **nil defaults to refuse**) and `forecast.go:162,261,294` (`AllowReservations`). **Zero readers in `pkg/funding`** — `windowActive` (`evaluate.go:634-642`) ignores it entirely. That asymmetry is correct and is exactly what Ruling 6 wants: pre-`Start` *work* coasts Unfunded, while pre-`Start` *fresh admission* is refused unless explicitly permitted. So **Ruling 6's forward half is already fully built**; only the backward half — spent stays spent — is missing. Worth knowing before anyone designs a new pre-window knob.

**(e) My own judgement on whether the split should have survived.** The coordinator asked whether A's identity-versus-policy split remains right once the retroactive un-funding is caused by a director rather than an attacker. **I think Ruling 6 is right and the split should go, and the reason is not that directors are less trustworthy than attackers — it is that the split conflates two questions.** "Is this policy currently in force?" is genuinely current-spec, and Ruling 6 does not touch it: work goes Unfunded the instant the window moves. "What did an elapsed hour cost?" is a question about the past, and the *policy in force during that hour* is as much a fact of the hour as the payer was. The split treated the second as policy because the ledger recomputes it, and recomputability was mistaken for revisability. That is the same error as the frozen backlog gauge in a different register: an implementation limitation was read as a semantic. **The cost is real but smaller than it looks** — release-on-renewal keeps working by per-window attribution (§P6), so what is actually being bought is P3, which was going to be needed for the deletion cells anyway. I would have argued for the split before Ruling 6 and I think I would have been wrong.

**(f) The interaction the coordinator asked about — `:38` "recovery is automatic" versus `:143-144` "window rotation stays an explicit operator action" — is not a contradiction, but it isolates the one cell where a deadline has a real argument.** Verified: `quota-semantics.md:143-144` says the controller *"deliberately does **not** auto-extend any envelope's `end` — window rotation stays an explicit operator action"*, and `BudgetStatus.PendingRenewals` (`budget_types.go:120-125`) only *notifies*, only when `spec.autoRenew` is set. These are consistent, because `:38-39` is a statement about the **tenant's** obligations ("nothing to resubmit, nothing to approve") and it names its own triggers — "new budget window, freed headroom" — one of which *is* the operator action. **But the consequence for my P8 clause 2 is sharp and I had it too smooth.** I wrote that `BlockedFunding` is "re-driven automatically when the chain or envelope returns". True — yet the two causes are not alike:
- **Blocked on identity** (grant chain lost or conflicted): repaired by someone *inside the chain*, who under Ruling 1 is an ordinary user with a direct interest and, post-P5, a conflict record naming them.
- **Blocked on a closed window**: repaired **only** by an explicit operator rotation that nothing schedules, nothing escalates, and nothing pages on unless `AutoRenew` happens to be set.

So blocked-forever is a genuine steady state whose exit is a human action nobody is assigned. **If David wants a deadline anywhere, this is the cell where the argument is strongest** — not the identity cell both designers sized `W` and `G` against. I still do not recommend a terminal transition (it destroys a promise to solve a notification problem), but the honest fix is an **escalating blocked-age alarm that does not depend on `AutoRenew`**, and residue question R6 is reframed around that rather than around a duration.

---

## 4. Unresolved disagreement, stated as disagreement

**One survives.** The exchange had two; Ruling 6 resolved the second, and I record that below rather than leaving a dead disagreement standing. I am not averaging the one that remains.

### Disagreement 1 — where the authority record lives

**Fable's strongest form.** The natural write surface for "a lead grants a researcher quota" is *the lead's own namespace*, which namespaced RBAC scopes today with no new policy machinery. Identity delegation and quota delegation are the same act, so make them the same write: `Budget.Spec.Grants` on the grantor's Budget. Legitimacy is a BFS from a configured root set — the same computational shape as `NewFamilyGraph` (`funding.go:60-73`), still a pure scan, no new CRD kind, no webhook, no controller, no consent protocol. A cluster-scoped registry has **no per-subtree write surface**, so it must either route every identity change through the root admins — contradicting "leads divide quota to researchers" as a self-service chain — or grow its own delegation ACLs, reinventing the chain it replaced. Sol conceded exactly this point: *"Cluster-scoped RBAC cannot naturally say 'this lead may create bindings only below this namespaced subtree.' The registry therefore needs its own rooted authorization mechanism. At that point it is a delegated grant registry"* (E:46).

**Sol's strongest form.** One object kind, one place to audit, and **revisions give identity history for free** — which is the only thing that makes P6's deletion blindness closable without a second history system. Owner injectivity is enforced transactionally by etcd name uniqueness rather than by a fail-safe (`metadata.name` *is* the principal ID, E:40). And it keeps the two questions structurally separate — *which principal is this namespace* versus *what quota is currently available* — so a Budget's lifecycle can never be an identity event. Sol's tightenings (E:40-44) are all real requirements: store the namespace **UID** not the name; store the parent grant/revision authority derives from; treat multiple owner-keyed bindings targeting one namespace as a fail-safe namespace conflict, because name uniqueness solves only the owner axis.

**Everything in P5 §2 works under either.** Only the object the trace reads changes. Both designers agree on that (D:57, E:112).

**What would settle it, concretely.** Not argument — two facts:
1. **Does the owner want identity history before P3?** If yes, the registry's revisions are the only mechanism on offer and Sol wins on that ground alone. If the answer is "P3's settlement store, when it lands", Fable wins, because a second history system is then bought and thrown away.
2. **Write a real `RoleBinding` for "a lead may grant a researcher".** In the grants-on-Budgets design that is a namespaced binding on `budgets` in the lead's namespace — write it and it either works or it doesn't. In the registry design, write the cluster-scoped policy that bounds a lead to its own subtree. **Whichever cannot be written without new policy machinery loses.** This is a half-hour of YAML, not a design debate, and `AGENTS.md:171-173` says to execute rather than speculate.

Also decisive if it appears: **existing owner strings are `MinLength=1` free text and forms like `org:ai` are not valid Kubernetes object names** (E:41). If a reversible canonical encoding cannot be validated from the object alone, the registry's transactional injectivity — its main technical advantage — evaporates.

### Former disagreement 2 — RESOLVED by Ruling 6, and neither designer's position was adopted wholesale

**What they argued.** Sol required immutable authority/topology epochs **now**: `Evaluate` receives a current snapshot and builds one current graph before replay (`evaluate.go:314-333`), so once an object or an old spec is gone its historical state is unrecoverable, and `CreationTimestamp` cannot encode a fact no longer present. The pinned `4/0/12` (`tenancy_r7_conflicts_test.go:270-273`) is the general shape, not an edge case. Fable refused to buy two history systems and would tolerate deletion blindness until P3, on the grounds that forward-only anchoring closes every *inducible* rewrite and the residue is loss of evidence rather than attack.

**How Ruling 6 settles it** (`OWNER-RULINGS.md:246-266`). **Sol's premise wins; Fable's mechanism wins.** History is *required* — "no one can take back quota that has been spent" is not satisfiable by a smarter scan, because an in-place spec edit leaves no trace (verified: `BudgetStatus` has `ObservedGeneration` and no prior-spec record, `budget_types.go:107-126`). But the mechanism is the one already parked, so **no parallel epoch system is bought**. Neither designer proposed this combination, and it is better than either: Sol was right that the invariant cannot be stated without durable history, and Fable was right that a second store is waste.

**What it converts.** P3 stops being a feature deferral and becomes a **correctness dependency** — `DECISIONS-NEEDED.md:22` currently classifies it as *"A feature deferral, not correctness (see `correctness-closeout-plan.md` §Out of scope)"*, and that classification is now wrong (§8, amendment 17). The disagreement is replaced by a scheduling question, which is §5/R2.

**One thing worth running anyway, because it sizes the urgency** (this survives from Sol's side of the argument and is the only part I would still spend money on before P3): **does anything actually plan against a wrong `RemainingGPUHours`?** Sponsors and aggregate caps both read it (`evaluate.go:858`, `:866`). Construct it — burn 40 GPU-hours, then delay `Start` past them or reduce `MaxGPUHours` to 10 — and check whether a sponsor's lending gate or an aggregate cap then admits width it should have refused. **If that reproduces, "spent is spent" is a live correctness defect rather than an audit-fidelity one, and P3 is urgent rather than merely required.** Compiled test, per `AGENTS.md:171-173`.

---

## 5. The owner's residue

Seven questions. Each answerable in a sentence. **I have not answered any of them.**

Two questions that were on this list are **off it**, because the owner answered them after the designers filed, and an answered question does not belong in a residue: *when a director delays an envelope's `Start` past hours already accrued and funded, do those hours stay funded?* — **answered by Ruling 6: they stay charged** (`OWNER-RULINGS.md:226-227`, and `:270-271` instructs its removal). And *may an administrator correct history?* (Sol's residual, B:189-194) — answered the same way, with Sol's own carve-out preserved: only through an explicit recorded adjustment event, never as a replay side effect.

**R1 — Where does the authority record live?** (§4 disagreement 1) Grantor-side `Budget.Spec.Grants` — one write, subtree autonomy, no new kind, blast radius is your own subtree; **or** a cluster-scoped owner-name-keyed registry — one audit surface, transactional injectivity, identity revisions, but it needs its own rooted authorization to be delegable at all. *Consequence:* everything else in P5 is invariant under this choice; only the object the trace reads changes.

**R2 — Is P3 scheduled, and when?** (Ruling 6's own open item, `OWNER-RULINGS.md:272-274`; replaces the former disagreement 2.) Schedule it now — "spent is spent" becomes enforceable and `INV-ACCRUAL-PREFIX-IMMUTABLE` becomes total; **or** defer it — the invariant is ratified with **no enforcement on the window, integral, or deletion axes**, and an in-place window edit stays destructive to history. *Consequence:* deferring means a stated invariant that nothing checks, which is the comment-as-enforcement pattern this project treats as a defect class — so if deferred it must be **written down as a known gap**, not assumed. Two requirements to fold in when it is built: the settled-accrual key needs a **window epoch** (§3.8c — `map[EnvelopeKey]SettledAccrual` at `evaluate.go:50` would break release-on-renewal), and it should cover the identity axis's in-place-edit cell too (§3.8a), not just the window's.

**R3 — `U`, the below-minimum unwind deadline** (the only number this recommendation needs). 15m covers GitOps and controller restarts; 1h covers most human repair; 24h destroys nothing inside a working day. *Consequence:* a never-runnable assembly holds its GPUs for `U`, and whoever queues behind it pays. **Must be cluster policy, not the tenant-declared `spec.runtime.checkpoint`** (§3.6), with a floor of ≥ 4× the activation interval.

**R4 — The draw's weighting** (Ruling 3 defers it; §3.1b shows a weighting already exists). Ratify today's *uniform-per-group, equalized-across-owners* draw; **or** weight proportional to width — fewer victims per deficit, systematically targets large runs; **or** weight by the owner's share of the over-allocation — fair between owners, blind to per-job cost; **or** inverse to elapsed progress — protects nearly-finished work, needs a progress signal the engine does not have. *Consequence:* whichever you pick, `INV-ODDS-PUBLISHED-MATCH-DRAWN` (§6) forces the Run status to publish exactly it. And **name the unit**: today it is the placement group, not the job.

**R5 — Within one burst of arriving demand, is a survivor re-exposed?** Independent draws — what is built today (`computeSeed` at `resolver.go:611-615`), simple, concentrates risk on nobody but exposes everyone repeatedly; **or** one-shot-per-burst — kinder, needs a burst boundary that does not exist. *Consequence:* fixes what the published odds are conditioned on. Note that **"odds of ever being descheduled" is not well defined without a demand model** — the status must say "per contention event" or it will be read as the former.

**R6 — Who is told that a `BlockedFunding` reservation is waiting on a human?** (Reframed by §3.8f — the old form of this question asked for a duration; the duration is not the problem.) This recommendation removes both `W` and `G`, because the cell they guard holds nothing and the inducibility they were sized against is closed by P5. But a reservation blocked on a **closed window** exits only by an explicit operator rotation that nothing schedules and nothing pages on unless `spec.autoRenew` happens to be set (`quota-semantics.md:143-144`; `budget_types.go:120-125`). So: an **escalating blocked-age alarm independent of `AutoRenew`** — no new terminal state, and the blocked object is reaped by ordinary deletion; **or** a terminal deadline after all — simpler to reason about, and it destroys a promise to solve a notification problem. *Consequence:* if you take neither, a blocked reservation is a silent indefinite state, which is the 2026-07-24 defect wearing its third outfit.

**R7 — Are contractual loans revocable by the lender?** (§3.5c) `quota-semantics.md:58-59` says they are not; the code re-evaluates `lendingAllows` on every fill, so they are. Amend the text to say loans are revocable at will — honest, and weakens the only non-recallable class; **or** honour the policy revision in force at the mint — keeps the promise, and needs the lease to record which revision authorized it. *Consequence:* under Ruling 1 the lender is an ordinary user, so this is now a tenant-facing guarantee rather than an admin self-consistency question.

---

## 6. How it would be proven

For each rule: the invariant, the test, and **the bounded adversarial question a future panel is asked instead of hunting freely.** The oscillation came from panels falsifying unspecified behaviour; free hunting cannot terminate, and these questions can.

| Rule | Invariant | Test | The question to ask a future panel |
|---|---|---|---|
| **P5** rooted trace | `INV-OWNER-TRACES-TO-ROOT`, `INV-GRANT-LOCAL` | Squatter fixture: an unrooted Budget naming the victim's owner in any namespace ⟹ zero change to `OwnerOf(victim)`, `Conflicts()`, and the victim's loss onset. Mutation-verify by deleting the rootedness check — the test must go red. | *Is there a funding-object mutation outside `chain(N)` that changes `OwnerOf(N)`, N's conflict set, or N's loss onset? Is there a state where an unrooted Budget affects anything at all?* |
| **P5** injectivity | `INV-OWNER-INJECTIVE`, `INV-OWNED-IS-LOCAL` | `TestInteriorExemptionAdmitsALeafOwnerInTwoNamespaces` (`tenancy_r7_conflicts_test.go:130`) flips from pin to regression test. Plus: no configuration produces an open Owned lease with `PaidByBudgetNamespace ≠ RunRef.Namespace`. | *Is there a legal configuration in which an Owned lease's payer namespace differs from its run's namespace?* |
| **P6/P7** prefix immutability, **owner axis** | `INV-ACCRUAL-PREFIX-IMMUTABLE` | Differential property test against the real `Evaluate`, generator emitting **non-zero** effective times (§P6), five fixed seeds for the reproduced shapes, mutation-verified against the anchoring line. `TestConflictRetroactivelyErasesAccruedHours` (`:170`) becomes an assertion of the new semantics. | *Is there a mutation with an effective time that changes an hour tuple already attributed to an interval ending before it — other than a recorded adjustment event?* |
| **P6** prefix immutability, **window and integral axes** (Ruling 6) | same invariant, now total over spec edits | Three fixtures, each asserting **the charge survives**: burn 40 GPU-hours then (a) delay `Start` past all of them — assert `ConsumedGPUHours` stays 40, **today it reads 0**; (b) reduce `MaxGPUHours` to 10 — assert 40, **today it reads 10** (`evaluate.go:993-997`); (c) rotate the window forward — assert old hours stay charged to the old window **and** the new window's integral is full, which is the release-on-renewal regression guard. All three fail today, deliberately, and (c) is the one that catches a P3 key without a window epoch (§3.8c). | *Is there an in-place spec edit that changes what an elapsed hour cost? Does the settled-accrual key distinguish windows — and if not, does a rotation carry old hours into the new integral?* |
| **P8** clause 1 | `INV-BINDING-CLOSES-NOTHING` | Across every entry point where a namespace flips bound→unbound, the open-lease set is unchanged and no `ClosureReason` names a funding cause. | *Is there a path by which loss of a funding principal closes a lease, kills a pod, or terminates a run at or above `minRunnableGPUs`?* |
| **P8** clause 2 | `INV-BLOCKED-VISIBLE` | Every due reservation exits every tick in exactly one of `{activated, released, blocked-with-cause-onset-and-cleared-gauges, unwound}`. Assert **absence** of the countdown and backlog gauges in the blocked state — that is what the 2026-07-24 defect was. | *Is there a tick on which a due reservation leaves in none of those four states, or is blocked while holding a lease, a pod, or a live gauge?* |
| **P8** clause 3 | `INV-BELOW-MIN-BOUNDED`, `INV-UNBOUND-MINT-COMPLETES` | Below-minimum gang, unbound: completion mints allowed to recorded width; at `U` leases closed, pods deleted, reservation released. Mutation-verify by deleting the unwind — assert the run **re-emits its intent gang**, which permanent limbo does not (the observable `37270af` had to be taught). | *Is there a state where a run below `minRunnableGPUs` holds an open lease for longer than `U`? Can minting exceed the recorded width?* |
| **Ruling 2** conservation | `INV-SUBTREE-CONSERVE` (§3.3) — **currently FALSE** | Fixture: manager 100, two leads each granted and running 60. Assert the excess classes Unfunded and the over-allocation counter reads 20. **Today this test fails, and that is the point of writing it first.** | *Is there a funded unit whose payer's grant lineage traverses `P` and which is not counted at `P`? Is any unit counted twice? Is sponsored consumption charged to the borrower's ancestors?* |
| **Ruling 3** odds | `INV-ODDS-PUBLISHED-MATCH-DRAWN` | For a constructed deficit, the odds published on each Run equal the odds the draw computes, over the same unit, at the same conditioning. Mutation-verify by perturbing the weighting on one side only. | *Is there a state where a published probability differs from the one the draw uses, or where the published conditioning ("per contention event") does not match the code?* |

**A standing instruction for future panels, which is the point of this section:** ask those questions. A finding of the form *"but terminating destroys X"* is now an **answered** question and should be returned as such with a citation to this document, not re-litigated. `AGENTS.md:171-173` still governs: a compiled reproduction beats an argument, in both directions.

---

## 7. The human test

### P5 — at 3am
The operator sees a `BindingConflict` naming a namespace, a reason, and — this is the change — **the grant and the namespace that produced it**. If the reason is "unrooted Budget", the alarm names an object that has **no effect on anything**, so the operator's correct action is to delete it at leisure, not to page anyone.
**To a tenant:** *"Nothing happened to you. Someone created a Budget claiming your owner name; it isn't connected to any real grant, so it was ignored."*
**Prerequisite:** none of this is visible today. `Conflicts()` has no consumer (`evaluate.go:176-182`). Wiring R26 is not optional garnish — it is half the rule.

### P6 — at 3am
An hour's bill never changes after the fact, **whatever the director moved**. When an operator fixes a mis-binding, or a director shrinks a count, moves a window, or cuts an hours cap, envelope balances move **forward from the edit**, not backward through the week. The single sentence an operator needs: *spent is spent; everything else is prospective.*
**To a tenant:** *"Your 32 hours are still your 32 hours. The change affects what happens next."*
**To a tenant whose director just delayed their window:** *"Your quota window now opens on Thursday. The 32 hours you already used are still charged and still yours — nobody is getting them back. From now until Thursday your job is unfunded: it keeps running, it isn't billed, and it's first in line if someone funded needs the GPUs. Thursday it re-funds by itself."* That is Ruling 6 read aloud, and it is the whole rule.
Today the opposite is true on every axis and the owner axis is pinned: 32 consumed becomes 0, and 32 already-spent hours are handed back as headroom (`tenancy_r7_conflicts_test.go:209-223`). Delaying `Start` produces the identical number by a legal action (§3.8), and reducing `MaxGPUHours` produces it partially. **A tenant cannot currently be told the sentence above truthfully, and the reason is P3.**

### P7 — at 3am
Nothing. That is the point. A gate that fires in CI, before the operator's night.

### P8 — at 3am
One rule, held in one sentence: **the only thing that ever gets torn down is a gang that holds GPUs and cannot run.** The operator sees:
- a **`BlockedFunding`** reservation with a cause naming the binding or the missing envelope, an **onset timestamp**, **no countdown**, **no backlog gauge**, and a blocked-age signal — so "how long has this been stuck" is answerable at a glance, which is exactly what was missing when the gauge froze at 1020;
- running work in **`Unfunded`** with an hours counter, unchanged and unbilled;
- a below-minimum gang with an **unwind deadline**, and after it, a requeue.

**To a tenant whose work was paused — and this is Ruling 4's gift, and it holds up:** *"Your job was paused because Run `<X>` in `<namespace>`, which is fully funded, needed 8 H100s, and your subtree is 20 GPUs over its allocation. Nothing was destroyed. It resumes when capacity frees or when your lead's grant is restored."* Every descheduling is attributable to a **named arriving claim** (`resolver.go:74-75` — no deficit, no action). "The system decided to shrink you" is never the answer.

Two honesty requirements this section imposes:
1. **The message must never say "resubmit".** It does today (`run_controller.go:1216-1226`, §3.4), and that single word contradicts `quota-semantics.md:38-39` and the code's own comment.
2. **Ruling 3's per-job odds must be published with their conditioning, and must be true.** `RunFundingStatus` (`run_types.go:308-318`) carries per-class GPUs and hours and a lender list — no risk surface at all. It needs, and `INV-ODDS-PUBLISHED-MATCH-DRAWN` must enforce:
   - **`overAllocatedBy`** on the Budget: how far the subtree is over, per flavour (the "you're over by" counter — **and it cannot be computed until §3.3 is built**);
   - **`atRiskGPUs`** on the Run: how much of its width is Unfunded and therefore first cut;
   - **`descheduleOdds`** on the Run, with the unit and the conditioning **written into the field's own words**: *"probability this group is drawn per contention event"* — **not** "odds of being descheduled", which is undefined without a demand model. For a multi-group run, publish per group and the run-level product, or say which one it is;
   - **`lastDraw`**: the seed and the named arriving claim, so the tenant's sentence above can be generated rather than composed by a human at 3am. The seed is already persisted in the closure reason (`resolver.go:599`).

   **Publishing a probability the draw does not use is the frozen-gauge defect in a new place, and this time it would be a number a researcher plans their week around.** If §3.3 is not built, `overAllocatedBy` must be **absent**, not zero — an absent field is honest, a zero is a lie.

---

## 8. Ratified text to amend

`AGENTS.md:176-177`: *"Decisions recorded in `docs/project/quota-semantics.md` and the concept docs are binding. Disagree in writing rather than diverging in code."* This section is that writing. **Note the hierarchy the rule itself establishes:** it names `quota-semantics.md` and the concept docs. `R7-tenancy-amendment.md` is a remediation design doc, so where the two conflict, quota-semantics wins.

**In `docs/project/remediation/R7-tenancy-amendment.md`:**

1. **`R7:126-127`** — *"the reservation path fails terminally (`opportunisticCoverPlan` finds no payer envelope → `failReservationNoEnvelope`…)"*. **Struck.** Becomes: enters `BlockedFunding` — inert, visible, indefinite, auto-redriven. **This is the sentence the shipped code implements, and removing it is the substance of P8.** It also conflicts with `quota-semantics.md:38-39`, which outranks it.
2. **`R7:588-592` (C-2)** — *"pre-existing leases reclassify Unfunded and coast"*. **Amended** with a carve-out: an assembly that has never reached `minRunnableGPUs` is unwound at `U`. Jointly proposed by both designers (C:101, D:51).
3. **`R7:165-166` and `R7:609-610` (§4, C-4)** — the interior-tier injectivity exemption and *"Interior tiers may span admin namespaces"*. **Struck.** Its stated premise was falsified by execution. `TestInteriorExemptionAdmitsALeafOwnerInTwoNamespaces` (`tenancy_r7_conflicts_test.go:130`) flips from pin to regression test.
4. **`R7:116-138` (§4 derivation)** — `ownerOf(namespace)` becomes the rooted grant trace. `Spec.Owner` survives as the family-axis *name* and loses self-naming authority; `Spec.Parents` becomes derived-from or validated-against grants.
5. **`R7:291-296` (§6a)** — *"no principal may write Budgets (or Leases)"*. **Dead by Ruling 1.** Replaced: every principal may write Budgets within its own subtree; containment is the trace, not the absence of write access. This sentence was destined for R18's runbook verbatim; R18 must be rewritten around delegation instead.
6. **`R7:307-312` (§6c)** — *"That is total quota compromise, not an incremental leak."* **Dead.** A hostile writer at any level must be contained to what they were granted: a containment requirement, not a trust boundary. Withdrawn by A in all three places it was used (D:13).
7. **`R7:287-288` (§6)** — *"both ends of every family edge are written by the same accountable party, so consent is meaningless"*. **Dead by Ruling 1.** The grant is the consent, asserted in the only direction that authenticates.
8. **`R7:445-449` (§10) and `R7:539-541` (§13.2)** — *"no new admission policy, no webhook, no consent protocol, no CRD guard"*, and vetoable default 2. **Void either way.** The registry reverses it outright; grants-on-Budgets still adds a `BudgetSpec` field. Batch the schema change into the lease-schema outage `R7:473-475` already schedules. §10's two-M-PR sizing does not survive.
9. **`R7:372-374` (§7)** — not a contradiction but an **unfinished item this recommendation depends on**: `Reservation.Spec.PayingEnvelope` is still a bare envelope name at `reservation_types.go:37`. Scope it before anything authorizes minting off the Reservation.

**In `docs/project/quota-semantics.md` (binding under `AGENTS.md:176`):**

10. **`:64-69` (Decision 3)** — the input tuple `(budgets, leases, clock)` gains the **grant/authority record**. Required by P5 under *either* mechanism. Sol's amendment (B:177).
11. **`:56-60` (Decision 2, lending)** — *"Borrowed capacity is contractual… it is not subject to unilateral recall."* **Currently false** (§3.5c): the replay re-evaluates `lendingAllows` every fill (`evaluate.go:797-798`). Either amend the promise or honour the mint-time revision. **Neither designer raised this; it is residue question R7.**
12. **`:71-81` (the ranking function — labelled *"the normative core of this document"*)** — if `INV-SUBTREE-CONSERVE` is built, greedy fill is no longer against *an envelope's* concurrency and integral but against a **path** of caps. **This must be written as an amendment to the normative core, not slipped in as an implementation detail.**
13. **`:41-44` and the `ConsumedGPUHours` doc-comment doctrine at `evaluate.go:104-108`** — *"History is evaluated under the current spec: moving the window forward (renewal) releases hours spent in the old window"*. **Struck, not scoped** (this is the change Ruling 6 makes to what I would otherwise have recommended, per §1e). Replaced: each hour is attributed to the binding, topology, concurrency and window in force **during** that hour. Release-on-renewal is preserved by that attribution rather than by clamping to the current window — the observable is unchanged for an ordinary rotation, and what changes is that moving a window can no longer alter what an earlier hour cost. `:41-44`'s "budget-window expiry no longer implies death" stands untouched; only the mechanism beneath it moves.

**Not ratified text, but stale and load-bearing:**

14. **`DECISIONS-NEEDED.md` P5** frames the choice as *"Narrow the exemption, or keep it and rely on RBAC + R26 alarm 3?"* Both legs of the second option are gone: RBAC-as-containment died with Ruling 1, and R26 alarm 3 is unwired (`evaluate.go:176-182`). Rewrite the entry.
15. **P8 is not in `DECISIONS-NEEDED.md` at all** — it exists only in issue #132 comments and in `BRIEF.md`. Writing it up is outstanding work (`CHECKPOINT.md:38`).
16. **`docs/concepts/budgets.md:70-79` states the wrong proximity order** (`owner → siblings → parents`). The code is `owner → parents → siblings → cousins` (`cover.go:105-120`), which `quota-semantics.md:48-50` states correctly. Concept docs are binding under `AGENTS.md:176`, so this is a binding doc that is simply wrong; fix it rather than amending anything.
17. **`DECISIONS-NEEDED.md:22` classifies P3 as *"A feature deferral, not correctness (see `correctness-closeout-plan.md` §Out of scope)"*.** **Now wrong.** Under Ruling 6 the settlement store is the mechanism that makes "spent is spent" enforceable on the window, integral, and deletion axes — nothing else can, because an in-place spec edit leaves no trace (§3.8a). Reclassify P3 as a correctness dependency of P6, and correct `correctness-closeout-plan.md`'s §Out of scope in the same pass. This is the amendment with the largest schedule consequence in this document.

---

## Where I am uncertain, plainly

1. **Nothing in this document was executed by me.** Every citation was verified by reading the code at `37270af`; no claim here was run. The three findings I would most want compiled before you act: (a) the over-allocation fixture in §3.3 — two leads at 60 under a manager at 100 — because my entire load-bearing gap rests on tracing `admit` rather than running it; (b) the wrong-`RemainingGPUHours` reproduction in §4 (former disagreement 2) and the three window-axis fixtures in §6, which together decide whether P3 is urgent or merely required; (c) whether `BlockedFunding` is genuinely inert given that the activation loop filters on `Pending` (`run_controller.go:1066-1068`) — I traced one loop and there may be others.
2. **`INV-SUBTREE-CONSERVE` is a shape, not a specification.** I am confident about the keying (payer's grant lineage, not spender's namespace) because that is what makes the loan case and the double-count case both fall out. I am **not** confident about the windowed-hours dimension: an ancestor's GPU-hour cap and a descendant's are integrals over possibly different windows, and I have not worked out what "traverses `P`" means when `P`'s window opened after the descendant's consumption began. That is real design work, and it may reveal that the invariant needs a window-alignment precondition.
3. **The trace's interaction with settlement compaction is unchecked.** `SettleAccrual` calls `Evaluate` recursively and `settlementSafe` (`evaluate.go:388,479`) gates compaction; a grant-derived binding should inherit whatever anchoring the binding has, but the no-straddle proof in `specs/LedgerCompaction.tla` should be re-checked against it. A flagged this (A:190) and I have not closed it either.
4. **I may be wrong that `W` buys nothing.** My argument is that the zero-hold cell holds nothing and P5 closes inducibility. If there is a resource I have not enumerated that a blocked reservation consumes — a queue position, a forecast slot, an admission-order rank — then Sol's caution about stale queue privilege (E:102) becomes a reason for a deadline after all, and residue question R6 changes character. I traced the reservation's holdings and found none, but "I found none" is weaker than "there are none".
5. **Ruling 6's cost may be larger than I have priced it.** I assert that release-on-renewal survives per-window attribution and that the only new machinery is P3 with a window-epoch key (§3.8c). What I have *not* worked out is whether `nextDepletion` (`evaluate.go:929-970`) and the segment-splitting loop (`:423-431`) stay correct when an envelope's integral is per-window rather than per-envelope: the depletion crossing is computed from `RemainingGPUHours()` against the *current* spec, and if remaining becomes window-relative then a replay that straddles a rotation has two integrals and one loop. That is where I would expect the implementation to find a problem I have not.
6. **Where I differ from both designers, labelled:** the `W`/`G` removal (§1c, P8); the payer-keyed conservation predicate (§3.3); the zero-timestamp defect in A's invariant (§P6); the three-layer P7 gate with rails first (§P7); the discriminator for P8's one non-uniform cell being "does a payer envelope exist" rather than cause or inducibility; the finding that Ruling 1's four-level chain exceeds `Tier`'s reach (§3.3); and, on the window axis, the `MaxGPUHours` erasure that Ruling 5's table obscures, the `EnvelopeKey` settled-accrual keying defect, the unread `PreActivationPolicy`, and the blocked-on-a-closed-window cell that has no assigned repairer (§3.8b–f). Each of these is mine and each should be attacked on its own evidence rather than credited to the exchange.
7. **Where I differ from a ruling, labelled:** nowhere. I checked Ruling 6 against the mechanism and it is right, including the part that overturns a position I had adopted (§1e). The two places I add to a ruling rather than dispute it are §3.8b (Ruling 5's table row for `MaxGPUHours` is misleading in isolation) and §3.8c (Ruling 6's release-on-renewal claim needs a key change nobody has stated).
