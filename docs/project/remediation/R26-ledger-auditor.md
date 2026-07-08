# R26 — Ledger auditor: verify the physical plane instead of assuming it

**Priority:** P1 (structural trust; catches unknown leaks, not just known ones)
**Design:** complete (Fable) · **Next:** Opus implements, Sonnet verifies
**Origin:** funding-model review (see `../funding-model-review.md`), David's
question: "can the system trust that all the allocs and frees add up?"

## Problem

The funding derivation (`funding.Evaluate`) is a pure, fuzz-tested function —
`NoOverdraft`, `Conservation`, `Determinism` property tests pin its arithmetic.
But it replays a **ledger nobody audits**. There is no loop anywhere that
compares open leases against live pods and nodes. Known consequences (each found
separately, all the same root): R8 failed-pod zombie leases; user-deleted pods
leaving open leases (and masking as `"Completed"` if survivors look Succeeded —
pod `DeleteFunc` returns `false`, `reconcilers.go:152`); R25 spare-node leak;
R2 pt3 restart drift; R3's placement divergence. The testing doc *declares*
invariants #2 (conservation) and #8 (no orphans) but the `pkg/invariant` checker
it references does not exist.

Fixing each leak individually leaves the unknown ones. The structural answer is
an auditor that enforces the invariant, not the causes.

## Invariants (checked continuously at runtime)

1. **Lease → reality:** every open lease maps to (a) a live node for every entry
   in `Spec.Slice.Nodes`, and (b) for `Role=Active`, a live, non-terminal pod of
   the owning run whose bound node is consistent with the slice.
2. **Reality → lease:** every running pod with `schedulerName=jobtree` maps to an
   open lease of its run.
3. (Reporting only) per-envelope conservation as already property-tested in the
   engine — surfaced as a metric so drift is visible, not just test-time.

## Design

- A periodic sweep (own controller or a `RequeueAfter` loop; default every 2m,
  configurable) that lists open Leases, Pods (jobtree-scheduled), and Nodes, and
  evaluates the invariants above. **Read the world once per sweep**, not per lease.
- **Repair for invariant 1:** close the violating lease with a **distinct reason
  `"Orphaned"`** — never `"Completed"` — after the condition persists for a grace
  window (default 2× sweep interval) to avoid racing in-flight binds/swaps. Emit
  a Warning event on the owning Run.
- **Report-only for invariant 2:** a pod running without a lease is either a bug
  in the mint path or a forged pod (R5/R6 territory); killing it is a policy
  decision, so the auditor emits event + metric and does not delete. (Decision
  logged, not asked: destructive repair is limited to closing leases — the
  budget-safe direction; the capacity-safe direction only alarms.)
- Metrics: `jobtree_ledger_violations{kind="lease_no_pod"|"lease_dead_node"|
  "pod_no_lease"}` gauge + `jobtree_ledger_repairs_total{reason}` counter.
- The grace window must exceed the swap window (failed lease closed → swap pod
  minted at PreBind) so an in-flight swap is never "repaired."

## Relationship to the point fixes

R8 is still required for *semantics* (a failed pod should fail/retry the run per
policy, not merely stop charging). R25 is still required (immediate, causal
close beats a 2m-delayed audit). The auditor is the backstop that makes ledger
integrity a **checked property** rather than the sum of known fixes.

## Verification spec (Sonnet)

1. Seed an open Active lease with no pod → after grace, closed `"Orphaned"`,
   event emitted, metric incremented.
2. Seed an open lease naming a nonexistent node → closed `"Orphaned"`.
3. Seed a running jobtree pod with no lease → alarm only, pod untouched.
4. In-flight swap (lease closed, swap pod pending) within grace → no repair.
5. Healthy world → zero violations, zero repairs across repeated sweeps.
