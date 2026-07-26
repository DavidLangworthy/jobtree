# Owner rulings issued during the P5-P8 exchange

These are David's, stated during the exchange. They are not designer recommendations. Both opening
positions (A, B) were written before them and inherit errors from the premises they replace.

## Ruling 1 — the trust model is DELEGATED, not admin-only (2026-07-26)

> "I'd like there to be a few trusted root quota admins that have full trust. Then they assign quota
> to normal users who are managers that can divide quota between their projects. Then the leads can
> divide quota to researchers, then researchers can horse trade with loans if needed."

Replaces the ratified premise "no principal writes Budgets" (`R7-tenancy-amendment.md:291-296`).
Consequences already accepted in the exchange:

- The §6c argument — "a hostile Budget-writer is already total quota compromise, so this adds no new
  trust" (`R7:307-312`) — is **dead**, and was never enforcement in any case: the only RBAC the chart
  ships grants `budgets` to the controller ServiceAccount (`deploy/helm/gpu-fleet/templates/rbac.yaml:19-24`).
  The amendment says so itself: *"It is posture, not code"* (`R7:399`). Withdrawn by Fable in D §1a.
- **Most Budget writers are ordinary users.** A hostile writer at any level must be *contained to what
  they were granted*, which is a containment requirement, not a trust boundary.
- Identity requires an assertion by someone **other than the claimant**. `Spec.Owner` is self-naming
  (`api/v1/budget_types.go:30-31`) and `Spec.Parents` is child-asserted (`:36`), so neither can carry
  authority. Conceded by Fable in D §2.
- "Admin-only `PrincipalBinding`" (B:19) needs revision too: under delegation there is no monolithic
  admin, so either every identity change routes through the root admins — contradicting self-service
  delegation — or the binding authority is itself delegable.

## Ruling 2 — over-allocation is LEGAL, and is settled by pausing, not by rejection (2026-07-26)

> "A director takes quota from one manager and gives it to another manager. The director should be
> free to do this at any time. But in doing so the reduced manager has over allocated their now
> assigned quota. This can happen. The only cost is that jobs funded by the reduced managers quota
> are at risk of being paused until the total gpus falls below the now assigned quota."

This is a **correction to the design, not a new requirement**, and it invalidates a proposal made in
the exchange:

