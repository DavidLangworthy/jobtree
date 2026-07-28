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

## Ruling 6 — spent quota cannot be taken back; spec changes are prospective on EVERY axis (2026-07-26)

> "No one can take back quota that has been spent. Suppose a job started on some quota, ran for a
> bit, then the start time of the quota was pushed back to some point in the future. The job is
> effectively running unfunded and at high risk until the quota window starts back up."

This answers the window-axis question ruling 5 left open, and it generalises: **one rule across all
axes.** Accrued history is immutable; every spec change takes effect forward only.

So for the pushed-forward `Start`, both halves hold at once and there is no tension between them:

- **The hours already accrued stay charged.** They were spent under the window in effect at the time.
  Nobody gets them back — not the tenant, not the envelope.
- **The job is unfunded from the moment of the edit**, because there is no active window now. It
  coasts, at risk, reclaimed only on demand — the ratified shortfall behaviour (`quota-semantics.md:27-34`).
  When the window opens again it re-funds automatically (`:38-39`).

This **unifies every axis onto one invariant** and removes the identity-vs-policy asymmetry Fable
proposed in D §2 (forward-only for who paid, current-spec for what the budget affords). Under this
ruling there is no policy exemption: owner binding, grant topology, concurrency, and window are all
prospective. `INV-ACCRUAL-PREFIX-IMMUTABLE` becomes **total over spec mutations** rather than scoped
to the identity axis.

**Release-on-renewal survives, but its implementation changes.** The ratified arithmetic (`evaluate.go:104-108`)
gets its behaviour from clamping to *the envelope's current window*: `ConsumedGPUHours` is "the
replayed funded accrual **within the envelope's current window**". Under this ruling each hour is
instead attributed to the window in effect during it. For an ordinary renewal the result is identical
— old hours belong to the old window, the new window starts with a fresh integral — so
"a reopened budget window re-funds" still falls out. What changes is that moving a window can no
longer alter what an earlier hour cost.

### The catch, and it is the important part

**On the window axis, forward-only is not computable from the current snapshot.** Adding a Budget is
a *creation*, and `CreationTimestamp` records when — which is why Fable's forward-only anchoring works
on the identity axis with no new machinery. Changing `Start` is an *in-place edit of an existing
object*, and nothing in the API records when a spec field changed or what it was before.
`metadata.generation` increments without saying what moved.

So this ruling cannot be enforced by a smarter scan. It requires the spent hours to be **recorded as
facts** rather than recomputed from current inputs. Which has a direct and useful consequence:

**P3 — the settlement store — stops being a feature deferral and becomes a correctness requirement.**
`DECISIONS-NEEDED.md` currently classifies P3 as *"A feature deferral, not correctness"*. Under this
ruling it is the mechanism that makes "spent is spent" enforceable, because persisted settled accrual
is exactly a record a later spec edit cannot reach.

That also resolves the exchange's second unresolved disagreement in a specific direction, without
adopting either designer's position wholesale: Sol argued for immutable authority/topology epochs
*now*; Fable refused to buy two history systems and wanted to wait for P3. This ruling says history is
required — Sol's premise — but the mechanism is the one already parked, so no parallel epoch system is
bought. **P3's status is now the live question, and it is upstream of P6.**

### Consequences to fold in

- The residue question ruling 5 posed (do already-accrued hours stay funded when `Start` moves) is
  **answered: they stay charged.** Remove it from the owner's list.
- **Add:** is P3 scheduled? Until it lands, "spent is spent" is a stated invariant with no enforcement
  on the window axis, and an in-place window edit remains destructive to history. That is a
  documented gap rather than a decision, but it should be documented rather than assumed.
- The "unfunded and at high risk" phrasing is exactly the existing `ClassUnfunded` + reclaim-on-demand
  path, so the mechanism needs nothing new — only the accrual anchoring does.

## Ruling 9 — management cannot take back spent quota, and Kubernetes is not where that gets sorted out (2026-07-28)

> "Management cannot take back quota that is already spent. They might wish they could and often do,
> but kubernetes is not the right place to sort that problem out."

Restates Ruling 6's core with a scope clause that is new and does independent work. Ruling 6 said
accrued history is immutable; this says the **recourse** for wishing it were otherwise is
organisational, not technical — the scheduler must stop trying to model it, and a proposal whose
justification is "but the manager needs to claw that back" is out of scope by construction rather
than merely unimplemented.

