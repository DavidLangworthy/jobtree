# Remediation sizing and schedule

> **Updated 2026-07-09 with two decisions from David**, which move real weight:
> - **R9 = Option A** — finish the JobSet lowering. Subsumes **R8** and the **JOBSET** track.
> - **R13 = clean break.** *"Never complicate the implementation to support side by side.
>   If there is a breaking change, we'll schedule it, stop the jobs, and restart."*
>   No dual-read window, no conversion webhook, no migration Job.
>
> Net effect: **~20–23 focused days → ~14–17.** The clean-break rule is a project-wide
> policy, not an R13 detail, and it shrinks R4 pt2b and R2 pt3 as well.
>
> **R9's shape then changed under a Fable design pass** — see
> [R9-jobset-amendment.md](R9-jobset-amendment.md). We borrow JobSet's *design*, not its
> controller: a real JobSet would break R5's trust anchor (its pods are created by the
> **batch Job controller**, not us) and force a permanent dual pod-creation path. The day
> count is unchanged; the architecture is not.

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
- **Blocked on a decision** ⇒ the size is a lie until the decision lands. Two of the
  five have now been made (R9, R13); the three that remain block nothing high-severity.

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
| **R2 pt3** restart reconstruction | **L−** | Blocked on its own prerequisite: **a Lease records no cohort and no pod name**, so gang membership is only recoverable by string-parsing lease names. Needs durable identity at mint time, plus *delta* re-funding (surviving leases are already charged — funding full width again double-counts). Plugin path; kind live-proof. | — |
| **R4 pt1b** safe cached reads | **L** | The original design was **proven unsafe and reverted**: caching breaks the cross-gang fold's read-your-write, and the sole committer overspends. The corrected approach (fold and PostBind key off *"is the real lease in the snapshot"*, not an in-memory flag) exists only as prose in the log — **no revised design doc**. Highest blast radius left. Kind live-proof. | needs a fresh design pass; staleness bound is David's |
| **R4 pt2b** settlement store | **L−** | Turns compaction on. Two caller contracts already written down (clamp `H = min(Now, …)`; add `WindowStart` and invalidate on window movement). Persistence location **settled by the clean-break rule**: Budget `status` sub-resource (a dedicated object existed only to keep old summaries readable). Extends the summary to aggregate caps. Kind live-proof. | — |

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
| **R9 = Option A**, amended | **~4–6d** | **Decided, then re-scoped** by [R9-jobset-amendment.md](R9-jobset-amendment.md): borrow JobSet's *design*, not its controller. Our controller stays the sole pod creator, so R5's trust anchor, the four CASCADE mint sites, and the clean-break rule all survive. Phased 9A-0…9A-4. **Cancels the JOBSET track's XL outright.** | 9A-3 wants the failure-policy default |
| **R8** pod-failure zombie | **absorbed into 9A-3**, at its own **L** | Item folded into R9; **cost not eliminated**. We build the failure edge ourselves — JobSet's `failurePolicy` is no longer doing it for us. | — |
| **R10** false rendezvous comment | **XS** | Two comment blocks. Patch truthfully *now* ("not yet implemented, see R9") — do not wait for 9A to land. | — |

**R9 was the schedule's critical path, and the amended Option A shortens it.** Option B
would have cost R9-B (**L**) + R8 (**L**) + the JOBSET track still owed (**XL**) ≈ 8 days.
The amended plan is ≈ 4–6 and cancels the track. Note this is **not** a further speedup
over the previous revision: the phases are the same shape (S + 2M + 2L). What changed is
that the architecture is now compatible with what we have already shipped.

### P3 — Kubernetes conventions & API hardening

| Item | Size | Why | Blocked on |
|---|---|---|---|
| **R11** status conditions | **L** | Four CRDs gain `status.conditions`; every `Phase`/`Message` write in a 2400-line controller is replaced. Now a **retrofit rather than a blocker**: R2 pt2's "Degraded" was overruled, so nothing is currently waiting on the taxonomy. | — |
| **R12** ownerRefs/finalizers | **M** | Smaller than the spec reads: the pod OwnerReference already landed with R5. Remaining is the Run finalizer that closes leases on delete (force-delete currently **leaks charging leases**) and the Reservation ownerRef. | — |
| **R13** rename `Lease` | **M** | **Decided: clean break.** 37 files reference the type; individually mechanical. No dual-read, no conversion webhook, no migration Job — that was the **L**. Still touches `pkg/funding`, the plugin's PreBind mint, and the controller at once. | name only (`GPULease` recommended) |
| **R14** CRD validation + CEL | **M** | Markers mirroring the existing `validate()`, plus CEL immutability on Lease. **Land in the same pass as R13** — the CEL rules attach to the very Kind R13 renames. The pair is ~1 day, not 2–3. | R13 |

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

## Impact of the two decisions (2026-07-09)

### R9 = Option A, **amended**: JobSet as reference, our own primitive

> Superseded in detail by **[R9-jobset-amendment.md](R9-jobset-amendment.md)** (Fable,
> 2026-07-09). Read that; this is the summary.

