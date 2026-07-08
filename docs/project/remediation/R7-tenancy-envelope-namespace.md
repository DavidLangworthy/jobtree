# R7 — Make namespaces a real tenancy boundary

**Priority:** P1 · **Design:** complete (Fable), **one product decision for David** · **Next:** Opus implements, Sonnet verifies
**Depends on:** benefits from R5's OwnerReference work; otherwise independent. The funding-model change is the heaviest of the P1 set.

## Problem (evidence)

- **Funding aggregates cluster-wide.** `EnvelopeKey` is `{Budget, Envelope}` with
  **no namespace** (`pkg/funding/evaluate.go:152,367`). Budgets are namespaced
  objects, but two budgets named `team-west` in different namespaces collapse into
  one envelope in the funding math. A tenant who creates a same-named Budget can
  read/dilute another namespace's envelope accounting.
- **Owner is an unauthenticated free-form string.** `run.Spec.Owner` is validated
  only as non-empty (`api/v1/run_types.go:295-297`); nothing binds it to the
  requesting identity or namespace. The family/sponsor graph keys on these owner
  strings, so a Run can name any owner.
- Net: namespaces are not a boundary; `index.md`'s "clear accountability" and
  "budget-correct" promises do not hold across tenants.

## Root cause

The funding model was built as a single-tenant, cluster-global ledger keyed by
name, and identity was taken on trust from spec fields. Multi-tenancy was never
threaded through the keys or the identity of `owner`.

## Design decision

Two coordinated changes; the first is mechanical, the second is a product fork.

1. **Namespace the envelope identity (mechanical, do this regardless).** Make the
   envelope key `{Namespace, Budget, Envelope}` and thread namespace through the
   claim/aggregate keys and the `AvailableWidth`/`Take` paths. Budgets are already
   namespaced, so this is a keying change, not a schema change. The golden oracle
   is the safety rail: within a single namespace the classification output must
   not change; only cross-namespace collisions stop aliasing.

2. **Bind `owner` to authenticated identity (product fork — David's call).**
   Options:
   - a. **Namespace *is* the tenant (recommended default).** Derive/validate the
     effective owner from the Run's namespace (or a namespace label), not a
     free-form spec string. Family/sharing is within a namespace; cross-namespace
     funding happens only via an explicit **sponsor ACL** on the lender's Budget
     that names the borrower namespace. Simple, aligns with k8s RBAC, and makes
     "who owns this / who pays" answerable from the namespace.
   - b. **Keep owner strings but authenticate them.** A pod/Run admission check
     (the R5/R6 policy, extended) that requires `spec.owner` to be in an allowed
     set for the requesting identity/namespace. More flexible, more moving parts.
   Recommend (a); it is the smaller, more RBAC-native model. (b) is the fallback
   if cross-namespace org hierarchies must be modeled by owner string rather than
   by namespace + ACL.

### Decision for David (flagged)

May budget **family-sharing and sponsor lending cross namespaces**, and is the
tenant the **namespace** (option a) or an **authenticated owner string**
(option b)? The mechanical envelope-namespacing (change 1) proceeds either way;
change 2 depends on this answer.

## Invariant

Funding accounting is isolated per namespace by default: a Budget/envelope in
namespace A cannot be named into, diluted by, or charged from namespace B except
through an explicit, authenticated sponsor ACL. "Who owns and pays for a run" is
derivable from authenticated identity, not a free-form field.

## Implementation spec (Opus)

- `pkg/funding/evaluate.go` + `pkg/funding/admission.go`: add `Namespace` to
  `EnvelopeKey` (and `claimKey`, aggregate keys). Update every construction site
  (`:152,367,659,716,800,962`) and the budget-index build. Budgets already carry
  namespace; use it.
- `pkg/cover` / `pkg/admission`: thread namespace so `Feasible`/`PerPodPayer`
  resolve envelopes within the run's namespace (+ sponsor ACLs).
- Sponsor ACL (option a): extend the Budget CRD's lending ACL to name borrower
  **namespaces**, not just owner strings; enforce in the cover/graph resolution.
- Owner binding (option a): derive effective owner from namespace; keep
  `spec.owner` as a display label or validate it against the namespace in the
  R5/R6 policy.
- Golden oracle: regenerate; **single-namespace scenarios must not diff** — that
  is the proof the keying change is behavior-preserving intra-namespace.

## Verification spec (Sonnet)

1. **Cross-namespace isolation.** Two namespaces each with a Budget named
   `team-west`; assert their envelopes no longer alias (a Run in ns-A cannot
   consume ns-B's `team-west` headroom). Pre-R7 they share; post-R7 they don't.
2. **Intra-namespace parity.** Golden oracle diff is empty for all existing
   single-namespace scenarios (behavior preserved where it should be).
3. **Sponsor ACL.** With an explicit ns-B→ns-A sponsor ACL, assert borrowing works
   across namespaces *only* through that ACL, with the lender's caps enforced.
4. **Owner spoof.** A Run naming an owner it is not entitled to (option a: an owner
   not matching its namespace) is rejected or normalized; assert it cannot charge
   another tenant.

## Interactions

- **R5** adds the OwnerReference to pods; R7's identity model should be consistent
  with it (the Run is the provenance anchor).
- Touches the same funding core as **R4** (compaction) and **R1** (claim keying) —
  coordinate the `EnvelopeKey`/`claimKey` change so the three do not conflict;
  land R7's keying change on a branch that R1/R4 rebase onto, or sequence R7's
  key change first since it is purely additive to the key struct.