- **`INV-GRANT-CONSERVE` cannot be an invariant** as Fable stated it in D §2 (*"the sum of P's grants'
  caps(f) ... does not exceed P's own envelope allocation(f)"*, with *"the fail direction is loud
  defect"*). A director's reduction makes `sum(children) > parent` **instantly and legally**. It is a
  reachable, expected state — so treating it as a defect would alarm on correct operation, and
  rejecting the director's write would violate "free to do this at any time".
- Conservation must move from **grant time to consumption time**. What may not be exceeded is what a
  subtree can *run*, not what was promised inside it. The candidate invariant becomes: for every
  Budget P and flavor f, the sum of **Owned** usage across P's whole subtree ≤ P's allocation(f) —
  over-allocation of *grants* stays legal and unalarmed.
- **The remedy is the mechanism the engine already has.** Excess demotes to Unfunded, and Unfunded
  already coasts rather than dying and is already first to be reclaimed under contention
  (`docs/project/quota-semantics.md:108`; `pkg/funding/evaluate.go:280-286`). Capacity returns by
  natural completion or by reclaim when funded demand actually needs it. Nothing is destroyed. That
  is Decision 1's demote-not-kill applied to a legal allocation change.
- The cost is bounded and named by the owner: **jobs in the reduced subtree are at risk of pause
  until usage falls below the new allocation.** Not cancellation, not failure — pause and drain.

### What this leaves open, and it is a real question

**Inside a reduced subtree, whose work pauses first?** The engine has a reclaim order for contention,
but this is a different question: the *manager* knows which of their leads' work matters, and the
engine does not. Options: engine-chosen by the existing order (no new mechanism, but the manager
cannot protect priority work); manager-expressed priority (needs a mechanism that does not exist);
or lowest-tier-first (predictable, but pauses researchers to protect leads by construction).

Also unresolved: over-allocation must be **visible** to the reduced manager — the amount by which
they are over, and what is therefore at risk — or the first they learn of it is work stopping.

### Possible consequence for P8, to be argued rather than assumed

If losing an allocation demotes-and-coasts rather than destroying, then losing a *funding principal*
is the same event with allocation zero — and the contested part of P8 (terminate the reservation at
one tick, or after W, or never) may be answering the wrong question. The 2026-07-24 panel's
"immortal reservation" complaint was about **invisibility** — Pending forever with the backlog gauge
frozen at 1020 — not about waiting as such. A reservation that waits indefinitely in an explicit
`BlockedFunding` state, with onset recorded and the gauge cleared, may be both honest and correct,
making terminality unnecessary rather than merely delayed. Neither designer proposed this; it follows
from Ruling 2 and should be attacked, not adopted on my say-so.

## Ruling 3 — the choice is a weighted random draw; the human lever is re-granting (2026-07-26)

Answers both questions ruling 2 left open.

> "If the scheduler is forced to choose, it does so probabilistically. We can figure out a weighting,
> but it is random and aspires to be fair. If the reduced manager is proactive, she can reduce the
> quota to one of her leads and leave another lead whole. That is the way a human can chose."

> "We should have a you're over by counter and ideally per job odds of being descheduled according to
> what ever rule we figure out."

**Most of this mechanism already exists**, which is the main finding here:

- `ActionLottery` (`pkg/resolver/resolver.go:34`) with a **seeded, reproducible** draw — candidates are
  sorted by run key then group index specifically so the seeded draw is deterministic (`:345`).
- Buckets are **keyed by the run's funding principal** (`ownerOf`, `:471`), so the fairness axis is
  already the quota principal rather than the namespace or the run.
- **Shrink happens before lottery** (`TestResolveShrinksBeforeLottery`, resolver_test.go:64), so
  malleable runs surrender width before anything is descheduled. Descheduling is the second resort.

Two consequences worth stating:

- **No priority API is needed.** The manager's lever is re-granting — reduce one lead, leave another
  whole — so there is no per-job or per-lead priority field to design, validate, or abuse. Human
  choice is expressed in the quota tree, which is already the authorization surface. This is a real
  simplification and it is the owner's, not a designer's.
- **Per-job odds are computable and auditable** precisely because the draw is seeded and the weights
  derive from bucket composition. That is what makes the requested per-job odds honest rather than
  decorative, and it yields an invariant: **the odds published on a Run must be the odds the draw
  actually uses.** A displayed probability that does not match the draw is the frozen-backlog-gauge
  defect in a new place — state wrong and the metric lying about it in the same breath.

### Still open — deliberately deferred by the owner

- **The weighting.** "We can figure out a weighting" — not decided. Candidates, with their bias:
  uniform per job (simplest, but a 1-GPU job and a 512-GPU job are equally likely while freeing
  wildly different capacity); proportional to width (fewer victims needed to close a deficit, but
  systematically targets large runs); inverse to elapsed progress (protects nearly-finished work,
  needs a progress signal the engine may not have); proportional to the owner's share of the
  over-allocation (fair between owners, ignores per-job cost).
- **A trap that must not be missed: repeated independent draws compound.** If the draw re-runs every
  reconcile tick, then per-tick fairness becomes near-certain eventual descheduling for everything in
  the reduced subtree — a 1-in-20 chance every 30 seconds is not a 1-in-20 chance. So the draw needs
  either one-shot semantics until the deficit changes (survivors are immune), or explicit hysteresis,
  or the published odds must be cumulative rather than per-tick and say so. This is the same
  durability class of error as the grace window: the endpoints look fine and the repetition is what
  bites. Whichever is chosen, the published odds must match it.

## Ruling 4 — descheduling is DEMAND-driven, never deficit-driven (2026-07-26)

> "Running work should only be descheduled if there is new fully funded work that needs to be
> scheduled. We don't deschedule just to deschedule."

**This resolves the repeated-draw trap recorded under ruling 3, and the code already implements it.**
`Resolve` takes a `Deficit` and returns immediately when it is not positive
(`pkg/resolver/resolver.go:39,74-75 @ 37270af`); it is invoked from the run controller's admission
and activation paths (`controllers/run_controller.go:458`, `:1332`), i.e. when a claim actually needs
capacity. There is no timer that reclaims. The escalation inside it is also already ordered the way
this ruling implies: reclaim fully-Unfunded groups first (`:94`), then shrink malleable runs
(`:131`), and only then the lottery.

So the trap I flagged — per-tick re-draws compounding into near-certain eventual descheduling — does
not exist in the shape I feared. Nothing is drawn on a clock. Two consequences:

- **Over-allocation alone causes no descheduling at all.** A manager may sit over-allocated
  indefinitely and nothing stops if no one else needs the capacity. The excess is simply Unfunded and
  coasting, first in line only when funded demand arrives. This is the existing demote-and-coast
  behaviour, now with an owner ruling behind it rather than an inference.
- **Descheduling is always attributable to a specific arriving claim.** That is a much better
  operator story than "the system decided to shrink you": there is a funded Run that needed the GPUs,
  and it can be named.

### What remains open, narrowed

The exposure rate is bounded by **demand arrival**, not by the tick — which is principled, but on a
busy cluster a job can still be entered into successive draws. So the residue is not "does it
compound on a clock" (answered: no) but:

- **What are the published per-job odds conditioned on?** Odds *per contention event* are computable
  and honest; "odds of ever being descheduled" is not well defined without a demand model. The status
  must say which it is, or it will be read as the latter.
- **Within one burst of arriving demand, is a survivor re-exposed?** If three funded claims arrive in
  quick succession, is that three independent draws or one? One-shot-per-burst is kinder and needs a
  burst boundary; independent draws are simpler and concentrate risk on nobody in particular but
  expose everyone repeatedly. Either is defensible; the published odds must match whichever is built.

## Ruling 5 — quota is time-bounded, so a reduction has TWO axes (2026-07-26)

> "Since quota is time bounded there are two different way a director might reduce quota. First she
> might just dial down the GPU Count. Second she might delay the start or accelerate the completion
> time."

Both levers are literally fields on `BudgetEnvelope` (`api/v1/budget_types.go @ 37270af`):
`Concurrency int32` is the count axis; `Start`/`End *metav1.Time` are the window axis; `MaxGPUHours`
is a third reduction of the same class as the window (it shrinks the integral).

This confirms that Sol's per-dimension formulation of consumption conservation was necessary rather
than pedantic — it insisted the predicate hold "separately to instantaneous concurrency and windowed
GPU-hours" (`E-sol-responds-to-fable.md` §3). The two owner levers are exactly those two dimensions.
A conservation rule written over one number would have been wrong for half of the reductions a
director can perform.

Mapping each lever to what is already decided:

| Lever | Dimension | Status |
|---|---|---|
| Dial down `Concurrency` | instantaneous concurrency | **Already ratified.** Decision 1 covers it explicitly — exhaustion demotes "(or concurrency, via a higher-ranked claim)" (`quota-semantics.md:27`). Excess demotes to opportunistic, reclaimed only on demand and unluckily (`:31`). |
| Accelerate `End` / reduce `MaxGPUHours` | windowed GPU-hours | **Already ratified.** "budget-window expiry no longer implies death. A run whose envelope window closes coasts as opportunistic and is re-funded when a new window opens — or reclaimed if the GPUs are needed" (`:41-43`). |
| **Delay `Start`** | windowed GPU-hours, backwards | **NOT settled — and it is a sharper form of P6.** |

### Why delaying `Start` is the interesting one

Moving `Start` *later* can push hours that have **already been accrued and already been funded**
outside the envelope's window. Under the ratified rule that history is evaluated against the current
spec — *"moving the window forward (renewal) releases hours spent in the old window, which is exactly
how 'a reopened budget window re-funds' falls out of the arithmetic"* (`EnvelopeAccount.ConsumedGPUHours`
doc comment) — those hours are re-evaluated and can become unfunded retroactively.

