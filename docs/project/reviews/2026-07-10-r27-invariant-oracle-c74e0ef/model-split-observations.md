# How the three-model lens split performed — R27 branch review (c74e0ef)

**Status: PRELIMINARY.** Written while the Judge panel is still adjudicating. These are the
*raised-and-attested* numbers, not the final scorecard. The decisive metric — **confirmed** after
adjudication, and which model found a confirmed finding no other lens caught — comes from the skeptic
panel, which died on the 9am UTC session limit and is being re-run via
`resumeFromRunId: wf_5ed1383f-2ce`. This note is updated when that lands.

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

## What this does NOT yet answer (task #55)

"Worth it" is not a raised count. A model that raises 10 plausible-but-wrong findings is worse than one
that raises 3 real ones. The number that decides #55 is **confirmed after adjudication**, plus
**confirmed-found-by-exactly-one-lens**, plus the failure mode that would argue *against* Fable:
**anything the Fable lens missed that an Opus or Sonnet lens caught.** All three come from the skeptic
panel. Until it completes, this stays PRELIMINARY and no verdict on the split is declared.