**What it settles outright.** `accrue` clamps the *reading* of consumed hours to the current cap
(`pkg/funding/evaluate.go:993-997`), so 500 spent GPU-hours display as 250 once the cap drops to 500.
That is the ledger reporting that spent quota was taken back. It is not a semantics choice between
candidate models — every model must report 500 — so it is a **bug**, not an open question. It is the
same defect class as the frozen backlog gauge: state right, metric lying about it.

**What it does NOT settle**, and the owner said so when stating it: how much remains going forward
after a re-grant. Zero, a proportional remainder, or the full new number all leave spent hours spent
and charged. The principle scopes the question to *forward entitlement only*; it does not choose
within it.

**Where it will weigh later.** Any proposal that reaches backwards — recomputing an old interval,
repairing a summary under a new window, clamping a historical reading — is refused by this ruling
without further argument. It is the standing answer to F1, F5, and the window-axis half of P6.

## Ruling 10 — allocation is CONCURRENCY × a mandatory window; GPU-hours are metered, never enforced (2026-07-28)

Reached over several turns. The owner's observation that started it:

> "I'm not sure the hours cap makes sense. I think the problem is that there are two ways of
> specifying how much quota a team gets and they kind of fight with each other."

### The finding

Every hard problem in the P5-P8 and quota-snapshot work traces to the **hours** cap. None traces to
concurrency:

| | Concurrency | GPU-hours |
|---|---|---|
| Memory | none — set 32, it is 32 | accumulates |
| Spending depletes it | no | yes |
| Idempotent under GitOps | **yes** | no — the resync refund |
| Needs a window to reset | no | yes |
| Re-grant ambiguity | none | zero / 250 / 500 |
| Retroactive-rewrite exposure | none | F1, F5, P6, the clamp |

Two-epochs, partial settlement, release-on-renewal, the GitOps refund, the clamping bug and the
windowed-hours conservation question the formal campaign parked are **all** hours problems.

### The code already leans this way

- `MaxGPUHours` is already optional (`*int64`) and the no-cap path already exists
  (`pkg/funding/evaluate.go:125`). Concurrency-only envelopes are legal and work **today**.
- Validation already enforces `maxGPUHours <= concurrency × window` (`api/v1/budget_types.go:282`,
  `:323`), so hours can only ever make a grant **smaller** than `concurrency × window` already implies.
- Setting hours already *requires* a window (`budget_types.go:286`) — the code knows hours are
  meaningless without one.
- Exactly three enforcement sites, all nil-guarded: `evaluate.go:125` (envelope integral), `:858`
  (lending hours), `:866` (aggregate-cap hours). `nextDepletion` (`:926-960`) goes inert with no cap.

So hours exist only to say *"less than this window would permit"* — which is said more simply by
lowering the concurrency or shortening the window, neither of which accumulates anything.

### The ruling

1. **The unit of allocation is concurrency over a window.** Both are required.
2. **Windows become MANDATORY.** They are optional today — `windowActive` returns true when `Start`
   and `End` are both nil (`evaluate.go`), so an envelope with no window is active forever. That is a
   gap, not an intent: *"I thought the whole point is that every quota grant had a duration."*
   Requiring a window is what makes the model bounded rather than merely conventional, and it removes
   the "a team allocated 64 GPUs runs 64 GPUs forever" hole without any hours cap.
3. **GPU-hours are metered, never enforced.** They are reported for chargeback and for the human
   conversation about consumption. They do not gate admission, do not demote work, and do not
   accumulate against a cap.

### Why this is consistent with Ruling 9

Ruling 9 says management wanting spent quota back is an organisational problem, not a Kubernetes one.
*"You are burning more than your share"* is the same kind of problem. Metering gives that conversation
its facts; enforcement tries to have the conversation on management's behalf and produces every
retroactivity hazard in the record while doing it.

### How the three burst patterns are served — all concurrency-shaped

The owner named them, and none needs an hours balance:

1. **Run and hope** — opportunistic/Unfunded. Coast, reclaimed only on demand and unluckily. Built.
2. **Temporary quota from your manager** — a windowed concurrency envelope that expires. Windows
   already exist.
3. **Horse-trade with a neighbour** — lending. `LendingPolicy.MaxConcurrency` already exists.

### What this settles

