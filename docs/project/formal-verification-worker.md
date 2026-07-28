# Formal-verification worker brief

You are the implementation worker for jobtree's TLA+/SMT campaign. The objective
is not to make a solver return green. It is to find design and implementation
bugs that violate common-sense jobtree behavior, and to leave executable rails
that would have caught the historical bugs.

## Start and report before a long run

1. Read `AGENTS.md` completely.
2. Run `git status --short --branch`, `git fetch origin main`, and record the
   exact `origin/main`, branch, and reviewed commit. This branch was cut directly
   from `main`; do not inherit work from another feature branch or from the old
   R26 Codespace.
3. Read `docs/project/formal-verification-campaign.md` completely.
4. Read, in order:
   - `docs/project/adversarial-review-playbook.md`
   - `docs/project/quota-semantics.md`
   - `docs/project/decisions/p5-p8/OWNER-RULINGS.md`
   - `docs/project/decisions/p5-p8/FINAL-RECOMMENDATION.md`
   - `docs/project/decisions/p5-p8/SMT-SCOPE.md`
   - `docs/project/remediation/R4-pt2-ledger-compaction.md`
   - `specs/README.md`
   - existing `LedgerCompaction*.tla`, configs, Make targets, and
     `pkg/funding/ledger_compaction_conformance_test.go`
5. Inspect the current implementation and specimens, including
   `pkg/funding/evaluate.go`, `pkg/funding/funding.go`,
   `api/v1/budget_types.go`, `api/v1/lease_types.go`, and
   `pkg/funding/tenancy_r7_conflicts_test.go`.
6. Inspect `/workspaces/tla-k8s` as a source of transition patterns and tooling,
   never as claimed jobtree coverage.
7. Report the commit, tool versions, baseline timings, proposed finite domains,
   and expected-proof versus expected-counterexample properties before starting
   a long check.

If `origin/main` advanced, first incorporate it without discarding local work,
then update the reviewed SHA in every result. Never use `git checkout --` to
undo a scratch edit.

## The first artifact is a coverage matrix

Map every relevant historical defect and runtime invariant to:

- the production state and transition that caused it;
- the model variable and transition that can express it;
- the property that fails;
- an executable Go specimen or trace;
- a negative-control mutation; and
- a candid abstraction-gap entry if it is not modeled.

A historical bug the model cannot reproduce is evidence that the abstraction is
inadequate. Do not proceed to broad proof claims until the most consequential
missing rows are addressed.

## Property order

1. Lease/workload/capacity lifecycle: only the scheduler mints; only the closer
   closes; no podless open lease bills or holds GPUs forever; no double
   capacity; gang, partial-mint, spare, swap, node-loss, cordon, and stale
   observation paths unwind or progress according to policy.
2. Effective-dated accrual-prefix immutability, including namespace conflict,
   delayed `Start`, reduced cap, and renewed-window negative controls.
3. Authenticated grant locality, owner injectivity, owned-is-local, and
   instantaneous subtree conservation over a bounded delegation graph.
4. Reservation/gang progress and every string/enum mirror that controls
   revisit or unwind.
5. Resolver/reaper consequences: legality alone does not justify destroying a
   healthy funded workload.

Use TLC for temporal interleavings and readable failure traces; Apalache/SMT for
bounded pure two-state mutations and arithmetic conservation; Go tests/fuzzing
for correspondence to `funding.Evaluate`; and static review for transitions the
model forgot.

## Evidence rules

- Build the smallest known-bad model first and require its expected
  counterexample.
- Add the desired positive property only after the negative rail is real.
- Mutate each load-bearing predicate or transition and observe the intended
  check fail, then restore it.
- Check for vacuous `Init`, impossible types, zero timestamps, caller-selected
  exceptions, and fairness assumptions that force the answer.
- Calibrate important abstract traces against compiled Go or a real controller
  trace. Label design validation separately from production conformance.
- Record exact bounds, versions, command, wall time, peak RSS, outcome, and
  reviewed SHA. A finite bounded check is not an unbounded proof.
- A nonzero exit is not automatically the expected counterexample; validate the
  violated property and witness.

Do not run the repository's full milestone adversarial-review workflow. Focused
Fable or Claude review is allowed, but preserve its raw output and do not
attribute another model's conclusions to Codex.

## Compute and escalation

Start with the 4-core/16-GB Codespace. Ordinary Apalache checks have about
5.5 GB of heap available; an opt-in stateful check may use 10 GB. Do not rerun
the known direct universal encoding unchanged after it exhausted 10 GB near the
VM limit. Improve the encoding or reduce to a meaningful bounded question.

When a valuable, non-vacuous check still needs more than roughly 10–12 GB,
record the measured command and failure and ask for a 32–64-GB runner. Do not
weaken the invariant merely to fit memory.

## Stop for a design decision

Stop and present the smallest two-outcome scenario if progress would require
choosing:

- grantor-side `Budget.Spec.Grants` versus a cluster authority registry;
- windowed-hours subtree conservation semantics;
- an unrecorded correction that rewrites history;
- a production persisted-settlement implementation;
- a destructive resolver action whose demand justification is unclear; or
- any other product rule for which current binding documents permit two
  materially different tenant/operator outcomes.

State which invariant each choice enables or falsifies and the operational
consequence. Do not let the model silently author product semantics.

## Completion

Leave focused modules/configs, expected-failure rails, executable Go
correspondence, Make/CI separation, a result report, and explicit residual blind
spots. Run focused rails and then `make verify`. Follow `AGENTS.md` for invariant
survey and fix mutation if production paths change. Commit and push coherent
work; open a draft PR only when the branch is reviewable.
