# R9 amendment — JobSet as reference, our own primitive

**Status:** design amendment (Fable, 2026-07-09), responding to two David rulings and
to code that postdates every JobSet-borrow document. **Amends:** `R9-rendezvous.md`
(Option A's implementation meaning), `borrow-vs-build.md` §5–§7 (the Option C borrow),
`pkg/lowering/lowering.go` (the seam's fate), `SIZING.md` (the R9/R8/JOBSET rows).
This document is the phasing of record for R9.

**The rulings this responds to:**
1. **R9 = Option A** (finish the JobSet lowering) — the remediation restatement of
   `borrow-vs-build.md` Option C (:109-134), which I recommended.
2. **Clean break, standing policy:** "Never complicate the implementation to support
   side by side. If there is a breaking change, we'll schedule it, stop the jobs, and
   restart. Clean old, clean new." No dual-read windows, no conversion webhooks, no
   migration jobs.

## 0. Verdict, plainly

**The new data changes the recommendation's shape, not its goal. Do NOT borrow the
`sigs.k8s.io/jobset` controller. Borrow JobSet's *design* — roles→replicatedJobs
mapping, headless-service DNS, index-stable pod identity, rendezvous env names,
success/failure-policy semantics — and keep jobtree's own controller as the sole
creator of every pod.** That is option (B) of this pass: *JobSet as reference, our
own primitive.* It delivers everything R9 Option A promised (rendezvous, stable
identity, the failure edge, gang co-termination) while keeping the five things the
repo has shipped since the borrow was decided: the plugin sole-committer, the
pod-annotation contract, the R5/R6 trust anchor, the four CASCADE mint sites, and
the clean-break policy.

I am overruling my own text in three places, explicitly:

- **`borrow-vs-build.md:164-166`** ("owner decision: do C; if JobSet lacks something
  we need, fork it and add it there") — superseded. The validation behind it
  (§6.1, 2026-07-04) checked JobSet against *upstream Kubernetes facts in
  isolation*; it predates the plugin cutover, R5/R6, and CASCADE, and §3 below shows
  those make the real-controller borrow fail on grounds §6 never listed. The
  fallback clause at `borrow-vs-build.md:158-160` ("if 1–3 prove too lossy … own the
  pods, still be a scheduler plugin") anticipated exactly this exit; we are taking
  it — for reasons sharper than Q1–Q3.
- **`borrow-vs-build.md:118-120`** ("JobSet FailurePolicy recreates a failed pod →
  jobtree's scheduler places the replacement onto a held spare slot") — wrong as
  written against the shipped swap. See §2.
- **`borrow-vs-build.md:287-288`** ("the rendezvous-env / headless-DNS /
  success-failure work … is borrowed from JobSet rather than built") — superseded.
  We build it, to JobSet's spec, on the emit path we already own.

## 1. Reconciling David's memory with the docs

David: *"losing swap was the cost of moving to JobSet. I think we decided to use
JobSet as reference and implement our own primitive."*

**On the decision:** no such decision is recorded. The recorded owner decision is the
opposite — borrow the real controller, fork it if lacking
(`borrow-vs-build.md:164-166`), and `pkg/lowering/lowering.go:26-31` (JOBSET-3:
"vendor sigs.k8s.io/jobset so the typed JobSet API is importable") plus
`make-it-real-plan.md:72` (JOBSET-10: "narrow `pods` rule to get/list/watch — JobSet
creates pods now") both encode the real-controller borrow. **But David's memory
describes the correct end state**, and this amendment ratifies it as the decision:
everything built since 2026-07-04 — the sole-committer cutover, CASCADE, R3, R5/R6 —
has *de facto* constructed the own-primitive, and §3 shows the borrow is no longer
compatible with what shipped. If the "reference, own primitive" conversation
happened, it was never written down; it is written down now.

**On swap:** David is right about the swap *as shipped*, and
`borrow-vs-build.md:118-120` is wrong about it. The doc's "swap survives" claim was
written for a lookup-steered swap — the plugin consults controller state and steers
a JobSet-recreated pod onto the held spare (§6.1 Q3, :189-192, itself conditioned on
"provided we persist the held slot across churn"). What was actually built (CASCADE-3,
`cascade-plan.md:54-84`) is a **pod-carried** swap: `emitSwapPod` stamps the consumed
spare's payer provenance and a required nodeAffinity onto one specific pod
(`controllers/run_controller.go:1905-1930`, `controllers/kube/bridge.go:404-408`),
Permit skips the funding gate on the marker (`cmd/scheduler/plugin/plugin.go:161-163`),
and PreBind mints from the carried provenance after validating it against a real
spare lease (`plugin.go:252-259`, `gang.go:350-368`). A JobSet-FailurePolicy-recreated
pod comes from a **shared, immutable pod template** — a batch Job's template cannot be
mutated after creation, and JobSet's elastic path mutates only `parallelism`
(`borrow-vs-build.md:182-188`) — so it structurally cannot carry a per-incident payer
triple, a per-incident `swap-node`, or a per-incident required affinity. Under a real
JobSet controller the shipped swap dies, or is rebuilt as plugin-side inference
(guess "this is a replacement", look everything up) — losing the authenticated,
self-describing contract the whole plugin is built on
(`plugin-cutover-plan.md:37-43` D3, :67-73 D7). So: **the docs record the opposite of
David's memory; the code proves David's memory right.** The one thing JobSet's swap
model genuinely does better — the replacement pod keeps the failed member's
completion index, i.e. rank-stable replacement — we copy as part of phase 9A-1 (§7).

## 2. What changed since the borrow was decided (the evidence)

Everything below postdates `borrow-vs-build.md` (2026-07-04) and
`R9-rendezvous.md`:

1. **The plugin is the sole committer and its entire input is pod metadata.** The
   controller emits unscheduled intent pods; the plugin gangs by
   `LabelRunName + AnnotationCohort` (`cmd/scheduler/plugin/gang.go:98-104`), reads
   width/GPUs/flavor/reason/nonce off the pod (`pkg/binder/binder.go:33-93`), and
   mints at PreBind (`plugin.go:234-304`). D3 chose this precisely to avoid per-pod
   Run/JobSet lookups and count-what-exists races (`plugin-cutover-plan.md:37-43`).
2. **Four mint sites now ride per-pod, per-incident annotations:** grow cohorts
   funded as deltas (`run_controller.go:2049-2055`, `gang.go:159-161`), swap
   (`run_controller.go:1905-1930`), long-lived spare holders
   (`run_controller.go:1591-1639`, `bridge.go:331-340`, `plugin.go:173-182`), and
   Promise (`run_controller.go:1573-1583`) — each live-proven or adversarially
   hardened (`cascade-plan.md:86-116`, IMPLEMENTATION-LOG R3 #4).
3. **R5/R6 made the pod annotation vocabulary a security boundary.** The VAP allows
   jobtree-owned fields only when `request.userInfo.username` is the controller's
   ServiceAccount (`deploy/helm/gpu-fleet/templates/validating-admission-policy.yaml:50-58`).
   The trust anchor is *who created the pod*.
4. **Grow-cohort and expected-width annotations are load-bearing and per-emission.**
   A Job template is immutable; `parallelism` alone is mutable. Every JobSet-grown
   pod would carry the base template's stale `expected-width` and no cohort — the
   plugin could not tell base from delta, and delta funding
   (`gang.go:159-161`) breaks.
5. **R2 pt3 needs durable gang identity on the Lease** — a Lease records no cohort
   and no pod name (`api/v1/lease_types.go:26-37`; IMPLEMENTATION-LOG: "`Spec.Reason`
   is the only durable signal separating grow width from base width — the same
   missing lease identity that blocks pt3's restart reconstruction";
   `SIZING.md:63`). Whatever identity we add, we control it end to end only if we
   control pod identity.
6. **R4 pt1b proved the cross-gang pending fold requires read-your-write**
   (`gang.go:144-150`; IMPLEMENTATION-LOG R4 pt1). Handing pod creation to the batch
   Job controller inserts a third actor with its own backoff into the
   decide→emit→fold timing that analysis has to bound.
7. **JobSet is not installed by the e2e harness and is a new cluster prerequisite**
   (`hack/e2e/versions.env:13-18`; `make-it-real-plan.md:65` JOBSET-3 "document
   JobSet controller as cluster prerequisite"). The own-primitive needs zero new
   prerequisites.
8. **Option C's two "must-validate" sharp edges (elastic width, spare-swap ↔
   FailurePolicy; `borrow-vs-build.md:125-126,144-149`) are now *implemented*,
   differently, on the direct path** — as cohorts and as the provenance-carrying
   swap. The question is no longer "can JobSet do these" but "would we tear out
   shipped, live-proven mechanism to re-house it in a controller that cannot
   express it."

## 3. Why the real-controller borrow now fails (three structural reasons)

**(a) The trust anchor does not survive — and the proposed widening is a laundering
hole.** The JobSet controller creates *Jobs*; the **batch Job controller in
kube-controller-manager** creates the pods. So under Option C the pod-create
`userInfo` the VAP sees is the job-controller's identity (a per-controller SA only if
kube-controller-manager runs `--use-service-account-credentials`; otherwise the
undifferentiated `system:kube-controller-manager`). Widening the VAP's
`isController` (`validating-admission-policy.yaml:50-51`) to trust that identity
trusts a controller that stamps out pods from **every tenant's Job templates**: any
user with ordinary `create jobs.batch` writes a Job whose template carries
`payer-*` + `lease-reason=Swap`, and the trusted job-controller creates the forged
pod for them. The R5 exploit (`R5-provenance-trust-anchor.md:20-26`) reopens with an
extra hop. The prompt's mitigation — "creating a JobSet is RBAC-gated and the
template carries provenance" — does not hold: the Job path exists independent of
JobSet RBAC, and once the creator is a system controller the VAP cannot distinguish
a controller-authored template from a tenant-authored one. Containing it requires
two *more* VAPs (jobtree-owned fields in `jobs.batch` templates restricted to
{jobtree SA, jobset-controller SA}; in `jobsets` templates to the jobtree SA), plus
the kube-controller-manager credential requirement, plus accepting that the pod-level
rule can never again say "one SA creates every jobtree pod." A three-resource
delegation chain where today one CEL comparison suffices. That alone justifies the
own-primitive; the plugin's `spareLeaseProvenanceValid`/`promiseProvenanceValid`
(`gang.go:350-433`) would be demoted from defense-in-depth to the actual boundary.

**(b) Immutable shared templates vs a per-pod, per-incident annotation contract.**
Swap (§1), grow (§2.4), and expected-width all require annotations that differ per
emission or per incident. A ReplicatedJob has one template, frozen at creation.
Every workaround moves information off the pod and into plugin-side lookups —
un-shipping D3 and reintroducing the count-what-exists race it was chosen to kill.

**(c) It forces a permanent dual pod-creation path — the thing the clean-break
policy exists to forbid.** JobSet has no concept of a funded, held, workload-less
spare (`borrow-vs-build.md:242` — "Spares — lost", said of Kueue; equally true of
JobSet: a sleep-forever holder pod is not a Job that completes, and would wreck
`successPolicy{All}`). Spares, swap pods, and Promise gangs would remain
directly-emitted controller pods *forever*, beside JobSet-created active pods. That
is not a migration window; it is a permanent side-by-side. Option C was conceived
when the controller emitted nothing but the gang; post-CASCADE it would split one
gang's pods across two creators with two identity schemes
(`jobset.sigs.k8s.io/jobset-name` + `job-completion-index` vs `LabelRunName` +
`AnnotationCohort`) and two security postures. "New is new, old goes away — no dual
path" (`plugin-cutover-plan.md:12-14`) settled this class of question already.

**What we give up, honestly:** SIG-maintained rendezvous/DNS/failure machinery
(~the residual is small; §7 shows what's actually left to build), and ecosystem
legibility — a jobtree Run's workload is not a JobSet, so Kueue/TrainJob tooling
(`borrow-vs-build.md:207-210`) won't recognize it. Accepted: post-cutover, jobtree
is already the scheduler *and* the quota layer, which is not a Kueue-composable
posture; a TrainJob→Run adapter is future migration surface, not architecture.
The "don't reinvent" principle (`borrow-vs-build.md:22-23`) is not violated by this
amendment — the reinvention already happened, feature by feature, because
requirements (funded spares, provenance-carrying swap, delta-funded grow, Promise)
exist that JobSet structurally cannot express. What remains to build is the small
part JobSet would have given us free (§7), which is R9-Option-B-sized.

## 4. The four CASCADE mint sites under this recommendation

**All four are unchanged.** That is the headline benefit: zero re-plumbing of
shipped, live-proven, adversarially-hardened mechanism.

| Site | Pod creator | Provenance carrier | Node choice | Plugin at Permit / PreBind |
|---|---|---|---|---|
| **Base gang / Grow** | controller `emitCohortPods` (`run_controller.go:1712-1760`; grow cohort at `:2049-2055`) | none — plugin derives payer via cover | plugin (advisory soft affinity from pack, `bridge.go:376-380`) | Permit gangs by `(run, cohort)` at `expected-width`, funds base full / grow delta (`gang.go:159-161`); PreBind `claimPayer` mints (`plugin.go:273-297`) |
| **Spares** | controller `emitSparePods` (`run_controller.go:1591-1639`); rendered as sleep-forever GPU holders (`bridge.go:331-340`) | funded by base cover's leftover payers | plugin (advisory toward pack's spare placements) | Permit: non-gating, waits on gang verdict (`plugin.go:173-182`); PreBind mints `RoleSpare` (`admission.go:205-235`) |
| **Swap** | controller `emitSwapPod`, one pod per incident (`run_controller.go:1905-1930`, driven by `HandleNodeFailure` `:1210-1222`) | pod annotations: consumed spare's `payer-owner/budget/envelope` + `swap-node` | **controller** — required nodeAffinity onto the reclaimed spare node (`bridge.go:404-408,465-483`) | Permit: skips gang+funding gate (`plugin.go:161-163`); PreBind mints from carried provenance after `spareLeaseProvenanceValid` (`plugin.go:252-259`, `gang.go:350-368`) |
| **Promise** | controller `emitPromisePods` (`run_controller.go:1573-1583`); top-ups preserve provenance via `gangProvenance` (`:1824-1865`) | pod annotations: the activation's attributed payer triple | plugin (advisory) | Permit: skips the gate (`plugin.go:161-163`); PreBind mints a naturally-Unfunded lease after `promiseProvenanceValid` charged-envelope check (`plugin.go:260-272`, `gang.go:389-433`) |

**Swap, concretely,** since it anchors David's recollection: the swap survives *because*
we are not moving to the JobSet controller. Nothing about its mechanism changes. Two
things are **added** by R9 (phase 9A-1): the swap pod takes over the failed member's
*ordinal identity* — today it is named `<run>-g<group>-swap-<unixnano>`
(`run_controller.go:1913`), which under rendezvous would hand the replacement a fresh
hostname and break the re-forming process group; it must instead inherit the replaced
member's `<run>-active-<i>` name/hostname (the one genuinely good idea in JobSet's
index-stable replacement, copied). And the failure *edge* (9A-3) must coordinate with
the swap trigger — that interaction is already tracked as the R21/R22/R25 bundle
(`SIZING.md:122`), unchanged by this amendment.

## 5. R5/R6 VAP: resolved by not moving

Under this recommendation the pod creator remains the jobtree controller's
ServiceAccount for **every** pod class — active, spare, swap, promise. The VAP's two
rules (`validating-admission-policy.yaml:52-58`) stand exactly as written; the trust
anchor ("a Lease can be minted only for a pod the jobtree controller created,"
`R5-provenance-trust-anchor.md:62-66`) survives untouched; nothing is widened. The
new pod-spec surface R9 adds (hostname/subdomain, rendezvous env, the headless
Service) is controller-authored like everything else; the Service should get an
ownerReference to the Run (same pattern as `bridge.go:427-440`). The only VAP work
in R9 is a *test*, not a rule change: extend the R5 forgery verification to a pod
carrying forged rendezvous identity. The three-VAP delegation chain of §3(a) is
recorded here as the cost we declined to pay.

## 6. `pkg/lowering` and JOBSET-3

- **JOBSET-3 (vendor `sigs.k8s.io/jobset`) does not happen.** No JobSet objects are
  ever created, so no typed API is needed. JOBSET-4 (the suspend/elastic spike) is
  cancelled; JOBSET-5/9/10's "JobSet creates pods" framing is retired.
- **`pkg/lowering` is deleted** (clean old, clean new — the seam guarded by
  `ErrNotImplemented` at `lowering.go:71-80` has no caller and now never will).
- **The mapping contract survives as the spec of the emit path.** Almost every line
  of the documented contract (`lowering.go:47-70`) is either already true of
  `buildPod` — `schedulerName=jobtree`, nodeName never pinned, `RestartPolicy=Never`,
  gang labels, real GPU request==limit (`bridge.go:355-374`) — or is exactly R9's
  remaining work: rendezvous env when width>1 (`lowering.go:63-64`) and
  successPolicy/failurePolicy semantics (`lowering.go:65-66`). Phase 9A-0 moves that
  contract's text into the bridge/emit documentation before deleting the package, so
  the shape survives the seam.
- `hack/e2e/versions.env:13-18`'s "JobSet is NOT installed" note is updated to say
  the prerequisite is permanently retired, and the false overlay comments at
  `api/v1/run_types.go:47,67` ("rendezvous env" claimed injected) are fixed to
  "injected by R9 phase 9A-2" until that phase lands, then made true (this is R10;
  do it in 9A-0 regardless).

## 7. What we copy from JobSet (the reference contract)

The point of "JobSet as reference" is compatibility-by-convention, so researcher
workloads and migration docs behave as if the shape were JobSet's, without the
dependency:

| JobSet gives | We implement as | Where |
|---|---|---|
| `replicatedJobs[]` = named role gangs | `Run.Spec.Roles[]` → per-role cohort pod sets (exists; single role v1) | `run_controller.go:1936-1942`, `api/v1/run_types.go` |
| Headless Service + stable hostnames | one headless Service per Run (ownerRef'd); `hostname` = pod name, `subdomain` = service | 9A-1 |
| `job-completion-index` / stable ordinal | `cohortPodName` ordinals (already deterministic, `run_controller.go:1693-1698`); swap inherits the replaced ordinal | 9A-1 |
| Rendezvous env via DNS + index | `MASTER_ADDR=<run>-active-0.<svc>.<ns>.svc`, `MASTER_PORT`, `WORLD_SIZE`, `NNODES`, `NODE_RANK` when width>1; never `RANK`/`LOCAL_RANK` (per-process, torchrun's job — `make-it-real-plan.md:69` JOBSET-7 already specced this) | 9A-2 |
| `successPolicy{All}` / `failurePolicy` | per-role `FailurePolicy` (`Fail` default / `Retry(n)` / `Ignore`) + the Failed lifecycle edge + gang co-termination + lease closure `WorkloadFailed` | 9A-3 (= R8's spec) |
| Gang start | already ours: Permit (`plugin.go:151-224`) | shipped |
| Elastic width (KEP-463 parallelism) | **deliberately diverged**: grow cohorts funded as deltas — funding needs a distinct admission unit per delta, which parallelism resize cannot express | shipped (`cascade-plan.md:42-52`) |

## 8. Is R8 still subsumed? — corrected accounting

**R8's *item* is absorbed into R9 as phase 9A-3; its *cost* does not evaporate.**
`SIZING.md:85` ("Its size is 0 under R9 Option A") and `:189` ("one of which
evaporates if R9 = Option A") assumed JobSet's failurePolicy does the work; under
the amended Option A we build the failure edge ourselves, at R8's own L sizing and
to R8's own spec (`R8-pod-failure-handling.md:26-48` — detect PodFailed, per-role
policy, close leases `WorkloadFailed`, unblock followers). What *does* evaporate is
the JOBSET track's XL (`SIZING.md:136`), the JobSet cluster prerequisite, and the
§3(a) VAP rework that a real borrow would have added. R8's "design the handler so it
is a no-op when a JobSet owns the pods" provision (`R8-pod-failure-handling.md:53-54,79`)
is deleted — no JobSet will ever own the pods.

## 9. Re-phased work and sizes

Supersedes the R9 row (`SIZING.md:84`), the R8 row (`:85`), the JOBSET track row
(`:136`), and decision 1 (`:144-145`). Sizes per the SIZING legend
(`SIZING.md:15-21`); funding-path phases carry the adversarial-review multiplier.

| Phase | Content | Size |
|---|---|---|
| **9A-0** ✅ *(2026-07-09)* | Ratify this amendment; delete `pkg/lowering` + retire JOBSET-3/4/5 and JOBSET-9/10's JobSet-creates-pods framing; move the mapping contract's text onto the emit path docs (`controllers/kube.buildPod`); fix the false rendezvous comments (`api/v1/run_types.go` — this is R10); update `hack/e2e/versions.env` note. No behavior change. | **S** |
| **9A-1** ✅ *(2026-07-10)* | Stable rendezvous identity: per-run headless Service (`ClusterIP=None`, `publishNotReadyAddresses`, owned by the Run for GC — `bridge.ensureRunService`); `hostname`/`subdomain` on emitted pods (`buildPod`, DNS-1123-guarded); the swap pod inherits the replaced member's **hostname** and removes the dead pod, but keeps a UNIQUE object name — `apply` diffs by name, so reusing the dead name in one pass no-ops (`emitSwapPod`/`takeOverFailedMember`, `binder.PodManifest.Hostname`). RBAC grants `services` create. Unit + full envtest green. **The doc originally said "replaces the unixnano name"; corrected — the object name stays unique, the HOSTNAME carries the inherited rank identity, which is what rendezvous DNS resolves on.** | **M** |
| **9A-2** | Rendezvous env when `width>1`: `MASTER_ADDR`/`MASTER_PORT`/`WORLD_SIZE`/`NNODES`/`NODE_RANK` per the 9A-0-preserved contract; webhook rejects researcher-set reserved names; none injected when width==1. | **M** |
| **9A-3** | The failure edge (absorbs R8, to R8's spec): `RunRole.FailurePolicy` (`Fail` default, `Retry(n)`, `Ignore`), PodFailed watch predicate, `handleWorkloadFailure` closing leases `WorkloadFailed`, gang co-termination on `Fail`, follow unblock, attempts in status. CRD change + envtests + adversarial review. Coordinate the failure detector with the R21/R22/R25 swap-trigger bundle. | **L** |
| **9A-4** | Live proof: `rendezvous-smoke.sh` (2-node kind, width=2 `torch.distributed` gloo all-reduce → both ranks rendezvous → Completed) and `failure-smoke.sh` (`exit 1` → run Failed, leases closed — pre-R8 it hangs). Plus the R5 forgery re-verify with rendezvous fields. | **L** |
| *(tracked in R2 pt3, not here)* | Durable gang identity on the Lease: cohort + pod name + **ordinal** at mint, so scheduler-restart reconstruction and rank stability share one identity. 9A-1 defines the ordinal; R2 pt3 must record it. Sequencing: 9A-1 before R2 pt3 is *started* or in the same design pass. | (R2 pt3's L) |

Total: S + 2M + 2L ≈ **4–6 focused days** — versus the old accounting of R9 XL
(3+ days) + R8 "free" + a JOBSET-track XL still on the board. Net board effect: one
XL is *cancelled* rather than deferred, at the price of R8's L staying real.

**Decision for David (one, small):** 9A-3's default failure policy — the R8 flag
(`R8-pod-failure-handling.md:56-59`) still stands: recommend per-role, default
`Fail`, `Retry`/`Ignore` opt-in. Nothing else in this amendment needs a new ruling
beyond ratifying the amendment itself.

> ✅ **RULED 2026-07-09 (David): take the recommendation.** Per-role policy, default
> `Fail`, `Retry(n, backoff)` / `Ignore` opt-in. 9A-3 implements it to
> `R8-pod-failure-handling.md`'s spec. No decisions remain open on R9.

## 10. Clean-break compliance

No side-by-side anywhere in this plan: the direct-emit path is the only path before
and after; no JobSet objects, no conversion, no dual identity schemes. The one
breaking change to *running workloads* is 9A-1's pod identity (hostname/subdomain
and the swap-name change) — per the standing policy: schedule it, stop the jobs,
ship, restart. Leases gain fields additively (R2 pt3); the Run CRD gains
`FailurePolicy` additively (9A-3). Conversely, note that the rejected alternative
was the policy's clearest violation: §3(c)'s spare/swap/promise pods would have been
a *permanent* second creation path beside JobSet's, not even a transitional one.

## 11. Superseded-text ledger

- `borrow-vs-build.md:118-120` (swap survives JobSet FailurePolicy) — **overruled** (§1).
- `borrow-vs-build.md:164-166` (owner decision: borrow, fork if lacking) — **superseded by this amendment** (§0, §3).
- `borrow-vs-build.md:287-288` (rendezvous/DNS/failure borrowed, not built) — **superseded** (§7).
- `R9-rendezvous.md:30-37` Option A — **stands as the decision, amended in meaning**: "finish the JobSet lowering" now means "deliver the lowering contract on the emit path we own"; its warning that "the gang key / provenance annotations must survive JobSet's pod template" (:35-37) was the right instinct — they cannot, which is half of §3.
- `R9-rendezvous.md:37,47-48,92` + `README.md:123-125` (Option A subsumes R8) — **corrected**: absorbed, not free (§8).
- `R8-pod-failure-handling.md:53-54,79` (no-op when JobSet owns pods) — **deleted provision**.
- `pkg/lowering/lowering.go:26-31` (JOBSET-3 vendoring) — **cancelled**; package deleted in 9A-0.
- `make-it-real-plan.md:65-73` JOBSET-3/4/5/9/10 — **retired**; JOBSET-7's env-name spec and JOBSET-8's failure-edge intent live on as 9A-2/9A-3.
- `SIZING.md:84-85,136,144-145,189` — **superseded by §9**.