That is the **same visible symptom as the P6 defect** the cross-vendor panel reproduced: an envelope's
consumed total drops and headroom it already spent reappears. The difference is decisive for how it
should be treated: P6's version was induced by a **foreign squatter Budget**, whereas this one is a
**legal action by an authorised director**. So it cannot simply be forbidden.

Fable's P6 proposal already drew the line this needs, and drew it correctly: forward-only anchoring
applies to the **identity** axis (who paid for hour h is a fact of hour h), while envelope **spec** —
windows and caps — stays deliberately current-spec, because that is admin policy. Under that split,
delaying `Start` retroactively un-funds *by design*. The question is whether that is intended at the
magnitude a director can produce.

**Open for the owner, and it is narrow:** when a director delays an envelope's `Start` past hours that
were already accrued and funded, do those hours (a) stay funded — history is anchored on both axes,
requiring the window used for an hour to be the window in effect during it; or (b) re-evaluate and
become unfunded — current-spec on the policy axis, matching "release-on-renewal", accepting that a
legal admin action can make already-spent headroom reappear? (b) is the status quo and the ratified
reading; (a) is safer for auditability and costs the release-on-renewal arithmetic that Decision 1
relies on. Note this is exactly the P6 question asked on the *window* axis instead of the *owner*
axis, so answering P6 without answering this leaves half the surface undecided.
