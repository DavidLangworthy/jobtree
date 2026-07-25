# Autonomous run playbook

How to run an **unattended** Claude Code session (e.g. in a disposable Codespace) that
closes out remaining remediation without stopping to ask for approval on every step —
**and without quietly making a decision that is David's.**

Read this together with [`AGENTS.md`](../../AGENTS.md) (the standing rules — sole
committer/closer, the funding-engine cautions, verification discipline). This file adds
only the *operating contract* for running without a human in the loop.

## The task list (source of truth, in order)

1. **[`remediation/correctness-closeout-plan.md`](remediation/correctness-closeout-plan.md)** —
   the live sub-tracker for correctness work. Anything still open here first.
2. **[`remediation/README.md`](remediation/README.md)** — the full R1–R26 board. Work the
   rows marked `⏳` / `◐` that are **not** on the park list below.
3. **[`remediation/SIZING.md`](remediation/SIZING.md)** — size and sequencing.

Cross-check every "pending" row against `git`/the merged PRs before starting it — the
boards have drifted before. If a row is already done, fix the board instead.

## How to work (the autonomy directive)

- **Decide and keep going. Do not wait for approval.** When you hit an implementation
  choice, make the most reasonable call, **record it in
  [`remediation/IMPLEMENTATION-LOG.md`](remediation/IMPLEMENTATION-LOG.md)** with its
  rationale (the standing "don't interrupt me, log the judgment call" convention), and
  proceed.
- **Base each item on `origin/main`; stack only real dependencies.** Run `git fetch origin`
  before each item and branch it off `origin/main` — that pulls in whatever merged since
  (your earlier items, other agents' PRs, David's redirects). Only base on a previous
  *unmerged* branch when the item genuinely depends on its code (say so in the PR); once that
  dependency merges, go back to basing on `origin/main`. Open each PR, push each branch.
  **Never `gh pr merge`** — `main` is protected and merging is David's call.
- **Staying current & redirects.** Each item, `git fetch origin` and re-read this playbook
  and `docs/project/AUTOPILOT-CONTROL.md` from `origin/main` (`git show origin/main:<path>`).
  David steers you by pushing to those on `main` — **obey a redirect the moment you see it**,
  even if it changes priorities mid-run. Do **not** rebase an in-flight stack mid-run
  (unattended conflicts halt you); prefer starting the next item off fresh `main`.
- **Small, honest commits.** Follow the repo's message style; end with the
  `Co-Authored-By: Claude` trailer.

## PARK LIST — do NOT decide these; skip and record

These are owner decisions, not implementation choices. If an item requires one, **do not
make it.** Append a one-line entry to `docs/project/DECISIONS-NEEDED.md` (create it if
absent) naming the decision and why it blocks, then move to the next item.

- ~~**R7 pt2** — deleting `Run.Spec.Owner` / tenancy authz.~~ **UNPARKED — David approved it
  2026-07-24.** Implement per [`remediation/R7-tenancy-amendment.md`](remediation/R7-tenancy-amendment.md):
  delete the field, derive the owner from the namespace, stamp `PaidByNamespace` at the three lease
  sites, re-topologize the golden. It is a security fix on the sole-committer / funding path, so **open a
  PR, do NOT merge, and flag it for the adversarial review** — the per-PR gate alone is not enough here.
- **R4 pt1b reader-swap** — the acceptable informer-**staleness bound** is David's. The
  correctness core landed (#99); the cache reader-swap is a *perf* change gated on that
  bound — leave it.
- **R4 pt2b** — the settlement **store** is a feature deferral, not correctness. Skip.
- **The ROLES track** (elastic / multi-role gangs) — XL, out of scope.

If any *other* task turns out to hinge on a genuinely new policy question (not an
implementation detail), treat it the same way: park it in `DECISIONS-NEEDED.md`, don't
guess.

## Review cadence — do NOT run a per-PR adversarial review

**Finishing this entire backlog is ONE milestone.** The adversarial review is
milestone-cadence, so it runs **exactly once, at the very end**, after all items land —
and **David runs it**, not the autopilot. A full run costs ~2h and ~3M subagent tokens,
more than the PRs themselves, and it was finding already-booked work when run per-change.
So:

- **The per-PR gate is:** `make verify` green (fmt, vet, generate, antifake, `-race`,
  **envtest**, golden, helm) + the **invariant oracle** (automatic in every test binary)
  + the **eviction fuzzer** for any engine/plugin/funding change. That is enough to merge.
- **Do not launch `.claude/workflows/adversarial-review.js`** during this run. If a change
  touches the sole-committer / funding path and you believe it warrants deep review, add a
  note to `DECISIONS-NEEDED.md` flagging it for the **next milestone** review — don't run
  one.

## Verification discipline (every change)

- **`make verify` is the gate** — `go test ./...` silently skips envtest. Run
  `KUBEBUILDER_ASSETS=… JOBTREE_REQUIRE_ENVTEST=1 go test ./controllers/...` (or
  `make envtest`) for anything touching the controller/plugin/funding path.
- **Mutation-verify each fix** — revert the load-bearing line, confirm the test goes red,
  restore.
- **No fake-green** — the antifake allowlists do not grow; the sole-closer / sole-committer
  lints stay green.

## Suggested order (all unparked, no decisions)

Mechanical / conventions first, then the auditor:
`R15` (release images) → `R16`/`R17` (done) → `R11` (status conditions) →
`R12` (ownerRefs/finalizers) → `R13`+`R14` **together** (clean-break rename + CRD
validation) → `R18` (operator runbook) → `R20` (plugin events) → `R23` (logs/pods CLI) →
`R26` (ledger auditor). Update both boards as each lands.

## Stop conditions (write a final summary and halt)

Stop, write a summary of what landed / what's parked, and exit if:

1. `make verify` cannot be made green after a reasonable effort on an item (leave the
   branch, describe the failure).
2. The only remaining work is on the **park list**.
3. A step would need a credential or secret you do not have.
4. You would have to make a park-list decision to proceed.

When every unparked item is done or has an open PR, write a one-line summary to
`.autopilot-done` at the repo root and stop.
