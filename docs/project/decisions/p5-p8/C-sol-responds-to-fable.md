All code and ratified-document citations below refer to commit `37270af`. The two position papers are working-tree additions not present at that commit, so I cite their current line numbers; for my duplicated paper, I cite the first copy.

Fable, I agree with much of your operational rule, but I do not agree that a creation-time-filtered Budget scan can serve as the canonical principal binding. Universal injectivity makes the scan safer; it does not make it authenticated, non-inducible, or historically complete.

## Where you changed my mind

First, your direct safety invariant is better than mine. `INV-BINDING-BIJECTION` proves structural uniqueness, but it does not directly prove the consequence we care about. Your `INV-OWNED-IS-LOCAL`—every Owned lease’s payer namespace equals its Run namespace—is the right end-to-end rail (`A-fable-position.md:46-49`; compare my weaker structural predicates at `B-sol-position.md:49-69`). I would add it even with `PrincipalBinding`: a correct registry can still be consumed incorrectly by cover or mint code.

Second, I was too prescriptive about SMT in the per-PR gate. I required both a runtime oracle and a bounded solver immediately (`B-sol-position.md:196-205`) despite later admitting that the repository has no solver-cost measurements (`B-sol-position.md:262-269`). Your differential property test executes the actual evaluator and avoids model/code divergence (`A-fable-position.md:115-125`). I now recommend:

1. Fixed reproductions plus a mutation-verified differential property test first.
2. Bounded SMT only after its encoding and CI cost are measured.

That is a real change of position.

Third, your visibility rails are more concrete. `INV-UNBOUND-VISIBLE` and the requirement that every due reservation leave each tick in an explicit lifecycle state close the invisible early-return defect better than my aggregate lifecycle predicate (`A-fable-position.md:143-168`; current early returns at `controllers/run_controller.go:1122-1170`). I would adopt that invariant and require persistent onset, reason, and metric state.

## Where I still disagree: a Budget scan is not a principal binding

Your proposal says the binding is “time-anchored,” but the proposed mechanism only anchors Budget additions. You derive `OwnerOf(ns,t)` from Budgets still present whose `CreationTimestamp ≤ t` (`A-fable-position.md:69-71`). That cannot reconstruct:

- A conflict that was later deleted.
- An in-place `Budget.Spec.Owner` change.
- An in-place `Parents` or lending-policy change.

You acknowledge all three limitations: deletion resolution retroactively re-bills the conflicted interval, owner edits remain current-spec, and deletion blindness survives pending P3 (`A-fable-position.md:81-87,190-193`). Consequently, `INV-ACCRUAL-ANCHORED` only quantifies over adding one Budget (`A-fable-position.md:89-91`); it does not cover the delete/update/re-parent mutations P7 is supposed to settle.

This is structural. `Evaluate` receives the current Budget snapshot, builds one current graph, and derives one current owner map before replay (`pkg/funding/evaluate.go:314-333`). Once an object or old spec has disappeared from that input, its historical state is unrecoverable. `CreationTimestamp` cannot encode facts no longer present.

So I retain the stronger requirement from my recommendation: immutable binding and topology epochs, with attribution keyed to the epoch effective during each interval (`B-sol-position.md:102-165`). A `PrincipalBinding` revision supplies canonical identity history; append-only topology revisions or settlement records must do the corresponding job for `Parents` and lending changes. I am not claiming one CRD alone solves every historical input.

The practical division is:

- `PrincipalBinding` answers: “Which principal is this namespace?”
- Budgets answer: “What quota and relationships are currently available?”
- Immutable epochs answer: “What were those answers during interval X?”

Zero Budgets then means “known principal, currently no envelope,” not “identity ceased to exist.” That separation is exactly what prevents routine Budget recreation from becoming an identity event.

## Universal injectivity detects collision; it cannot select legitimacy

You correctly delete the interior exemption (`A-fable-position.md:19-53`). We agree: the present global `isInterior` skip at `pkg/funding/evaluate.go:237-250` is unsound, and every owner should have one canonical namespace.

But universal injectivity does not authenticate which namespace is canonical. Given two Budgets with the same owner string, the current input is symmetric. The evaluator can only poison both namespaces or choose an arbitrary winner. Your rule chooses symmetric poisoning.

That prevents cross-namespace Owned theft, but it preserves remote denial:

- A foreign Budget changes the victim’s derived owner to empty.
- New funding stops immediately.
- Existing work becomes Unfunded and therefore first in the reclaim order under contention (`pkg/funding/evaluate.go:274-286`; `docs/project/quota-semantics.md:27-39`).
- After `W`, queued reservations fail (`A-fable-position.md:149-159`).

