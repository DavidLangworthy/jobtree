# R7 pt2 — JUDGE-ONLY run (the adjudication the panel never reached)

**Reviewed commit:** `0b77fbe7d4449beec1cd463474a7353b0e84ee7c` (`0b77fbe`), PR #127.
**Run:** `wf_7b4dc0d8-aa0`, resumed — Scout/Review replayed from cache, Attest re-run, **Judge run live**.
**Harness:** `.claude/workflows/adversarial-review.js` from `feat/cross-vendor-trace-seat` (unmerged),
plus one local patch (below). `skepticQuorum: 2`. **Date:** 2026-07-25.

The 2026-07-24 panel raised 42 findings and adjudicated **none** — the Judge phase had never once
run. This run existed only to reach real verdicts. It wrote no fixes.

## VERDICT: `BLOCKED`, with 21 findings DEFERRED — not green, and not offered as green

```
verdict     BLOCKED — a lens produced no verifiable work; this is NOT a green review
confirmed   4        unresolved 0        refuted 0        deferred 21
traceSeat   {"vendor":"openai","model":"gpt-5.6","mode":"cross-vendor relay, read-only (compiles nothing)"}
usage       49 agents, 0 errors, 1,140,908 subagent tokens, 2,264 s
```

`BLOCKED` because the `codex-sol` *review* lens produced no verifiable work (below). Separately,
`judgeOnly` bounded Judge to 4 of 25 attested findings, so **21 came back `deferred`** — raised,
attested, never adjudicated. Both facts are load-bearing: a bounded run must never read as clean.

## Scope, and why these four

42 findings × 3 seats × (investigator + shaper) ≈ 252 agents. Judging was scoped to where a verdict
changes something: the reaper-check on fixes made *in response to this panel* (self-review is the
correlated-failure mode the panel exists to break), and findings nobody had reproduced.

`api/v1/lease_types.go:61` and `pkg/funding/evaluate.go:661` were in the requested scope and could
**not** be judged: both were raised only by `codex-sol`, which is blocked, so they never entered
`raised`. Named here so the gap is visible.

## The four verdicts

### 1. `controllers/run_controller.go:1234` — **HIGH, CONFIRMED unanimously. Still open.**

> *Empty derived owner routes reservation activation into `InvalidRequest` early return, making
> `failReservationNoEnvelope` unreachable — reservations become immortal.*

Two skeptics compiled and ran probes on **both** branches:

```
branch 0b77fbe : 20 ticks over 20 simulated hours — reservation stays Pending on tick 1 AND tick 20,
                 same error each tick, backlog gauge frozen at 1020 forever
                 (metrics.ClearReservationBacklog is only reached on paths the early return skips)
main           : tick 1 → failReservationNoEnvelope → state=Failed ; tick 20 → err=nil
```

`preExisting=false`, `fixIsReaper=false`, all three seats agree. `R7-tenancy-amendment.md:126-127`
and `:590` both state this path "fails terminally at `failReservationNoEnvelope`". It does not — and
**`failReservationNoEnvelope` has no test anywhere in the repo, on either branch**, which is why
nothing went red.

**This contradicts the fix made for this finding on 2026-07-24, and the panel is right.** That fix
holds the reservation and returns `nil`, on the stated reasoning that terminating it "would destroy
a legitimate reservation over an admin typo — the reaper shape". The panel checked rather than
argued: `main` already terminated, the amendment specifies terminating, and a third probe showed
recovery after terminal failure, so terminating is provably not a reaper. **The fix removed the
error spam and left the reservation immortal.** The defect is still open on #127.

This run wrote no fixes, by instruction. This entry is the handover.

### 2. `cmd/scheduler/plugin/gang.go:731` — **MEDIUM, confirmed by reproduction; consequence disputes the severity**

> *The namespace-equality check is not "strictly stronger" than the owner check it replaced — it
> accepts a charge against a different principal's Budget in the same namespace.*

Reproduce (ran code): CONFIRMED, and a genuine regression — `Segment{Namespace: default, BudgetName:
intruder}` returns **true**, where the deleted `b.Spec.Owner != run.Spec.Owner` check returned false.
Consequence (ran code): `refuted=true` **at the claimed medium severity** — the lease classes
Unfunded, holds no funded capacity, is junior and reclaimable, and is reachable only with the R5/R6
policy absent *and* an admin misconfiguration. It concedes a LOW residue: the **"strictly stronger"
comment overclaims**, and the same-namespace-different-owner case is untested.

