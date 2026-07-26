# Design brief — P5, P6, P7, P8 (jobtree funding/tenancy semantics)

You are one of two independent designers. Your job is a RECOMMENDATION, not an implementation.

## Why you are being asked

Four owner decisions are parked. Attempts to implement around them have oscillated: a fix is
written, an adversarial panel proves it defective, the opposite fix is written, the panel proves
THAT defective. Both endpoints of P8 are now proven defective by executed reproductions. That is
the signature of a MISSING SPECIFICATION, not of bad engineering — an adversarial panel is a
falsifier and can always falsify an unspecified behaviour. The oscillation ends only when the
semantics are decided and written down.

## What a good answer looks like (both properties are required)

1. IT MUST MAKE SENSE TO A HUMAN OPERATOR. A rule that is technically defensible but astonishing
   in the field is wrong. If an SRE at 3am cannot predict what the system will do to their
   reservations, or cannot explain to a tenant why their work died, the rule fails.
2. IT MUST BE PROVABLE. Specifically it must be:
   - COMPLETE: every state has a defined outcome. Enumerate (cause of principal loss) x (what the
     run currently holds) x (how long the condition has persisted). No cell may be undefined.
   - CONSISTENT: it must not contradict already-ratified text. Those decisions are BINDING (see
     AGENTS.md: "Decisions recorded in docs/project/quota-semantics.md and the concept docs are
     binding. Disagree in writing rather than diverging in code."). If you believe a ratified
     decision is wrong, say so explicitly as a proposed amendment — do not quietly contradict it.
   - ENCODABLE as an executable invariant in pkg/invariant (alongside INV-CLOSED-MONOTONE etc.),
     so no implementation can drift from it silently. If you cannot state it as an invariant, it
     is not yet decided. Give the INV- name and its predicate.
   - NON-INDUCIBLE where safety demands: name the trust boundary, and state which principals may
     cause which transitions.

## Sources — READ THESE FIRST, do not work from this brief alone

Everything is in the git repo at the current directory. The parked decisions P5, P6 and P7 are
written up in full (with reproductions and citations) at commit 37270af:

    git show 37270af:docs/project/DECISIONS-NEEDED.md

Binding / ratified text you must not contradict without saying so:

    docs/project/quota-semantics.md          (binding)
    docs/project/remediation/R7-tenancy-amendment.md   (the R7 pt2 design, esp. §4/§5, C-2, C-4)
    AGENTS.md                                 (standing rules)

The code under discussion:

    pkg/funding/evaluate.go                   (OwnerOf, deriveOwners, the replay, tiering)
    controllers/run_controller.go             (activateReservation, failReservationNoEnvelope)
    cmd/scheduler/plugin/gang.go              (promiseProvenanceValid, PreBind mint)
    pkg/invariant/                            (the existing executable invariants)

## P8 — not yet in the repo, so it is reproduced verbatim below

This is the newest decision and the one that triggered this exercise. It was produced by an
adversarial panel in which every claim below was EXECUTED, not argued.

🚨 **The fix-diff panel found that my terminal fix introduced a cross-tenant denial of service. This is the most important thing on this issue.**

Every one of these was EXECUTED, not argued. 16 findings across 4 lenses so far; these are raised, not yet adjudicated, but I am not going to spin them.

**HIGH — `evaluate.go:239`: any tenant can now terminally destroy another namespace's reservations.**
> Namespace `default` is correctly bound to `org:team`. A Budget named `squatter` is created in namespace `attacker` whose only distinguishing feature is `owner: org:team`. **Nothing in `default` changes.** `OwnerOf("default")` flips `org:team` → `""`, and one `ActivateReservations` tick marks `default/res` **Failed**. The same foreign Budget makes `promiseProvenanceValid` refuse `default`'s own top-up naming `default`'s own budget and its own envelope.

`Budget.Spec.Owner` is a free-form string the victim does not control. Before my fix that condition stalled; **my fix made it destructive.** The irony is exact: R7 pt2 exists because `Run.Spec.Owner` was forgeable, and I turned `Budget.Spec.Owner` into a weapon closing it.

**HIGH — the same terminal path fires on routine GitOps.** A namespace with zero Budgets and one due reservation → **one tick** → `Failed`. In production that window is any `kubectl delete && apply`, `replace --force`, a helm upgrade that recreates Budgets, or a GitOps sync that prunes before applying. Resync is one minute; a gang scheduler runs for months.

**Where that leaves the previous ruling, stated precisely:** the 2026-07-25 seat proved `fixIsReaper=false` for an *admin typo*. I generalised that to "terminating is safe" and it is not — it is safe for the scenario tested and unsafe for two the panel did not test. That is my error, not the seat's.

**HIGH — and it does not even fix the original defect in general.** The new guard sits behind two earlier returns, so when the run already holds leases the immortal reservation survives: 20 ticks → `Pending`, countdown still set, gauge still `{H100-80GB 1020}`, lease still open. A second door is *worse* — reservation Pending forever with the gauge **cleared**, so it is invisible rather than merely frozen.

**CRITICAL — `gang.go:756`: the refusal I KEPT strands partial gangs.** 4-GPU Promise gang, 2 ranks minted, admin adds a second Budget → 20 hours → leases still open, `Unfunded: 40.03 GPU-hours` and climbing, pods re-emitted forever. A live gang losing a rank to node failure cannot replace it. That is the refusal the earlier reproduce seat *confirmed as a genuine regression fix* — and it reaps through a different field than the pin the veto named.

**MEDIUM — my recovery test is decorative.** It passes with the repair deleted. I mutation-verified the terminal behaviour and never mutated the recovery test; the lens did, and the mutant survives. The playbook's exact warning, in a test I wrote to answer a panel.

**MEDIUM — my "better operator message" claim is false**: the sibling call site still surfaces `owner and flavor must be set`, and the message I install is overwritten one Reconcile later.

I am **not** re-fixing under a live panel — the branch stays frozen and Judge is bounded to exactly these files. But the shape is already clear: **holding is wrong (immortal reservation) and terminating is wrong (adversarially inducible destruction)**. The gap between them — how durable must the condition be, and may a party the victim does not control induce it — is a tenancy policy question, and per the park list that is David's, not mine to guess.
🅿️ **parked: whether losing a funding principal may DESTROY a namespace's reservations, and whether a party the victim does not control may induce it — because both endpoints are now proven defective and picking between them is a tenancy policy call, not an implementation detail.**

Holding is wrong: the 2026-07-24 panel confirmed the immortal reservation, Pending 20+ ticks with the gauge frozen, where `main` terminated at tick 1.
Terminating is wrong: the fix-diff panel reproduced a cross-tenant DoS (a foreign Budget naming your owner destroys your reservation in one tick) and destruction on routine GitOps windows.

The candidate rules all involve a number or a semantic only you can set — a grace window before terminal (an owner number, like R4 pt1b's staleness bound); terminating only for `zero Budgets` and never for the two *inducible* conflict reasons; authenticating `Budget.Spec.Owner` first and then terminating freely; or keeping terminal and accepting the DoS as gated by Budget-create RBAC.

**The part I want to be blunt about: the unsafe option is live on the branch right now.** Terminal is what is committed, so option (4) is in force by accident rather than by decision. I am not reverting it under a live panel — reverting reinstates a confirmed HIGH — and I am not choosing between them, because that is the park-list line. Writing it up as **P8** with both reproductions in the archive PR.

And the correction that matters for how the earlier ruling gets read: `fixIsReaper=false` was established for an **admin typo**. It does not cover a hostile Budget or a delete-then-apply window. Generalising it to "terminating is safe" was mine.

## Deliver

A recommendation covering P5, P6, P7 and P8. For each:
  - the rule you recommend, stated so an operator could act on it
  - the enumeration showing it is complete
  - the INV- name and predicate that would make it executable
  - which ratified text supports it, and any you are proposing to amend
  - what you are trading away, and who bears that cost
  - the residue that is genuinely a values choice for the owner, stated as a single question
    with the options and their consequences — do not answer it for him, make it small and sharp

Where the four interact, say so. P6 and P8 are both "what happens when a namespace loses its
funding principal"; P5 is "when is an owner binding trustworthy at all"; P7 is "how would we
machine-check any of it". A recommendation that treats them as independent is probably wrong.

Be concrete and cite file:line. Say plainly where you are uncertain. Length is not a virtue;
being right and checkable is.
