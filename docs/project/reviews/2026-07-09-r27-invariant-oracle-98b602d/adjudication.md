# Adjudication — the nine findings whose skeptics died

The original review (`98b602d`) exhausted the session quota mid-Judge and left nine findings
unadjudicated. This is the replacement panel, run against `e2b5355`.

| | |
|---|---|
| **Run ID** | `wf_ccf52b3f-013` |
| **Judged against** | `e2b5355` (not `98b602d` — the code had moved, and four commits landed unreviewed) |
| **Panel** | 3 skeptics per finding, three *different* lenses: CODE TRACE, REPRODUCTION, CONSEQUENCE/REAPER-CHECK |
| **Cost** | 32 agents, 2.39M subagent tokens, 919 tool calls, ~1h43m |
| **Outcome** | 0 unresolved. Every finding settled. |

**Every vote came from a judge that ran code.** `ranCode=true` on all 23 surviving verdicts. The
reproduction lens was told, in as many words: *if you did not run anything, say so — do not rule
`not-a-defect` on an argument you could have tested.*

Raw data: `adjudication.json`.

## Verdicts

| | Severity | Outcome | Tally | Disposition |
|---|---|---|---|---|
| **F1** | medium | **not a defect** | 2–0 | A Pending run holding open leases is the legal half-assembled-gang state. The oracle stays silent, the run re-adopts and *reuses* the held lease, and the lease is unfunded — so no budget is charged. Closed. |
| **F2** | low | **fixed** | 3–0 | `8a1dc2c` deleted the `group != ""` guard. |
| **F3** | **critical** | **fixed** | 3–0 | `8a1dc2c`'s `leaseGroupIndex`. Confirmed by driving a production-shaped lease. |
| **F4** | high | **still present** | 3–0 | **Reproduced. Fixed here.** See below. |
| **F5** | medium | **still present** | 3–0 | **Reproduced. Fixed here.** See below. |
| **F6** | low | **still present** | 2–0 | Reproduced. **Resolved as intended behaviour** in `e681a96`: the comment that framed a semantic choice as an optimisation is replaced, and the reasons are written down. A dead judge held a `not-a-defect` verdict on this one, so treat the 2–0 as 2–1. |
| **F7** | high | **still present** | 2–0 | **Reproduced. Fixed here.** |
| **F8** | medium | **fixed** | 3–0 | `b4f6a6d`. Two judges independently reverted the fix and watched the reaper return. **The earlier quorum's refutation was wrong.** |
| **F9** | low | **still present** | 2–0 | Half fixed by `b4f6a6d` (grow coverage, verified load-bearing). The pod-plane half is fixed by `9f6a744`; the coverage gap it named is covered by `controllers/two_plane_test.go`. |

## What F4 and F5 actually were: a defect I introduced

`8a1dc2c` was meant to fix F3 — `reclaimSquatter`'s pod eviction was dead code because it keyed on a
label the sole committer never stamps. It made the eviction **live**. And being live, it over-evicted:

- `leaseGroupIndex` defaults a missing label to `"0"`, and `emitCohortPods` stamps `"0"` on every pod,
  so `removePodsForGroups(victim, {"0"})` deleted **the victim's entire pod set, cluster-wide**…
- …while `CloseLease` closed **exactly one lease**.

The victim was left holding open leases with no containers behind them. A judge's reproduction, on a
production-shaped fixture:

```
AFTER:  victim phase=Running openLeases=[filler-keep] pods=0
IMMORTAL LEASE: Running with an open lease and ZERO pods after 20 simulated hours;
                nothing closes it, the oracle never fires.
```

`INV-WIDTH-ASSEMBLED` counts **leases**, not pods. The oracle was structurally blind to it.

### Why the obvious fix is a reaper

"Close all the victim's leases" would have worked — and would have **evicted funded work**. A run's
funding class is per-*lease*: one run can hold an `Owned` lease and an `Unfunded` lease at once, the
ordinary co-funded shape. A judge's fix-probe (`TestF4_FixReapsFundedSiblingLease`) demonstrated this
before it shipped. That is the third time on this path that the CONSEQUENCE lens has caught a repair
that was worse than the bug.

### The fix

The conflict is detected at `node#ordinal` **slot** granularity. The eviction can only act at **node**
granularity, because a `PodManifest` names a machine and not an ordinal. So:

