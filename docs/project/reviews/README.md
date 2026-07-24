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
| 2026-07-24 | [R7 pt2 — namespace-derived funding owner](2026-07-24-r7-pt2-owner-from-namespace-f52d3cf/) | `f52d3cf` | ⏸ PAUSED (usage limit, mid-Review) | 1 critical CONFIRMED-by-reproduction (interior-owner exemption), deferred → owner decision; 3 UNRESOLVED. No Attest/Judge. |
| 2026-07-10 | [R27 branch — oracle, sweep, quiescence driver](2026-07-10-r27-invariant-oracle-c74e0ef/) | `c74e0ef` | DEFECTS CONFIRMED | 5 critical (4 fixed, 1 refuted); panel hand-adjudicated |
| 2026-07-09 | [R27 — the invariant oracle](2026-07-09-r27-invariant-oracle-98b602d/) | `98b602d` | DEFECTS CONFIRMED | see record |
