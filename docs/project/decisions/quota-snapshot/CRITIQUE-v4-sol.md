Do not ship v4 unchanged. I found two genuine defects, including a third compound interaction.

## Question 1 — reversing entries

**The disclosure argument is not sufficient. Reversing entries, as specified, are a sanctioned retroactivity path.**

Append-only storage preserves the sequence of assertions; it does not make the effective total immutable. A writer can append `-500`, then append replacement entries allocating those 500 hours however it wants. The original remains visible, but the reported history has still been rewritten economically. Nothing in §5 prevents reversing every entry.

The design leaves every load-bearing question unanswered:

- No authority is named. Ordinary quota operators must not have this power, and the meter’s create permission alone would grant unrestricted historical correction.
- A reversal need not identify a specific entry, field, or interval.
- There is no bound preventing repeated, partial, chained, or double reversals.
- “Status fields are projections of the ledger” does not say whether they show raw usage or corrected net usage. Raw makes the reversal ineffective; net means the historical total changes.
- Deterministic IDs constrain accrual entries, not compensating entries.
- Auditability detects an arbitrary rewrite; it does not prohibit one.

Rulings 6 and 9 require hours actually spent to remain charged and refuse repairs of historical readings under later policy ([OWNER-RULINGS.md:221](/Users/david/mycode/jobtree/docs/project/decisions/p5-p8/OWNER-RULINGS.md:221), [OWNER-RULINGS.md:289](/Users/david/mycode/jobtree/docs/project/decisions/p5-p8/OWNER-RULINGS.md:289)). Correcting a genuinely false measurement is distinguishable from taking back real usage—but that is an explicit, governed exception. Section 5 currently smuggles it in while claiming no retroactivity has reopened ([DESIGN-v4.md:152](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v4.md:152)). That claim is false.

Replace reversing entries with two distinct records and three distinct report fields:

- The jobtree usage ledger contains only immutable metering facts. No negative or reversing entries. `meteredGPUHours` is the raw sum and its settled prefix never changes.
- Corrections live in a separate append-only chargeback-adjustment stream, preferably outside Kubernetes per Ruling 9. If kept in-cluster, it needs separate RBAC and a dedicated chargeback authority—not quota managers or the ordinary meter writer.
- Every adjustment names the exact entry or finite set of entries, affected quantity, reason, evidence, issuer, approval, and any superseded adjustment. Free-floating negative accrual is forbidden.
- Reports expose `meteredGPUHours`, `adjustmentGPUHours`, and `billableGPUHours` separately. The net must never be presented as historical GPU usage.
- Funding, allocation status, and usage invariants consume only raw metering facts.

“Correct only the projection” is coherent only when the projection is rebuildable from immutable raw usage plus that separately governed adjustment stream. An ad hoc mutable projection merely moves the backdoor.

If the desired product semantics are instead that *best-known historical usage itself* may change after metering defects, that needs a new owner ruling and an explicit as-recorded/as-corrected model. It cannot be claimed as compatible with immutable totals.

## Question 2 — **GENUINE THIRD COMPOUND FOUND**

**Grant windows × `INV-ENVELOPE-WITHIN-GRANT` × prospective reductions recreate the self-defeating-cut problem on the time axis.**

Concrete reproduction:

1. Grant `A → B` has window `[0,100)`.
2. B’s envelope is `[0,100)`, validly contained.
3. At time 20, A prospectively accelerates the Grant’s end to 50.
4. B’s unchanged envelope is now outside its authorising Grant, violating the hard containment invariant.

Every presently available outcome is wrong:

- Reject or quarantine the Grant change: A’s cut never takes effect.
- Retain last-good: B remains authorised until 100.
- Treat the changed Grant as credit-free: B drops to zero immediately at time 20, rather than at 50.
- Quarantine B or block its renewal: the fuse returns.

This is exactly the cap-reduction defect that §2b solved with `min(envelope, grant)`, but the solution was applied only to concurrency ([DESIGN-v4.md:84](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v4.md:84)). The time dimension was left as a rejection invariant.

The repair is to generalize clamping across every Grant dimension:

```text
effectiveConcurrency = min(envelope.concurrency, grant.cap)
effectiveWindow      = envelope.window ∩ grant.window
```

Consequences:

- Grant expiry clamps authority to zero; it never causes quarantine.
- Accelerating a Grant end takes effect at the new end.
- A Budget renewal may land before its Grant renewal, but remains effective only through the current intersection.
- Replace `INV-ENVELOPE-WITHIN-GRANT` with `INV-EFFECTIVE-WITHIN-GRANT`. Authored overhang is legal, visible over-allocation—not invalid state.
- Add a temporal diagnostic; the current per-flavour `overAllocatedBy` cannot explain “your envelope extends 12 days beyond its authority.”

Two related clauses also need precision:

- `INV-SINGLE-INBOUND-AUTHORITY` must be time-indexed. Non-overlapping `A → P [0,50)` and `B → P [50,100)` should be a staged handoff, not two composing authorities. If the invariant instead counts objects globally, the replacement is quarantined while the incumbent exists and P falls to zero at expiry. State explicitly whether seamless reparenting is supported.
- Quarantine must be object-**revision** granular. For an invalid update to an incumbent Grant, “retain the previously accepted binding” and “a quarantined Grant authorises nothing” currently conflict ([DESIGN-v4.md:128](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v4.md:128)). The accepted revision should remain authoritative while the candidate revision is credit-free; deletion and valid shortening must still take effect immediately.

Credit-free quarantine plus absent-authority-means-zero is otherwise coherent: when no accepted active authority exists, zero is the correct fail-closed result. The defect is allowing ordinary expiry, renewal ordering, or handoff to become quarantine ambiguity.

Dead weight now present:

- The half-windowed **hours-validation** gap is obsolete because `maxGPUHours` is deleted. Retain only the simpler rule that half-windowed envelopes violate `INV-WINDOW-REQUIRED` ([DESIGN-v4.md:61](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v4.md:61)).
- “Principal quarantined” is leftover terminology; §4 quarantines Grants, not principals ([DESIGN-v4.md:194](/Users/david/mycode/jobtree/docs/project/decisions/quota-snapshot/DESIGN-v4.md:194)).
- The authored `INV-ENVELOPE-WITHIN-GRANT` becomes actively harmful once the necessary temporal clamp is adopted.

This is not a nit. It is a real third compound, although the repair is local and follows the concurrency clamp already present rather than requiring a new architecture.
