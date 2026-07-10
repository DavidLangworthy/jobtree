# Adversarial review — R27 branch (the invariant oracle, the sweep, the quiescence driver)

**Reviewed commit:** `c74e0ef` on `fix/r27-invariant-oracle` (whole branch,
`git diff $(git merge-base main HEAD)..HEAD`, 47 files).
**Fixes landed on:** `6f2ec85`, `ab963d0`, `11d1178`, `f015007`, `0236d81` (same branch).
**Verdict:** **DEFECTS CONFIRMED — 5 critical.** All five adjudicated; four fixed, one refuted.

## How this review was adjudicated (read this before trusting the record)

The harness panel did **not** run to completion. It died twice — first on the Anthropic
session limit (resets 9am UTC), then on a Codespaces idle-stop mid-resume — leaving the
Judge phase partial: **58 of ~112 agents** completed (Scout + all three Review lenses +
Attest + ~18 judges), the rest never ran. The completed agents are preserved in
[`journal-snapshot.jsonl`](journal-snapshot.jsonl) (rebuild-proof; the live journal lives
outside `/workspaces`), and [`RESUME.md`](RESUME.md) holds the one-step resume pointer.

Rather than pay the resume tax a third time (each restart re-runs the in-flight judges),
the findings were **hand-adjudicated** from the banked review evidence + partial judge
verdicts, and — for every critical — by **executable reproduction against the pure
engine** (build the state, run the Go test, watch it fail, fix, watch it pass, confirm the
check still fires on the genuine bad state). Executable reproduction is stronger than a
skeptic's prose; the tradeoff is the loss of formal panel independence on the items only
one lens raised. Where the banked panel disagreed (applyResolution), the disagreement is
recorded below and settled on evidence, not vote count.

