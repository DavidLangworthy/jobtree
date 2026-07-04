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
