<!-- Internal process doc (docs/project/ is excluded from the built site).
How David and the agents work on jobtree. Written 2026-07-09, distilled from the R1–R9 remediation
work. This exists because the lessons below were learned in conversations, and a lesson that lives
only in a conversation is a lesson that will be re-learned. -->

# Working agreement

jobtree is built by David plus a set of Claude agents. This records how, and — more usefully — *why*,
in the form of rules that have already been paid for.

## Model split

- **Fable** — design, analysis, hard root-cause. The `docs/project/remediation/` specs are Fable
  output. Its decisions are binding until explicitly amended.
- **Opus** — writes the code, from the "Implementation spec" section.
- **Sonnet** — mechanical debugging, repro harnesses, verification sweeps, broad code search.

The point is cost/throughput: spend judgment where correctness of new code is at stake, and delegate
sweeps and searches. It is not a hierarchy — Fable overruled Opus's framing of R9, and was right.

## The rules, and what each one cost to learn

### 1. Disagreement over silence — in both directions

David: *"I want to hear disagreement, not silence."* Standing instruction. It has paid twice, going
opposite ways:

- Opus flagged that R9's Option A collided with CASCADE's per-pod swap provenance, instead of
  implementing quietly. That flag is what started the re-examination.
- David then pushed back — *"I think we decided to use JobSet as reference and implement our own
  primitive"* — against a doc that said the opposite. **He was right and the doc was wrong.**

Neither the flag nor the pushback was individually sufficient. The flag surfaced the topic; the
pushback corrected the framing; Fable then found the argument that neither had (JobSet creates *Jobs*;
the batch controller creates the pods, so the R5 trust anchor cannot survive the borrow).

### 2. When a human's memory contradicts a doc, check the *code*

Docs record what was decided. They do not record what was **learned and never written down**.
`borrow-vs-build.md:118-120` claimed swap survives a JobSet borrow. David remembered that losing swap
was the price. The code settled it: what shipped (CASCADE-3) is a *pod-carried* swap, which a shared,
immutable Job template structurally cannot express. **The docs recorded the opposite of his memory; the
code proved his memory right.**

### 3. A half-right finding, said out loud, is a lead

Opus's objection — *"a required node-affinity doesn't fit a uniform pod template"* — was the shallow
version of the real argument. Surfacing it anyway is what got Fable to the deep one. A partial finding,
stated, is a lead. A partial finding, swallowed because it isn't airtight, is nothing.

### 4. Silence is not consent

This is the single most expensive lesson here, and it recurred in **four** different substrates in one
week. Each is the same bug: *absence of evidence read as evidence of absence.*

- A review lens returned `summary: "test"` with a finding titled `"a"`. It contributed no findings, and
  zero findings read as *clean*. The regression it was assigned to find shipped.
- In the same harness, `confirmed = confirms.length >= 2` meant a **dead** skeptic silently helped
  *refute* a real finding. Two crashed agents could bury a bug.
- `go test ./...` printed `ok` for a package whose entire integration suite had **skipped** (no
  `KUBEBUILDER_ASSETS`).
- Branch rules required no status checks, so a PR merged over a **red** CI.

The fix, everywhere: **fail closed on absent evidence.** An agent must show its work (cite `file:line`,
answer every assigned question, and have an independent agent verify the quotes are real). A gate must
be a single definition both CI and a developer run (`make verify`). A skeptic panel needs a *quorum* —
under-quorum findings surface as `UNRESOLVED`, never dropped. See
`.claude/workflows/adversarial-review.js`.

### 5. Adversarially review every funding-path change before merging

Four consecutive changes to the sole-committer / funding path each contained a real, merge-blocking
defect that unit tests and the golden oracle both passed:

| Change | What review found |
|---|---|
| R3 | A gate-free **cross-tenant charge** — provenance validated a field `Evaluate` never reads |
| R4 pt1 | A **double-fund** — caching broke the cross-gang fold's read-your-write |
| R4 pt2a | A **live lease settled past the clock** — 16 → 24 GPU-hours, `Owned` width 4 → 0 |
| R2 pt2 | A **malleable run killed at its checkpoint grace** — a regression Opus itself introduced |

Budget for the review *finding* something. It has, every time. And note the fourth: the process caught
the author. That is the point of the process, not an embarrassment to it.

### 6. Decisions live in docs, verbatim, with their reasoning

David does not want to be interrupted for decisions mid-work: *"follow your recommendation and make a
note to explain it later."* So every judgment call lands in
`docs/project/remediation/IMPLEMENTATION-LOG.md`, and every ruling of his is quoted **verbatim** in the
doc it governs. The log is the memory.

Corollary, learned on R2 pt2: **when a doc's decision prose fights the doc's own invariant, the
invariant wins.** The invariant is the promise made to the operator; the prose is one attempt to keep
it. Overrule, and write down why.

### 7. A decision has a shelf life

`borrow-vs-build.md` §6.1 was *right on the evidence it had*. It checked JobSet against upstream
Kubernetes **in isolation** — before the plugin cutover made us the sole committer, before R5/R6
established the trust anchor, before CASCADE built per-pod provenance. The decision never became wrong.
**The system moved underneath it.**

**Re-validate a decision against what you have since built, not against what you knew when you made
it.** The trigger is not the calendar — it is shipping something that changes an invariant the decision
depends on. Three such events happened; none prompted a re-read. See `borrow-vs-build.md` §11.

### 8. Stop an agent rather than let it design on a stale premise

David's rulings arrive as the conversation clarifies them. The R7 design workflow was stopped and
resumed **three times** as *"a project is just another principal"*, *"permissions flow with
accountability"*, and *"the namespace pays"* each landed. Grounding phases replay from cache, so a
restart costs almost nothing. **It is far cheaper to restart a design than to review a wrong one.**

### 9. Measure before optimizing

CI wall-clock: the `ci` job was 74s and splitting it would have *lost* time (four jobs × ~20s runner
setup to save ~30s). The e2e job was 307s with **210s in one step**. A generic "add Bazel + affected-target
testing" plan would have optimized a **5.3-second** problem (`go test -race ./...`, warm) across a graph
where 13 of 21 packages transitively import `pkg/funding` anyway.

Two measurements nearly lied: GitHub Actions caches on a feature branch **are not readable from `main`**
(so the first post-merge run is cold, not warm); and the two binaries do **not** share a cheap dependency
graph (the scheduler pulls 670 packages the manager never sees).

### 10. Prefer subtractive fixes

The best fix removes the thing that can be wrong.

- R7: `Run.Spec.Owner` is a forgeable free string. Don't validate it — **delete it**, and let the
  namespace be the owner. No field, no forgery.
- R13: David's *"never complicate the implementation to support side by side"* deletes an entire class
  of migration machinery. Clean old, clean new.
- R9: refusing the borrow deleted a permanent dual pod-creation path.

### 11. Ship in stacked, single-idea PRs; don't watch CI

Open the PR, branch off it, keep working. Check `gh pr checks <n>` for `fail` before merging — but do
not sit and watch. Each PR carries one idea and its reasoning in the body, because the PR body is where
a reviewer meets the argument.

## What we owe each other

- **The agent owes**: the failure quoted verbatim, not summarized; the regression it introduced named
  as its own; the finding it only half-understands, surfaced anyway; and no green report that rests on
  a check that never ran.
- **David owes**: the ruling, and the reason behind it — because the reason is what generalizes. *"If
  Bob gives Alice his wallet it's his money that gets spent"* decided one fork and then, unprompted,
  three more.
