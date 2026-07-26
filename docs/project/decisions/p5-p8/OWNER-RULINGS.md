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