Dead: the two-epoch problem; zero/250/500; the GitOps resync refund; release-on-renewal; windowed
-hours subtree conservation (the campaign's parked decision); partial settlement as a *correctness*
requirement. The clamping bug survives as a **reporting** defect rather than a funding one.

Alive and unchanged: identity via the snapshot (P5); **concurrency conservation** (F4), still the
largest unbuilt piece and now the only dimension that needs it; the grandparent tier; the PreBind
placement bug. Rulings 6 and 9 still govern the metered record, demoted from funding correctness to
reporting honesty.

### Migration

Concurrency-only is adoptable as **policy today** with no code change — stop setting `maxGPUHours` and
the enforcement paths go dead before anyone deletes them. Two follow-ons: make windows required (with
an end date for existing open-ended envelopes), and decide what `maxGPUHours` becomes — removed,
deprecated, or reinterpreted as a reporting threshold. It must not survive as a field that looks
enforcing and is not.

## Ruling 11 — delete `maxGPUHours`; shard the snapshot rather than approach etcd's limit (2026-07-28)

> "Re: Max gpu hours, if we're not using it, it's gone, right?"
> "The etcd thing is a real issue to sort out. We want to steer well clear of those boundaries. If
> something is getting that big, I'd break it up, and tolerate losing atomic update."

### `maxGPUHours` is deleted, not deprecated

Answers open question Q2, and the house rule already decided it: *"Never introduce a side-by-side
compatibility path. If a change breaks, we schedule it"* (`AGENTS.md:178`). `hack/antifake/crdfields.go`
exists to catch exactly this shape — a CRD field that looks like it does something and does not.

So under Ruling 10 the field goes, along with its three enforcement sites (`evaluate.go:125`, `:858`,
`:866`), `nextDepletion` (`:926-960`), the `ValidateMaxHoursWindow` rail
(`api/v1/budget_types.go:282,313-323`), and `AggregateCap.MaxGPUHours`. Schedule the break; do not
leave a tolerated no-op.

Metering is unaffected: consumed GPU-hours are still computed from lease history and still reported.
What disappears is the *cap*, not the *number*.

### Shard the snapshot; do not engineer up to the object limit

Kubernetes objects cap near 1.5 MiB. The ruling is to stay well clear rather than compress, chunk, or
optimise toward it — and if the document gets large, **break it up and accept that cross-shard updates
are not atomic**.

**Shard by root subtree, so a whole lineage lives in one shard.** That is the choice that makes the
lost atomicity harmless:

- **Ancestor walks never cross a shard.** Subtree conservation (`INV-SUBTREE-CONSERVE`) walks from a
  payer up to its root; if the lineage is intact within one shard, the check is always performed
  against one consistent version. This is the property that would break under any other sharding key,
  so it is not an implementation detail.
- **It gives Ruling 8 for free.** Per-subtree quarantine and per-subtree sharding are the same
  boundary. A shard that fails validation holds last-good while others advance — which is exactly the
  localization already ruled for, now falling out of the storage layout instead of needing separate
  machinery.
- **What is genuinely lost is cross-subtree simultaneity**, and Ruling 8 already declined to buy it.
  Two orgs' quota can be at different versions for a moment. The only edge that spans subtrees is
  **lending**, where a revoked loan may take effect late — bounded staleness of the same kind the
  replay already has, since lending eligibility is re-evaluated on every fill
  (`pkg/funding/evaluate.go:797-798`).

**Sizing is a measurement, not a guess.** Size a real org tree first, pick a shard boundary with
generous headroom, and record the number. "Well clear" needs a figure attached or it is a hope.

## Ruling 12 — versions are never skipped, only observed late; late versions do not matter (2026-07-28)

> "Ok, so skipped versions are not really skipped, they are late. Late versions don't matter."

Closes design question Q4 and dissolves Sol's gap-detection concern rather than building for it.

A watch that misses an intermediate version has not lost an *effect*; it has seen the world later. The
scheduler always acts on the most recent state it holds, converges to the correct one, and never acts
wrongly relative to what it knew. Gap detection would buy nothing a consumer could use.

The one thing genuinely given up, stated plainly so nobody discovers it later: **a revocation that is
reverted before anyone observes it never takes effect.** That is correct rather than regrettable — a
grant change reverted within a watch interval is a flapping edit, not a decision, and a manager who
needs a revocation to bite holds it long enough to be seen. Ruling 9's spirit applies: the recourse for
"I meant that briefly" is organisational.

Consequence: **no gap detection, no version-continuity requirement, no `firstSeen` bookkeeping for
continuity.** `INV-SNAP-MONOTONE` still rejects a version that moves *backwards*, which is a different
defect — that is republication rewriting the present, not lateness.

## Ruling 13 — `U` is settable cluster policy (2026-07-28)

> "U should be settable."

`U` is the deadline after which a gang still below `minRunnableGPUs` is unwound: leases closed, pods
deleted, reservation released, run requeued. It is the only number left in the design.

**Settable, and specifically cluster policy — not tenant-declared.** That distinction is the whole
point. The obvious reuse is `spec.runtime.checkpoint` (`controllers/run_controller.go:944-949`), and it
is wrong twice over: tenant-declared means a tenant can hold GPUs indefinitely by declaring a large
value, and its zero default means immediate destruction for everyone who does not set it. Both are
failure modes the deadline exists to prevent.

So: a cluster-level setting, with a **default** (most operators will never set it) and an **enforced
floor** of at least a few activation intervals, so a misconfiguration cannot reintroduce
destroy-at-one-tick. Operators may raise it; nothing may lower it past the floor.

## Ruling 14 — Ruling 10 is a PRODUCT EXCLUSION, not an equivalence (2026-07-28)

> "Ruling 10. Yes. It's a product decision. Not a direct equivalence."

Both critics attacked Ruling 10's coverage claim independently and reached the same verdict by
different routes. Sol gave it mathematical form; Fable reached it from consequence. That convergence
is the strongest evidence this process produces, and the ruling is amended rather than reversed.

**The pattern jobtree deliberately does not support**, stated so it is never re-derived as a surprise:

> *"10,000 GPU-hours during Q3, may burst up to 128 GPUs, tenant chooses when to spend them."*

Formally the feasible set `0 ≤ u(t) ≤ 128` with `∫ u(t) dt ≤ 10,000`. **No concurrency × window
rectangle represents it**: 128 across Q3 permits far more than 10,000 hours; lowering concurrency to
`10,000 / quarter` destroys the burst; shortening the window preserves both only by choosing the
tenant's burst timing in advance. The three burst mechanisms do not substitute — opportunistic work is
not an entitlement, lending needs a peer and moves concurrency rather than a fungible time budget, and
repeated temporary grants make the manager an online allocator.

**My argument for Ruling 10 was wrong and is withdrawn.** I claimed hours were redundant because
validation enforces `maxGPUHours ≤ concurrency × window`, so hours could only ever describe a smaller
rectangle. Sol: that proves the integral entitlement is a *subset*; it does not prove the subset is
representable by another rectangle. **A rectangle cannot express a non-rectangular feasible region.**

**What stands.** jobtree allocates *capability over a period*, not *fungible compute credits*. A
tenant who wants credit-style spending gets it through the burst mechanisms, at the cost of choosing
timing with their manager rather than unilaterally. That is a legitimate product boundary and every
simplification Ruling 10 bought — no balance, no epochs, idempotent grants, no retroactive rewrite —
still follows from it.

**What changes.** The design must state the exclusion rather than claim equivalence, and any future
request for compute credits is answered by pointing here, not by re-deriving the argument.

### Two factual errors in Ruling 10, corrected

Neither changes the conclusion; both change the migration, and both were asserted rather than verified.

1. *"Setting hours already requires a window (`budget_types.go:286`)"* is **backwards**. That branch
   *tolerates* windowless hours, checking only non-negativity when `Start` and `End` are both nil. And
   a half-windowed envelope (`Start` set, `End` nil) matches neither it nor the concurrency×window rail
   at `:274-285`, so **its hours are validated against nothing at all** — a pre-existing gap this
   ruling accidentally discovered.
2. *"Exactly three enforcement sites"* undercounts badly. Beyond `evaluate.go:125`, `:858`, `:866` and
   `nextDepletion`: the admission lookahead in `AvailableWidth` gates on hours at four more sites
   (`:1167`, `:1171`, `:1194-1196`, `:1215-1217`), and those implement the **born-opportunistic
   protection** of `quota-semantics.md:23-26`. Plus the accrue clamps (`:993-997`, `:1024-1027`) and
   `pkg/funding/admission.go:89-136`. Deleting hours therefore removes a real admission protection,
   not just three gates — that loss must be replaced or accepted explicitly.

## Ruling 15 — `U` defaults to 1 hour (2026-07-28)

> "Make Us default 1 hr."

Settles the shipped default for the below-minimum unwind deadline. A gang still below
`minRunnableGPUs` after **1 hour** is unwound: leases closed by normal release, pods deleted,
reservation released, run requeued.

Why the number is defensible: it covers a GitOps window, a controller restart, and most human repair,
while bounding how long a never-runnable assembly can hold GPUs on an idle cluster. A shorter default
would unwind work that a routine deploy would have recovered; a longer one makes the stuck case
expensive for whoever queues behind it.

Cluster policy, per Ruling 13 — **not** tenant-declared, and specifically not
`spec.runtime.checkpoint` (`controllers/run_controller.go:944-949`), which is tenant-set and defaults
to zero.

**Still open:** the enforced **floor**. Operators may raise `U`; the floor is what stops a
misconfiguration lowering it toward destroy-at-one-tick. It should be a small multiple of the
activation interval, and that multiple needs the interval measured rather than guessed.

## Ruling 16 — grants are a separate object, namespaced in the GRANTOR's namespace (2026-07-28)

> "I prefer the separate binding object. Can it just borrow the RBAC from a?"

Closes design question Q1. Yes — by making it **namespaced in the grantor's namespace** rather than
cluster-scoped, it borrows option (a)'s authorization exactly: *"you may write grants in your own
namespace"* is namespaced RBAC that already exists, and it is naturally subtree-bounded because a
principal's namespace **is** its position in the tree.

Grantor's namespace, never the grantee's — a grant living in the grantee's namespace would be
self-asserted, which is the defect being fixed.

### The reason for the choice, which is not the one either designer gave

**A separate object separates "may delegate" from "may change my own allocation." A field on the
Budget cannot.**

Kubernetes RBAC is per-resource, not per-field. Under option (a), `Grants` was a field on
`Budget.Spec`, so anyone permitted to add a grant was also permitted to raise their own
`concurrency` — the API conflates delegating what you hold with giving yourself more. With a separate
object the two are independently grantable:

```
lead:  create/update/delete  grants   in their namespace
lead:  get/list              budgets  in their namespace
```

A lead may sub-divide what they were given and **may not enlarge their own allocation.** That is
Ruling 1's *"contained to what they were granted"* made enforceable by the API server rather than by
the producer's good behaviour, and it is the strongest containment property available without new
policy machinery. Neither designer proposed it: Fable argued for the field, Sol for a cluster-scoped
registry, and this is the third shape.

Three smaller consequences, all favourable: each grant has its own lifecycle and status, so a
colliding grant is quarantined individually instead of poisoning a whole Budget; revocation is
deleting an object rather than editing a list; and GitOps diffs are one file per grant.

### What is given up, and why it is consistent

**Transactional injectivity.** A cluster-scoped object keyed by owner name would get uniqueness from
etcd for free; namespaced means two namespaces can each claim the same owner, so injectivity returns
to a producer check plus quarantine. That is the trade **Ruling 8 already made deliberately** — reject
was replaced by quarantine — so this is consistent rather than a new concession.

Also given up: a single audit surface. Grants are scattered across namespaces, exactly as Budgets are
today, and the compiled snapshot is the aggregated view.

## Ruling 17 — the containment claim is narrowed to what the API server actually delivers (2026-07-28)

> "Yes, accept the narrowed claim."

Both critics reached this independently and phrased it differently, which is why it is a ruling rather
than an edit. Sol: the promise must become *"no global quota is created."* Fable: narrow it to *"the
location-forgery property"* the API server actually delivers.

**What the two-object split DOES guarantee.** A Grant can only be written where its author holds
namespace access, and the grantor is derived from `metadata.namespace`'s immutable UID rather than
from any writable field. So a principal cannot forge a grant *from a namespace it does not control*,
and **no global quota is created** — conservation holds across the whole tree.

**What it does NOT guarantee, and must stop claiming.** That a lead cannot enlarge the allocation they
control. **The system has no notion of one human or team controlling two principals** (Sol Q1-8, Fable
Q1-2). An actor holding namespaces A and B writes a grant `B → A`, and every injectivity, rootedness
and acyclicity check passes — the owners differ, the namespace UIDs differ, the document is valid.
Preventing that needs admission-time rules keyed on authenticated `UserInfo` plus an
actor-to-principal registry: new machinery, not free RBAC, and out of scope.

**Consequence for how this is described.** "A lead may sub-divide what they were given and may not
enlarge their own allocation" was my sentence and it is withdrawn. The correct sentence is: *a
principal may only grant from where it has authority, and the total the tree can run is bounded by
what the roots allocated.* Someone who controls two principals can move quota between them — which is
also true of any organisation where one person holds two budgets, and is an organisational problem in
the sense Ruling 9 established rather than a technical one.

**And the RBAC that was claimed to already exist does not.** `deploy/helm/gpu-fleet/templates/rbac.yaml`
contains no `grants` resource and no lead Role; the controller holds full create/update/patch/delete
on Budgets. The split is proposed policy, not a shipped boundary — the same error as the dead §6c
argument, made again. Either the chart ships real grantor/grantee RBAC, or the design says the
property is aspirational. There is no third option.
