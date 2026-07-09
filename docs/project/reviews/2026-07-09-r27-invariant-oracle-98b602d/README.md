# Adversarial review — R27, the invariant oracle

| | |
|---|---|
| **Commit reviewed** | `98b602d` — *fix(R27): the oracle, and the three defects it found* |
| **Branch** | `fix/r27-invariant-oracle` (stacked on `review/adversarial-playbook`, PR #80 → #79) |
| **Date** | 2026-07-09 |
| **Harness** | `.claude/workflows/adversarial-review.js` |
| **Run ID** | `wf_fa18f69e-b2a` |
| **Verdict** | **DEFECTS CONFIRMED** — and **INCOMPLETE**: see below |
| **Follow-up** | `cdcf6f7` fixes the confirmed finding and one refuted-but-real reaper. #48 and #49 remain open. |
| **Cost** | 73 agents, 3.01M subagent tokens, 798 tool calls, ~1h57m wall clock |

## Status: PAUSED, NOT FINISHED

The run **exhausted the account's session quota** part-way through the Judge phase. 26 of 73 agents
died with `You've hit your session limit`. Every one of them was a skeptic.

The consequence is precise and must not be glossed: **9 findings were never adjudicated.** They are
recorded below as `UNRESOLVED`, not as refuted and not as clean. The harness is fail-closed by design —
a dead skeptic is not a vote, and an under-quorum finding stays on the table — so the machinery behaved
correctly. But the review's conclusions are partial.

Among the unresolved is one rated **critical**, whose three judges all died before casting a vote.
I verified it by hand instead (see disposition), and it is real.

### Resuming

Cached agents replay instantly; only the dead judges re-run.

```
Workflow({
  scriptPath: "/workspaces/jobtree/.claude/workflows/adversarial-review.js",
  resumeFromRunId: "wf_fa18f69e-b2a",
  args: { ...identical args... }        // see harness-log.txt and the task envelope
})
```

Resume is same-session only. If the session is gone, re-run the Judge phase against the
`UNRESOLVED` list below — the lens reports in `findings.json` carry the full evidence each skeptic
would need.

**Schedule the retry for the end of a working day.** A full run of this harness on a change this size
costs roughly two hours and three million subagent tokens, and it will eat the day's quota.

## Files

| File | What it is |
|---|---|
| `findings.json` | the harness's raw return: verdict, confirmed, unresolved, refuted, per-lens summaries |
| `leads.json` | the Scout's mechanical scan of the diff for the playbook's tells (13 leads, 7 classes) |
| `harness-log.txt` | the run's narration, including the lens that had to be force-retried and every dead judge |

## What ran

Seven lenses: the four **standard** lenses that always run (`std:ledger-lifecycle`,
`std:order-dependence`, `std:signal-and-identity`, `std:test-integrity`) plus three written for this
change (`invariant-soundness`, `reclaim-both-planes`, `resolver-width-gate`).

19 findings raised. **1 confirmed, 9 refuted, 9 unresolved, 0 lenses blocked.** All 57 evidence
citations were independently attested against the files.

`std:signal-and-identity` returned **no output at all** on its first attempt. The harness rejected it
and forced a retry, which then produced 2 findings and 9 verified citations. Without the fail-closed
retry, that lens would have contributed zero findings and the review would have read *cleaner* for it.
This is the exact failure the harness was built to prevent, and it fired on its first serious outing.

## Findings

### CONFIRMED

| # | Severity | Class | Finding |
|---|---|---|---|
| 1 | **high** | 3 — last-writer-wins | `reclaimSquatter` writes `victim.Status.Phase` outside `runPhaseTracker`, so a reclaimed unfunded run's terminal fate depends on `state.Leases` order |

**Disposition: FIXED in `cdcf6f7`.** `reclaimSquatter` now takes `runPhaseTracker` as a parameter, so the call site was a compile error until it was passed. The permutation fixture was widened to give the squatter two leases (one squatting, one uncovered rank on the failing node); it now runs 120 orderings and asserts on the pod plane. Mutation-tested: reverting the tracker call turns the test red.

The mechanism, as the lens established it by running all 24 orderings across 5 process repeats: an
unfunded run can *both* squat on a funded run's spare slots *and* hold its own rank on the failing
node. When that node fails, two writers touch its phase. `failGroupWithoutSpare` routes through
`phases.apply(Failed)`; the new `reclaimSquatter` writes `Pending` directly. They never coordinate, so
the last one wins, and the run's terminal fate is decided by lease storage order.

**Correction to the lens's framing.** The lens said this "permanently kills a run that R14 says must
be demoted and requeued." That is backwards, and I repeated it before catching it. R14's
demote-not-kill governs *reclamation*; it does not govern *destruction*. A rank that died on a fenced
node with no cover kills the gang whatever its funding class. The correct verdict for a run that
suffers both is `Failed`. The defect is the **nondeterminism**, not the verdict — and `Failed` is
terminal while `Pending` is not, so the nondeterminism is not cosmetic. The lattice restores the
deterministic answer, which is what the code did before `reclaimSquatter` existed. Caught by Fable
while writing `docs/project/tla/spec-brief.md`: it could not state the phase-join invariant while two
documents disagreed about the join's value.

This is playbook class 3, reintroduced by a new writer that skips the very tracker built to kill it —
in the same commit that added the permutation rail for class 3. The rail did not catch it because my
fixture gave the squatter only one lease, so the two writers never met. **The rail was right; the
fixture was too small.**

### UNRESOLVED — never adjudicated, judges died

These are **not cleared.** Each needs a skeptic quorum before it can be dismissed.

| Severity | Finding | Skeptics | Disposition |
|---|---|---|---|
| **critical** | `reclaimSquatter`'s pod eviction is **dead code in production**: plugin-minted leases carry no group-index label | 0/3 | **Confirmed by hand → task #49 (R28).** Verified directly: `pkg/admission/admission.go:216-224`, `PodLeaseWithRole` — the sole production mint path — stamps only `LabelRunName` and `LabelRunRole`. `binder.buildLease`, which *does* stamp `LabelGroupIndex`, is dead post-cutover. |
| **high** | Terminal branch closes a failed run's out-of-scope and surviving-group leases but leaves their pods running (half-plane double-allocation) | 0/3 | **Confirmed by hand → task #48.** Reproduced with a scratch probe: run Failed, every lease released, both containers still in `State.Pods`. |
| **high** | `reclaimSquatter` closes one lease but deletes the whole group's pods, over-evicting and orphaning sibling leases | 0/3 | Open → task #52. Needs a ruling: is group-granular pod eviction correct when the conflict is slot-granular? |
| medium | `reclaimSquatter` demotes to Pending but only closes the one conflicting lease, stranding the victim's other-group open leases on a non-terminal run | 2/3 | Open → task #52 |
| medium | Malleable-above-min victim left Running with open leases whose pods were deleted — a two-plane split invisible to the oracle | 0/3 | Open → task #52 |
| medium | Malleable run at its declared minimum is failed and swept because the gate counts **base-gang** GPUs but compares against a **total-GPU** minimum | 0/3 | **CONFIRMED and FIXED in `cdcf6f7`.** Settled by reading `pkg/resolver/resolver.go:503`, not by vote: the lottery guard permits a cut while `Remaining - grp.GPUs >= MinTotalGPUs`, and `Remaining` counts grow leases. Reproduced, then fixed with a new `runnableGPUsForRun`. **The same reaper was in `pkg/invariant`, where it would have panicked in CI on a healthy run.** The lens that refuted the sibling finding was wrong. |
| low | `reclaimSquatter` skips pod removal entirely on an empty group label | 0/3 | **Guard removed in `cdcf6f7`** (an empty group is a legitimate key, not a missing value). The root cause — the sole committer never stamps the label — is R28, task #49, still open. |
| low | `reclaimSquatter` widens the blast radius of the pre-existing stale-`ev` misclassification in `HandleNodeFailure` pass 2 | 0/3 | Open → task #52 |
| low | New resolver-settlement tests omit the pod plane and grow leases | 0/3 | **Partly fixed in `cdcf6f7`**: the permutation fixture now asserts on the pod plane, and a grow-lease regression test was added. The remaining coverage gaps stay open → task #52. |

### REFUTED

Nine findings were refuted by quorum. Two of them are **refuted correctly but reported misleadingly**,
and that is the most instructive thing in this record:

- *"`reclaimSquatter` skips pod removal on an empty group label"* → refuted as **pre-existing**.
- *"`reclaimSquatter` evicts pods at GROUP granularity while the swap reclaims exact SLOTS"* → refuted as **pre-existing**.

Both refutations are literally true: the old code removed no pods either, so the change does not
*worsen* anything. But that framing conceals the actual state of the world — **the fix is inert in
production**, because the label it keys on is never set. A separate lens raised exactly that as
`critical`, and its judges died before voting.

**The lesson for the harness:** "pre-existing, therefore not worsened" is a valid refutation for a
*regression* review and a dangerous one for a *correctness* review. A fix that does nothing is not
"not a regression" — it is a fix that does nothing. The refutation rules in
`adversarial-review-playbook.md` already say pre-existing is a classification rather than a dismissal;
they now also need to say that **a change which fails to achieve its stated purpose is a finding in its
own right, regardless of what the code did before.**

Other refutations worth keeping:

- *"envtest violations become recovered-panic requeue loops that time out"* — refuted, with a
  standalone reproduction of controller-runtime v0.24.1's recover block: the full `INVARIANT VIOLATION`
  banner reaches stderr on **every** requeue pass before the panic is recovered, so a violation is loud,
  not silent. Good news, and it means the oracle does not create task #39's shape.
- *"`INV-WIDTH-ASSEMBLED` reaps a malleable run that shed its borrowed base gang"* — **refuted, and the
  refutation was WRONG.** The closely-related unresolved finding said the same thing from another angle
  and never reached quorum. I settled it by reading `pkg/resolver/resolver.go:503` and reproducing it:
  the invariant *was* a reaper, and would have panicked in CI on a run the resolver had deliberately
  left runnable. Fixed in `cdcf6f7`.

  **This is the most important line in this record.** A quorum of skeptics refuted a true finding.
  Two lenses independently raised it; one panel killed it and the other panel died. The harness's
  fail-closed machinery is what kept the second one visible as `UNRESOLVED` rather than letting the
  refutation stand for both. *A refuted finding is not a settled one when a sibling finding says the
  same thing and never got a vote.*

## What the review found that I did not

The scout's mechanical scan flagged two things the lenses then developed into real findings, both of
which I had written and not seen:

- `defer c.checkInvariants(...)` registered *before* the metrics defer, so LIFO order means the
  admission metric is recorded even on a reconcile the oracle then rejects.
- `Reconcile`'s not-found early return happens *before* `before := c.snapshotWorld()`, so that path runs
  no invariant check at all — a real gap in the comment's claim that it "runs on EVERY return".

Neither is severe. Both are exactly the sort of thing a human reviewer's eye slides over, and both were
found by grepping for a tell rather than by reasoning.

## What the review missed

Nothing yet identified. The two defects I found by hand during the run (R28's missing group label, and
the terminal-run pod leak) were *also* independently raised by lenses — the lenses just lost their
judges. That is a good sign for the harness and a bad sign for running it against a quota ceiling.

## Meta

- **Cost the run at ~2 hours and ~3M subagent tokens** before starting one. Schedule accordingly.
- Invoke by `scriptPath`, never by `name:` — the name resolves a cached copy of the script and silently
  ran a stale version at the start of this session.
- The `Scout` phase paid for itself: 13 leads, 7 classes, and two real findings that came from a grep
  rather than an argument.
