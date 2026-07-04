# Quota semantics (decision record, July 2026)

*Decided 2026-07-02 by the project owner. Resolves remediation items R14 and R15
(`remediation-plan.md`), which gate the Kubernetes port. This document is the source of truth for
the semantics; implementation tracking stays in the remediation plan.*

## The principle

**Quota is a claim, not a wall — and claims are ranked, not labeled.**

Idle capacity is always usable: the scheduler never leaves GPUs dark because a ledger says no.
But claims are enforceable on demand: work backed by your own current quota is safe, and work
running on anyone else's slack — a family member's excess, or no quota at all — runs at the
pleasure of the rightful owner and is reclaimed only when a funded claim actually needs the
capacity.

Three decisions follow from it.

## Decision 1 (R14): GPU-hours are enforced by evaluation, and exhaustion demotes rather than kills

`MaxGPUHours` becomes a real, enforced limit — but running out of it does not kill work.

- **Admission lookahead.** A run is admitted as funded only if `width × period` GPU-hours fit
  within the envelope's remaining integral (in addition to the concurrency check that exists
  today). `period` is a cluster-configurable accounting horizon (default: 24h). This prevents
  admitting work that would be born opportunistic.
- **Exhaustion demotes.** When an envelope's integral (or concurrency, via a higher-ranked
  claim) no longer covers a running job, the job becomes **opportunistic**: it keeps its GPUs
  and keeps running. Destroying healthy work on an idle cluster because a ledger hit zero is
  pure waste.
- **Opportunistic work is reclaimed only on demand, and unluckily.** When funded work actually
  needs the capacity, opportunistic leases are the first cut, selected by the attested lottery
  within the class. A funded admission that fails packing reclaims opportunistic capacity
  *before* falling back to a reservation.
- **No overdraft.** Funded consumption against an envelope never exceeds its caps. Hours accrued
  while opportunistic are metered in a separate, visible **unfunded** bucket ("this run consumed
  400 unfunded GPU-hours"), never as a negative envelope balance.
- **Recovery is automatic.** When quota exists again (new budget window, freed headroom), the
  job evaluates as funded again. Nothing to resubmit, nothing to approve.

A pleasant consequence: budget-window expiry no longer implies death. A run whose envelope
window closes coasts as opportunistic and is re-funded when a new window opens — or reclaimed if
someone funded needs the space. This unifies with the existing "opportunistic fill" concept from
M6: over-quota runs and filler workloads are the same class.

## Decision 2 (R15): family shares excess in proximity order; owners can always recall

- **Within the family, no gates.** A run may draw on family envelopes' excess capacity without
  any lending policy, searched in proximity order: own envelopes first, then parents and
  siblings, then cousins — same-location before cross-location at each tier.
- **The owner can always recall.** An envelope owner's own claims outrank every borrower's. When
  the owner's admission needs headroom currently consumed by family, the family user's claim
  evaluates as opportunistic (see Decision 3 — no demotion event occurs; the ranking simply
  changes) and the owner's run proceeds. You can never be locked out of your own budget; family
  can never be told "no" while you sit idle.
- **Lending policy governs strangers only.** Sponsors and non-family borrowers require the
  lender's `lending` policy (ACL + caps), exactly as today. Borrowed capacity is contractual:
  the lender pre-consented and declared caps, so it is *not* subject to unilateral recall.
  Family sharing needs no consent precisely because recall protects the owner; lending caps do
  not apply to family usage.
- **Opt-out.** An envelope may declare itself unshareable (`sharing: none`) to be excluded from
  family excess entirely.

## Decision 3: funding class is derived, never stored

Funded vs. opportunistic is **not a field on any CRD**. It is an artifact of the quota and the
running jobs: a pure, deterministic function of `(budgets, leases, clock)`, recomputed by
whoever needs it. Leases record immutable consumption *facts* (who, what, which envelope, what
interval); classifications are *evaluations* of those facts against current quota.

**The ranking function** (the normative core of this document): per envelope, order all claims —

1. the envelope owner's own runs,
2. borrowers by proximity to the owner: children, then siblings, then cousins, then sponsors,
3. within a tier: admission time, then name (deterministic tiebreak);

then greedy-fill against the envelope's concurrency and remaining integral. Claims that fit are
**funded** (owned / shared / borrowed per their relationship); the remainder — including all
claims with no covering quota at all — are **opportunistic (unfunded)**. A new claim may
displace the lowest-ranked funded claim (that is recall); equal claims never reshuffle, so
classifications are stable between evaluations and the preemption candidate pool does not churn.

What this buys:

- **Recall is not a protocol** — no demotion write, no race, no stale label. The owner's claim
  appears; the evaluation changes.
- **Re-promotion is arithmetic** — quota returns; the evaluation changes.
- **No staleness bugs** — derived state cannot lag its inputs; a whole class of model-checking
  counterexamples is unrepresentable.
- **Audit by replay** — immutable facts + deterministic function means the classification at any
  past instant is recomputable, in the same spirit as the seeded lottery.

**Status is a cache, never an authority.** `Run.status.funding` and `Budget.status` surface the
derived breakdown for humans, dashboards, and metrics — but nothing in the control path reads a
classification back from status. Admission, renewal evaluation, and the resolver recompute from
facts. The only writes are genuine events: an actual preemption closes a lease with a reason,
as today.

## The four classes

| Class | Backing | Counts against | Reclaim exposure |
| ----- | ------- | -------------- | ---------------- |
| **owned** | requester's own envelope | envelope concurrency + GPU-hours | general resolver phases only |
| **shared** | a family envelope's excess | lender's envelope usage (visible to lender) | owner recall re-ranks it opportunistic |
| **borrowed** | sponsor via lending policy | lender's envelope + lending caps | contractual — no unilateral recall |
| **unfunded** | nothing (metered separately) | nothing (visibility only) | first cut, by lottery, on demand |

Consolidated reclaim order when capacity is needed: **unfunded → spares → malleable shrink →
general lottery.** Note that recall alone frees nothing physically: a recalled (now-unfunded) job
keeps running if the owner's run can pack elsewhere; eviction happens only when the GPUs
themselves are needed.

## Implementation notes

- **Lease role vs. funding class are orthogonal.** `Spare` is a role; owned/shared/borrowed/
  unfunded is a derived class. The current conflation (`Role: Borrowed`) is untangled by R15's
  implementation: roles stay on the lease (fact), classes come from the derivation.
- The accounting `period` is admission lookahead and evaluation/reporting cadence — not a
  lease-rewrite cycle. Leases stay open-ended; hour accrual already integrates start→now.
- Malleable runs may be partially funded: the greedy fill funds as much width as quota affords;
  the remainder evaluates opportunistic.
- The cover planner's phase order and the ranking function must agree (proximity order is shared
  between "where do I look for capacity" and "who keeps it under pressure").
