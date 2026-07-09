# Remediation sizing and schedule

Written 2026-07-09, after R1, R3, R5, R6 landed complete and R2 (2 of 3 parts) and
R4 (2 of 4 sub-parts) landed partial. Every item below is work that **must be
done** — nothing here is a proposal to skip anything. This is a planning
instrument: what is big, what is blocked, and what can run in parallel.

## Size legend

Sizes are in **focused implementation hours**, including the adversarial review
that every sole-committer / funding-path change now gets before merge (it has
caught a real, merge-blocking bug on four consecutive changes). They do **not**
include waiting on CI, which is why PRs are stacked.

| Size | Hours | Shape |
|---|---|---|
| **XS** | ≤ 1 | One file, mechanical, no design, no cluster. |
| **S** | 1–3 | A few files; unit tests; no cluster; low blast radius. |
| **M** | 3–6 | One coherent PR: code + unit + envtest + adversarial review. |
| **L** | 1–2 days | Multi-PR, or a kind live-proof, or a CRD change, or the funding engine. |
| **XL** | 3+ days | Architectural. Needs a decision first, and changes the shape of other items. |

Three multipliers, learned the hard way:

- **Funding engine or sole-committer path** (`pkg/funding`, `cmd/scheduler/plugin`,
  `run_controller.go`'s adoption/mint/swap) ⇒ adversarial review before merge, and
  budget for it finding something. It has, every time.
- **Kind live-proof required** ⇒ add half a day. Live proofs have caught bugs unit
  tests structurally cannot (the `buildPod` cohort drop, the `sparesPerGroup`
  field-name bug).
- **Blocked on a decision** ⇒ the size is a lie until the decision lands. Five
  decisions gate roughly a third of what is left.

## Where we actually are

| | Item | Status |
|---|---|---|
| ✅ | **R1** phantom-lease leak | complete |
| ✅ | **R3** opportunistic → Promise path | complete |
| ✅ | **R5 + R6** provenance trust anchor + mandatory-scheduler VAP | complete (VAP CEL still wants a kind verify) |
| ◐ | **R2** gang recovery | pt1 de-wedge ✅, pt2 adopt-at-width ✅, **pt3 restart reconstruction** open |
| ◐ | **R4** plugin hot path | pt1 metrics ✅, pt2a compaction primitive ✅, **pt1b caching** + **pt2b settlement** open |

So: **4 of 26 complete, 2 substantially underway** — call it 5.2 of 26 by weight.
Plus off-board infrastructure that was not in the R-list: the e2e-image fix, the
three silent passes (`make verify`, envtest fail-closed, unique CI check names),
the fail-closed review harness, and the CI wall-clock work.

That is slower than the raw count suggests, and it is the right trade. The five
finished items are the P0 correctness core — the ones where the scheduler plugin
is the sole committer of GPU funding and a mistake silently double-spends a
budget or strands a gang. Four of them shipped **because** an adversarial review
found a real defect before merge: a cross-tenant charge (R3), a double-fund (R4
pt1), a live lease settled past the clock (R4 pt2a), and a malleable run killed at
its checkpoint grace (R2 pt2). The remaining set is, on average, materially easier.

## The board

### P0 — correctness at the new committer

| Item | Size | Why | Blocked on |
|---|---|---|---|
| **R2 pt3** restart reconstruction | **L** | Blocked on its own prerequisite: **a Lease records no cohort and no pod name**, so gang membership is only recoverable by string-parsing lease names. Needs durable identity at mint time, plus *delta* re-funding (surviving leases are already charged — funding full width again double-counts). Plugin path; kind live-proof. | — |
| **R4 pt1b** safe cached reads | **L** | The original design was **proven unsafe and reverted**: caching breaks the cross-gang fold's read-your-write, and the sole committer overspends. The corrected approach (fold and PostBind key off *"is the real lease in the snapshot"*, not an in-memory flag) exists only as prose in the log — **no revised design doc**. Highest blast radius left. Kind live-proof. | needs a fresh design pass; staleness bound is David's |
| **R4 pt2b** settlement store | **L** | Turns compaction on. Two caller contracts already written down (clamp `H = min(Now, …)`; add `WindowStart` and invalidate on window movement). Persistence location is an **undecided fork**: Budget `status` sub-resource vs a new kind. Extends the summary to aggregate caps. Kind live-proof. | persistence fork |

R2 pt3 and R4 pt1b **share the same insight** — stop trusting in-memory `minted[]`,
trust the leases actually present. Build them to share it; do pt3 first, since it
is the one that forces durable lease identity.

### P1 — multi-tenant safety

| Item | Size | Why | Blocked on |
|---|---|---|---|
| **R7** namespace tenancy | **L** (pt1 **M**) | `EnvelopeKey` has no namespace, so same-named Budgets in different namespaces alias into one envelope. Six construction sites in `evaluate.go`. Funding engine ⇒ review + **golden regen**, with intra-namespace parity as the rail. | **pt2 (owner identity) is David's** |

Land R7's keying change **before** R4 pt1b/pt2b and R13 — all three touch the same
`EnvelopeKey`/Lease reference surface, and rebasing that churn twice is waste.

### P2 — workload lifecycle

| Item | Size | Why | Blocked on |
|---|---|---|---|
| **R9** rendezvous | **XL** | The biggest fork in the project. **Option A** (finish JobSet lowering) gets the headless Service, stable hostnames, rendezvous env, *and* failure/restart semantics — it **subsumes R8 entirely** and closes the JOBSET track. **Option B** (direct-inject + Service) is ~half the work and leaves R8 fully separate. Real proof is a 2-node kind cluster running an actual `torch.distributed` all-reduce. | **David: A or B** |
| **R8** pod-failure zombie | **L** | A crashed pod leaves the run Running forever, charging budget. New CRD fields, envtest ×4, a `failure-smoke.sh` that doesn't exist yet, and it edits the adoption path. **Its size is 0 under R9 Option A.** | **R9's fork**, and the default policy is David's |
| **R10** false rendezvous comment | **XS** | Two comment blocks. Can be truthfully patched *now* ("not yet implemented, see R9") independent of the fork. | — |

**R9 is the schedule's critical path.** Not because it is hard to start, but because
Option A deletes R8 (an L) and the JOBSET track (an XL), while Option B keeps both.
The decision is worth days.

### P3 — Kubernetes conventions & API hardening

| Item | Size | Why | Blocked on |
|---|---|---|---|
| **R11** status conditions | **L** | Four CRDs gain `status.conditions`; every `Phase`/`Message` write in a 2400-line controller is replaced. Now a **retrofit rather than a blocker**: R2 pt2's "Degraded" was overruled, so nothing is currently waiting on the taxonomy. | — |
| **R12** ownerRefs/finalizers | **M** | Smaller than the spec reads: the pod OwnerReference already landed with R5. Remaining is the Run finalizer that closes leases on delete (force-delete currently **leaks charging leases**) and the Reservation ownerRef. | — |
| **R13** rename `Lease` | **L** | 37 files reference the type. Individually mechanical, but it touches `pkg/funding`, the plugin's PreBind mint, and the controller simultaneously. | **David: name + migration mode** |
| **R14** CRD validation + CEL | **M** | Markers mirroring the existing `validate()`, plus CEL immutability on Lease. **Land in the same pass as R13** — the CEL rules attach to the very Kind R13 renames. | R13 |

R15's finding that the release pipeline never built an image means **no production
install exists yet**, which makes R13's hard-rename-without-migration very plausible
— that would cut it from **L** to **M**. Worth deciding on that basis.

### P4 — admin, release & project hygiene

| Item | Size | Why | Blocked on |
|---|---|---|---|
| **R15** install can't work | **S** | `release.yaml` has **zero** image build/push steps; the chart points at `:latest` tags that were never pushed. 2 files. | — |
| **R16** ServiceMonitor selector | **XS** | Confirmed live bug: the Service never carries the label the ServiceMonitor selects on, so it matches nothing. One label + a Chart.yaml dependency gate. | — |
| **R17** prod overlay | **XS** | Confirmed live bug: `controller.leaderElect` **does not exist as a key**, so 3 prod replicas write concurrently. Scheduler is off in both overlays. 3–4 files. | — |
| **R18** operator runbook | **M** | Docs plus two scripts that don't exist (`break-glass.sh`, `uninstall.sh`), and a live kind test of wedge-and-recover. | describes R6/R12/R13, so write it after |
| **R19** LICENSE + governance | **S** / **M** | XS if MIT or headerless. **M** if Apache-2.0 with headers across 107 Go files. | **David: license + governance** |

R16 and R17 are each **under an hour** and are *real, confirmed bugs in what we
ship*. They are the best ratio on the board.

### P5 — observability & correctness papercuts

| Item | Size | Why | Blocked on |
|---|---|---|---|
| **R21 + R22 + R25 + the stale-node bug** | **L** (one bundle) | All four are in `HandleNodeFailure`. **R21**: a `kubectl cordon` is read as node failure and triggers a destructive swap while the original pod keeps running — **two live copies of the same rank**. **R22**: the reclaim sweep closes *any* co-located run's lease. **R25**: a spare-only node deletion leaks an immortal charging lease, and the caller **string-matches the error text** to swallow it. **Plus** the stale-node-event bug found via the envtest flake: a replayed node event can close a healthy node's leases. Golden regen; live proof. | — |
| **R26** ledger auditor | **L** | The one loop that would have caught R25 and the lease leaks on its own. New controller; its swap-grace window must exceed whatever R21/R22/R25 settle on. | soft: land after the bundle |
| **R20** plugin events | **M** | The plugin emits **zero** Events; a gang stuck in Permit is invisible. Touches `decide()`'s error typing in the hot path — observe only, change nothing. | coordinate `explain.go` with R23 |
| **R23** logs/pods/artifacts | **M** | Three new CLI files. No engine, no plugin. Safe. | pairs with R8 for `--previous` |
| **R24** doc honesty | **S** | ~7 files. `index.md`'s "sole committer" hedge can now simply be **dropped** — R3 made it true. | — |

**The R21/R22/R25 bundle is the highest-severity work remaining.** R21 alone can
produce two live copies of the same distributed-training rank — silent data
corruption, not a crash. It should go first among the unblocked items.

### Tracks

| Track | Size | Note |
|---|---|---|
| **JOBSET** (#17) | **XL** | This *is* R9 Option A. Sized once, not twice. |
| **ROLES** (#21) | **XL** | Only `Roles[0]` is honored today. Multi-role gangs touch the plugin's gang key, the cover, and the pack. |

## Decisions on the critical path

Five decisions gate roughly a third of what is left. They are worth more than any
week of coding:

1. **R9: Option A (JobSet) or B (direct-inject)?** Option A subsumes **R8** (an L)
   and the **JOBSET** track (an XL). This is the single highest-leverage decision.
2. **R13: new Lease kind name + migration mode.** Since no production install
   exists (see R15), a hard rename with no dual-read window is on the table, which
   turns an **L** into an **M**.
3. **R7: is the tenant a namespace or an authenticated owner string?** Gates R7 pt2.
4. **R8: default failure policy** (`Fail` vs `Retry(n)`, per-role vs per-run).
   Moot if R9 = A.
5. **R19: license** (Apache-2.0 vs MIT) and whether governance becomes real.
   Legal, so it should start early even though the code is trivial.

There are also two decisions the specs left implicit that I will surface rather
than silently pick: **R4 pt2b's persistence location** (Budget `status` vs a new
kind — the latter is R13-sized), and **R4 pt1b's acceptable informer-staleness
bound**.

## Suggested order, in parallel lanes

**Lane 1 — highest severity, unblocked, no decisions.** The one I would run first.
1. **R21 + R22 + R25 + stale-node** as one bundle (**L**). Data corruption.
2. **R26** ledger auditor (**L**). The backstop that catches this whole class.
3. **R12** finalizers (**M**). Force-delete currently leaks charging leases.

**Lane 2 — finish P0, sequenced by the shared insight.**
4. **R2 pt3** (**L**) — forces durable lease identity.
5. **R4 pt1b** (**L**) — reuses it; needs a design pass first.
6. **R7 pt1** (**M**) — namespace the `EnvelopeKey` *before* R4 pt2b and R13.
7. **R4 pt2b** (**L**).

**Lane 3 — mechanical, parallelizable, Sonnet.** No dependency on anything above,
and disjoint files, so these can run concurrently with Lanes 1–2:
- **R16** (XS), **R17** (XS), **R10** (XS), **R24** (S), **R15** (S).
That is ~1 focused day for five real, confirmed bugs and the doc-honesty debt.

**Lane 4 — after the decisions land.** R9 (→ R8, JOBSET), R13 + R14 together,
R11, R19, R18, R20 + R23, ROLES.

## Honest schedule

At the observed rate — roughly one **L** or two **M** per focused day, *with* full
adversarial verification on every funding-path change — what remains is:

- 3 XS + 3 S ≈ **1 day** (parallelizable onto Sonnet)
- 5 M ≈ **3 days**
- 9 L ≈ **12 days**
- 2 XL ≈ **7 days** (one of which evaporates if R9 = Option A)

**≈ 20–23 focused days**, or roughly **3–4 weeks**, assuming the decisions land as
they are needed rather than after. Lane 3 running concurrently, and Option A
collapsing R8 + JOBSET, plausibly brings that to **15–18 days**.

The estimate's dominant risk is not any single item. It is that **five undecided
forks sit upstream of about a third of the work**, and two of them (R9, R13)
change the size of other items rather than just their start date.