David's decision stands — *finish the lowering* — but a Fable design pass changed what
"lowering" means. **Do not borrow the `sigs.k8s.io/jobset` controller.** Borrow JobSet's
*design* (roles→replicatedJobs, headless-service DNS, index-stable identity, rendezvous
env names, failure-policy semantics) and keep jobtree's own controller as the sole
creator of every pod. This is David's own recollection — *"JobSet as reference, implement
our own primitive"* — which **no doc had recorded**, and which the code has been *de facto*
building since the plugin cutover.

Three structural reasons the real-controller borrow now fails, none of which existed when
`borrow-vs-build.md` chose it:

1. **The trust anchor does not survive, and widening it is a laundering hole.** JobSet
   creates **Jobs**, not pods; the **batch Job controller** in kube-controller-manager
   creates the pods. So the VAP's `isController` check
   (`validating-admission-policy.yaml:50-51`) would have to trust an identity that stamps
   out pods from *every tenant's Job templates*. Anyone with ordinary `create jobs.batch`
   writes a Job whose template carries `payer-*` + `lease-reason=Swap`, and the trusted
   controller creates the forged pod for them — **R5's cross-tenant charge, reopened with
   one extra hop**. Containing it needs two more VAPs, a `--use-service-account-credentials`
   assumption about kube-controller-manager, and giving up the rule that *one* SA creates
   every jobtree pod. The plugin's `spareLeaseProvenanceValid` / `promiseProvenanceValid`
   would be demoted from defense-in-depth to *the* boundary.
2. **Immutable shared templates vs a per-pod, per-incident annotation contract.** A
   `ReplicatedJob` has one template, frozen at creation. Swap, grow, and expected-width all
   need annotations that differ per emission or per incident. Every workaround moves
   information off the pod into plugin-side lookups — un-shipping the self-describing-pod
   contract the plugin is built on.
3. **It forces a *permanent* dual pod-creation path.** JobSet has no concept of a funded,
   held, workload-less spare: a sleep-forever holder pod is not a Job that completes, and
   would wreck `successPolicy{All}`. Spares, swap pods, and Promise gangs stay
   controller-emitted **forever**, beside JobSet-created active pods — two creators, two
   identity schemes, two security postures. That is not a migration window. It is exactly
   what the clean-break policy forbids.

**On swap, David was right and the doc was wrong.** `borrow-vs-build.md:118-120` claimed
"JobSet FailurePolicy recreates a failed pod → jobtree's scheduler places the replacement
onto a held spare slot." That was written for a *lookup-steered* swap. What shipped
(CASCADE-3) is a **pod-carried** swap: `emitSwapPod` stamps the consumed spare's payer
provenance and a required nodeAffinity onto one specific pod. A pod recreated from a
shared, immutable Job template structurally cannot carry a per-incident payer triple. The
docs record the opposite of his memory; **the code proves his memory right.** (And my own
earlier framing of this as "a required node-affinity doesn't fit a uniform template" was
the shallow version of the argument — the deep one is the trust anchor.)

The one thing JobSet genuinely does better — a replacement pod keeps the failed member's
completion index, i.e. **rank-stable replacement** — we copy, in 9A-1.

**Re-phased (supersedes the phases in the previous revision):**

| Phase | Content | Size |
|---|---|---|
| **9A-0** | Ratify the amendment; delete `pkg/lowering`, retire JOBSET-3/4/5; move its mapping contract onto the emit-path docs; fix the false rendezvous comments (**this is R10**); correct `hack/e2e/versions.env`. No behavior change. | **S** |
| **9A-1** | Stable rendezvous identity: per-run headless Service (Run ownerRef), `hostname`/`subdomain` on emitted pods, and the swap pod **inherits the replaced member's ordinal** instead of a `unixnano` name. Sole-committer path ⇒ adversarial review. | **M** |
| **9A-2** | Rendezvous env when `width>1`: `MASTER_ADDR`/`MASTER_PORT`/`WORLD_SIZE`/`NNODES`/`NODE_RANK`; webhook rejects researcher-set reserved names; nothing injected at `width==1`. | **M** |
| **9A-3** | The failure edge — **absorbs R8, to R8's own spec**: `RunRole.FailurePolicy`, PodFailed watch, `handleWorkloadFailure` closing leases `WorkloadFailed`, gang co-termination, follow unblock. CRD change + envtest + review. Coordinate its failure detector with the R21/R22/R25 bundle. | **L** |
| **9A-4** | Live proof: `rendezvous-smoke.sh` (2-node kind, real `torch.distributed` all-reduce) and `failure-smoke.sh` (`exit 1` → Failed, leases closed — pre-R8 it hangs). Plus an R5 forgery re-verify with the rendezvous fields. | **L** |

**≈ 4–6 focused days.** The **JOBSET track's XL is cancelled outright**, not deferred —
along with the JobSet cluster prerequisite and the VAP rework a real borrow would have
needed. **R8's item is absorbed; its cost is not.** My earlier "its size is 0 under Option
A" assumed JobSet's `failurePolicy` did the work; we now build that edge ourselves, at R8's
own **L**. Corrected below.