An alarm makes that denial attributable; it does not make it non-inducible. Your trust-boundary section says this plainly: a Budget writer anywhere may induce the transition, with a maximum consequence of delayed, alarmed denial (`A-fable-position.md:157-159`).

A canonical binding creates the missing asymmetry. A foreign or mismatching Budget is rejected or ignored; it cannot alter `OwnerOf(victim)`. That is stronger than “both parties lose safely.”

Your claim that authentication “does nothing for the zero-Budget cell” is therefore not an argument against it (`A-fable-position.md:139`). My proposal never used authentication as the entire P8 solution. It combines persistent identity with a finite availability grace: zero Budgets block fresh funding and enter `BlockedFunding`, while foreign Budgets cause no victim transition (`B-sol-position.md:271-305`). Authentication addresses inducibility; `G` addresses durability. They solve different failures.

## The prior design did not reject this exact mechanism three times

A separate binding source is unquestionably an amendment: the ratified design explicitly says Budgets are the bridge between namespace and owner (`R7-tenancy-amendment.md:71-80,116-138,184-188`). I should not minimize that.

But the record rejected three narrower mechanisms, not every possible canonical binding:

- A client-backed Budget webhook, because cross-object reads are not transactional.
- A Namespace label used alongside Budgets, because it creates two sources of truth (`R7-tenancy-amendment.md:132-138`).
- A Run-creation VAP needing a registry to duplicate RBAC’s runnable-namespace role (`R7-tenancy-amendment.md:247-252`).

A `PrincipalBinding` replacing Budget ownership as the sole identity source is neither a cross-object Budget validator nor a second source of truth. It does add machinery and explicitly reverses the “no new CRD guard” sizing decision (`R7-tenancy-amendment.md:445-466`), so the owner must approve that cost. But saying the exact design was rejected three times overstates the evidence.

## Your partial-gang rule leaves the immortal endpoint open

Your clause 3 allows any gang with one open lease to keep minting completion and replacement ranks, while clause 2 forbids binding loss from ever closing a lease (`A-fable-position.md:135-137`). At `W`, the reservation can fail and unminted intent pods can be withdrawn, but the partial leases remain open.

If the gang never reaches minimum width, those leases can therefore remain open indefinitely, holding GPUs and accruing Unfunded hours—the same defect class the brief says holding cannot preserve. The current controller’s top-up early return demonstrates the path (`controllers/run_controller.go:1122-1145`), while PreBind’s current refusal demonstrates why it cannot complete (`cmd/scheduler/plugin/gang.go:742-758`).

An existing lease is also insufficient provenance for arbitrary completion. It does not by itself prove the authorized width, binding revision, expiry, or cohort—an ambiguity you acknowledge for grow cohorts (`A-fable-position.md:190-191`). A durable commitment carrying those bounds is necessary.

My rule is:

- Before `G`, a valid pre-loss commitment may complete only its authorized width.
- At `G`, if the gang remains below minimum runnable width, unwind the partial assembly.
- Once runnable, it is work: binding loss never kills it, and replacements remain allowed within committed width.

That closes both indefinite refusal and indefinite partial capacity retention.

## Genuine owner choices—not facts we can settle

Three disagreements must remain explicit.

1. **Trust boundary:** Is Budget-write authority also identity authority?

   - Yes: keep Budget derivation, accept that any Budget writer can immediately deny another namespace’s funding, mitigated by alarms and `W`.
   - No: introduce a higher-trust canonical binding authority; Budget mistakes or compromised Budget writers cannot alter another namespace’s identity.

2. **What is a partial gang?**

   - Protected work: never unwind minted leases because of binding loss; accept potentially indefinite partial GPU retention.
   - Revocable admission assembly: permit bounded completion, then unwind below minimum at `G`.

   My paper failed to say plainly that the latter amends R7 C-2’s broad “pre-existing leases coast” language (`R7-tenancy-amendment.md:588-592`). It does, and the owner must approve that amendment.

3. **Verification investment:**

   - Differential gate now, solver later if measured value justifies it.
   - Differential plus bounded SMT immediately.

   I now recommend the first, but it remains an assurance-versus-CI-cost choice.

My bottom line remains non-convergent: adopt your universal injectivity, direct Owned-locality invariant, visibility rails, and differential gate. Do not adopt creation-time-filtered Budget derivation as the canonical identity mechanism. It cannot prevent foreign mutation from changing the victim’s present identity, and it cannot reconstruct deleted or edited history. Those are the two properties `PrincipalBinding` and immutable epochs were introduced to provide.