- evict at the finest granularity the pod plane *can* express — the node;
- close **exactly** the leases whose containers are being deleted, so both planes drop together;
- and if any of those leases is **funded**, **decline the swap** rather than evict it, exactly as the
  code already does for a funded conflict. Choosing between funded runs belongs to `pkg/resolver`.

`R28b` was the durable fix, and it landed in `e681a96`: the packer's groups now reach the ledger and the
pods, so "the pods of this group" means one group rather than the whole run. The node-scoped eviction
and its funded-sibling guard stay — a `PodManifest` still names a machine and not an ordinal, so
same-machine co-ranks cannot be separated, and the guard is what keeps a coarse eviction from becoming
a reaper.

## F7: `closeRunLeases` → `releaseRun`

A terminal run released every lease and kept every container. `Bridge.apply` deletes exactly the pods
*absent* from `State.Pods`, so nothing ever stopped them. The ledger called the GPUs free; the kubelet
kept them busy; the engine planned new work onto them that could never bind.

The success path (`completeRun`) has always closed the leases **and** culled the pods. Every failure
path did half. Rather than add a call at three sites and trust the fourth to remember, the pod cull now
lives **inside** the only function that closes a terminal run's leases. It is not a step a caller may
forget.

The checkpoint-grace window is exempt by construction: it is a deliberate, bounded half-plane state —
the run parks `Pending` with a deadline and its containers keep running *so they can write a
checkpoint*. It closes the dead group's lease and calls nothing here.

## Mutation tests

Each fix reverted; each test confirmed red; restored.

| Mutation | Result |
|---|---|
| M11 group-wide pod removal + single-lease close | F4 and F5 tests **fail** |
| M12 drop the funded-sibling guard | decline test **fails**: *"the swap evicted FUNDED work… 0 pods survive"* |
| M13 drop the pod cull from `releaseRun` | F7 test **fails**; **checkpoint-grace still passes**, so the cull does not reach it |

## Four judges died, and three of them had answers

`judge2:F1`, `judge2:F6`, `judge1:F7`, `judge2:F9` all hit `StructuredOutput retry cap (5) exceeded`.
Three had already reached a verdict — visible in their last tool call — and lost it to schema
validation. F6's dead judge said `not-a-defect`, so F6's 2–0 might have been 2–1.

**The fix, for the next run:** hand a malformed report verbatim to a cheap model and ask it to map the
text onto the schema, forbidding it from adding, inferring or inventing anything; if a required field
has no support in the text, it must fail rather than fabricate. Repair the *shape*, never the
*substance* — a degenerate report that did no work must still BLOCK, or the harness launders a no-op
lens into a green review, which is the exact failure it was built to prevent. The independent Attest
phase already checks every citation against the real files, so a repairer cannot invent evidence.

## Panel design note

Three skeptics, three different lenses, one model. The lenses decorrelate reasoning failures; they do
not decorrelate *model* failure. Next run: **Sonnet on REPRODUCTION, Opus on CODE TRACE, Fable on
CONSEQUENCE**, with asymmetric aggregation — a reproduction confirms alone, a refutation needs both the
trace and a failed reproduction, and the consequence lens may veto a *fix* without touching the
*finding*. Majority voting over non-exchangeable judges is not sound, and this run's `ranCode` flag was
a workaround, not a solution.

---

## Closing the last of it

**Task #52** ("the reclaimSquatter questions the review never adjudicated") is closed. It existed
because five findings from the original run reached 0/3 or 2/3 skeptics. All five are now settled:

| Was #52's | Verdict | Where it went |
|---|---|---|
| group-granular eviction, slot-granular conflict (high) | still present | fixed in `9f6a744`; root cause fixed in `e681a96` |
| victim's other-group leases left open on a Pending run (medium) | **not a defect** | the legal half-assembled-gang state; the run re-adopts and reuses the lease |
| malleable-above-min victim Running with pods deleted (medium) | still present | fixed in `9f6a744` |
| stale `ev` blast radius (low) | still present, **intended** | documented in `e681a96`; task #54 |
| tests omit the pod plane and grow leases (low) | half fixed | now covered |

**Task #49 / R28b** is closed by `e681a96`. **Task #54** is closed by the same commit, as a written
ruling rather than a code change — the panel's own mutation of the "obvious fix" failed a genuinely
funded victim, and the reasons are now in the source above `ev := c.evaluate(now)`.

Nothing from this review remains open.
