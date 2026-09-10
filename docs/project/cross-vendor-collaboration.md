<!-- Internal process doc (docs/project/ is excluded from the built site).
A survey of every place Anthropic (Claude/Fable/Opus/Sonnet) and OpenAI (Codex) worked on the same
piece of jobtree, reconstructed from the full PR history on 2026-09-10. Written because the pattern
was remembered as "the time both agents opened a PR and we picked one" — the archaeology says that
happened once, and the far more common shape was one vendor authoring and the other reviewing. -->

# Cross-vendor collaboration: how Codex and Claude actually divided the work

Reconstructed by walking all 152 pull requests (`gh pr list --state all`). Two things are worth
stating before the detail, because both are counter to how it is remembered:

1. **Only five PRs in the project's history were ever closed.** Every one carries a note naming the
   PR that landed instead. So the "rival implementations" question has a complete, checkable answer.
2. **Parallel implementation happened once.** Everywhere else the two vendors sat on opposite sides
   of the author/reviewer line, which is a different — and better — use of the decorrelation.

## 1. The one true contest: #22 (Claude) vs #23 (Codex)

Both PRs were opened **39 seconds apart** on 2026-07-03 against the same task: reconcile
`docs/fundamentals.md` with the derived funding model (R14/R15).

| | PR | Branch | First commit | Commits | Fate |
|---|---|---|---|---|---|
| Claude | [#22](https://github.com/DavidLangworthy/jobtree/pull/22) — docs: reconcile fundamentals.md, tidy the site, and plan follow/ETA | `docs/fundamentals-gap-analysis` | 20:18:46Z | 5 | **merged** (`3c4163d`) |
| Codex | [#23](https://github.com/DavidLangworthy/jobtree/pull/23) — [codex] Update fundamentals for derived funding semantics | `codex/update-fundamentals` | 20:19:12Z | 1 | **closed** |

Codex's stayed at its opening commit. Claude's kept being worked for another three hours — gap
analysis → reconciliation → nav tidy → follow/ETA plan → owner decisions — and merged at 23:04Z. The
closing note on #23:

> Superseded by #22 (merged, commit 3c4163d), which comprehensively reconciled `fundamentals.md` […]
> This PR is now CONFLICTING against that rework and its intent (derived funding semantics) is
> already delivered on `main`. Closing as superseded — thank you.

Swept closed in the same minute: [#7](https://github.com/DavidLangworthy/jobtree/pull/7) — an older
Codex PR (2025-11-19, `codex/fix-admission-section-in-fundamentals.md`) aimed at the same file, whose
note records that #22 had already fixed the `[Bind-Now]`/`[Plan-Later]` math rendering.

**Honest caveat.** The two proposals were never discussed side by side *on GitHub*. #22 has zero
comments; #23 has only its closing note. The "improved it and then chose" part is visible only as
#22's commit stream continuing after #23 appeared. Whatever comparison happened, happened in a
session that was not archived — which is precisely the failure mode
[`working-agreement.md`](working-agreement.md) exists to prevent.

## 2. The other four closures are not contests

They are the same work re-landed. GitHub auto-closes a PR when its stacked base branch is deleted on
merge, and refuses to reopen it:

| Closed | Re-landed as | Cause |
|---|---|---|
| [#80](https://github.com/DavidLangworthy/jobtree/pull/80) R27 invariant oracle | [#83](https://github.com/DavidLangworthy/jobtree/pull/83) | base `review/adversarial-playbook` deleted on merge of #79 |
| [#89](https://github.com/DavidLangworthy/jobtree/pull/89) two half-plane leaks | [#92](https://github.com/DavidLangworthy/jobtree/pull/92) | stacked base squash-merged as #88 |
| [#108](https://github.com/DavidLangworthy/jobtree/pull/108) R12 ownerRefs/finalizers | [#118](https://github.com/DavidLangworthy/jobtree/pull/118) | base deleted on merge of #107 |

Note the asymmetry worth fixing: for #80 the note is on the *closed* PR, for #89 and #118 it is only
on the *winner*. A reader who lands on #89 sees a closed PR with no explanation at all.

## 3. Codex reviewed, Claude authored the fix

This is the dominant shape, and the highest-value one. One Codex review fanned out into four separate
Claude-authored PRs.

[#86](https://github.com/DavidLangworthy/jobtree/pull/86) assessed seating Codex as a cross-vendor
seat on the adversarial panel and ran a **live spike** against the funding path (codex-cli 0.144.1,
`-m gpt-5.6`). The rationale is the panel's own documented failure mode: the Claude tiers
(Opus/Sonnet/Fable) are heterogeneous by role but *share a training prior* and fail together. Codex
returned four candidates:

| Codex finding | Disposition | Landed as |
|---|---|---|
| Codex-1 — `EnvelopeKey` was `{Budget, Envelope}` with no namespace; Budgets are namespaced | CONFIRMED, high, tenancy; mutation-verified | [#87](https://github.com/DavidLangworthy/jobtree/pull/87) |
| Codex-4 — `planSingleDomain` unguarded on `len(groups)==0` | confirmed, unreachable in prod (validation rejects), fixed for symmetry | [#87](https://github.com/DavidLangworthy/jobtree/pull/87) |
| Codex-3 — a flavored `AggregateCap` counting across flavors | confirmed | [#94](https://github.com/DavidLangworthy/jobtree/pull/94) |
| Codex-2 — owner→identity binding | parked pending a tenancy threat-model ruling; unparked by David 2026-07-24 | [#127](https://github.com/DavidLangworthy/jobtree/pull/127) |

#87 carries the only PR comment in the repo that is pure cross-vendor adjudication:

> Codex #1/#2 (PR #86) independently re-derived R7's already-documented Problem bullets 1 and 2; #2
> (owner→identity binding) remains R7 part 2, the product fork R7 routes to David.

That sentence is the useful result of the whole exercise: a different vendor, reading cold, landed on
the same two problems a Fable design spec had already named. Independent re-derivation is evidence the
spec was right, not wasted effort.

Later, from the standing seat: [#135](https://github.com/DavidLangworthy/jobtree/pull/135) parks P6
off a Codex HIGH finding at `pkg/funding/evaluate.go:661`, and records the limit honestly — the
finding is **traced, not reproduced**, because Codex ran sandboxed read-only and compiled nothing.

## 4. Claude/Fable found it, Codex authored

The reverse direction is rarer but real. [#149](https://github.com/DavidLangworthy/jobtree/pull/149)
(on `codex/tla-smt-codespace`) models physical GPU ownership across a pod retry:

> Fable raised the lead; Codex independently reproduced it with a compiled test.

The WIP pins the bad behaviour and deliberately ships no production fix — the ownership/recovery rule
was routed to David as a design question. Adjacent: [#84](https://github.com/DavidLangworthy/jobtree/pull/84)
is Claude recovering *Codex's* lost proof session off a dead Codespace (the raw rollout deliberately
uncommitted, since this repo is public), and [#145](https://github.com/DavidLangworthy/jobtree/pull/145)
continues that TLA+/Apalache campaign.

## 5. The standing seat, and what it cost to make honest

Seating Codex permanently produced its own PR run, every one of them a lesson paid for by actually
running the panel rather than reasoning about it:

- [#136](https://github.com/DavidLangworthy/jobtree/pull/136) — the trace seat becomes a cheap Sonnet
  relay shelling out to `codex exec`, plus `judgeOnly` so the Judge phase is reachable at all. Billing
  to a different pool matters: the panel's heaviest line item stops competing with the reviewer's own
  quota.
- [#141](https://github.com/DavidLangworthy/jobtree/pull/141) — authenticate with the ChatGPT
  subscription (`CODEX_AUTH_JSON`), not a drained metered API key that had been returning
  `Quota exceeded` on 3 of 4 judge calls.
- [#142](https://github.com/DavidLangworthy/jobtree/pull/142) / [#143](https://github.com/DavidLangworthy/jobtree/pull/143)
  — subscription auth **rejects every explicitly named model**, so the hardcoded `-m gpt-5.6` silently
  downgraded a working cross-vendor seat to the Opus fallback. Distinguishing `MODEL` from `AUTH` from
  `QUOTA` matters because the three remedies are different.

### Does the seat change verdicts? (from [#138](https://github.com/DavidLangworthy/jobtree/pull/138))

| seat | model | votes | ranCode | decisive | reaperVetoes |
|---|---|---|---|---|---|
| reproduce | sonnet | 4 | 4 | **4** | 0 |
| trace | **openai gpt-5.6** | **1** | 0 | **0** | 1 |
| consequence | fable | 4 | 4 | 0 | 1 |

**It raises findings; it changed no verdict** — and it cannot, structurally: every decisive vote
belongs to the seat that runs code, and the Codex seat is read-only. When its quota ran out it
correctly emitted `CODEX UNAVAILABLE` and cast nothing rather than a counterfeit vote. More quota buys
votes; a writable scratch dir would buy decisiveness. That is the actual upgrade path, and it is still
open.

## 6. The design exchange — both proposals survive the merge

The closest thing to a real bake-off, and the reason the contest pattern feels like it happened more
than once. [#144](https://github.com/DavidLangworthy/jobtree/pull/144):

> Two designers worked from an identical brief, **decorrelated by vendor on purpose** (Anthropic
> Fable; OpenAI codex), exchanged twice, then a third reader judged the arguments rather than tallying
> them.

`BRIEF.md` → `A-fable-position.md` / `B-sol-position.md` (written blind to each other) → `C-`/`D-`/`E-`
exchange rounds → `OWNER-RULINGS.md`. [#150](https://github.com/DavidLangworthy/jobtree/pull/150) ran
the same play at larger scale for the quota design: five drafts, four adversarial rounds with both
critics, twelve owner rulings, landing `DESIGN-v5.md`.

This *is* "both proposed, both were improved, one was chosen" — but the losing side was never a PR to
close. It is archived inside the winning merged PR, because the critiques cite it. Superseded drafts
are kept for exactly that reason.

## 7. Rules this history suggests

1. **Prefer reviewer/author across vendors over racing implementations.** The race (§1) produced one
   discarded PR and no recorded argument. The review split (§3) produced four confirmed defects, one
   of them high-severity tenancy, with mutation verification on each.
2. **A closed PR must name its replacement in a comment on itself**, not only in the winner's body.
   Two of the five closures fail this today (§2).
3. **When a design is contested, archive the losing position in the merged PR.** §6 does this; §1 did
   not, and #23's content is now recoverable only from a closed branch.
4. **Say which limb a cross-vendor finding stands on.** "Traced, not reproduced" (§3) is a different
   claim from a compiled repro, and it changes whether the fix is a bug fix or a regression gate.

## Note on issue numbers

References like `#35`, `#48`–`#64`, "task #50" inside review archives and PR bodies are **adversarial-review
harness finding/task IDs, not GitHub issues.** This repo has only ever had eight issues (#71, #90,
#91, #121, #124, #128, #132, #148). Chasing those numbers on GitHub lands on unrelated PRs, because
GitHub shares one numbering space between issues and PRs.
