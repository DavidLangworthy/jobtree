<!-- Internal architecture-decision doc (docs/project/ is excluded from the built site).
Written 2026-07-04 after the ML-workload research. Captures owner decisions + a borrow-vs-build
analysis so the direction survives a lost session. Provisional on the JobSet/scheduler-framework
coexistence questions in §6, which need validation against current k8s. -->

# Borrow vs Build: jobtree's workload + scheduler layer

## 0. Decisions locked (owner, 2026-07-04)

1. **jobtree becomes a Kubernetes scheduler-framework plugin**, not a `nodeName`-pinning binder. It
   places pods through the scheduler's extension points (Filter/Score/Permit/Bind) so it participates
   in fit-checks, preemption, and DRA instead of fighting them. (Today `bridge.go:290` sets
   `spec.nodeName` directly and skips kube-scheduler — the source of the "kubelet admission is the
   only fit-check" failure mode.)
2. **Roles are formalized** — a Run is a *gang of gangs*: a set of named role-pools (RL:
   `{trainer, sampler, grader, exec}`; plain training: one role). Each role is essentially today's
   single gang (its own template, width, groups, spares, elasticity, criticality). The whole Run is
   one funded, co-admitted unit; reclaim is by per-role criticality.

**Open (this doc's main question):** for the workload materialization + roles + rendezvous + DNS +
success/failure machinery, do we **build our own** or **borrow JobSet** (and/or LeaderWorkerSet)?
Principle: *don't reinvent things.*

## 1. What jobtree actually is (so we know what's at stake)

Non-test Go, by area (2026-07-04):

| Area | LOC | Nature |
|---|---|---|
| `pkg/funding` | 1353 | **Moat** — the RQΛ funding calculus (derived classes, family/sponsor, ranking) |
| `pkg/resolver` | 612 | **Moat** — reclaim ordering (unfunded→spares→shrink→lottery), attested seed |
| `controllers/run_controller.go` | 1812 | Engine — admission, elasticity, completion, funding-coupled placement drive |
| `pkg/pack` `pkg/cover` `pkg/topology` `pkg/binder` | 357+283+248+286 | Topology-aware Cover→Pack→Bind + lease minting |
| `pkg/forecast` | 338 | Reservation ETA |
| `controllers/kube/*` | ~1000 | Bridge (298: pod/lease apply, **the pause-pod body**), reconcilers, webhooks |
| `api/v1` | 1835 | CRDs |
| `cmd/kubectl-runs` | 1399 | CLI |
| **Total** | **~10,200** | |

The shape that matters: **~4–5K LOC is genuine moat** (funding calculus + reclaim + immutable lease
ledger + funding-coupled placement policy). The **workload "body"** — the thing this whole ML thread
is about — is `bridge.go`'s ~300 LOC of *pause-pod glue*, and it's almost entirely **unbuilt**
(rendezvous, headless DNS, success/failure policy, roles, elastic membership, GPU requests). **So the
borrow decision is about avoiding future build, not discarding sunk cost.**

## 2. The borrow-vs-build inventory

| Subsystem | Build or borrow | Candidate to borrow | Verdict & why |
|---|---|---|---|
| **Funding calculus** (derived classes, family/sponsor, borrow caps, unfunded) | **BUILD** | Kueue quota/cohorts is *close* but has no derived-class, audit-by-replay, or unfunded tier | Keep — this is the entire reason jobtree exists. Nothing to borrow. |
| **Reclaim ranking** (proximity→recency→lottery, demote-not-kill) | **BUILD** | Kueue preemption, scheduler-framework preemption | Keep the *policy* (funding-coupled, TLA-specced); the *mechanism* rides the scheduler plugin. |
| **Immutable lease ledger** (audit-by-replay) | **BUILD** | none | Keep — the auditable spine. |
| **Placement bind** (put pod on node) | **BORROW** ✅decided | **scheduler-framework** (Bind extension) | Decided. Stop pinning `nodeName`. |
| **Fit-checks / preemption / DRA participation** | **BORROW** | scheduler-framework | Comes free once we're a plugin. |
| **GPU device exposure** (`nvidia.com/gpu`, `NVIDIA_VISIBLE_DEVICES`) | **BORROW** | NVIDIA device plugin; **DRA** (the k8s direction) | Obvious. Never build device management. |
| **Topology-aware gang placement** (pack-to-empty in fabric domains) | **BUILD (policy) + borrow (host)** | Kueue TAS, Volcano, coscheduling | Keep the pack-to-empty *policy* (funding-coupled, stronger than TAS); express it *as* Filter/Score in the plugin. |
| **Gang / all-or-nothing admission** | **BORROW or thin-build** | coscheduling `PodGroup`, JobSet, Volcano | Express via the plugin's Permit gate over a role-set; JobSet already gives all-or-nothing. |
| **Workload materialization** (pods, roles, rendezvous env, headless DNS, success/failure/restart policy) | **BORROW** (the crux) | **JobSet** (`replicatedJobs`), **LeaderWorkerSet** | JobSet's `replicatedJobs` *is* the gang-of-gangs/roles model, with DNS + SuccessPolicy + FailurePolicy already built and maintained by SIG Batch. See §4–§6. |
| **Elastic membership / fault-tolerant re-rendezvous** | **BORROW** | torchelastic (c10d), JobSet FailurePolicy | Borrow the mechanism; jobtree provides the *slot* (spare) fast. |
| **CRDs / controllers / webhooks** | already borrowed | controller-runtime / kubebuilder | Keep. |
| **CLI** (`kubectl runs`) | **BUILD** (thin veneer) | kubectl | Keep — small, researcher-facing. |

**Punchline:** everything in the "body" column (placement, GPU exposure, materialization, rendezvous,
DNS, success/failure, elastic membership) has a mature k8s primitive to borrow. Everything in the
"brain" column (funding, reclaim, ledger, placement *policy*) has no equivalent and must be ours.
The clean architecture is **brain = jobtree, body = borrowed**.

## 3. JobSet / LWS provenance (since it came up)

`JobSet` and `LeaderWorkerSet` are **Kubernetes SIG Batch** projects (`kubernetes-sigs/jobset`,
`kubernetes-sigs/lws`) — *not* part of Kueue. Kueue is a sibling from the same working group and can
queue/suspend a JobSet, but doesn't own it. So JobSet/LWS are the k8s-native "gang" and
"gang-of-gangs" building blocks, and Kueue is the k8s-native "queue/quota" layer. jobtree overlaps
Kueue (quota) but has a richer brain; jobtree does **not** overlap JobSet (workload shape) — which is
exactly why JobSet is a borrow candidate, not a competitor.

## 4. Why JobSet is the natural "roles" borrow

A JobSet is *a set of replicated Job templates that start together*, with:
- **`replicatedJobs[]`** — literally a list of named role-gangs, each a full pod template with a
  replica count. This is the `roles[]` model from decision #2, already specified.
- **Headless-service DNS + stable pod hostnames** — the rendezvous substrate (`MASTER_ADDR`) the
  podspec plan said jobtree would have to build.
- **`SuccessPolicy` / `FailurePolicy`** (operator=All/Any, `maxRestarts`, rules) — the completion and
  restart semantics `runGangComplete` reimplements narrowly.
- **Exclusive placement** (one replica per topology domain) and **startup ordering**.

Building this ourselves is the ~1–2K LOC the podspec plan (`plan-workload-podspec.md` §3–§6, §8) was
about to write. JobSet is mature, SIG-maintained, and understood by the ecosystem (Kueue, dashboards,
`kubectl`).

## 5. The three options

### Option A — Build our own workload layer (the podspec-plan trajectory)
jobtree owns pods directly; we build roles, rendezvous, DNS, success/failure, elastic membership.
- **Pro:** total pod-granular control (topology packing, spare-swap, per-slice leases, malleable
  width); the "jobtree generates the pods" thesis; no external dependency.
- **Con:** reinvents a mature SIG project; ~1–2K LOC + perpetual maintenance chasing k8s; workloads
  aren't JobSets, so ecosystem tooling (Kueue, PyTorchJob converters, dashboards) doesn't understand
  them. **Violates "don't reinvent."**

### Option B — Be Kueue-with-a-better-brain (borrow JobSet fully; queue/fund it)
jobtree becomes an admission/funding layer that suspends/admits/funds JobSets; JobSet owns pods.
- **Pro:** minimal reinvention; ecosystem-native; roles/DNS/success-failure all free.
- **Con:** **cedes pod-granular control** — JobSet's controller creates/relocates its own pods, so we
  lose topology-aware packing, spare-swap, and per-slice lease attribution: the *mechanism* of the
  moat. We'd differentiate from Kueue only by funding model, in a niche Kueue already owns.

### Option C — Hybrid: lower a Run to a JobSet, place it with our scheduler plugin (recommended)
- The **jobtree `Run` stays the researcher API** (owner, budget, `roles[]`, funding, `follow`).
- jobtree **lowers a Run into a JobSet** (`roles[]` → `replicatedJobs[]`) + its own Budget/Lease
  objects. JobSet gives us pods, roles, DNS, success/failure, restart — **borrowed.**
- JobSet's pods carry `schedulerName: jobtree`. jobtree's **scheduler-framework plugin** applies the
  brain at scheduling time: **Filter/Score** = topology-aware pack-to-empty within fabric domains;
  **Permit** = gang/funding gate (hold the whole role-set until the gang + its funding fit);
  **Bind** = place + mint the per-pod Lease. **Reclaim** = the plugin's preemption path driven by the
  funding ranking.
- **Fault tolerance:** JobSet FailurePolicy recreates a failed pod → jobtree's scheduler places the
  replacement onto a **held spare slot** → torchelastic re-rendezvous → framework reloads checkpoint.
  jobtree supplies the slot fast; the framework carries state forward (not transparent, by nature).
- **Pro:** borrows the *body* (JobSet) **and** the *scheduler mechanism* (framework) while **keeping
  the moat** (funding/topology/leases) exactly where it has to live — at scheduling/binding time.
  Pod-granular placement is preserved (that *is* what a scheduler does). Workloads are JobSets →
  ecosystem-native. Minimal reinvention.
- **Con / must-validate (§6):** the coupling has sharp edges — gang-gating a JobSet via Permit,
  elastic width vs JobSet's replica model, and coordinating spare-swap with JobSet's FailurePolicy.

**Recommendation: Option C.** It is the only one that honors both "don't reinvent" (borrow JobSet +
scheduler framework) *and* "keep the moat's mechanism" (pod-granular funding/topology/leases). It
reframes jobtree honestly: **a funding-aware topology scheduler + a thin Run CRD that lowers to
JobSet**, not "a system that owns pods." Estimated build avoided vs Option A: the entire rendezvous /
DNS / success-failure / restart / role-materialization surface (~1–2K LOC + maintenance); build
added: the lowering (Run→JobSet) and the scheduler plugin (which we were going to need for decision #1
regardless).

## 6. What must be validated before committing to C

These are the load-bearing uncertainties (need checking against current JobSet ≥ v0.x and the
scheduler framework):

1. **Gang gating.** Can the plugin's **Permit** cleanly hold *all* of a JobSet's pods (across
   `replicatedJobs`) until the whole gang + funding fits, and reject/requeue atomically? (coscheduling
   plugin does this per `PodGroup`; JobSet + a PodGroup label is the likely bridge.)
2. **Elastic width.** jobtree's `malleable` grow/shrink vs JobSet's replica model. Can we resize a
   `replicatedJob`'s replicas (or per-role parallelism) live, and does the plugin re-place cleanly?
   This is JobSet's weakest fit — may need per-role scaling or LWS for some roles.
3. **Spare-swap ↔ FailurePolicy.** When JobSet recreates a failed pod, can jobtree guarantee the
   scheduler puts it on the pre-held spare slot (not a fresh scheduling decision)? Interaction of
   `maxRestarts`, `terminationGracePeriod`, and the held lease.
4. **Per-slice leases.** Minting/closing a Lease per JobSet pod at Bind, and keeping the funding
   derivation correct as JobSet churns pods.
5. **Reclaim = preemption.** Expressing "demote-not-kill, unfunded-first, owner-recall" as
   scheduler-framework preemption (or a controller that evicts + lets the plugin re-gate) without
   losing the deterministic, audit-by-replay property.
6. **DRA trajectory.** Whether to target the device plugin now or DRA (where k8s GPU scheduling is
   heading) — affects how the plugin reasons about GPU fit.

If any of 1–3 prove too lossy, the fallback is a **narrow Option A for the pod layer** (own the pods,
still be a scheduler plugin) — i.e., keep the scheduler decision, drop the JobSet borrow. That's why
decision #1 (scheduler plugin) is independent of and safe ahead of the JobSet question.

## 6.1 Validation outcome (2026-07-04, citation-backed) — C confirmed

A research pass validated §6 against current (2025–2026) Kubernetes. **Verdict: Option C is
feasible-with-specific-caveats; no showstopper forces the Option-A fallback.** Owner decision: **do C;
if JobSet lacks something we need, fork it and add it there rather than invent from scratch.** Details:

- **The big new fact this doc predated: native in-tree Workload-Aware Scheduling.** kube-scheduler is
  growing first-class gang scheduling (`Workload`/`PodGroup`, `.spec.schedulingGroup`,
  `gang.minCount`) **and** workload-aware preemption (KEP-4671/5547/5710; alpha in k8s 1.35, advancing
  in 1.36), explicitly to unify the coscheduling/Kueue/Volcano split. This **strengthens** C — the
  ecosystem is standardizing on "scheduler treats a workload as one gang," exactly our model.
  **Maturity caveat (corrected):** WAS is **alpha, off by default**; the API already churned
  (`v1alpha1`→`v1alpha2`), gang's beta slipped to 1.37, the controller-facing API (KEP-6089) is
  unshipped, and Kueue itself hasn't decided how to consume it. So **build our own funding-aware Permit
  gate now; track WAS and prototype behind a feature gate — do not depend on it yet** (see §6.3).
- **Q1 gang gating: FEASIBLE (high).** Framework `Permit` (Approve/Deny/**Wait**) + walking the waiting-
  pods list to `Allow`/`Reject` a whole set atomically is exactly this; coscheduling does it per
  `PodGroup` via Permit. Correction: **Kueue does NOT use Permit** — it gates by keeping the Job
  `.spec.suspend=true` and leaves placement to kube-scheduler; **our design is the coscheduling/WAS
  Permit path, not Kueue's.**
- **Q2 elastic width: FEASIBLE with a real constraint.** JobSet v0.12.0 (2026-05) shipped elastic
  pod-level scaling (mutable child-Job `parallelism`, KEP-463). **So model a role's width as one Indexed
  Job with `parallelism = width` (replicas=1)** — then `malleable` maps to live `parallelism`, and a
  single pod failure is replaced pod-granularly by the Job controller. Scaling `replicatedJobs[].replicas`
  (the *count* of Jobs) stays immutable (JobSet non-goal). For persistent **serving/sampler** roles that
  must resize+roll live, **LWS** (has an HPA `scale` subresource) is the better per-role primitive —
  draw the training-Job vs serving-LWS line per role.
- **Q3 spare-swap: FEASIBLE** — "scheduling re-decides" is not a loss because **we are the scheduler**;
  the replacement pod carries role/index labels, so Filter/Score steers it onto the pre-held spare,
  provided we persist the held slot across churn (our reconciler already owns that). JobSet v0.12 also
  added single-Job `RestartJob` (no longer only whole-JobSet recreate). `podReplacementPolicy` GA'd 1.34.
- **Q4 per-pod leases: FEASIBLE (clean)** — write the Lease CR at PreBind/Bind/PostBind; reconcile via
  ownerRefs; non-atomic so make idempotent. Least risky of the six.
- **Q5 reclaim = preemption: mechanism fits, ONE semantic gap.** Custom PostFilter can do
  "unfunded-first/owner-recall" victim ordering. BUT **"demote-not-kill" is NOT scheduler preemption**
  (preemption *deletes* victims; there is no "keep running, reprice the lease"), and **replay-determinism
  lives in our `pkg/resolver` + lease ledger, not the framework.** So the pattern is: **jobtree's
  resolver decides (deterministic, logged) → a controller evicts (or PostFilter evicts) → the Permit gate
  re-gates freed capacity.** Demotion stays a jobtree controller action. (This is the doc's own preferred
  path — now the confirmed one.)
- **Q6 DRA: target it, keep the fallback.** Core **DRA is GA and default-on in k8s 1.34** (structured
  parameters let a custom scheduler reason about ResourceClaims/Slices natively, incl. multi-node
  NVLink/GB200 topology). But the NVIDIA DRA driver is still ~v0.x and the classic `nvidia.com/gpu`
  device plugin dominates the installed base — so build the resource model around DRA, keep a
  `nvidia.com/gpu` fallback until target clusters are ≥1.34 with the DRA driver.
- **External validation of the whole thesis:** **Kubeflow Trainer v2 (`TrainJob`) is built on JobSet**
  (KEP-2170) — the ML ecosystem is standardizing on JobSet as the workload substrate, so lowering to
  JobSet buys ecosystem alignment, not just a borrowed controller. And **Volcano `vcjob` bundles its own
  scheduler**, which collides with "we are the scheduler" — a poorer fit than JobSet-as-body.
- **Honest caveat on the moat:** Kueue now has **Topology-Aware Scheduling** (beta, default-on v0.14)
  and **ProvisioningRequest**; the "stronger than TAS" gap is closing. But Option B is still correctly
  rejected: Kueue admits at the Workload level and cedes pod placement to kube-scheduler, so it
  *structurally* cannot do our pod-granular topology packing + per-pod leases. C and B aren't exclusive —
  jobtree could still let Kueue own queue/quota while our plugin owns placement, if ever attractive.

**Net: proceed with C on the sequencing in §8. The only thing that pushes to narrow-Option-A is if
parallelism-as-width (Q2) or held-slot persistence (Q3) proves too lossy in the spike — neither looks
blocking.**

## 6.2 Kueue TAS: mirror the conventions, don't take the dependency (decided 2026-07-04)

**TAS is not a placement library — it runs inside Kueue's admission flow.** You cannot use TAS without
Kueue owning ClusterQueue quota + preemption; it computes a topology-domain assignment at admission and
hands final binding to kube-scheduler via node affinity. So "adopt TAS" = "adopt Kueue's admission
model" = **cede the funding moat.** Decision: **mirror TAS's conventions, don't take the dependency.**

- **What TAS covers:** gang co-location — a `Topology` CRD, a node-label hierarchy (block/rack/host),
  and `podset-required-topology` / `podset-preferred-topology` annotations, per-podset. Good enough for
  our fabric-domain + `groupGPUs` need.
- **What it misses (ours):** pack-to-empty (leave big contiguous holes for the next large job — TAS does
  least-fragmenting bin-pack), funding-coupled placement (TAS has no notion of who-pays / funding class
  / reclaim ranking), per-slice lease attribution, and spare placement.
- **Impact on our features if we took full Kueue:**
  - **Family sharing — big cost.** Kueue's hierarchical cohorts + borrowing/lending + `reclaimWithinCohort`
    get ~60–70% (a quota tree ≈ family DAG; reclaim-within-cohort ≈ owner-recall-*ish*), but structurally
    **cannot** express the **unfunded/opportunistic tier** (run over quota, cut first), **demote-not-kill**
    (Kueue preemption deletes victims), or the **audit-by-replay lease ledger** — the three things that
    are the "why jobtree" thesis.
  - **Roles — fine.** Kueue admits multi-podset Workloads (JobSet `replicatedJobs`) all-or-nothing and
    TAS applies per-podset; heterogeneous RL gangs are well-supported. Not a casualty.
  - **Spares — lost.** Kueue has no held hot-standby concept, let alone productive spares. Doesn't fit.
- **The move:** borrow TAS's *conventions* — the `Topology` CRD shape, node-label hierarchy, and the
  `podset-required/preferred-topology` annotation UX — so workloads/JobSet express locality the
  ecosystem-legible way, but implement the actual pack in our **scheduler plugin's Filter/Score**, where
  it stays funding-coupled, mints per-slice leases, and honors spares. We already have `pkg/pack` (357
  LOC of pack-to-empty), so taking TAS's packer saves little and ours knows about funding, which theirs
  never will. **TAS is a compatibility target, not a dependency.**
- Kueue-for-quota-only is rejected for the same reason: quota *is* the moat. (C and B still aren't
  mutually exclusive if that ever changes — Kueue could own queue/quota while our plugin owns placement.)

**TAS fact-check confirmed all of the above (citation-backed, 2026-07-04), with two sharper findings:**
- **The precise mechanism proves the shape mismatch.** TAS computes a domain assignment at *admission*,
  stamps each pod with a `nodeSelector` + a scheduling gate, and a `TopologyUngater` controller removes
  the gate so **stock kube-scheduler binds** — i.e. TAS is an *admission-time nodeSelector injector that
  defers to kube-scheduler*, not a scheduler-framework plugin. Adopting it would put Kueue *above*
  jobtree (two schedulers with overlapping concerns), the opposite of "we are the scheduler." The KEP
  itself rejected exposing the assignment to kube-scheduler, and there is no standalone-TAS path.
- **`reclaimWithinCohort` is owner-recall by *eviction*, not re-rank** — it kills the borrower. Combined
  with "preemption is DELETE-only" and "no unfunded tier," this confirms the three moat features Kueue
  structurally can't do. (Roles fully supported; family-DAG structure genuinely close via hierarchical
  cohorts, GA'd v0.17 — worth studying, not adopting.)

## 6.3 The real long-term topology home: upstream native WAS, not Kueue TAS

For a system that intends to *be* the scheduler plugin, the right integration target is **not** Kueue's
TAS (KEP-2724, welded to admission) but the **native kube-scheduler Workload-Aware Scheduling** track —
specifically **KEP-5732 (native topology-aware scheduling)**, which adds first-class
`PlacementGenerator`/`State`/`Scorer` scheduler-framework plugin interfaces, alongside **KEP-4671**
(gang) and **KEP-5710** (workload-aware preemption). That is where jobtree's funding-coupled topology +
gang + reclaim logic would eventually plug in natively. **But it is alpha and unstable today** (off by
default, API churn, Kueue itself undecided on consuming it). **Posture: build our own topology
assignment + Permit gate now (the assignment→nodeSelector→gate pattern is small and we already have
`pkg/pack`); track KEP-5732/4671 and prototype behind a feature gate; adopt when it stabilizes
(~1.37+).** Borrow Kueue's and WAS's *designs*, not their *stacks*.

## 7. Impact on the near-term plan

This supersedes parts of `plan-workload-podspec.md`:
- The **single `PodTemplateSpec`** decision becomes **`roles[]`** (decision #2); v1 still supports one
  role, so scope is unchanged but the seam is honest.
- The **`nodeName` overlay + GPU-limit injection in `buildPod`** is replaced by the **scheduler
  plugin** (decision #1) and, under Option C, by **lowering to JobSet** (JobSet + device plugin set
  the GPU request; the plugin places). The GPU-resource *gap* finding still stands — it just gets
  fixed by JobSet's real requests + the plugin, not by hand-stamping `nodeName` pods.
- The rendezvous-env / headless-DNS / success-failure work in the podspec plan is **borrowed from
  JobSet** rather than built (Option C).
- Still ours regardless: the funding/topology brain, per-role criticality reclaim, `follow`, leases,
  the zero-GPU path for CPU roles, and the RL role model.

## 8. Sequencing implication

1. Prove the **scheduler plugin** (decision #1) on the *current* single-gang model — replace the
   `nodeName` pin with a Filter/Score/Permit/Bind plugin that reproduces today's placement + funding.
   This is independently valuable and de-risks everything.
2. Validate the **§6 questions** with a JobSet spike (lower a trivial 2-role Run to a JobSet, schedule
   it with the plugin, gang-gate, mint leases).
3. If C holds: build the **Run→JobSet lowering** + `roles[]`. If not: build the pod layer ourselves
   (Option A) but keep the plugin.
4. RL role-pools, per-role elasticity/criticality, and fault-tolerant carry-forward land on top.

---

*Companion docs: `plan-workload-podspec.md` (the mannequin/GPU-gap findings + injection contract, now
partly superseded by Option C), `ml-vision.md` (why-jobtree-for-ML opportunities), `plan-follow-and-eta.md`.*