The 2026-07-24 fix refuses exactly this world (`OwnerOfNamespace(...) == ""`), so the reproduced
scenario is closed. The overclaiming comment is not. `fixIsReaper=false` for the shipped shape.

### 3. `cmd/scheduler/plugin/gang.go:732` — **REAPER VETO on the fix that shipped**

Both voting seats set `refuted=true` *as a defect requiring action*: `Evaluate` never reads
`Lease.Spec.Owner` (exhaustive grep), and an executed probe showed a forged-owner lease and an
honest one classify **identically** — Owned, funded width 4, 4 GPUs, 8 GPU-hours. `preExisting=true`
"in the strong sense: the change narrows this exposure."

Then the consequence seat vetoed the repair:

> `fixIsReaper=true` — restoring an owner pin "reaps healthy runs and buys nothing… it buys nothing
> because owner strings aren't secrets, and it reaps because of the top-up path (`gangProvenance`,
> `run_controller.go:2774-2819`) where a legacy `Spec.Owner` or an admin reorg of
> `Budget.Spec.Owner` would wedge a healthy, funded, **running** gang forever via PreBind refusal."

**That is a veto on the `seg.Owner` pin shipped on 2026-07-24.** That change's own comment weighed a
stranded *Promise pod* and accepted it. It never considered the top-up path, where a live funded
gang wedges forever. The named mechanism is specific and checkable, and it points at reverting.

Note the harness labels this `confirmed` because its rule is *a reproduction confirms alone* and the
seat reproduced the **mechanism** (`reproduced=true`) while judging the consequence nil
(`refuted=true`). Do not read that label as "actionable defect".

### 4. `pkg/metrics/metrics.go:237` — **refuted as a blocker; the underlying bug is real**

Both seats ran code and reproduced the gauge collision, and both refuted it *for this PR*: `git diff`
between `main` and HEAD for `pkg/metrics/metrics.go` and `controllers/budget_controller.go` is
**zero lines**. Pre-existing, untouched, not worsened; the retopology diversifies namespaces rather
than consolidating them. This vindicates the 2026-07-24 decision **not** to fix it, on a stronger
argument than the one given at the time.

## ATTRIBUTION — does the cross-vendor seat change VERDICTS, or only raise findings?

| seat | model | votes | ranCode | decisive | reaperVetoes |
|---|---|---|---|---|---|
| reproduce | sonnet | 4 | 4 | **4** | 0 |
| trace | **openai gpt-5.6** | **1** | 0 | **0** | 1 |
| consequence | fable | 4 | 4 | 0 | 1 |

```
byLens    ledger-lifecycle raised 4 / confirmed 2 · signal-and-identity raised 6 / confirmed 2
          order-dependence 4/0 · consequence-and-reapers 3/0 · test-integrity 8/0
byModel   opus raised 22 / confirmed 4   ·   fable raised 3 / confirmed 0
confirmedFoundByExactlyOneLens   {ledger-lifecycle: 2, signal-and-identity: 2}
```

**On n=1: it raises findings, and it changed no verdict.** `decisive=0`. It voted **once out of
four** — the other three returned the literal token `CODEX UNAVAILABLE` after OpenAI returned
*Quota exceeded*, casting no vote rather than a counterfeit one. That is lesson 11 working in
production, and it is worth more than the votes it did not cast: a Claude opinion wearing the
cross-vendor badge would have been indistinguishable from a real one in this table.

There is also a **structural** reason, independent of quota. `decisive=4` belongs entirely to the
seat that runs code, and the trace seat is read-only — codex said so itself: *"Targeted Go tests
could not be executed because the environment is fully read-only and Go could not create its
/tmp/go-build… directory."* In this panel verdicts are changed by **execution**, and the
cross-vendor seat is the one seat structurally unable to execute. If the goal is for it to change
verdicts and not merely raise findings, it needs a writable scratch dir — not just more quota.

`codex-sol` contributed `0` to `confirmedFoundByExactlyOneLens` here, because it was blocked. Its
two `high` findings from 2026-07-24 (`evaluate.go:661`, `lease_types.go:61`) remain the seat's
strongest evidence, and both are still unadjudicated.

## Which wall — cores, or quota?

**Neither, on this evidence.**

- **Not the plan quota.** Two runs today, **24/24 and 49/49 agents completed, 0 errors**, no
  usage-limit failure of any kind. Contrast 2026-07-24, which lost 8 agents to `You've hit your
  session limit`.
