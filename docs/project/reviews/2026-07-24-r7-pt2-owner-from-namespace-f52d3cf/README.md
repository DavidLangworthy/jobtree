# R7 pt2 — namespace-derived funding owner — adversarial review (PAUSED)

- **Reviewed commit:** `f52d3cf` (branch `r7pt2/tenancy-owner-from-namespace`, PR #127),
  full 8-commit stack via `git diff main...HEAD` against `origin/main` `eade379`
  (launched against `5043674`; `main` advanced during the run — the diff is committed-ref
  deterministic regardless).
- **Harness:** `.claude/workflows/adversarial-review.js` via `scriptPath`. 5 standard Claude
  lenses (ledger-lifecycle, order-dependence, signal-and-identity, consequence-and-reapers,
  test-integrity; opus/fable) + a `codex-sol` cross-vendor seat. `skepticQuorum=2`.
  Phases: Scout → Review → Attest → Judge.
- **runId:** `wf_cd9db73e-4d3` (session `e7c65415`).
- **Verdict:** ⏸ **PAUSED / UNRESOLVED — the run did not adjudicate.**

## Why it is paused

The run hit a **usage limit** and was interrupted at 2026-07-24T13:30Z, mid-**Review**
(`[Request interrupted by user]` recorded on two review agents). **Attest and Judge never
ran**, so there is no skeptic adjudication, no reaper-veto pass, and no `attribution`
return. Per AGENTS.md this record is **not green**: the completed lenses' findings are
`UNRESOLVED` except where a lens ran its own compiled reproduction (a reproduction confirms
alone).

The prior autopilot turn intended to sleep until the quota reset and **resume the same run**
(`resumeFromRunId`). That resume is **same-session-only** and this run is from session
`e7c65415`; the resuming turn is a new session, so a cache-replay resume is unavailable.
Re-launching a fresh full panel is a ~2h / ~3M-token, day's-quota operation that AGENTS.md
and the autonomous-run playbook reserve for **David** — the autopilot does not launch it.

## What completed (raw returns in `journal-snapshot.jsonl`)

| Agent | Model | Lens | Status |
|---|---|---|---|
| `a6855e2a…` | sonnet | Scout (mechanical diff scan) | ✅ returned |
| `a31323ad…` | opus | ledger-lifecycle | ✅ returned (with compiled reproduction) |
| `a8864f7c…` | opus | order-dependence / last-writer-wins | ✅ returned (with compiled metamorphic run) |
| `afdffa98…` | opus | (review lens) | ⏸ interrupted, no result |
| `a8b5eea1…` | fable | (review lens) | ⏸ interrupted, no result |
| `codex-sol` | gpt-5.6 | cross-vendor caller seat | ✖ never recorded a result |

No Attest, no Judge, no skeptic quorum ran.

## Findings and dispositions

### F1 — CRITICAL (CONFIRMED by reproduction): interior-owner exemption defeats the leaf-owner-spans-namespaces fail-safe
- **File:** `pkg/funding/evaluate.go:220` — `if _, isInterior := interior[owner]; isInterior { continue }`
- **Taxonomy:** class 1 (immortal lease) / class 5 (identity coarsening).
- **Claim:** an owner that is BOTH directly leaf-bound (its own `Budget.Spec.Owner`) in two
  runnable namespaces AND named as some `Budget.Spec.Parents` entry (interior) is exempted
  from `ConflictLeafOwnerSpansNamespaces`. Both namespaces then resolve to that owner with
  zero recorded conflict; `cover.NewInventory` is owner-keyed cluster-wide, so a Run in
  tenant-a mints a senior, non-recallable **Owned** charge against tenant-b's envelope that
  no reclaim/lottery sweep can ever close.
- **Reproduced three ways:** the opus ledger lens (compiled, 20 simulated hours, lease
  classes Owned at h=0,5,10,15,20 and never demotes); the codex-sol cached finding
  (`codex-sol-cached-finding.md`); and **independently by the reviewing autopilot** on
  `f52d3cf` — `OwnerOf(alice)=="org:ai"`, `OwnerOf(bob)=="org:ai"`, `conflicts==[]` with an
  interior child present, vs. `OwnerOf(alice)==""` + two `LeafOwnerSpansNamespaces` conflicts
  without it.
- **Disposition:** **deferred → owner decision (DECISIONS-NEEDED.md, "R7 pt2 — interior-owner
  exemption").** The interior exemption is **intentional** per the R7 design
  (`R7-tenancy-amendment.md` §4/§5, C-4: *"Interior tiers may span admin namespaces (nothing
  classes Owned against them)"*), and the design books the residual "a Run whose derived owner
  is an interior node" hazard to the **RBAC precondition + R26 alarm 3** (both posture /
  not-yet-implemented), **not** to the injectivity fail-safe. The finding shows the design's
  leaf/interior dichotomy is unsound when one owner occupies both roles. Whether to narrow the
  exemption (risking the legitimately-allowed multi-namespace-pool case — a potential reaper)
  or to accept it and rely on implementing R26 alarm 3 is a **tenancy design decision**, not a
  mechanical fix. Not guessed. **The reaper-veto lens that would vet any proposed fix did not
  run.**

### F2 — HIGH (UNRESOLVED): the exemption's test asserts only the safe subcase
- **File:** `pkg/funding/tenancy_r7_test.go:93` (`TestInteriorTierExemptFromInjectivity`).
- **Taxonomy:** class 8 (test proves the safe subcase, not the dangerous one). The test binds
  the interior owner in exactly one namespace, so a corrected fail-safe and the buggy one pass
  it identically. Tied to whatever F1 is decided to be. Under-quorum → `UNRESOLVED`.

### F3 — LOW (UNRESOLVED): `collided[ns] = owner` is last-writer-wins (diagnostic only)
- **File:** `pkg/funding/evaluate.go:225`.
- **Taxonomy:** class 3. The order-dependence lens confirmed by execution (X vs Y attribution
  flips 38/262 over 300 runs; conflict-slice order also map-nondeterministic). **Decision plane
  is invariant** (500-permutation `state.Leases` shuffle → 1 distinct outcome); only the
  `Conflicts()` **diagnostic** is nondeterministic, with no production consumer today (R26's
  auditor is the intended future consumer). Correct fix is a commutative fold + sorted output,
  not a deterministic map order. Under-quorum → `UNRESOLVED`; safe to fold in alongside the F1
  decision.

### F4 — LOW (UNRESOLVED): `resolver.ownerOf` falls back to raw `run.Namespace` when Evaluation is nil
- **File:** `pkg/resolver/resolver.go:480`.
- **Taxonomy:** class 2 (comment-as-enforcement). Adjudicated benign by the ledger lens (a
  lottery/reclaim bucket key only when no funding facts exist; opens/closes no lease), a
  coupling/hygiene concern. Under-quorum → `UNRESOLVED`.

## Attribution (the honest, partial answer)

The owner asked: *what did `codex-sol` catch that no Claude seat did?* **In this partial run,
nothing that is attributable.** The `codex-sol` seat never recorded a result before the
interrupt, and its **cached** finding (the interior-owner S-1 hole) was **independently
reproduced by the opus `ledger-lifecycle` Claude seat** with a compiled 20-hour simulation.
So the cross-vendor seat surfaced no *unique* defect here; the cross-vendor value in this run
was **corroboration**, not a defect only it saw. A real `confirmedFoundByExactlyOneLens`
number requires the Attest/Judge phases, which did not run.

## To resume / complete (David's call)

Re-launch a fresh panel against the current head (committed refs), archive over this record:

```
Workflow({ scriptPath: ".claude/workflows/adversarial-review.js", args: { /* same R7 pt2 config, head <current> */ } })
```

(Cross-session `resumeFromRunId: "wf_cd9db73e-4d3"` will only cache-replay from within
session `e7c65415`.)
