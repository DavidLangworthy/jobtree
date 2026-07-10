# Codex as a review-panel subagent — feasibility assessment

**Date:** 2026-07-10
**Author:** Claude (Opus 4.8), at David's request
**Status:** assessment + **live spike run on this Mac, 2026-07-10** — no harness code changed

---

## 0. Spike results (ran it — it works)

Installed `codex-cli 0.144.1` and drove `codex exec` against this repo with the
`OPENAI_API_KEY` already in the env. Everything the wrapper design depends on is real:

- **Auth:** the restricted key authenticates for *inference* via `CODEX_API_KEY` even
  though it lacks `api.model.read` scope (can't list models, doesn't need to).
- **Model name:** `gpt-5.6` and `gpt-5.5` work; **`gpt-5.6-codex`/`gpt-5.5-codex` 400 —
  they don't exist.** The `-codex` suffix only exists for `gpt-5`. Tell the wrapper
  `-m gpt-5.6`.
- **Read-only reviewer:** `--sandbox read-only` (the default) let it grep/read the tree
  and write nothing. `approval: never`, no prompts.
- **Structured output:** `--output-schema finding.schema.json` returned **valid,
  conforming JSON** in one shot — so Codex can hit our `REPORT_SCHEMA` directly, no shaper
  needed for the clean path.
- **Honest exit codes:** bad model → non-zero exit; good run → 0. The fail-closed rail is
  satisfiable off the exit code alone.

**The hunter run is the headline.** `gpt-5.6`, high effort, read-only, pointed at
`pkg/funding/{funding,evaluate,admission}.go` + `pkg/pack/pack.go` with the playbook as
context. In ~90s / ~29 read-grep steps it returned **four omission-class candidates, each
with a verbatim citation** — and **all four citations Attest clean** (exact line matches):

| # | candidate (omission) | citation (verified verbatim) |
|---|---|---|
| 1 | Budget key drops namespace → same-named budgets collide across tenants | `pkg/funding/evaluate.go:177` |
| 2 | Funding trusts `run.Spec.Owner` (self-declared) instead of deriving tenant | `pkg/funding/evaluate.go:527` |
| 3 | Aggregate-cap flavor ignored when attaching envelopes | `pkg/funding/evaluate.go:194` |
| 4 | `deriveGroups` can return empty; result unchecked → "success" with zero placements | `pkg/pack/pack.go:98` |

Finding **#4 is the exact `deriveGroups` omission class** the playbook and the R28
commits already flag — a cross-vendor seat re-derived a known-dangerous path unprompted.
Whether each is a *true* defect still needs domain adjudication (e.g. #1 hinges on whether
Budgets are namespaced), but that is the panel's job — the point is the seat **did real
work, cited real lines, and hunted omissions**, which is the failure mode Claude seats
share a prior on. Artifacts in the session scratchpad (`hunter-last.md`, `schema-last.json`).

**Verdict of the spike:** the Sonnet-wrapper path is not just feasible, it's a few-hour
job, and the hunter role earns a look on n=1. Do the n>1 measurement before committing.

---

## 1. What "the discussion" actually is

There is no standalone design doc named "codex on the review panel." The discussion
lives in the adversarial-review harness itself and two recent commits:

- `.claude/workflows/adversarial-review.js` — the fail-closed, multi-lens review panel.
- `2554534` *review: heterogeneous skeptics, asymmetric aggregation* — made the panel
  **heterogeneous by model**, pairing each seat to what that model is good at:
  Sonnet→REPRODUCTION, Opus→CODE TRACE, Fable→CONSEQUENCE/REAPER.
- `1711007` *review: measure whether each model earns its seat* — David asked "whether
  one Fable is worth three Opus," asked **not to be answered from taste**, so the harness
  now emits `attribution` (`byLens`, `byModel`, `confirmedFoundByExactlyOneLens`) and
  task #55 asks, after every review, what the Fable lens missed that an Opus lens caught.

The through-line: **a panel of three samples from one distribution fails together.**
Commit `2554534` says this in as many words — three Opus skeptics "share a prior, so
they fail together," and once refuted a *true* finding in unison. Splitting across
Claude tiers (Opus/Sonnet/Fable) decorrelates *some* of that prior, but all three still
share Anthropic pre-training, RLHF, and the same blind spots. **The maximally
decorrelated seat is a different vendor's model.** That is what "using codex on the
review panel" means: seat OpenAI's `gpt-5-codex` as a cross-vendor finder, and let
`byModel` attribution tell us — from data, not taste — whether it earns the seat.

This is a natural, not speculative, next step in the exact discussion the commits record.

## 2. How a lens is wired today

Each lens is spawned by an injected primitive:

```js
report = await agent(prompt, { label, phase, model: lens.model || 'opus',
                               effort: lens.effort || 'high', schema: REPORT_SCHEMA })
```

`agent()`, `phase()`, `log()`, `args` are **injected by the Workflow runtime**, not
defined in the repo. Critically: **`agent()` only knows Claude models** (opus, sonnet,
fable, haiku). There is no built-in seam that routes `model: 'codex'` anywhere. So
seating Codex is not a config change — it requires either a subprocess shim or a wrapper
lens (§4).

