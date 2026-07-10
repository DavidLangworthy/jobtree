# How the three-model lens split performed — R27 branch review (c74e0ef)

**Status: adjudicated for the criticals; panel incomplete.** The harness Judge panel never finished
(two lifecycle deaths). The critical findings were hand-adjudicated by executable reproduction; the
raised-findings attribution below is complete, and the confirmed-vs-refuted split is reliable for the
criticals. It is NOT a full panel scorecard — treat the sub-critical attribution as raised-only.

Reviewed commit: `c74e0ef` on `fix/r27-invariant-oracle`. Feeds task **#55** ("is one Fable worth
three Opus?"). Related: [[model-split]], [[adversarial-panel-design]], [[oracle-not-more-review]].

## The design: model matched to the lens's cognitive task

The standing split — Opus writes/traces, Sonnet verifies/measures, Fable designs/analyzes — was applied
to the review lenses themselves. Each of the three caller lenses was assigned the model whose strength
matches the *kind of thinking the lens requires*, not just spread around for variety.

| Custom lens | Model | The question it asks | Why that model |
|---|---|---|---|
| oracle-reaper | **Fable** | "Is this invariant false in a legal state?" | a claim about the *legal state space* — abstract refutation, not code-tracing |
| sweep-safety | **Opus** | "What interleaving makes settle.go destroy funded work?" | tracing a concurrent race through the newest code |
| generator-honesty | **Sonnet** | "What does the driver actually reach? What mutations survive it?" | empirical instrument-and-measure |

The four standard lenses ran on the harness's default model assignment (mixed).

## Raised-findings attribution (preliminary)

By model: **opus 6, fable 10, sonnet 5** raised. By lens:

| Lens | Model | Raised |
|---|---|---|
| oracle-reaper | fable | 4 |
| sweep-safety | opus | 2 |
| generator-honesty | sonnet | 5 |
| std:consequence-and-reapers | (mixed) | 6 |
| std:signal-and-identity | (mixed) | 2 |
| std:ledger-lifecycle | (mixed) | 1 |
| std:test-integrity | (mixed) | 1 |

## What actually happened — the split earned its seat

The differentiation was **real, not cosmetic**. Each model surfaced findings inside its assigned lane:

- **Fable** produced the two *critical reaper* findings — `INV-LEASE-HAS-POD` and `INV-TERMINAL-NO-PODS`
  are false on legal states (a drained pod under an open lease; the routine async graceful-deletion
  window `bridge.load` doesn't filter). Pure legal-state-space refutation — the analytical shape Fable
  was assigned. These invariants were added *this branch* (R27b), so this is the panel catching my own
  new code.
- **Sonnet** produced *measured* findings: 0 of 800 generator seeds ever reach pod deletion; specific
  mutations (`baseGangGPUsForRun`, `leaseGroupIndex` default, the `reclaimSquatter` fail-closed branch)
  survive the generator; `mintPending` fabricates physically impossible lease topologies. Quantified,
  not argued.
- **Opus** traced concrete races through `settle.go`: the empty-run-name-label pod reaped via the
  `"namespace/"` doomed key; the mint-after-terminal race reported as "Shirked."

## The strongest signal is a two-model convergence

The reaper pair was found **twice, by two models, by two methods**:

- **Fable / oracle-reaper**, by analysis: the invariant is false in a legal state.
- **Sonnet / generator-honesty**, by measurement: the 800-seed generator stays green *only because its
  "legal world" model excludes pod deletion entirely* — it can never reach the refuting state.

Abstract refutation and empirical coverage-gap, arrived at independently, landed on the same defect from
opposite directions. Neither alone is as convincing as the pair. That convergence is the concrete
argument for paying for heterogeneity rather than running one model three times.

## Task #55 — what the criticals actually show

"Worth it" is not a raised count; it is *confirmed*, and *found-by-exactly-one-lens*. On the five
criticals, hand-adjudicated:

- **Sonnet (generator-honesty) found a confirmed critical no other lens caught.** C4 (the
  `RunnableGPUs`→`baseGangGPUsForRun` mutation caught by nothing) only surfaces by actually
  mutation-testing the suite — an *empirical* act. Neither Fable's analysis nor Opus's trace reached
  it. Sonnet also produced the measurement that explains *why* the C1/C5 reapers shipped green (the
  generator can't reach pod-deletion states). The measurement lens earned its seat distinctly.
- **Fable (oracle-reaper) both found the headline and overreached.** It raised C1 (the real
  INV-TERMINAL-NO-PODS reaper — confirmed, the single most valuable finding) AND C5 (INV-LEASE-HAS-POD
  "reaper" — **refuted** by two skeptics; the invariant is correct). So Fable's critical hit-rate was
  1 confirmed / 1 false-positive. Its analytical strength is real *and* it is the lens most prone to
  calling a correct-but-alarming state a reaper.
- **Opus (sweep-safety) confirmed C2** and traced the settle races that the analytical lens stated
  abstractly.

**The honest answer to "is one Fable worth three Opus": no — the value is diversity, not dominance.**
On this review the empirical lens (Sonnet) caught a confirmed critical Fable and Opus both missed, and
Fable overreached on one critical. The strongest result — C1 — came from *convergence* (Fable's
refutation + Sonnet's coverage measurement), not from any single model. Keep all three; do not swap the
panel for N copies of the best-scoring one.

Caveat: this rests on the five criticals only. The full panel (which would let us measure the
sub-critical confirmed-by-exactly-one-lens rate) never completed.