- **Not cores.** Sampled 1–5 recently-active agent transcripts throughout; no sustained queue behind
  a slot cap. 49 agents in 38 minutes.
- **The wall was OpenAI's quota**, on the cross-vendor seat only — 3 of 4 trace seats returned
  *Quota exceeded* — compounded by 5–15 minute waits on an external `codex exec` subprocess, which
  is latency, not CPU.

**A bigger runner would have bought nothing in this run.** The levers that would have changed the
outcome are OpenAI quota and a writable sandbox for the codex seat.

## The blocker that ate the first attempt: attest against the SHA, not the tree

The first Judge attempt today returned `BLOCKED` in 8 minutes with **all five standard lenses**
reporting "fabricated or unlocatable citations". Nothing was fabricated. The tree was on the fixed
branch while the cached reports cited the reviewed commit, and every miss was a line displaced by
the size of a comment added in a fix —

```
run_controller.go:1234 → actual 1262     evaluate.go:220 → actual 239
gang.go:743            → actual 792      evaluate.go:256 → actual 288
resolver.go:480 → quote "return run.Namespace" does not appear (the fix rewrote that line)
```

— plus one quote the fix had rewritten. All five lenses blocked, `raised` came back empty, and the
panel returned before Judge existed. **A correct-looking BLOCKED verdict produced entirely by tree
drift, with the most expensive phase never run.** The failed attestations were then cached and would
have replayed forever.

Fixed by `harness-attest-against-sha.patch` (in this directory): an optional `attestAgainst: '<sha>'`
makes Attest read every cited file with `git show <sha>:<file>` instead of the working tree; omit it
and behaviour is unchanged. **A citation is evidence about the commit it was made against.** The
attest agents then said so themselves — *"I checked out each cited file at commit 0b77fbe using
`git show 0b77fbe:<file>`"* — and 5 of 5 lenses attested clean. The patch belongs in the harness's
own PR (`feat/cross-vendor-trace-seat`); it is filed here so it is not lost.

One honest discrepancy: the deterministic attestation run by hand on 2026-07-24 flagged
`test-integrity`'s `smoke_test.go:68` quote as out-of-period at `0b77fbe`; the agent-based Attest
passed it. **The mechanical check is the stricter of the two.**

## `codex-sol` is cache-pinned blocked

Its live re-run produced prose the shaper could not map (*"no substantive analysis provided in
prose"*), and all three attempts **completed** rather than dying — so they are cached and replay as
invalid. It cannot be revived within this run ID; that would need a fresh one, or a changed lens
prompt to re-key its calls. Its findings are unreachable here.

## Deferred (21) — raised and attested, never adjudicated

```
3x pkg/funding/evaluate.go:220        3x pkg/resolver/resolver.go:480     2x pkg/funding/evaluate.go:173
1x evaluate.go:{219,225,231,256,260}  1x pkg/resolver/resolver.go:305     1x resolver_test.go:229
1x gang_test.go:517                   1x crd_validation_envtest_test.go:202
1x order_independence_test.go:27      1x hack/e2e/runbook-smoke.sh:140
1x pkg/forecast/forecast_test.go:400  1x test/e2e/smoke_test.go:68
```

`evaluate.go:220` is **P5** and stays parked regardless: narrowing a ratified tenancy invariant is an
owner decision. Its reaper leg remains contested by the fable seat's Review-phase ruling.

## Files

| File | What |
|---|---|
| `judge-result.json` | The harness's full return value — every verdict, vote and reasoning string |
| `harness-attest-against-sha.patch` | The Attest fix, for the harness's own PR |

The 2026-07-24 panel's archive (`../2026-07-24-r7-pt2-cross-vendor-panel-0b77fbe/`) is carried into
this PR as well. It existed only on the unmerged #127 branch; if that PR is ever closed the record
would have gone with it.

## Standing items

- **P5** (interior-tier exemption) and **P6** (retroactive GPU-hour rewrite) remain parked owner
  decisions. Neither was decided here.
- The **HIGH immortal-reservation defect (§1) is open** and its fix needs redoing along the lines the
  panel proved: terminate as `main` did and as the amendment specifies, and give
  `failReservationNoEnvelope` the test it has never had.
- The **`seg.Owner` pin (§3) carries a reaper veto** with a named mechanism and should probably be
  reverted.