Three properties of the current harness make an *external, untrusted* model unusually
safe to add — they were built for a different reason and happen to be exactly what we need:

1. **Lenses now emit PROSE; a cheap Sonnet "shaper" maps prose→schema** (commit
   `2554534`, failure #1). The shaper repairs *shape, never substance* — it may not add,
   infer, or invent, and unsupported required fields fall through to BLOCKED. So a Codex
   lens does **not** need to produce our exact JSON; it can emit prose and reuse this path.
2. **The Attest phase independently re-opens every cited file and checks the quote
   verbatim.** A model cannot fabricate evidence without being caught by machinery that
   *does not trust it*. This is the property that makes seating a foreign model defensible.
3. **The panel fails closed.** A lens that produces no valid output after retries
   returns `{ blocked: true }` and the verdict can never read GREEN. A Codex seat that is
   down, unauthed, rate-limited, or times out must land on this same rail — never a
   silent pass. (See the founding anecdote: silence is not consent.)

## 3. What the Codex CLI gives us

`codex exec` is the non-interactive mode, and it happens to expose exactly the four
knobs a review lens needs:

| Need | Codex flag | Notes |
|---|---|---|
| Structured output | `--output-schema <path>` | Enforces a JSON Schema on the final message. Point it at our `REPORT_SCHEMA`. |
| Capture final message | `-o` / `--output-last-message <path>` | Writes the final answer to a file while still printing to stdout. |
| Read-only (a reviewer must not write) | `--sandbox read-only` | **This is the default.** A reviewer edits nothing. |
| Event stream / telemetry | `--json` | stdout becomes JSONL (`turn.completed` carries token usage → feeds `byModel` cost data). |
| Model + effort | `-c model=gpt-5-codex -c model_reasoning_effort=high` | Effort levels `minimal…high,xhigh`, mirroring our `effort`. |
| Hermetic run | `--ignore-user-config`, `--ignore-rules`, `-C <repo>` | No `$CODEX_HOME/config.toml` or `.rules` bleed-through. |
| Prompt input | arg, or `codex exec -` reads stdin | We hand it the same assembled `RULES + PLAYBOOK + context + leads + lens.prompt`. |
| Auth | `CODEX_API_KEY` env var, or ChatGPT sign-in | See §5. |

The combination that matters: **`codex exec --sandbox read-only --output-schema report.schema.json -o out.json -c model=gpt-5-codex -c model_reasoning_effort=high "<prompt>"`** produces a schema-valid review report from a read-only sandbox, non-interactively, with a non-zero exit on failure we can trap. That is a drop-in finder.

## 4. Two integration paths

### Path A — Wrapper lens (a Sonnet seat that shells out to codex)

Add a **caller lens** whose `model` is `sonnet` and whose prompt instructs it to run
`codex exec …` via its shell tool, read `out.json`, and relay/adjudicate the findings.
No harness change at all — caller lenses are already an additional-lens API, and lens
agents already have shell access (the reproduction lens compiles and runs tests; `ranCode`
is a first-class field).

- **Pros:** zero harness modification; works *today*; gives immediate n>1 attribution
  data for task #55; the wrapping Sonnet naturally maps Codex's prose into `REPORT_SCHEMA`.
- **Cons:** the seat's diversity is *partly laundered through Sonnet* — Sonnet writes the
  final report, so a shared-prior failure could re-enter at the relay. Mitigated by the
  shaper discipline (relay shape, not substance) but not eliminated. Also: attribution
  buckets it under `sonnet` unless we tag it, muddying `byModel`.

### Path B — Native subprocess seat (a first-class `codex` model)

Add a `runCodexLens()` in the workflow that spawns `codex exec` directly, reads the
schema-valid `out.json`, and runs it through the **same** `invalidReason()` validation
and the **same** Attest phase as a Claude lens. Tag `lens.model = 'codex'` so `byModel`
and task #55 see a real cross-vendor seat. Fail closed on non-zero exit / empty output.

- **Pros:** a *true* cross-vendor finder — no Claude in the substance path; clean
  attribution; reuses validation + Attest unchanged; the fail-closed rail already exists.
- **Cons:** one real unknown — **can the Workflow runtime spawn a subprocess?** `agent()`
  et al. are injected; whether the sandbox exposes `child_process`/`execFile` needs a
  10-minute spike. If it does not, Path B collapses into Path A (the shell-out has to
  happen inside a lens agent that *does* have a shell).

**Recommendation:** ship **Path A first** as a cheap experiment to collect attribution
data (does Codex confirm anything no Claude seat did?), and promote to **Path B** only if
the seat earns it — which is precisely the "measure, don't answer from taste" rule the
panel already runs on. Do not build B before A produces a single `confirmedFoundByExactlyOneLens`
hit for the Codex seat.

## 5. Deployment: this runs on a Codespaces VM

David's note — this will run on a Codespaces devbox, not the local M5 Air — simplifies
the risk profile:

- **Install** is per-codespace and ephemeral: `npm i -g @openai/codex` (Node 20 is
  present; the spike installed `codex-cli 0.144.1` this way in ~1s). Bake it into the
  devcontainer `postCreateCommand` so every codespace has it.
- **Auth:** the box already has `OPENAI_API_KEY` set, but Codex reads **`CODEX_API_KEY`**
  — a one-line remap (`export CODEX_API_KEY="$OPENAI_API_KEY"`), or a dedicated Codespaces
  secret. **Confirmed in the spike:** a *restricted* key with no `api.model.read` scope
  still authenticates for inference. ChatGPT sign-in is the subscription alternative but
  browser login is awkward headless — **API key via Codespaces secret is the call.**
- **Model:** `-m gpt-5.6` (verified accepted; `gpt-5.6-codex` does not exist — see §0).
- **Sandbox stakes are low:** the codespace is already a throwaway VM, and we run Codex
  `--sandbox read-only` anyway. Even `workspace-write` would only touch an ephemeral
  checkout. The real containment is the Attest phase, not the OS sandbox.
- **Egress:** the codespace must reach `api.openai.com`. Default Codespaces networking
  allows this; note it if egress policy ever tightens.
- **Cost:** each review adds one `gpt-5-codex` high-effort pass over the diff + context.
  `--json`'s `turn.completed.usage` gives exact tokens — wire that into `byModel` so the
  "is it worth its seat" question is answered in dollars *and* in confirmed findings.

## 6. What it would take — concrete

1. **Spike (½ hr):** in a codespace, `npm i -g @openai/codex`, `export CODEX_API_KEY`,
   run `codex exec --sandbox read-only --output-schema <REPORT_SCHEMA.json> -o out.json
   -c model=gpt-5-codex "review this diff: …"` against a known-buggy commit from the
   playbook's specimen set. Confirm it emits schema-valid JSON and a non-zero exit on a
   forced auth failure. Also confirm whether the Workflow runtime can `execFile` (decides A vs B).
2. **Path A (½ day):** add one caller lens `{ name: 'codex-cross-vendor', model: 'sonnet',
   effort: 'high', prompt: <shell out to codex exec, relay findings>, questions: [...] }`.
   Point it at 2–3 of the seven historical defects and see if it flags them.
3. **Instrument:** tag the seat so `byModel` separates it; capture Codex token usage from
   the JSONL. Extend task #55's question: *"any confirmed finding the Codex seat caught
   that no Claude lens did, and any true finding the Codex seat wrongly refuted."*
4. **Decide from data (n>1):** if the seat produces `confirmedFoundByExactlyOneLens` hits,
   promote to Path B (native `codex` model, subprocess seat validated + attested like any
   Claude lens). If it only echoes Claude seats, it has not earned the seat — drop it.

## 7. Risks & how the existing harness already covers them

| Risk | Covered by |
|---|---|
| Codex down / unauthed / times out → silent green | Fail-closed rail: no valid output ⇒ `blocked:true` ⇒ verdict cannot be GREEN. Wire the subprocess's non-zero exit to this path. |
| Codex fabricates a citation | Attest phase re-opens the file and checks the quote verbatim; it does not trust the lens. |
| Codex emits malformed JSON | `--output-schema` on the Codex side + `invalidReason()` + the prose→schema shaper on ours. |
| Diversity laundered through Sonnet (Path A) | Known limitation; the reason Path B exists. Acceptable for the *experiment*, not the durable form. |
| Prompt/context leaks user config or repo `.rules` into Codex | `--ignore-user-config --ignore-rules -C <repo>` for a hermetic run. |
| Cost creep | `--json` usage telemetry into `byModel`; the seat must justify its dollars, same rule as every other seat. |

## 8. Open questions for David

1. **A then B, or straight to B?** I recommend A-first (measure before building the
   native seat). Overridable if you'd rather pay once for the clean version.
2. **Auth:** dedicated `CODEX_API_KEY` Codespaces secret (metered) vs. ChatGPT sign-in
   (subscription, but painful headless). I lean API-key-secret for a headless devbox.
3. **Reviewer or hunter? — decided: hunter first, skeptic too.** The panel's documented
   failures are *finding* failures (miss it, or refute a true finding), and 5 of 6
   historical defects were OMISSIONS. A cross-vendor **skeptic** can only adjudicate what
   a Claude seat already surfaced — it cannot repair an omission. A cross-vendor **hunter**
   attacks the omission blind spot directly, which the spike just demonstrated (4 cited
   omission candidates). Since the tokens are ~free, *also* seat it as a Judge-phase
   skeptic — cross-vendor disagreement is worth the most exactly where the Claude seats
   agree with each other. So: **primary = hunter (finding lens on `gpt-5.6`), secondary =
   skeptic.** The wrapper's Sonnet does double duty as the prose→schema cleanup you noted.

---

*Sources:* [Codex non-interactive mode](https://learn.chatgpt.com/docs/non-interactive-mode),
[Codex config reference](https://learn.chatgpt.com/docs/config-file/config-reference),
[Codex sandboxing](https://developers.openai.com/codex/concepts/sandboxing),
[Codex CLI features](https://developers.openai.com/codex/cli/features).
