# Adversarial review archive

Every substantive adversarial review of a sole-committer path is archived here. A review costs real
time and produces findings that outlive the pull request that prompted it — the refuted ones as much
as the confirmed ones, because next quarter someone will raise the same hypothesis and deserve to know
it was already traced to the code and killed.

See `AGENTS.md` for when a review is required, and `adversarial-review-playbook.md` for the defect
taxonomy the lenses hunt with.

## Layout

```
docs/project/reviews/
  README.md                                  <- this index
  YYYY-MM-DD-<slug>-<short-sha>/
    README.md                                <- verdict, scope, findings, disposition
    findings.json                            <- the harness's raw return value
    leads.json                               <- the scout's mechanical diff scan
```

**The short SHA of the reviewed commit is required** in the directory name and in the record. A finding
without the commit it was found against is unfalsifiable a month later: the line numbers have moved, the
function has been renamed, and nobody can tell whether it was fixed or merely displaced. If the branch is
later rebased, note the new SHA — do not drop the old one.

## Every finding carries a disposition

| Disposition | Means |
|---|---|
| `fixed in <sha>` | the defect is gone, and that commit says how |
| `refuted (why)` | the panel traced it to the code and it does not hold — record the reason, not just the verdict |
| `deferred → task #N` | real, not now, tracked |
| `pre-existing → task #N` | the change did not introduce it. **This is a classification, not a dismissal.** File it. |

A finding with no disposition is an open wound. The record is not complete until every one has a row.

## Reading a record

The verdict line is the least interesting part. Three things repay attention:

- **What the scout's mechanical scan flagged that the lenses then cleared**, and why. That is where the
  taxonomy's tells are over-broad, and it is how the playbook improves.
- **Findings the panel refuted.** A refutation grounded in a quoted trace is a durable fact about the
  system. Most are worth reading twice.
- **What the review missed**, once you know. Add the class to the playbook. The taxonomy is meant to grow
  by exactly one entry each time the system surprises us.

## Index

| Date | Review | Commit | Verdict | Confirmed |
|---|---|---|---|---|
| 2026-07-26 | [R7 pt2 — the FIX-DIFF review](2026-07-26-r7pt2-fixdiff-ec5cb64/) | `ec5cb64` | **NOT GREEN** — the first review of the *repairs* | 50 raised / 20 sites; Attest clean (5 lenses, 56 citations, pinned to the sha). **A fix introduced a cross-tenant DoS**: a Budget in any namespace naming your owner destroys your reservation in one tick — parked as **P8**, and the unsafe option is what is committed. The kept refusal strands partial gangs (`Unfunded` GPU-hours climbing). **Three decorative tests**, all written to answer earlier panels; two fixed (`37270af`, `9ed3193`), one open. |
| 2026-07-25 | [R7 pt2 — JUDGE-ONLY run (cross-vendor trace seat)](2026-07-25-r7pt2-judge-0b77fbe/) | `0b77fbe` | `BLOCKED` + **21 deferred** — bounded Judge, not green | **4 adjudicated, 0 unresolved.** 1 HIGH CONFIRMED unanimously and still open (immortal reservations — `main` terminated, the branch does not). 1 **reaper veto on a shipped fix** (`seg.Owner` pin wedges a running gang via the top-up path). 2 refuted as blockers. Cross-vendor trace seat = **openai gpt-5.6**: `decisive=0` — it raises findings but changed no verdict; 3 of 4 votes lost to OpenAI quota and it compiles nothing. |
| 2026-07-24 | [R7 pt2 — cross-vendor panel (5 Claude lenses + OpenAI `gpt-5.6`)](2026-07-24-r7-pt2-cross-vendor-panel-0b77fbe/) | `0b77fbe` | `BLOCKED` — Scout+Review only; Attest/Judge killed by the usage limit | 42 raised / 25 sites; all `UNRESOLVED` at the time. 18 fixed, 2 parked (P5, P6), 2 pre-existing → F7. 83/85 citations attested mechanically. First run with a **cross-vendor seat**: 2 `high` findings no Claude lens raised. Adjudicated the next day — see the 2026-07-25 row. |
| 2026-07-10 | [R27 branch — oracle, sweep, quiescence driver](2026-07-10-r27-invariant-oracle-c74e0ef/) | `c74e0ef` | DEFECTS CONFIRMED | 5 critical (4 fixed, 1 refuted); panel hand-adjudicated |
| 2026-07-09 | [R27 — the invariant oracle](2026-07-09-r27-invariant-oracle-98b602d/) | `98b602d` | DEFECTS CONFIRMED | see record |
