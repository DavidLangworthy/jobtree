# AGENTS.md

Standing instructions for AI agents working in this repository. Read this before you touch code.

## The one thing to understand first

**An open `Lease` charges a budget and holds GPUs.** `pkg/funding.Evaluate` derives every funding
class from the set of *open* leases; the class is never stored. So a lease nobody closes bills forever
and holds capacity forever — silently. Nothing crashes. Nothing turns red. This single fact generates
most of the defects this repo has shipped.

The scheduler plugin is the **sole committer**: it mints one Lease per pod at PreBind.
`controllers.CloseLease` is the **sole closer**: `hack/antifake` fails the build if anything else
writes `Lease.Status.{Closed,Ended,ClosureReason}`. There is no allowlist.

## Before you change the funding engine, the scheduler plugin, or any sole-committer path

1. Read **`docs/project/adversarial-review-playbook.md`** in full. It is the distilled record of the
   real defects found on this path, one class per defect, each with the tell, where it lives, how to
   confirm it, and the specimen. It is not background reading; it is the map.
2. Read **`docs/project/history-run-phase-writers.md`** if you are touching `HandleNodeFailure` or
   anything that writes `run.Status.Phase`. It traces one field through seven consecutive defects and
   explains exactly why that function, and no other, keeps producing the same bug.
3. Run an adversarial review before merging. See below.
4. **Mutate every fix.** Revert the load-bearing line, confirm the test goes red, restore. A test that
   passes against the reverted fix does not test the fix. This has caught decorative tests twice.

## Verification

`make verify` is the gate; CI runs exactly it. `go test ./...` is **not** sufficient — `controllers/kube`
silently skips its whole envtest suite when `KUBEBUILDER_ASSETS` is unset and still prints `ok`.

`pkg/invariant` is an oracle wired into every engine entry point. Under `go test` a violation **panics**;
in production it logs and increments `jobtree_invariant_violations_total`. You do not enable it — being
a test binary is the enablement. If it fires, the state is illegal: **if a test asserts that state, the
test is wrong.**

To survey every violation in one pass instead of fixing panics one at a time:

```bash
JOBTREE_INVARIANT=warn go test -count=1 -v ./controllers/... 2>&1 | grep INVARIANT-WARN
```

`make verify` and CI never set that variable. If you find it set anywhere but an interactive shell,
that is a finding.

## Adversarial reviews are archived. Archive yours.

A review costs real time and tokens and produces findings that outlive the PR. **Every substantive
adversarial review gets a directory under `docs/project/reviews/`.**

```
docs/project/reviews/
  README.md                                  # the index; add a row
  2026-07-09-r27-invariant-oracle-98b602d/
    README.md                                # verdict, scope, findings, disposition
    findings.json                            # the harness's raw return value
    leads.json                               # the scout's mechanical diff scan
```

Directory name: `YYYY-MM-DD-<slug>-<short-sha>`. **The short SHA of the reviewed commit is required** —
a finding without the commit it was found against is unfalsifiable a month later. Record the SHA even
if the branch has since been rebased; note the rebase instead of dropping it.

Every finding gets a **disposition**: `fixed in <sha>`, `refuted (why)`, `deferred → task #N`, or
`pre-existing → task #N`. A finding with no disposition is an open wound. "Refuted as pre-existing" is
a classification, not a dismissal — file it.

### These runs are expensive. Schedule them.

A full review of a non-trivial change costs roughly **2 hours of wall clock and ~3M subagent tokens**,
and it will exhaust a day's quota. The 2026-07-09 R27 review lost 26 of 73 agents to a session limit
mid-quorum, leaving nine findings — including one rated *critical* — permanently unadjudicated.

So:

- **Do not launch a large adversarial review in the middle of an interactive working session.** Ask
  first, or queue it.
- **Launch it at the end of the working day, or when the user says they are stepping away.** That is
  when the quota is free and the two-hour wall clock costs nothing.
- Say what it will cost before starting one, and check that now is a good time.
- For a quick sanity pass mid-session, run a *subset*: two lenses, `skepticQuorum: 1`. Say in the
  record that it was a partial run.
- If a run dies on quota, **archive it as PAUSED with the resume command**, and never let the partial
  result read as green. Under-quorum findings are `UNRESOLVED`, which is not the same as refuted.

Run one with:

```
Workflow({ scriptPath: ".claude/workflows/adversarial-review.js", args: {...} })
```

Use `scriptPath`, **not** `name: "adversarial-review"` — the name resolves a cached copy of the script
and will silently run a stale version.

Resume a dead run with `resumeFromRunId`; completed agents replay from cache and only the dead ones
re-run.

The harness is fail-closed by construction: a lens that produces no verifiable work **BLOCKS**; it never
reads as green. Four standard lenses always run and cannot be removed by the caller, because a caller
who forgets to ask about lease lifecycle would otherwise get a clean review — an unenforced obligation,
which is the exact bug class the harness exists to catch.

## Things that are not valid reasons to dismiss a finding

- *"The test suite passes."* It passed for every defect on the list.
- *"The comment says it cannot happen."* A comment is an assertion nothing runs.
- *"That needs an unusual sequence of events."* This scheduler runs for months. Node failures, cordons,
  budget-window rollovers and controller restarts are all routine.
- *"It's pre-existing."* Making a dead path reachable is worsening it.
- *"Pre-existing, therefore the change does not worsen it."* Valid for a **regression** review, and
  dangerous for a **correctness** review. It refuted two true findings on 2026-07-09 while concealing
  that the fix under review was **inert in production**. A change that fails to achieve its stated
  purpose is a finding in its own right, whatever the code did before.
- *"Only some of the skeptics returned a verdict."* Silence is not consent in either direction. An
  under-quorum finding is `UNRESOLVED` and stays on the table.

## Working rules

- **Never `git checkout -- <file>` to undo a scratch edit.** It restores to `HEAD`, not to what you had a
  minute ago, and it has silently destroyed uncommitted work here. Copy the file first, or commit.
- Sub-agents must never run mutating git commands, and must never spawn sub-agents.
- Prefer a compiled, running reproduction over an argument. The engine is pure: `ClusterState` plus a
  static clock **is** a simulator, so any hypothesis about engine behaviour is a few minutes of work.
  **Do not speculate when you can execute.**
- A pipeline's exit status is the last command's. `make verify | tail -2 && git push` pushes on failure.
  Use `set -o pipefail`.
- Decisions recorded in `docs/project/quota-semantics.md` and the concept docs are binding. Disagree in
  writing rather than diverging in code.
- Never introduce a side-by-side compatibility path. If a change breaks, we schedule it, stop the jobs,
  and restart. Clean old, clean new.
