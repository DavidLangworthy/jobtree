# Adversarial review — R7 pt2 (funding owner from namespace), cross-vendor panel

**Reviewed commit:** `0b77fbe7d4449beec1cd463474a7353b0e84ee7c` (`0b77fbe`) on
`r7pt2/tenancy-owner-from-namespace`, PR #127.
**Diff under review:** `git diff main...HEAD`, merge-base `e918ca6b` — 8 commits, 82 files,
+1119/−427.
**Run:** `wf_7b4dc0d8-aa0`, harness `.claude/workflows/adversarial-review.js` invoked by
`scriptPath` (not `name`). `skepticQuorum: 2`.
**Date:** 2026-07-24. **Authorised by David** as a pre-merge review, explicitly overriding the
autonomous-run playbook's no-per-PR-review rule for this change.

---

## VERDICT: `BLOCKED` — PARTIAL, AND NOT A CLEARANCE

The harness's own returned verdict is verbatim:

> `BLOCKED — a lens produced no verifiable work; this is NOT a green review`

Phases **Scout → Review** completed. **Attest and Judge did not.** The run hit the session usage
limit at ~18:50 UTC; all four Attest agents and both retrying lenses (`std:test-integrity`,
`codex-sol`'s re-shape) died on it. Because no lens survived attestation, the harness's own
`attribution`, `confirmed` and `unresolved` arrays came back **empty** — the fail-closed rail
working exactly as designed rather than laundering a partial run into a green one.

Per AGENTS.md: **an unadjudicated finding is `UNRESOLVED`, not refuted.** Silence is not consent in
either direction. Every finding below is `UNRESOLVED` except those marked **reproduced**, which
carry a compiled, running test — and the harness's own asymmetric rule is that a reproduction
confirms alone.

**Do not read this record as "the change is clean."** It records what was found, what was fixed,
what was parked, and what nobody adjudicated.

### Why the Judge phase was not run

Two reasons, and the first is the honest one.

1. **The runner has 4 cores, so the harness caps concurrency at `min(16, cores−2)` = 2.** The Judge
   phase over 26 raised findings is 26 × 3 heterogeneous skeptics × (investigator + shaper) ≈ 150
   agents through a 2-wide queue. That is a multi-hour serial run, and it is the same wall that
   killed this review twice already (segments 1 and 2 both died mid-panel and delivered no
   adjudication at all). Continuing to spend the quota on a phase that has never once completed,
   instead of banking verified fixes, was the call taken — announced on issue #132 *before* doing
   it, not rationalised after.
2. The load-bearing findings do not rest on argument: four were independently reproduced here with
   compiled tests (below), which is the strongest evidence class the harness recognises.

**What that costs, stated plainly:** the fixes listed below were **not reaper-checked by the fable
consequence seat**. That is a real gap. If any of them destroys a legal state, this review did not
catch it. The Judge phase is a clean re-run on a bigger box; every finding and its verified
citations are in `findings.json` to feed it.

---

## Citation attestation — done mechanically instead

The harness's Attest phase (a Sonnet agent per lens) died on the usage limit. It was replaced with
a **deterministic** check that is strictly stronger in one respect: every `{file, line, quote}` was
re-read out of `git show 0b77fbe:<file>` — the exact reviewed commit, not the working tree, which
by then carried fixes — and matched verbatim (whitespace-normalised) within ±15 lines, the same
tolerance the harness's Attest prompt allows. No model is involved; it cannot be talked into a pass.

```
report 1 (std:ledger-lifecycle)      14/14 citations verified
report 2 (std:order-dependence)      10/10
report 3 (std:signal-and-identity)   14/14
report 4 (codex-sol)                  6/6
report 5 (std:consequence-and-reapers) 7/7
report 6  SKIPPED — degenerate schema probe, not a lens report
report 7 (std:test-integrity)         6/6

TOTAL: 57/57 citations verified verbatim against 0b77fbe
```

**No lens fabricated a citation**, including the cross-vendor relay. Full output in
`attestation.txt`; the script is `/tmp`-scratch and reproduced in this README's method description.

### One thing the rail caught, worth recording

Journal report 6 is a shaper that returned `summary: "Test summary to check schema formatting works
correctly for this tool call, at least two hundred characters long to satisfy the minLength
constraint…"` with one placeholder answer, one `foo.go:1` citation, and zero findings. It is a
schema probe, not analysis. `invalidReason()` rejected it (1 evidence < `minEvidence` 3; 1 answer <
5 assigned questions) and forced a retry — i.e. the fail-closed validator did the exact job the
harness's opening comment says it exists to do. It is recorded here because "the rail fired" is a
measurement, and this file is where measurements go.

---

## The panel

| Lens | Model | Standard? | Raised |
|---|---|---|---|
| `scout:tells` | sonnet | standard | 9 leads (`leads.json`) |
| `std:ledger-lifecycle` | opus | standard | 4 |
| `std:order-dependence` | opus | standard | 4 |
| `std:signal-and-identity` | opus | standard | 6 |
| `std:consequence-and-reapers` | **fable** | standard | 4 |
| `std:test-integrity` | opus | standard | 3 |
| **`codex-sol`** | **sonnet wrapper → OpenAI `gpt-5.6`** | **caller (cross-vendor)** | **5** |

**26 raised findings across 17 distinct sites.** Usage: 24 agents, 16 completed, 8 killed by the
session limit, 646,581 subagent tokens, 32 minutes wall clock before the wall.

---

## ATTRIBUTION — did the cross-vendor seat earn it?

AGENTS.md asks for this every time, and asks that one review be called an anecdote. **This is n=1.**

The harness's own `confirmedFoundByExactlyOneLens` is unavailable: it is computed over *confirmed*
findings, and nothing was confirmed because Judge never ran. What follows is **raised-by-exactly-one-lens**,
computed by hand from the per-lens reports in `findings.json`. It is a weaker statistic and is
labelled as such.

### Raised by `codex-sol` and by NO Claude seat

| Finding | Severity | Site |
|---|---|---|
| **Current owner binding recomputation retroactively rewrites historical GPU-hour charges** | high | `pkg/funding/evaluate.go:661` |
| **Optional `PaidByBudgetNamespace` permits uncharged GPU claims via unvalidated writers** | high | `api/v1/lease_types.go:61` |

**The first one is the answer to the question the seat was seated for.** Five Claude lenses read the
same diff with the same playbook and none of them framed the owner derivation as a *temporal*
problem. Codex did, and it came with a falsifiable numeric prediction — that a 4-GPU lease running
across a conflict window would read 4 charged GPU-hours, then 0, then **12** once the conflict was
resolved. Compiled and run on this commit, it reads exactly 4 / 0 / 12. A different vendor's prior
produced a defect class the Anthropic seats did not reach for, which is precisely the failure mode
(*"Claude tiers share a prior and fail together"*) the seat exists to break. Parked as **P6**.

The second is real but already booked: it is DECISIONS-NEEDED **F7 sub-question 4** (pt1 shipped the
field optional-with-legacy-fallback instead of the amendment's required + loud rail). Codex found it
independently, without being told.

### Where the seat added corroboration rather than novelty — said plainly

- The **critical** interior-tier exemption (`evaluate.go:220`) was raised by **four** lenses:
  `codex-sol`, `std:signal-and-identity`, `std:consequence-and-reapers`, `std:test-integrity`.
  Codex re-derived it from source under a read-only sandbox **without being told P5 existed** — its
  prompt does not mention it. That is meaningful convergence, not new information.
- Empty-owner pooling in the resolver was raised by five lenses.
- `codex-sol`'s finding 5 (R26 alarms unwired) restates a deferral the authors documented themselves.

The relay itself behaved well and is worth recording: it ran `codex exec` **live** (`CODEX_OK`,
exit 0, 15.9 KB), spot-checked all 17 of codex's citations with `sed -n` before relaying, kept its
own commentary in separately-labelled `RELAY NOTE` blocks so the measurement survived, and used its
own supplementary grep of the three mint sites to **down-scope** one of codex's own findings rather
than inflate it. A relay that argues against its source's severity is doing the job properly.

### Raised by exactly one Claude lens

| Finding | Severity | Lens (model) |
|---|---|---|
| Empty derived owner turns reservation activation into a permanent hard error | high | `std:ledger-lifecycle` (opus) |
| The fail-safe's documented "coast" is false — Unfunded is the resolver's *first* reclaim target | high | `std:signal-and-identity` (opus) |
| `metrics.BudgetKey` lacks a namespace, unlike `funding.EnvelopeKey` | low | `std:signal-and-identity` (opus) |
| The new map iterations have no permutation/repetition rail | low | `std:order-dependence` (opus) |

### The question AGENTS.md asks by name

**"Any confirmed finding the Fable lens missed that an Opus lens caught."** Four, listed directly
above — all four sole-lens findings came from Opus seats, and the fable seat raised none of them.
Two are `high`.

**"Any reaper veto the other roles did not notice."** None — and the fable seat did something more
interesting instead, which has no name in the harness's vocabulary yet. It issued the **opposite of
a veto**, rated `high`:

> *P5 interior-tier exemption narrowing was not a reaper; the parked reaper-veto rationale is
> unsupported.*

The 2026-07-24 partial review parked P5 partly on the worry that narrowing the exemption would reap
the legitimate multi-namespace-pool case — a worry recorded *as if it were a finding*, by a run in
which the reaper-veto lens **never executed**. The seat whose job that is has now looked, and says
the worry does not hold up. P5 remains parked (see its entry), but on one leg instead of two. That
the consequence seat's distinctive contribution was to **attack a parking decision** rather than a
fix is, on n=1, the clearest argument its seat pays for itself.

---

## Findings and disposition

Severity is the raising lens's. `↻` marks a finding independently **reproduced here** with a
compiled, running test.

| # | Finding | Sev | Site | Raised by | Disposition |
|---|---|---|---|---|---|
| 1 | ↻ Interior-tier exemption lets an owner that is both leaf-bound in ≥2 namespaces and named as any Budget's Parent evade `ConflictLeafOwnerSpansNamespaces` | critical | `pkg/funding/evaluate.go:220` | codex-sol, signal-and-identity, consequence, test-integrity | **parked → P5** (owner decision on a ratified tenancy invariant). Behaviour pinned by `TestInteriorExemptionAdmitsALeafOwnerInTwoNamespaces`. Reaper rationale now contested — see attribution. |
| 2 | ↻ A conflicted namespace retroactively un-charges GPU-hours already burned, and bills the conflicted interval once resolved | high | `pkg/funding/evaluate.go:661` | **codex-sol only** | **parked → P6**. Pinned by `TestConflictRetroactivelyErasesAccruedHours` (4/0/12 and 32→0→32 with headroom 8→40→8). |
| 3 | ↻ Empty derived owner drives reservation activation into `FailureReasonInvalidRequest`, returned as a hard error every tick | high | `controllers/run_controller.go:1234` | ledger-lifecycle only | **fixed** — activation now refuses that tick with a message naming the namespace binding, deliberately *not* terminal. Mutation-verified. |
| 4 | ↻ All unbound/conflicted namespaces share the single `""` reclaim/lottery bucket, so unrelated tenants share one ticket | medium | `pkg/resolver/resolver.go:305/306/476/480` | 5 lenses | **fixed** — namespace-scoped, collision-proof bucket key. Mutation-verified (draw rate 11/60 → ~1/2). |
| 5 | `collided[ns]` is last-writer-wins over a Go map; `Conflicts()` order is map order | medium/low | `pkg/funding/evaluate.go:225/231` | order-dependence, consequence, test-integrity | **fixed** — both walks sorted, smallest colliding owner wins. Mutation-verified. |
| 6 | `BindingConflict`'s doc claims R26's auditor consumes it; nothing consumes `Conflicts()` at all | high/medium | `pkg/funding/evaluate.go:173` | ledger-lifecycle, order-dependence, consequence | **fixed (comment)** — the comment now says the fail-safe is currently silent. The *wiring* is F7 sub-question 4, still owner-facing. |
| 7 | The fail-safe's documented "coast" is false: Unfunded is the resolver's first reclaim target | high | `pkg/funding/evaluate.go:256` | signal-and-identity only | **fixed (comment)** — `OwnerOf` now states that coasting means "not billed, not closed by the engine", not "left alone". |
| 8 | `promiseProvenanceValid` no longer pins `seg.Owner`, so a forged Promise pod can stamp a false principal on a real lease | low/medium | `cmd/scheduler/plugin/gang.go:731/732` | signal-and-identity, ledger-lifecycle | **fixed** — `seg.Owner` pinned to the derived owner; **an existing assertion was reversed** (see below). Mutation-verified. |
| 9 | An unbound/conflicted namespace can still mint at PreBind — the promise path skips the funding gate | medium | `cmd/scheduler/plugin/gang.go:732` | codex-sol (run A) | **fixed** — new `funding.OwnerOfNamespace` shares the engine's own `deriveOwners`, so the plugin and the engine cannot drift. Mutation-verified. |
| 10 | `PaidByBudgetNamespace` is optional with no `ValidateCreate` rule | high | `api/v1/lease_types.go:61` | **codex-sol only** | **pre-existing → F7 sub-question 4** (pt1 decision, owner-facing). Not reachable through the sole-committer path: all three mint sites stamp it unconditionally (`binder.go:400`, `admission.go:249`, `run_controller.go:504`) — verified by the relay's own grep. |
| 11 | Binding conflicts have no operational alarm consumer | medium | (R26) | codex-sol | **deferred → F7 sub-question 4**; documented by the authors as out of scope for this PR. |
| 12 | `internal/manifestcorpus` Run manifests still carry the deleted `spec.owner`; no test proves a tenant cannot submit it | medium | `internal/manifestcorpus/corpus.go:20` | codex-sol (run B) | **fixed** — dead field stripped, and a new envtest proves the API server *prunes* a submitted `spec.owner` while the rest of the spec persists. Mutation-verified by re-adding `owner` to the CRD schema. |
| 13 | The committed e2e webhook test asserts the deleted owner validation | high | `test/e2e/smoke_test.go:68` | codex-sol (run B) | **fixed** — retargeted to `allowBorrow=true` + `maxBorrowGPUs=0`, which the CRD admits (`minimum: 0`) and only `RunFunding.Validate()` rejects, so it still proves the R29 point. Tightened to require `Forbidden` only. |
| 14 | The new map iterations have no permutation/repetition rail | low | `controllers/order_independence_test.go:27` | order-dependence only | **fixed** — repetition rail (200 evaluations) *and* a permutation rail (4 budgets, 24 orderings). Honest note: the permutation half is **prospective** — today's nondeterminism is map-order, so only the repetition half fails under mutation. |
| 15 | `metrics.BudgetKey` lacks a namespace field, unlike `funding.EnvelopeKey` | low | `pkg/metrics/metrics.go:237` | signal-and-identity only | **not fixed — dependent on P5.** The key includes `Owner`, and one-owner-per-namespace is the invariant, so two same-named budgets in different namespaces cannot collide *unless* the P5 interior hole is open. Changing exported metric labels is also a dashboard-visible change. Revisit with P5. |
| 16 | Docs still tell users to write `spec.owner` on a Run | — | `docs/examples/worked-examples.md:82`, `docs/migrations/{slurm,kueue}.md`, `docs/project/plan-workload-podspec.md` | codex-sol (partly) + own sweep | **fixed** — field removed; the two migration examples had **no `namespace:` at all**, which under the new model shows an unfundable Run, so both now carry one. `Budget.spec.owner` references untouched — still a real field. |

### An assertion was reversed, deliberately

Playbook class 8 asks whether a modified test is being reshaped to accommodate a defect.
`TestPromiseProvenanceValid` previously asserted *"seg.Owner is now COSMETIC — the check no longer
reads it"* and accepted `Owner: "org:whatever"`. That is now asserted the other way.

The reasoning, recorded so it can be overturned: `seg.Owner` is cosmetic to the **funding decision**
(`Evaluate` bills by `EnvelopeKey` and reads the owner off the real Budget), but it is copied onto
`Lease.Spec.Owner` at mint. Leaving it unpinned does not let a forged Promise pod charge anyone it
could not already charge — it lets it write a **false principal onto a real, GPU-holding lease**,
the first field any audit of this ledger reads. "Cannot mis-charge" and "cannot mis-state who holds
the GPUs" are different guarantees and only the first survived the change. Pinning costs nothing:
`opportunisticCoverPlan` builds every legitimate segment with `Owner: ev.OwnerOf(run.Namespace)`,
the same value the check derives. This is the answer to F7 sub-question 3, which asked exactly
whether `seg.Owner` being cosmetic introduces a laundering path. It did.

---

## Reproductions

All run against `0b77fbe`, in a scratch copy, before any fix was applied.

**Interior-tier exemption (P5), re-confirmed on *this* commit** rather than inherited from the older
`f52d3cf` claim:

```
WITHOUT the interior child:  OwnerOf(tenant-a)="" OwnerOf(tenant-b)=""
                             conflicts=[{tenant-a org:ai LeafOwnerSpansNamespaces} {tenant-b org:ai …}]
WITH one child Budget naming org:ai as a Parent:
                             OwnerOf(tenant-a)="org:ai" OwnerOf(tenant-b)="org:ai"  conflicts=[]
```

One unrelated child Budget in a third namespace switches the fail-safe off for a binding it exists
to catch, because `interior` is keyed on the owner *string*: being a Parent **anywhere** exempts the
owner **everywhere**. Note why nothing caught it — `TestInteriorTierExemptFromInjectivity` is
*correct*, and covers the legitimate shape (pool in one namespace, named as a Parent elsewhere). The
abuse shape, one owner holding both roles, was simply never written down. The playbook's omission
class, inside the test suite.

**Retroactive erasure (P6):** one 8-GPU lease, four hours, 40 GPU-hour cap.

```
BEFORE (one budget):        consumed=32.0  remaining=8.0   class=Owned
AFTER  (2nd owner Budget):  consumed=0.0   remaining=40.0  class=Unfunded
RESTORED (Budget removed):  consumed=32.0  remaining=8.0   class=Owned
```

and codex's own timeline, predicted before it was run:

```
T1 (+1h, bound)       charged = 4.0
T2 (+2h, conflicted)  charged = 0.0
T3 (+3h, resolved)    charged = 12.0     (temporally attributed would be 8)
```

**Resolver bucket collapse:** tenant-a holds one unfunded group, tenant-b nine. With the shared
`""` bucket tenant-a is drawn **11/60**; bucketed per tenant, ~1/2.

**Reservation hard error:** with the guard reverted the test reports back
`reservation default/res: owner and flavor must be set` — for a Run that has no such field.

---

## Gate on the fixes

`make verify` (fmt, vet, generate, antifake, `-race`, **envtest**, golden, helm, krew,
build-flags) **green**, plus the **800-seed quiescence/eviction fuzzer** under `-race` **green**,
with all fixes applied. Every fix was **mutation-verified** individually: the load-bearing line
reverted, the test confirmed red, the line restored.

## Artifacts

| File | What |
|---|---|
| `findings.json` | Every shaped lens report, verbatim, plus the raised set grouped by site |
| `leads.json` | The scout's mechanical diff scan (9 leads) |
| `attestation.txt` | The deterministic 57/57 citation check against `0b77fbe` |
| `codex-hunter-raw.json` | A raw `codex exec` hunter result, schema-valid, as returned |
| `hunt-prompt.md`, `run-hunt.sh`, `report.schema.json` | The exact cross-vendor seat invocation, reproducible |

Raw per-agent transcripts live in the session's workflow directory
(`…/subagents/workflows/wf_7b4dc0d8-aa0/`) and are not committed.

## Operating notes for the next run of this harness

1. **Concurrency is the binding constraint, not tokens.** `min(16, cores−2)` on a 4-core runner is
   2. A panel designed to fan out becomes a serial queue, and the Judge phase never completes.
   Run this on a machine with real cores, or expect Review-only.
2. **`resumeFromRunId` worked across a process restart** within the same session directory —
   segment 3 replayed the Scout and four completed lenses from cache instead of re-billing them.
   It did **not** work across sessions in segment 2. The persisted journal is what makes it work.
3. **Do not pass `commit` for a multi-commit stack.** The harness turns it into `git show <sha>`,
   which is the last commit only. This run passed no `commit` so `DIFF` fell back to
   `git diff main...HEAD`, and put the head SHA in `context` instead.
4. **The OS sandbox for the codex seat needs one host tweak.** Codex's bundled bubblewrap cannot
   create a loopback netns while `kernel.apparmor_restrict_unprivileged_userns=1`; its shell tool
   then fails on every call and it degenerates into web-searching for the repo — a seat that looks
   alive and does nothing. `sudo sysctl -w kernel.apparmor_restrict_unprivileged_userns=0` fixes
   it, and the seat then runs genuinely `--sandbox read-only`, no approval bypass needed.