A sequencing constraint falls out: **9A-1 defines the pod ordinal, and R2 pt3 must record
it on the minted Lease.** Rank-stable replacement and scheduler-restart reconstruction want
the same identity. Do 9A-1 before R2 pt3 starts, or design them together.

### R13 = clean break, and the rule generalizes

*"Never complicate the implementation to support side by side."* No dual-read, no
conversion webhook, no migration Job. This is a **project-wide policy**, and it is
cheap to hold because R15 established that `release.yaml` builds **no images at
all** — there is no production install to migrate.

| Item | Was | Now | Why |
|---|---|---|---|
| **R13** rename `Lease`→`GPULease` | **L** | **M** | A mechanical rename across ~37 files + CRD + RBAC + docs + regen. The *migration* was the L. |
| **R14** CRD validation + CEL | M | M | Unchanged — but lands in the same pass as R13, so the pair is ~1 day, not 2–3. |
| **R4 pt2b** settlement store | **L** | **L−** | The undecided persistence fork **resolves**: put the summary in Budget `status`. A dedicated object existed only to make old summaries readable across a change — recompute from the ledger instead. |
| **R2 pt3** restart reconstruction | **L** | **L−** | Freely add the cohort label + pod-name annotation to minted Leases. Reconstruction need not cope with unlabelled legacy leases. |

When a spec offers "dual-read window vs hard rename," the answer is now always the
hard rename, recorded in `IMPLEMENTATION-LOG.md`.

## Decisions on the critical path

Two of the five are now **made** (2026-07-09), and they were the two that changed
the *size* of other items rather than just their start date:

- ✅ **R9: Option A** (finish the JobSet lowering). Retires R8 and the JOBSET track.
- ✅ **R13: clean break** — hard rename, scheduled outage, no side-by-side. Also
  resolves R4 pt2b's persistence fork by policy.

Three remain, and none of them blocks the highest-severity work:

1. **R7: is the tenant a namespace or an authenticated owner string?** Gates R7
   **pt2 only**; pt1 (namespacing the `EnvelopeKey`) proceeds regardless, and pt1 is
   the piece that must land before R4 pt2b and R13.
2. **R8/9A-2: default failure policy** (`Fail` vs `Retry(n)`, per-role vs per-run).
   Now expressed as a JobSet `failurePolicy`, so the decision is smaller — but it is
   still needed before 9A-2.
3. **R19: license** (Apache-2.0 vs MIT) and whether governance becomes real. Legal,
   so start it early even though the code is trivial.

One implicit decision still stands, and I will surface it rather than silently pick
it: **R4 pt1b's acceptable informer-staleness bound**. (R4 pt2b's persistence
location is now settled by the clean-break rule: Budget `status`.)

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

**Lane 4 — now unblocked by the two decisions.** **9A-0 design pass first**, then
9A-1…9A-4 (retiring R8 and JOBSET); **R13 + R14 together** (clean break); R11, R19,
R18, R20 + R23, ROLES.

## Honest schedule

At the observed rate — roughly one **L** or two **M** per focused day, *with* full
adversarial verification on every funding-path change.

**Before the two decisions:** ≈ 20–23 focused days.

**After them:**

| Bucket | Items | Days |
|---|---|---|
| XS + S | R10, R16, R17, R24, R15, R19, 9A-0 | **~1.5** |
| M | R12, R14, R18, R20, R23, R13, 9A-2, 9A-4 | **~4** |
| L | R2 pt3, R4 pt1b, R4 pt2b, R7 pt1, R11, R21+R22+R25 bundle, R26, 9A-1, 9A-3 | **~11** |
| XL | ROLES | **~3** |

**≈ 14–17 focused days.** With Lane 3 (the mechanical items) running concurrently
on Sonnet, **≈ 11–13**.

Where the ~6 days went:

- **R9 = A (amended)**: R9-B (L) + R8 (L) + JOBSET (XL) ≈ 8 days → 9A-0…9A-4 ≈ 4–6 days.
  The **JOBSET track's XL is cancelled**, not deferred. R8's *item* is absorbed as 9A-3;
  its **L cost is not** — we build the failure edge rather than inheriting JobSet's.
- **R13 = clean break**: R13 **L → M**, and it pairs with R14 in one pass. Net **−1**.
- **The clean-break rule**, applied beyond R13: R4 pt2b's persistence fork resolves
  to Budget `status` (no dedicated object, no migration), and R2 pt3 may add lease
  labels freely without coping with unlabelled legacy leases. Net **−0.5**.
- Removing the two decision-wait risks from the critical path is worth more than the
  raw days: **nothing that is now blocked is also high severity.**

The dominant remaining risk is **9A-3** (grow / swap / Promise under JobSet). Swap
hard-targets one node with one spare's provenance, and that does not fit a uniform
pod template. If 9A-0's design pass concludes swap must stay a directly-emitted pod,
that is a documented exception, not a failure — but it should be decided *before*
9A-1 rather than discovered during it.