The raised-findings model attribution is in
[`model-split-observations.md`](model-split-observations.md) (task #55).

## Every finding, with its disposition

A finding with no disposition is an open wound. All are dispositioned.

### Critical

| # | Finding | Lens | Disposition |
|---|---|---|---|
| C1 | **INV-TERMINAL-NO-PODS is a reaper** — fires on the ordinary graceful-deletion window after every completion; `bridge.load` doesn't filter `DeletionTimestamp`, so a terminal run's Terminating pod re-lists and panics the next reconcile of *any* run | oracle-reaper (fable), sweep-safety (opus), std | **fixed in `6f2ec85`** — `PodManifest.Terminating`, excluded from the oracle's pod count; apply still sees it. Both directions pinned (silent on Terminating; still fires on a present pod). Reproduced against a live envtest apiserver before fixing. |
| C2 | **SwapDeclined half-plane** — the decline branch closes the spare lease but leaves its pod holding GPUs the ledger calls free; strands forever if the run re-admits inside grace | sweep-safety (opus), std | **fixed in `ab963d0`** — decline branch now drops the pod, as the accepted-swap path does. |
| C3 | **applyResolution reaps a run inside its checkpoint-grace window**, deleting checkpoint writers before the deadline | std:consequence | **fixed in `11d1178`** — held parked until the deadline; Reconcile:167 reaps on expiry. Contested (one trace-only refutation #23 said "correct behaviour"); settled by `releaseRun`'s own contract + executable repro. |
| C4 | **RunnableGPUs mutation** (`runnableGPUsForRun`→`baseGangGPUsForRun`) caught by neither generator nor suite | generator-honesty (sonnet) | **fixed in `f015007`** — coverage test; verified by hand mutation (fires INV-WIDTH-ASSEMBLED under the mutation, silent under correct code). |
| C5 | **INV-LEASE-HAS-POD is a reaper** — claimed false on external pod loss | oracle-reaper (fable) | **REFUTED** (two skeptics, #28/#30). The flagged state (open lease, zero pods) is not legal — it *is* the immortal-lease bug the oracle exists to catch; in prod it logs+counts, not panics. Keep the invariant. The real gap it exposes — nothing *heals* external pod loss — is **pre-existing → #32 (R26)**. |

### High

| Finding | Disposition |
|---|---|
| External pod loss leaves an open lease with no closer, forever | **pre-existing → #32 (R26 ledger auditor).** INV-LEASE-HAS-POD correctly alarms it; the healer is R26's job. Same root as C5. |
| INV-TERMINAL-NO-PODS graceful window (second lens, high) | **fixed in `6f2ec85`** (= C1). |
| Slot-conflict collapses to node granularity, defeating R22's exact-GPU premise (the `#ordinal` is a per-lease local index, not a physical GPU id) | **pre-existing → #37 (R2 pt3, durable lease identity).** Architectural; the ordinals the engine mints cannot express physical-GPU identity. |
| `mintPending` fabricates physically impossible lease topologies | **deferred → #58** (test-driver defect, not engine). |
| A `leaseGroupIndex`→"0" default mutation is caught by nothing | **deferred → #61** (coverage; INV-GROUP-STAMPED gives partial cover). |

### Medium

| Finding | Disposition |
|---|---|
| `removeSparePodOnNodes` ignores the placement group (R28b) | **fixed in `ab963d0`** — group-aware now (`podGroupIndex`). |
| Declined-swap strands the spare holder pod | **fixed in `ab963d0`** (= C2). |
| Empty-run-name label reaped by the sweep via key `"namespace/"` | **fixed in `0236d81`** — both doomed-building loops skip empty run names (fail safe). |
| INV-TERMINAL-NO-PODS graceful window (medium lens) | **fixed in `6f2ec85`** (= C1). |
| `shrinkRun` insufficient-groups error path strands closed groups' pods | **pre-existing → #59.** |
| Same-run stale-lease `ReclaimedBySpare` closure leaves the pod on the swap-target node | **deferred → #60.** |
| Counting every pod role in `Run.Pods` lets a surviving spare mask an active-set deletion | **deferred → #32 (R26)** — per-role/per-group lease↔pod correspondence. |
| Quiescence driver's legal world excludes pod deletion (why the reapers shipped green) | **deferred → #58.** |
| `reclaimSquatter` fail-closed decline branch never exercised by the generator | **covered by hand tests** (`TestSwapDeclines…`); generator gap → #58. |
| Mint-after-terminal race reported as `Shirked` though nothing shirked | **deferred → #61** (test-panic flake surface under a narrow race). |
| `runPhaseTracker` severity-lattice mutation caught only by hand-written order-independence tests | **no action — adequately covered.** The mutation *is* caught; the generator simply isn't the thing that catches it. |

### Low

| Finding | Disposition |
|---|---|
| Own-stale-lease `ReclaimedBySpare` removes no pod on the spare's slots | **deferred → #60** (same site as the medium above). |
| `groupIndexForPodIndex` is a second implementation of `pack.deriveGroups`, pinned only by a 5-tuple example test (class-7 clone) | **deferred → #61.** |
| `applyResolution` unit tests not backstopped by the oracle | **deferred → #61** (the new grace test now runs under `CheckSteady`). |
| Orphan-run rule cannot cover a same-name recreated Run | **deferred → #57 (R12).** The Run finalizer makes the orphan-run premise unreachable. |

## What the scout flagged that the lenses then cleared

The scout's class-1/2 leads on `reclaimSquatter`'s multiple exit points and `releaseRun`'s
"CALL IT ONLY ON A TERMINAL RUN" comment-as-enforcement were traced by the sweep-safety and
std lenses and found **not** to be live immortal-lease defects on this branch (every terminal
phase writer pairs with a whole-run closure). They remain the right things to re-check on the
next edit to those functions — the leads are in [`raised-findings-digest.json`](raised-findings-digest.json).

## What this review teaches the playbook

The single highest-value finding — the two invariants added this branch, one a reaper — came
from **two models by two methods converging**: fable's oracle-reaper lens refuted it by
analysis, sonnet's generator-honesty lens showed the generator *couldn't reach* the refuting
state. The lesson already in the taxonomy (an invariant that is wrong is a reaper) now has a
second: **a green generator is only as honest as the events it models** — the quiescence
driver excluded pod deletion, so it certified two reapers as safe. That gap is #58, and it is
the reason C1/C5 shipped green.
