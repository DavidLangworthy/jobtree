# TLA-found: the spare top-up re-provisions a swap-consumed low index

**Date:** 2026-07-11. **Status:** fixed (PR #100). **Origin:** the question *"is the
disabled-`deletePod` fuzzer tail a candidate for model checking?"* — it was, and the
experiment found more than the tail.

---

## The question

Phase 1 of the correctness closeout left one thing open: the quiescence metamorphic
fuzzer (`controllers/quiescence_test.go`) keeps its external-pod-deletion event
(`deletePod`) disabled, because a "swap-pod-vs-topup, mint-timing" interaction the
random driver reaches only occasionally could not be cleanly characterized. The
question was whether TLC — we have a mature TLA environment (`specs/`, TLC + Apalache,
Makefile targets, clean-cfg/bug-cfg discipline) — was the right instrument.

It was, for three reasons that held up against the actual specs:

1. **It's an interleaving-plus-identity bug** — TLC's core competency. `NodeFailure.tla`
   already says it: *"TLC earns its keep here because it explores every lease-processing
   order by construction."* The Go fuzzer samples orderings; TLC exhausts them and hands
   back a **minimal deterministic counterexample**.
2. **The seam was already modeled.** `NodeFailure.tla` had the right abstractions —
   `Kinds = {Primary, Spare, Swap}`, pod lifecycle `{Intent, Bound, Gone}`, the bind-time
   mint window, slot-exact identity, per-object *named* identity (`Ids`, not integer
   counts) — and the clean/bug cfg discipline that reproduced R21/R22/R25.
3. **The gap was exactly the top-up.** Its spares came from a *fixed* `SparePlacement`
   function; there was no action re-provisioning a consumed spare (`emitSparePods`). That
   missing action is precisely the swap-vs-topup seam.

The one modeling decision that made it pay off: **keep spares as distinct named
identities, never an integer counter** — the bug class (#91, and this one) is
count-vs-name, so a counter would have abstracted the defect away.

---

## What TLC found

Extending the model with a named `TopUpSpare` action (mirroring `emitSparePods`) and the
invariants `NoDuplicateSpareName`, `ConsumedSpareStaysConsumed`, `SpareWidthAccounted`
turned up **two** things:

- `NodeFailureCountTopUp.cfg` fails `NoDuplicateSpareName` — reproduces the already-fixed
  #91 duplicate-name bug (now a permanent regression rail).
- `NodeFailureConsumedCount.cfg` fails `ConsumedSpareStaysConsumed` — **a live defect in
  the post-#91 Go code.**

**The live bug.** `emitSparePods` computed `count = TotalSpares/gpusPerPod −
consumedSpareCount` and looped indices `0..count-1`, truncating the range from the top.
That is only sound if a swap always consumes the *highest* spare index. It doesn't:
`findSpareLease` picks a spare by **group**, and `sparePlacements` walks groups
ascending, so a failure on group 0 consumes spare **index 0** — the low one. With one
low index consumed, `count` drops to 1, the loop visits only `i==0`, finds its pod gone
(the swap removed it), and **re-emits the consumed spare** — over-provisioning funded
capacity the swap already re-used — while a genuinely-missing *high* index past the bound
is never refilled. #91's name-keyed presence check stopped duplicate *rebuilds*; it never
touched the wrong loop bound.

**Minimal trace (6 actions, no external delete needed):** `FenceNode → StartFailureSweep
→ ProcessActiveSwap` (spare index 0 consumed, lease closed "Swap") `→ ... → TopUpSpare`:
`count = 2−1 = 1`, range `{0}`, name `spare-0` absent → re-emits the consumed spare.

---

## Confirming it against the real code

A model prediction is a hypothesis, not a bug. The finding was verified against the
actual Go before any fix: a focused repro test (`2` spares, consume index 0 via a swap,
reconcile, assert `spare-0` is not re-emitted) was **red at HEAD**, tracing the mechanism
to `findSpareLease` (group match, not index) and `sparePlacements` (ascending order).

**The fix:** retire consumed indices **by name**. Phase 4 stamps every minted lease with
its pod name (`StampGangIdentity` at PreBind), so a closed-"Swap" spare lease names
exactly the index to retire. Build that retired set, scan the full declared range
skipping present-or-retired names, and cap emissions at `declared − consumed` live spares
(the cap also bounds a legacy unnamed-consumed lease so it can never over-provision).

---

## Verification (every layer)

- **TLA** (`make node-failure-spec-check` / `-counterexamples`): clean `NodeFailureTopUp.cfg`
  passes all invariants; `NodeFailureConsumedCount.cfg` fails `ConsumedSpareStaysConsumed`;
  `NodeFailureCountTopUp.cfg` fails `NoDuplicateSpareName`. Base cfg unchanged at 2123
  distinct states. Both new bug cfgs wired into CI.
- **Go**: regression guard `TestSpareTopUpDoesNotReprovisionSwapConsumedLowIndex`,
  **mutation-verified** (restore the truncated loop → "created 1, want 0"). The #91 test
  still passes. Full controllers unit suite + envtest (controllers + kube) green.

---

## The division of labor that held

**TLA found and pinned the defect; the Go fuzzer stays the acceptance gate.** TLC turned
"deep interaction, event disabled" into "here is the exact ordering and the invariant to
enforce." That invariant, stated for the implementation:

> For every spare index *i* of a run, at most one live pod and one open lease may carry
> the name `sparePodName(run, i)`, and an index whose RoleSpare lease closed with reason
> "Swap" is permanently retired — no emission may target it again. Never truncate the
> top-up range by `count`.

**Still open:** this fix closes the swap-vs-topup defect, but re-enabling the fuzzer's
`deletePod` (Phase 1's acceptance gate) is a separate step — do it next, now that this
interaction is closed.