- The design-level model-checking scope (`testing-and-simulation.md`) should model the
  *evaluation* semantics — concurrent admissions racing the ranking function — rather than
  demotion/promotion message protocols, which no longer exist.
- R7's rule stands and is sharpened: preemption of *funded* work remains capacity-only; budget
  shortfall now manifests as opportunistic classification, never as a lottery over funded runs.

## Decision record (2026-07-04): spare billing rate and AutoRenew

Two fake-features-audit findings (#22, #25) resolved as part of the control-plane honesty pass
(`docs/project/fake-features-audit.md`, `docs/project/make-it-real-plan.md` Track D):

- **Spares are billed at the same rate as active GPU-hours — no discount exists or is planned for
  v1.** `pkg/funding/evaluate.go`'s `accrue`/`commit` do not branch on `Slice.Role`; spare width and
  hours are surfaced as a separate *reporting* bucket (`EnvelopeAccount.SpareWidth`,
  `EnvelopeUsage.SpareGPUs`) purely for visibility, not to change the charge. `M6-failure-and-spares.md`'s
  "Open Questions" billing item is closed on this basis. Revisit only if a real discount policy is
  built; until then, no doc may claim spares cost less.
- **`Budget.Spec.AutoRenew` is wired, narrowly.** `BudgetReconciler`/`BudgetController.ReconcileBudget`
  reads it and populates `BudgetStatus.PendingRenewals` — the envelopes whose window closes within
  `notifyBefore` — when set; an unset `AutoRenew` always yields an empty list. It deliberately does
  **not** auto-extend any envelope's `end` — window rotation stays an explicit operator action (the
  Budget's own concurrency/integral invariants are validated at admission time by the mutating/
  validating webhooks, not by a background rewrite of the spec). This closes finding #22
  (previously read by nothing) without introducing a second, harder-to-audit writer of Budget specs.
