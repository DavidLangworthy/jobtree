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
3. Do **not** run a full adversarial review per PR. The per-PR gate is `make verify` + the invariant
   oracle (both below) + a mutated fix — a cheap, continuous legality check that runs on every test
   binary. Run the **big adversarial review at milestone cadence** — once per milestone, or when a
   change introduces a genuinely new sole-committer mechanism (a new invariant surface) — **not** for
   routine changes on this path. Rationale: a full review costs more wall-clock and tokens than the PR
   it reviews, and now that the oracle carries the continuous load it mostly resurfaces already-booked
   findings. Batch it. See below for how to archive one when you do run it.
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

Three properties are load-bearing and easy to break by "simplifying" them:

**Investigators write prose; a cheap model shapes it.** Forcing a thinking model to emit JSON while it
reasons killed four skeptics mid-verdict — three had already decided, and one was the only dissent on
its finding. The shaper repairs the **shape**, never the **substance**: it may not add, infer or invent,
and must report `unsupported` rather than fabricate. Relax that and a lens which did no work gets
laundered into a well-formed report, the validator passes, and the rail says green.

**The skeptic panel is heterogeneous.** Three skeptics on one model are three samples of one
distribution; they fail together. On 2026-07-09 all three reached for *"pre-existing, therefore not
worsened"* and refuted a true finding. **Sonnet** reproduces, **Opus** traces, **Fable** weighs
consequence.

**And the vote is not a vote.** Heterogeneous judges are not exchangeable, so majority counting is
unsound. A **reproduction confirms alone** — a compiled test that exhibits the bad state is a fact. A
**refutation needs both** the trace *and* a reproduction that was tried and failed; absence of evidence
counts only when somebody looked. The **consequence lens may veto a fix** without touching the finding:
*"real bug, proposed fix is a reaper"* is a distinct outcome, and it has caught three. Everything else is
`UNRESOLVED`.

### Report the attribution, every time

Each run returns `attribution`: raised/confirmed per lens, the same aggregated by the lens's **model**,
and per skeptic role the counts of `votes`, `ranCode`, `decisive` and `reaperVetoes`. The number that
matters is **`confirmedFoundByExactlyOneLens`** — the defects only one lens saw.

Put it in the archive record, and call out two things by name: any confirmed finding the **Fable lens
missed that an Opus lens caught**, and any **reaper veto** the other roles did not notice. We are
measuring whether the models earn their seats rather than assuming it. One review is an anecdote; say so.

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
- *"The trace shows it cannot happen."* Not on its own. If nobody ran the code, nobody looked. The engine
  is a simulator; a reproduction takes minutes.
- *"A quorum refuted it."* A quorum has been wrong. Two judges later reverted the fix and watched the
  reaper return. If a sibling finding says the same thing from another angle and never got a vote, the
  refutation has not settled anything.

## Working rules

- After completing and verifying repository changes, commit them and push the current working branch
  unless the user explicitly requests otherwise.
- **Never `git checkout -- <file>` to undo a scratch edit.** It restores to `HEAD`, not to what you had a
  minute ago, and it has silently destroyed uncommitted work here. Copy the file first, or commit.
- **Merging a stack of PRs: use `gh pr merge --merge` (merge commits), never `--squash`, and never
  `--delete-branch`.** Squashing a lower PR rewrites its commits into one new SHA, so every PR above it
  then conflicts with `main` and needs a rebase dance; merge-commits preserve the original SHAs, so each
  PR merges cleanly. And `--delete-branch` on a stacked PR **closes** every child PR based on that branch.
  Merge bottom-up, retargeting each child to `main` as its parent lands.
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
