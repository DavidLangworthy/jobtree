<!-- Internal master plan (docs/project/ is excluded from the built site). Produced 2026-07-04 by a
multi-agent, code-grounded planning pass. Unifies the Option-C architecture (borrow-vs-build.md) and the
fake-features remediation (fake-features-audit.md). Tracks are structured for parallel subagent execution. -->

# Make jobtree Real — Master Remediation Plan

*Unifies the LOCKED Option-C architecture (`borrow-vs-build.md`) with the 26-finding fake-features remediation (`fake-features-audit.md`). Structured for parallel execution by subagents. Branch baseline: `feat/follow-deps`. Source-of-truth docs live on `docs/ml-workload-plan`.*

---

## 1. North Star

**From simulator to product.** jobtree today is a genuinely sophisticated GPU scheduling/accounting *brain* (`pkg/funding`, `pkg/resolver`, the immutable lease ledger, funding-coupled placement) wired to a *mannequin body*: every pod it creates is `registry.k8s.io/pause:3.10` with **zero GPU requests** (`bridge.go:35,291-294`), placement is a hand-stamped `spec.nodeName` (`bridge.go:290`) that skips kube-scheduler entirely, and `RunSpec` has no image/command/resources field at all (`run_types.go:23-31`). One fake trunk sterilizes a whole tree of otherwise-real features (completion, follow, elastic, node-failure swap, ETA).

**The Option-C architecture, in 4 lines:**
1. **Brain stays ours** — `pkg/funding` (who pays), `pkg/resolver` (reclaim order), the lease ledger, and pack-to-empty topology policy are the moat; nothing borrowable replaces them.
2. **Placement becomes a real kube-scheduler-framework plugin** (`schedulerName: jobtree`) owning Filter/Score (pack-to-empty), Permit (gang + funding gate), Bind/PreBind (per-slice Lease mint), PostFilter (resolver-driven demote-not-kill reclaim) — instead of pinning `nodeName`.
3. **Workload body is borrowed** — a `Run` *lowers to a JobSet* (`roles[]` → `replicatedJobs[]`), which supplies real pods, roles, headless DNS, success/failure/restart. LWS for persistent serving roles.
4. **Mirror Kueue TAS conventions, don't adopt it; track native WAS (KEP-5732/4671) but build our own Permit gate now; fork JobSet if it lacks something.**

**The single discipline:** *No feature is "done" until a real cluster e2e (kind/minikube — not envtest) exercises it with **zero hand-injected state**.* No test may set `pod.Status.Phase`, `spec.nodeName`, or a terminal `Lease.Status` to fake the output the system was supposed to produce. Docs must match code at file:line. No CRD field may ship unread.

---

## 2. The Tracks (A–G)

Seven tracks. **A (PLUGIN)** and **B (JOBSET)** are the two halves of the Option-C trunk and sit on the critical path; **C (CASCADE)** un-fakes everything hanging off that trunk; **D (TRUTH)**, **F (TESTINFRA)**, **G (CLI)** are fully parallel starting now; **E (ROLES)** is the gang-of-gangs fast-follow gated on A+B.

---

### Track A — PLUGIN (critical path, root)

**Objective:** Replace the `nodeName`-pinning binder with a real scheduler-framework out-of-tree plugin that owns Filter/Score/Permit/Bind/PostFilter, keeping `pkg/funding`/`pkg/resolver`/the reservation-ETA loop as-is. **Prove bit-for-bit parity with today's single-gang placement+funding before any JobSet/roles work lands on top.**

**Why:** Today `run_controller.go` decides funding fit *and* exact placement synchronously in one Reconcile (`pack.Planner` at `:985-1004`), then `binder.Materialize` bakes the node into both Pod and immutable Lease in the same pass; `bridge.go:280-297` stamps `spec.nodeName` and creates pods with **zero requests**, so kubelet admission never runs and two pods can double-book a GPU. Decision #1 fixes this: fit-checking, gang admission, and preemption ride the real framework. This track does **not** depend on JobSet landing (borrow-vs-build §8 step 1).

| id | title | files | dependsOn | ‖ | effort |
|---|---|---|---|---|---|
| PLUGIN-1 | Scaffold scheduler binary + register no-op jobtree plugin (Filter/Score/Permit/PreBind/PostFilter pass-through); `KubeSchedulerConfiguration` profile `jobtree`; second Deployment | `cmd/scheduler/main.go`, `cmd/manager/main.go`, `go.mod`, `config/scheduler/`, `deploy/helm/.../scheduler-deployment.yaml` | — | no | L |
| PLUGIN-2 | Split pod materialization into unbound "intent" pods vs scheduler-decided placement; drop `NodeName` pin from `PodManifest`, carry topology-domain hint + labels; rework node-failure spare-swap helpers (`:664-724,1483-1591`) to delete-and-replace not procedural rewrite | `pkg/binder/binder.go`, `controllers/kube/bridge.go`, `controllers/run_controller.go` | PLUGIN-1 | no | XL |
| PLUGIN-3 | Port pack-to-empty node selection into Filter/Score (reject wrong-domain/flavor; Score prefers *less* free — pack-to-empty) against live `framework.NodeInfo`, not the old `node.Status.Capacity` read; unit-test against `pkg/pack/pack_test.go` fixtures for policy-identity | `pkg/pack/pack.go`, `pkg/topology/snapshot.go`, `pkg/topology/labels.go`, `pkg/pack/pack_test.go` | PLUGIN-1 | **yes** | L |
| PLUGIN-4 | Permit: gang gate across role-set (group by `LabelRunName`+`LabelGroupIndex`, Wait-then-Allow, coscheduling-style) + atomic funding gate via `Evaluation.NewAdmission/Take`. **Resolve the key design decision:** controller-time cover/inventory becomes *advisory* (reservation/ETA + whether to make intent pods); Permit is the single authoritative width committer. Concurrency regression test: two Runs racing Permit never over-commit an envelope | `pkg/funding/admission.go`, `pkg/funding/evaluate.go`, `controllers/run_controller.go` | PLUGIN-2 | no | XL |
| PLUGIN-5 | PreBind/Bind: mint per-slice Lease at *actual* bind time (reuse `buildLease` shape, source paid-by from Permit's committed Admission, node from real bind); make `bridge.go apply()` tolerate leases the scheduler wrote; idempotent name from pod UID | `pkg/binder/binder.go`, `controllers/kube/bridge.go`, `api/v1/lease_types.go` | PLUGIN-4 | no | L |
| PLUGIN-6 | PostFilter: resolver-driven reclaim; **demote-not-kill stays a controller action** (publish resolver decision, return Unschedulable, controller evicts/demotes, freed pods re-gate through Permit). Emit attested lottery seed via real `EventRecorder` (zero exist today) | `pkg/resolver/resolver.go`, `controllers/run_controller.go`, `controllers/kube/reconcilers.go` | PLUGIN-4, PLUGIN-5 | no | L |
| PLUGIN-7 | GPU resource model: real `nvidia.com/gpu` requests/limits in `buildPod`; `nodeSelector[LabelGPUFlavor]`; GPUType→RuntimeClassName; DRA-ready branch behind a flag (nvidia.com/gpu fallback). Runs against today's pause container immediately | `controllers/kube/bridge.go`, `api/v1/run_types.go`, `pkg/topology/labels.go` | PLUGIN-1 | **yes** | M |
| PLUGIN-8 | Reconcile annotation ledger against real node allocatable; sum Bound pods' real requests per node vs `Status.Allocatable`, flag drift, delete `PodGPUAnnotation` reconstruction — closes the kubelet-admission race | `controllers/kube/bridge.go`, `controllers/kube/reconcilers.go`, `controllers/run_controller.go` | PLUGIN-7 | no | M |
| PLUGIN-9 | Re-run full existing scenario/regression suite against the plugin; reproduce identical funding/placement outcomes (parity deliverable); rewrite any hand-set phase/nodeName tests to drive the real plugin | `controllers/kube/scenario_test.go`, `pkg/binder/binder_regression_test.go`, `pkg/resolver/resolver_regression_test.go` | PLUGIN-2,4,5,6 | no | L |
| PLUGIN-10 | kind e2e: real scheduler binary, stub device-plugin advertising `nvidia.com/gpu`, real admission, real oversubscription→PostFilter→controller eviction with logged seed — the exit-criteria enforcement mechanism | `controllers/kube/scenario_test.go`, `cmd/scheduler/main.go`, `Makefile` | PLUGIN-8, PLUGIN-9 | no | L |

**Exit:** no `NodeName:` assignment outside the plugin pkg; kill the plugin binary → pods stay Pending (real scheduling gate); one authoritative width committer (Permit) proven by concurrency test; every Lease carries the *actual* bound node; real `nvidia.com/gpu` requests with kind e2e showing kubelet queuing on exhaustion; seed via real Event; demote-not-kill never uses framework delete-preemption. **Closes #1, #2, #9, #23.**

---

### Track B — JOBSET (critical path, body)

**Objective:** Lower a `Run` to a real JobSet so real images/commands/env/volumes run with a real GPU request, real rendezvous env, and JobSet-sourced success/failure — retiring the pause-pod mannequin. Scope stops at the JobSet object boundary; node placement is Track A's job, consuming the `schedulerName: jobtree` pods this track produces.

**Why:** `RunSpec` has no workload surface; the plan-doc `spec.template` design was never merged and is superseded by `roles[]`. `binder.Materialize` + `buildPod` synthesize one pause pod per chunk with GPU count as a string annotation nothing enforces (audit #1/#2). No JobSet dependency, scheme, RBAC, or e2e exists today — all genuinely new work.

| id | title | files | dependsOn | ‖ | effort |
|---|---|---|---|---|---|
| JOBSET-1 | Add `RunSpec.Roles[]` (`RunRole{Name,Template PodTemplateSpec (+PreserveUnknownFields),Width,GPUsPerPod,GroupGPUs,Spares}`); validate exactly one role in v1, `TotalGPUs==Width*GPUsPerPod`, reject researcher-set NodeName/SchedulerName/RestartPolicy | `api/v1/run_types.go` | — | **yes** | M |
| JOBSET-1b | Decide criticality's fate: ship `Criticality` **only** with a real resolver-ranking consumer, or hold it out of v1 (recommended B: land with resolver work, don't repeat the accepted-but-unread pattern) | `api/v1/run_types.go`, `pkg/resolver/resolver.go` | JOBSET-1 | **yes** | S |
| JOBSET-2 | `make generate manifests`; verify non-shallow deepcopy of nested PodTemplateSpec + PreserveUnknownFields suppresses schema inlining (262144-byte limit); commit both CRD copies; `make verify-generate` gate | `api/v1/zz_generated.deepcopy.go`, `config/crd/bases/...runs.yaml`, `deploy/helm/.../crds/...runs.yaml` | JOBSET-1 | no | S |
| JOBSET-3 | `go get sigs.k8s.io/jobset` (v0.12.x, KEP-463 elastic); register `jobsetv1alpha2.AddToScheme`; document JobSet controller as cluster prerequisite; decide install mechanism | `go.mod`, `go.sum`, `cmd/manager/main.go` | — | **yes** | S |
| JOBSET-4 | Spike/go-no-go: validate `.spec.suspend` gate, live per-role `parallelism` resize (KEP-463), and successPolicy/failurePolicy landing on `status.conditions` pollable by `load()`; confirm hostname/`JOB_COMPLETION_INDEX` convention; **fork task if (a)/(c) fail** | `docs/project/plan-workload-podspec.md` | JOBSET-3 | no | M |
| JOBSET-5 | Implement `lowerToJobSet(run)`: deep-copy role Template into one ReplicatedJob, `parallelism/completions=Width`, `replicas=1`, `schedulerName=jobtree`, no nodeName, force namespace/name/RestartPolicy=Never, stamp `LabelRunName/GroupIndex/RunRole`, preserve researcher fields | `pkg/binder/binder.go`, `controllers/kube/bridge.go` | JOBSET-2, JOBSET-4 | no | L |
| JOBSET-6 | Inject real `nvidia.com/gpu` request==limit=`GPUsPerPod`; delete `PodGPUAnnotation` as source of truth (`load()` reads real `Resources.Requests`); default `/dev/shm` emptyDir(Memory) | `controllers/kube/bridge.go`, `pkg/binder/binder.go` | JOBSET-5 | no | M |
| JOBSET-7 | Gated rendezvous env when `Width>1` (MASTER_ADDR from JobSet DNS confirmed in spike, MASTER_PORT, WORLD_SIZE, NNODES, NPROC_PER_NODE, NODE_RANK from JobSet's index env); never RANK/LOCAL_RANK; none when Width==1; reject researcher-set reserved names at admission | `pkg/binder/binder.go`, `controllers/kube/bridge.go` | JOBSET-5, JOBSET-4 | **yes** | M |
| JOBSET-8 | successPolicy `{All}` + real failurePolicy (maxRestarts+rules); surface `status.conditions` through `load()`; **close the "Failed pod hangs the run forever" gap** (add Failed branch to `runGangComplete`/`completeRun`); add JobSet watch to `SetupWithManager` | `controllers/run_controller.go`, `controllers/kube/bridge.go`, `controllers/kube/reconcilers.go` | JOBSET-5 | no | M |
| JOBSET-9 | Delete `buildPod`/`pauseImage` + raw-Pod create/delete loop; replace with JobSet create/delete diff following the Lease/Reservation diff pattern — one path, not a fake beside a real one | `controllers/kube/bridge.go` | JOBSET-6,7,8 | no | M |
| JOBSET-10 | RBAC + Helm: `jobset.x-k8s.io` ClusterRole rule; narrow `pods` rule to get/list/watch (JobSet creates pods now); keep no-wildcard CI check green; NOTES.txt prerequisite | `deploy/helm/.../rbac.yaml`, `deploy/helm/.../NOTES.txt` | JOBSET-9 | **yes** | S |
| JOBSET-11 | Real kind e2e: submit a Run (Width=2, busybox `sleep 5; exit 0`), assert real JobSet/parallelism/GPU-request/rendezvous-env, real pods Succeeded (kubelet, not hand-set), Run→Completed via real watch, and `exit 1`→Failed via real failurePolicy | `controllers/kube/scenario_test.go`, `Makefile`, `.github/workflows/ci.yaml` | JOBSET-9 | no | L |
| JOBSET-12 | Docs truth-pass: rewrite kueue.md "PodTemplate generation automatically", add SLURM/torch-env table to slurm.md, update quota-semantics.md injected-vs-owned contract, mark superseded plan-doc sections closed | `docs/migrations/kueue.md`, `docs/migrations/slurm.md`, `docs/project/quota-semantics.md`, `docs/project/plan-workload-podspec.md` | JOBSET-9 | **yes** | S |

**Exit:** no hand-set terminal phase anywhere; docs describe the real roles[]/JobSet code; every RunRole field has a real reader (criticality ships with a consumer or not at all); GPU accounting reads real requests; failurePolicy proven by an `exit 1`→Failed e2e; `make verify-generate` green; `buildPod`/`pauseImage` gone; kind e2e with real JobSet controller; output consumable unmodified by Track A. **Closes #1, #2; unblocks #5, #14, #20.**

---

### Track C — CASCADE (critical path, un-fake downstream)

**Objective:** Once the trunk is real (A+B), make every downstream feature with correct-but-inert controller code actually fire against real pods/kubelets/exits: completion, follow-gating, elastic grow/shrink, node-failure spare-swap with honest state semantics, opportunistic spare use, and a real ETA writer.

**Why:** One fake trunk sterilizes a tree of real code. `runGangComplete`, `evaluateFollow`, `growRun`/`shrinkRun`, `HandleNodeFailure`, `mirrorETA` are all correctly written and green — but every test proves the plumbing by hand-injecting the exact terminal state (`pods[i].Status.Phase = PodSucceeded`) the feature was meant to derive. Re-verify each against a real cluster, delete every injection, close the two structural gaps (spare counted as fully consumed; no ETA writer exists).

| id | title | files | dependsOn | ‖ | effort |
|---|---|---|---|---|---|
| CASCADE-1 | Delete hand-injected completion (`scenario_test.go:240-247,303-310`); new `test/e2e/` proving completion+follow via a real container exiting 0 on kind; keep a scoped unit test of `runGangComplete`/`evaluateFollow` given synthetic manifests (not "scenario") | `controllers/kube/scenario_test.go`, `controllers/run_controller.go`, `controllers/kube/reconcilers.go`, `test/e2e/` | JOBSET-1, PLUGIN-1 | **yes** | L |
| CASCADE-2 | Elastic grow/shrink patches real JobSet child-Job `parallelism` (KEP-463), not pause-pod bookkeeping; keep per-pod Lease mint/close; e2e asserts live parallelism + real pod count change | `controllers/run_controller.go`, `controllers/kube/bridge.go`, `pkg/binder/binder.go` | JOBSET-2/elastic, CASCADE-1 | **yes** | L |
| CASCADE-3 | Node-failure swap: wire real RANK/WORLD_SIZE/MASTER_ADDR from JobSet DNS; rely on JobSet RestartJob/FailurePolicy + plugin Filter/Score steering onto pre-held spare; **build ONE honest state mechanism** (checkpoint-restore *or* torchelastic re-rendezvous); rewrite spares-and-fill.md (delete "resumes without losing model state"); e2e resumes from last checkpoint, logged | `controllers/run_controller.go`, `docs/user-guide/spares-and-fill.md`, `controllers/kube/scenario_test.go` | JOBSET-DNS, PLUGIN-3, CASCADE-1 | **yes** | XL |
| CASCADE-4 | Make opportunistic spare use structurally possible: split node usage into hard(Active) vs soft/reclaimable(Spare) in `topology.Node`/`WithUsage`; add `cover.Request.AllowSpareOpportunistic` (unfunded-only); mint evictable lease; reuse existing overlap-close eviction; e2e: unfunded pod runs on idle spare, evicted the instant the owner's swap needs it | `controllers/run_controller.go`, `pkg/topology/snapshot.go`, `pkg/cover/cover.go`, `pkg/pack/pack.go` | PLUGIN-2, JOBSET-1 | **yes** | L |
| CASCADE-5 | Ship a real ETA writer: document the `rq.davidlangworthy.io/eta` contract; reference sidecar patches its own pod annotation via SA token; minimal RBAC (patch self-pod only, no over-broad grant); tiny Go/Python SDK alt; e2e: container writes ETA → sidecar patches → `status.eta` reflects it live | `controllers/run_controller.go`, `pkg/binder/binder.go`, `docs/operator-guide/observability.md` | JOBSET-1, CASCADE-1 | **yes** | M |
| CASCADE-6 | Doc truth pass for every claim CASCADE makes real (spares-and-fill, elastic-runs, researcher-guide ETA, fundamentals-gap-analysis, milestones M9) — same PR as each mechanism | `docs/user-guide/spares-and-fill.md`, `docs/user-guide/elastic-runs.md`, `docs/user-guide/researcher-guide.md`, `docs/project/fundamentals-gap-analysis.md`, `docs/roadmap/milestones.md` | CASCADE-1..5 | no | S |

**Exit:** zero hand-set terminal phase in `scenario_test.go`; `make e2e` proves completion by real container exit + an `exit 1`→Failed case; spares-and-fill honest about the real mechanism/cost; no accepted-but-unread field; `computeUsage`/Snapshot distinguish hard vs reclaimable proven by a real opportunistic-tenant eviction e2e; elastic e2e asserts live JobSet parallelism. **Closes #3, #5, #7, #14, #20.**

---

### Track D — TRUTH (fully parallel now, control-plane honesty)

**Objective:** Make every control-plane observability/forecast claim honest — ETA, remedies, conflictSet/kill-probability, checkpoint, AutoRenew, spare discount, event streams, phantom forecast controller, elasticity metrics, completions — each either genuinely wired (with a test that *varies inputs and observes different outputs*) or deleted from every doc. **No GPU or workload dependency — runs fully parallel to A/B/C.**

**Why:** A second layer of control-plane fakes sits on top of the real brain: a hardcoded 15-min ETA constant (`forecast.go:18,131`), a static `defaultRemedies()`, CRD fields read by nothing (`Runtime.Checkpoint`, `Budget.AutoRenew`), fields on no type at all (`conflictSet`, `killProbability`), zero event emission, a spare-discount claim contradicted by the charging code, a seed never logged, M9 marked both done and not-done.

| id | title | files | dependsOn | ‖ | effort |
|---|---|---|---|---|---|
| TRUTH-1 | Make ETA a real function of computed deficit (feed `estimateDeficit` into `conservativeEarliest`; monotonic lead clamped at min); table test: ETA strictly increases with deficit | `pkg/forecast/forecast.go`, `pkg/forecast/forecast_test.go`, `docs/user-guide/reservations.md`, `docs/user-guide/researcher-guide.md` | — | **yes** | M |
| TRUTH-2 | Remedies from real scope signals (unfunded>0, spare exists, malleable!=nil) **or** relabel as fixed `reclaimOrder`; wire `buildPlanPayload` to actually print the rows | `pkg/forecast/forecast.go`, `pkg/forecast/forecast_test.go`, `cmd/kubectl-runs/cmd/plan.go`, `docs/user-guide/researcher-guide.md`, `docs/migrations/kueue.md`, `docs/migrations/slurm.md` | TRUTH-1 | no | M |
| TRUTH-3 | Wire `Runtime.Checkpoint` as a real grace window in `HandleNodeFailure` (new CheckpointGrace phase + deadline, mirror follow-grace) **or** delete field+schema+docs; envtest varies Duration zero vs nonzero | `api/v1/run_types.go`, `controllers/run_controller.go`, `docs/user-guide/researcher-guide.md`, `docs/migrations/slurm.md`, `docs/project/fundamentals-gap-analysis.md` | — | **yes** | M |
| TRUTH-4 | Add real `ConflictSet`/banded `KillProbability` to `ReservationForecast` (resolver dry-run entry point names cut runs; band from resolver phase) + wire into CLI **or** delete from all three docs+CLI; same choice everywhere | `api/v1/reservation_types.go`, `pkg/resolver/resolver.go`, `pkg/forecast/forecast.go`, `cmd/kubectl-runs/cmd/plan.go`, `docs/user-guide/researcher-guide.md`, `docs/migrations/kueue.md`, `docs/product/researcher-budget-ux.md` | TRUTH-1, TRUTH-2 | no | L |
| TRUTH-5 | Wire real `EventRecorder` into all reconcilers (`mgr.GetEventRecorderFor("jobtree")`); emit Normal/Warning at admit/reserve/activate/resolver-action/swap/complete; envtest reads back real `corev1.Event` | `controllers/kube/reconcilers.go`, `cmd/manager/main.go`, `controllers/run_controller.go` | — | **yes** | L |
| TRUTH-6 | Emit/log the attested lottery seed (Warning event `ResolverAction` with `action.Reason`, or `ctrl.Log`); envtest asserts seed appears in Event/log — not by calling resolver directly | `pkg/resolver/resolver.go`, `controllers/run_controller.go`, `cmd/manager/main.go`, `docs/operator-guide/admin-setup.md` | TRUTH-5 | no | S |
| TRUTH-7 | Wire `Budget.AutoRenew` into `BudgetReconciler` (extend End by Period, or `RenewalDue` status) **or** delete field+deepcopy+CRD+samples+docs; record decision in quota-semantics.md | `api/v1/budget_types.go`, `api/v1/zz_generated.deepcopy.go`, `controllers/kube/reconcilers.go`, `config/crd/bases/...budgets.yaml`, `deploy/helm/.../crds/...budgets.yaml`, `docs/project/quota-semantics.md` | — | **yes** | M |
| TRUTH-8 | Implement spare-discount charge rate (multiplier on `leaseHours` when `Slice.Role==Spare`) **or** delete the "accounted at a discount" claim; record decision (default: delete now) | `pkg/funding/evaluate.go`, `docs/user-guide/researcher-guide.md`, `docs/roadmap/design/M6-failure-and-spares.md`, `docs/project/quota-semantics.md` | — | **yes** | M |
| TRUTH-9 | Add real `jobtree_forecast_latency_seconds` histogram around `forecast.Plan`; delete "forecast controller" from admin-setup.md (forecast is an inline library call) | `controllers/run_controller.go`, `pkg/metrics/metrics.go`, `docs/operator-guide/admin-setup.md` | — | **yes** | S |
| TRUTH-10 | Build `elastic_grows_total`/`elastic_shrinks_total`/`elastic_width_current` emitted from `growRun`/`shrinkRun` success points, asserted via `metrics.Snapshot()` **or** uncheck M9; remove elastic-runs.md "will follow" hedge | `pkg/metrics/metrics.go`, `controllers/run_controller.go`, `docs/user-guide/elastic-runs.md`, `docs/roadmap/milestones.md` | — | **yes** | M |
| TRUTH-11 | Generate shell completions from the real Cobra tree (`GenBash/Zsh/FishCompletion`); delete the hand-written map; test adds a throwaway subcommand and asserts it appears | `cmd/kubectl-runs/cmd/completions.go`, `cmd/kubectl-runs/cmd/root.go` | — | **yes** | S |
| TRUTH-12 | Final cross-doc reconciliation sweep; grep the 15 closed-finding keywords, confirm zero stale hits outside `docs/project/` | index.md, researcher-guide, kueue.md, slurm.md, researcher-budget-ux, admin-setup, milestones, elastic-runs, M6 design, quota-semantics | TRUTH-1..11 | no | S |
| **TRUTH-13 (added)** | **Gap:** fix the `jobtree_reservations_backlog_seconds` staleness/leak (#21) — requeue timer while `PendingReservation!=nil`, key by reservation not just flavor, clear on activation/release. *Not in the original track breakdown; added so #21 is not dropped* | `controllers/run_controller.go`, `pkg/metrics/metrics.go` | — | **yes** | M |

**Exit:** every changed test varies a real input and observes a different output; every doc claim backed by file:line or deleted; no accepted-but-unread field; M9 honest; every metric emitted from the real path and asserted via `Snapshot()`; events read back from a real manager. **Closes #6, #8, #9, #10, #12, #13, #15, #16, #18, #19, #21(via -13), #22, #23, #24, #25, #26.**

---

### Track E — ROLES (gated on A+B; gang-of-gangs fast-follow)

**Objective:** Turn a Run from a single homogeneous gang into a real gang-of-gangs: `RunSpec.Roles[]` where each role has its own template/width/groups/spares/elasticity/criticality, admitted atomically, reclaimed per-role by criticality (protect trainer, shrink samplers first), with per-role GPU-hour attribution via a finally-populated `LeaseSpec.CompPath`, a real zero-GPU CPU path, and a per-role JobSet vs LeaderWorkerSet materialization choice.

**Why:** `RunSpec` is architecturally single-shape; `Reconcile` has no role loop; `LeaseSpec.CompPath` is declared/deep-copied but written by zero code (the accepted-but-unread pattern, decision #2 names it); `run_types.go:209-211` hard-rejects `TotalGPUs<=0` so a CPU-only grader can't exist; `pkg/resolver` has no criticality concept. Extends A+B's single-role seam from one role to N.

| id | title | files | dependsOn | ‖ | effort |
|---|---|---|---|---|---|
| ROLES-1 | Additive `RunSpec.Roles[]` (adds Locality/Malleable/Spares/Criticality/ServingMode per role); validate exactly-one-of {single Template, Roles}; regen deepcopy+CRD; PreserveUnknownFields | `api/v1/run_types.go`, `zz_generated.deepcopy.go`, `config/crd/bases`, `deploy/helm/.../crds` | JOBSET-1 | no | M |
| ROLES-2 | Disambiguate "role": lease-slice Active/Spare vs new gang-role (new `gang-role` label key); fix quota-semantics.md | `pkg/binder/binder.go`, `api/v1/lease_types.go`, `docs/project/quota-semantics.md` | — | **yes** | S |
| ROLES-3 | Per-role materialization: extend binder group loop to role×group; `Request.RoleName` into labels/names; invoke Materialize once per role, collect all before applying | `pkg/binder/binder.go`, `controllers/run_controller.go` | ROLES-1, ROLES-2 | no | M |
| ROLES-4 | Atomic cross-role admission: snapshot per GPUType, per-role plan against one shared Evaluation/Inventory with holds, commit all-or-none, whole-Run reservation fallback; envtest: role B doesn't fit ⇒ zero leases for role A | `controllers/run_controller.go`, `pkg/cover/cover.go`, `pkg/funding/evaluate.go` | ROLES-3 | no | XL |
| ROLES-5 | Per-role criticality gates reclaim order: exclude Protected from shrink/lottery (or last-resort); deterministic (criticality,proximity,recency,name) tiebreak; test: Protected never chosen even when only candidate | `pkg/resolver/resolver.go`, `pkg/resolver/resolver_test.go` | ROLES-1, ROLES-4 | **yes** | L |
| ROLES-6 | Zero-GPU/CPU-only role path (bypass topology/Cover/Pack, schedule by pod count vs CPU/mem); no `nvidia.com/gpu` entry; decide+document zero-hour lease vs no lease | `api/v1/run_types.go`, `pkg/pack/pack.go`, `controllers/run_controller.go`, `pkg/topology/snapshot.go` | ROLES-1 | **yes** | L |
| ROLES-7 | Populate `LeaseSpec.CompPath` (`[run,role]`) at mint; per-role GPU-hour rollup into `RunFundingStatus.ByRole` | `api/v1/lease_types.go`, `api/v1/run_types.go`, `pkg/binder/binder.go`, `pkg/funding/evaluate.go` | ROLES-3 | **yes** | M |
| ROLES-8 | Per-role independent grow/shrink (iterate `Roles[i].Malleable`, key elastic groups by (role,group)); Protected never below its own min | `controllers/run_controller.go` | ROLES-3, ROLES-5 | no | L |
| ROLES-9 | Per-role materialization target: JobSet (run-to-completion) vs LeaderWorkerSet (serving, HPA scale); `ServingMode` selects; both carry `schedulerName` | `controllers/run_controller.go`, `api/v1/run_types.go` | ROLES-1, JOBSET-2, JOBSET-3 | no | XL |
| ROLES-10 | Cross-role addressing seam only (stable per-role headless DNS + `JOBTREE_ROLE_<NAME>_ADDR` env); **NOT** weight-sync; honest scope doc | `controllers/run_controller.go`, `docs/project/roles.md` | ROLES-9 | **yes** | M |
| ROLES-11 | Real kind e2e: 3-role Run (trainer Protected/JobSet, sampler Fungible/LWS, grader CPU-only); assert real GPU limits, contention shrinks only sampler (real scale-down), distinct CompPaths + differing byRole hours, completion from real exits | `test/e2e/roles_test.go`, `hack/kind-e2e.sh` | ROLES-4..9 | no | XL |
| ROLES-12 | Docs-match-code: ml-vision.md, borrow-vs-build §7, researcher guide — state exactly what shipped incl. weight-sync NOT implemented | `docs/project/ml-vision.md`, `docs/project/borrow-vs-build.md`, `docs/researcher-guide.md` | ROLES-9, ROLES-10 | **yes** | S |

**Exit:** no hand-injected state; `CompPath` non-empty verified from a real API server; criticality actually gates resolver (Protected never selected, CI-enforced); no unread RunRole field; zero-GPU path provably GPU-free; docs honest incl. weight-sync boundary; ROLES-11 on a real cluster. **Closes the ml-vision role/CompPath/zero-GPU gaps; extends #1/#2 closure to N heterogeneous roles.**

---

### Track F — TESTINFRA (fully parallel now, verification backbone)

**Objective:** Build the kind e2e harness (real API server + real manager + real JobSet/plugin + fake-but-real device plugin + real kubelet) that submits a Run and proves a real container runs to completion with **zero hand-injected pod status**, wire it into CI as a named required job, and ship the anti-fake lint gates that make regression to simulator-theater structurally hard. **The verification backbone for every other track.**

**Why:** envtest has no kubelet, so it structurally cannot catch the pause-pod class of bug — the exact reason `scenario_test.go` hand-sets `PodSucceeded`. Nothing else can be called "real" until it passes through a harness with a real kubelet running a real container to a real exit code. Scaffolding (TESTINFRA-1,2,3,5,6) has no code dependency and starts now; the *test cases* (TESTINFRA-4) are gated on the trunk and are *expected to fail red until it lands* — that red is the proof the harness isn't itself fake.

| id | title | files | dependsOn | ‖ | effort |
|---|---|---|---|---|---|
| TESTINFRA-1 | `make kind-up/down/e2e`; script creates cluster, applies CRDs, installs pinned JobSet CRDs+controller; fail-hard-don't-skip discipline | `Makefile`, `hack/e2e/kind-up.sh`, `hack/e2e/kind-down.sh`, `hack/e2e/versions.env` | — | **yes** | M |
| TESTINFRA-2 | Dockerfile for cmd/manager; `make e2e-image` build+`kind load`; `values-e2e.yaml` overlay; deploy via helm using existing webhook-cert machinery | `Dockerfile`, `Makefile`, `deploy/helm/.../values-e2e.yaml` | TESTINFRA-1 | no | M |
| TESTINFRA-3 | Fake device-plugin: real v1beta1 gRPC server advertising N `nvidia.com/gpu` per node, empty Allocate; DaemonSet — lets the real kubelet enforce GPU limits | `cmd/fake-device-plugin/main.go`, `hack/e2e/manifests/fake-device-plugin-daemonset.yaml` | TESTINFRA-1 | **yes** | L |
| TESTINFRA-4 | `test/e2e` package (build-tag `e2e`): submit real container `sh -c 'sleep 2; exit 0'`, assert Completed only via real `podSucceeded` watch off a kubelet-written status; negative `exit 1`→Failed; follow chain. **Expected red until trunk lands — do not stub green** | `test/e2e/completion_test.go`, `test/e2e/follow_test.go`, `test/e2e/helpers.go` | TESTINFRA-2, TESTINFRA-3, JOBSET/PLUGIN trunk | no | L |
| TESTINFRA-5 | Anti-fake lint: `check-no-fake-terminal-status.sh` fails on any `_test.go` setting a workload Pod `.Status.Phase` terminal outside `test/e2e/`; shrink-only ratcheted allowlist | `hack/check-no-fake-terminal-status.sh`, `hack/e2e/fake-status-allowlist.txt`, `.github/workflows/ci.yaml` | — | **yes** | S |
| TESTINFRA-6 | Anti-fake lint: `check-crd-fields-read.sh` fails on any exported api/v1 field with zero non-generated non-test readers; seed allowlist with `Runtime.Checkpoint`+`AutoRenew` (provably active) | `hack/check-crd-fields-read.sh`, `hack/e2e/unread-fields-allowlist.txt`, `.github/workflows/ci.yaml` | — | **yes** | M |
| TESTINFRA-7 | Wire kind e2e into CI as a named required-on-main job; upload kind diagnostics (events, manager/JobSet logs) on failure | `.github/workflows/e2e.yaml` | TESTINFRA-4,5,6 | no | S |
| TESTINFRA-8 | Retire/re-scope `scenario_test.go` completion tests: keep as fast plumbing checks, re-comment that real proof lives in `test/e2e/`, add to allowlist by name | `controllers/kube/scenario_test.go`, `hack/e2e/fake-status-allowlist.txt` | TESTINFRA-4,5 | no | S |
| TESTINFRA-9 | Docs: honest e2e-tier scope in testing-and-simulation.md + README (real kubelet+container+fake device plugin; still NOT real GPU HW / multi-node fabric) | `docs/project/testing-and-simulation.md`, `README.md` | TESTINFRA-7 | **yes** | S |

**Exit:** `make e2e` bootstraps+tears down a cluster with no manual steps; completion proven only by real kubelet exit (grep-verified zero terminal-phase writes in e2e files); a real `exit 1`→Failed case; both anti-fake lints in CI with shrink-only ratchets; named e2e CI job required on main; `make e2e` against a clean checkout is *provably red today* and flips green when the trunk merges. **Closes #5, #11 (via lint), #1/#2 end-to-end; closes systemic pattern #2 generally.**

---

### Track G — CLI (fully parallel now)

**Objective:** Turn `kubectl-runs` from a local-JSON simulator (zero client-go, self-labeled "the local simulator") into a real Kubernetes client that talks to a live API server by default, while keeping the simulator as an honest `--local`/`--dry-run` mode; add YAML to submit; add a kind e2e; correct every doc/krew/helm surface that markets the simulator as live.

**Why:** `cmd/kubectl-runs/cmd/*` has no kubeconfig/rest.Config anywhere; every subcommand goes through `StateStore.Load/Save` + in-process `reconcileRun`. But `cmd/manager/main.go` proves the ingredients exist (scheme, namespaced CRDs, real `BudgetReconciler` persisting `Status.Usage/Headroom`, webhooks). The fix is almost entirely wiring — and critically must **not** re-run the scheduling/funding brain client-side against live objects (that would relocate the fake into the CLI). **No trunk dependency — runs fully parallel.**

| id | title | files | dependsOn | ‖ | effort |
|---|---|---|---|---|---|
| CLI-1 | `--kubeconfig/--context/--local/--dry-run` flags; build `client.Client` from real rest.Config reusing manager's scheme; resolve namespace from context; stop hardcoding `default` | `cmd/kubectl-runs/cmd/root.go`, `cmd/manager/main.go`, `go.mod` | — | no | M |
| CLI-2 | `runsBackend` interface seam; `localBackend` re-homes StateStore+reconcile; `liveBackend` real Get/List/Create/Update/Watch; select on `--local` | `cmd/kubectl-runs/cmd/backend.go`, `state.go`, `helpers.go`, `root.go` | CLI-1 | no | L |
| CLI-3 | submit: `sigs.k8s.io/yaml.Unmarshal` (YAML⊃JSON), promote to direct dep; live `CreateRun`; keep client-side Default/Validate as UX; print what the API returns (no fabricated synchronous "bound") | `cmd/kubectl-runs/cmd/submit.go`, `controllers/kube/webhooks.go`, `go.mod` | CLI-2 | **yes** | M |
| CLI-4 | plan/explain/watch: **delete live `reconcileRun`** (no client-side scheduler racing the manager); render whatever Status the manager wrote; real watch in live mode | `cmd/kubectl-runs/cmd/plan.go`, `explain.go`, `watch.go` | CLI-2 | **yes** | M |
| CLI-5 | budgets: stop re-running the funding brain; List Budgets and print `.Status.Usage/.Headroom` directly; recompute only in `--local`; document staleness | `cmd/kubectl-runs/cmd/budgets.go`, `controllers/kube/reconcilers.go`, `api/v1/budget_types.go` | CLI-2 | **yes** | M |
| CLI-6 | leases/shrink/sponsors: live List/Get + get-mutate-update retry-on-conflict; CLI's job ends at Update (manager reconciles async) | `cmd/kubectl-runs/cmd/leases.go`, `shrink.go`, `sponsors.go` | CLI-2 | **yes** | M |
| CLI-7 | RBAC for researchers: optional `jobtree-researcher` ClusterRole (get/list/watch/create/update runs; get/list/watch budgets/reservations/leases), toggled by values key; rolebinding recipe | `deploy/helm/.../rbac.yaml`, `values.yaml`, `docs/operator-guide/admin-setup.md` | CLI-1 | **yes** | S |
| CLI-8 | kind e2e (`make e2e-kind`): helm-install real chart, build real binary, submit→watch→budgets→leases, assert on Status the *manager* wrote after a real round-trip; gate behind env var; non-blocking CI job | `Makefile`, `.github/workflows/ci.yaml`, `deploy/helm/gpu-fleet` | CLI-3,4,5,6 | **yes** | L |
| CLI-9 | fake-client unit tests per subcommand asserting verb/GVK/namespace via recording interceptor (not exit 0); forbid hand-setting Status-under-test then echoing it | all subcommand files | CLI-3,4,5,6 | **yes** | L |
| CLI-10 | Fix tests faking submitted input format: real `.yaml` block-syntax fixtures in doc_examples_test.go/root_test.go (not JSON with a renamed extension) | `doc_examples_test.go`, `root_test.go`, `docs/cli/kubectl-runs.md` | CLI-3 | **yes** | S |
| CLI-11 | Relabel researcher-guide/admin-setup/NOTES.txt (live default, `--local` opt-in); **delete fabricated `conflictSet`/kill-probability transcript lines** (never emitted); replace log-grep-for-seed recipe with what the CLI can surface | `docs/user-guide/researcher-guide.md`, `docs/operator-guide/admin-setup.md`, `deploy/helm/.../NOTES.txt` | CLI-1 | **yes** | M |
| CLI-12 | Release smoke check: `kubectl-runs --help` asserts `--kubeconfig`/`--local` present so a regression fails release CI | `.github/workflows/release.yaml`, `plugins/krew/runs.yaml` | CLI-1 | **yes** | S |
| CLI-13 | Generate completions from the Cobra tree (dedup with TRUTH-11 — assign to whichever track lands first) | `cmd/kubectl-runs/cmd/completions.go` | — | **yes** | S |

**Exit:** no live path re-runs the scheduler/funding brain against live objects; every mutating command proves effect via a *separate* read; fake-client tests assert the real verb/GVK; real bare-YAML decoded; `make e2e-kind` in CI asserts on manager-computed Status; four doc surfaces agree on default vs opt-in; fabricated `conflictSet`/kill-probability lines gone; no client-side recompute of server-written Status. **Closes #4, #11, #12 (partial), #17, #26.**

---

## 3. Dependency Graph & Parallelization Guide

### Critical path (must be serial across tracks)

```
PLUGIN-1 ──► PLUGIN-2 ──► PLUGIN-4 ──► PLUGIN-5 ──► PLUGIN-6 ──► PLUGIN-9 ──► PLUGIN-10
                                                                                  │
JOBSET-1 ──► JOBSET-2 ──► JOBSET-5 ──► JOBSET-6/7/8 ──► JOBSET-9 ──► JOBSET-11    │
   │            JOBSET-3 ──► JOBSET-4 ┘                                            │
   └──────────────────────────────► (trunk real) ──► CASCADE-1..5 ──► CASCADE-6   │
                                                          │                        │
                                     ROLES (gated on JOBSET-1/2/3 + PLUGIN) ◄──────┘
```

The trunk is **PLUGIN ∥ JOBSET → CASCADE**. PLUGIN and JOBSET are *independent of each other* (borrow-vs-build §8: prove the plugin on the current single-gang model first; JobSet is a separate borrow) and can run concurrently. CASCADE needs both. ROLES is the fast-follow after the single-role seam exists.

### Fully parallel RIGHT NOW (no trunk dependency)

- **Track D (TRUTH):** all of TRUTH-1,3,5,7,8,9,10,11,13 have `dependsOn: []`. Pure control-plane honesty — no GPU, no workload. Start immediately.
- **Track G (CLI):** CLI-1 → CLI-2 unblocks CLI-3..6 (all parallel), plus CLI-7/11/12/13 off CLI-1. Pure client-go wiring.
- **Track F (TESTINFRA) scaffolding:** TESTINFRA-1,3,5,6 have no code dependency. Build the harness + anti-fake lints now so they're red-and-waiting; TESTINFRA-4 (the test cases) is *supposed* to be red until the trunk lands.

### Gated

- **CASCADE:** entirely gated on JOBSET-1 (real container) + PLUGIN-1/2/3 (real placement).
- **ROLES:** gated on JOBSET-1/2/3 + PLUGIN — extends the single-role seam.
- **PLUGIN-9/10, JOBSET-11, TESTINFRA-4/7:** the e2e/parity closers, gated on their track's mechanism tasks.

### "If you have N agents, assign them like this"

| N | Assignment |
|---|---|
| **1** | Serial critical path: PLUGIN-1 → PLUGIN-2/3/7 → PLUGIN-4/5/6 → JOBSET-1..9 → CASCADE-1..6. Do TRUTH/CLI honesty in the gaps. |
| **3** | **A1:** PLUGIN (critical path). **A2:** JOBSET (parallel trunk half; starts JOBSET-1/3 immediately). **A3:** TRUTH + TESTINFRA scaffolding (all no-dep). Converge on CASCADE once A1+A2 land. |
| **5** | Above + **A4:** CLI (fully independent, start CLI-1 now). **A5:** TESTINFRA harness (TESTINFRA-1,2,3,5,6,7) + then pick up CASCADE-4 (opportunistic, only needs PLUGIN-2). |
| **7 (max fan-out)** | **A1** PLUGIN · **A2** JOBSET · **A3** TRUTH · **A4** CLI · **A5** TESTINFRA · **A6** CASCADE (idle until trunk, then all-in; meanwhile prototype CASCADE-3 state mechanism + CASCADE-5 sidecar/SDK which are largely independent) · **A7** ROLES (idle until JOBSET-1/2/3 land, then ROLES-1..12; meanwhile ROLES-2 rename + ROLES-6 CPU-planner design are no-dep). |

**Immediate cold-start (day 1, zero blockers):** PLUGIN-1, JOBSET-1, JOBSET-3, all no-dep TRUTH tasks, CLI-1, TESTINFRA-1/3/5/6, ROLES-2.

---

## 4. Phasing / Milestones

### Phase 0 — Plugin parity on a single gang + kind harness
- **Tracks:** PLUGIN-1..10, TESTINFRA-1..3/5/6, plus TRUTH/CLI honesty running alongside.
- **Exit:** a `schedulerName: jobtree` pod is placed by the real plugin binary (kill it → pods stay Pending); real `nvidia.com/gpu` requests make kubelet admission real; the full existing scenario/regression suite reproduces **bit-for-bit identical** funding/placement outcomes to the old `nodeName`-pin model; anti-fake lints are live in CI (red without allowlist). *The moat is proven to survive the port before any body work lands.*

### Phase 1 — Real workload via JobSet + un-fake cascade + real CLI
- **Tracks:** JOBSET-1..12, CASCADE-1..6, CLI-1..13, TESTINFRA-4/7/8/9.
- **Exit:** a Run lowers to a real JobSet; a real container runs to exit 0 and the Run reaches Completed **only** via the real kubelet→watch chain (zero hand-injection); an `exit 1` drives Failed; elastic grow/shrink moves real JobSet parallelism; node-failure swap has one honest state mechanism; `kubectl runs` talks to a live cluster by default; `make e2e` is green in CI as a required job.

### Phase 2 — RL roles + control-plane truth
- **Tracks:** ROLES-1..12, TRUTH-1..13 fully landed.
- **Exit:** a 3-role RL Run (trainer/sampler/grader) admits atomically, reclaims by criticality (Protected trainer never cut before Fungible sampler), reports per-role GPU-hours via a populated `CompPath`, with a real zero-GPU CPU path — proven on a real kind cluster; every control-plane claim (ETA, remedies, conflictSet, checkpoint, AutoRenew, discount, events, metrics, completions) is either wired-and-tested or deleted from every doc.

### Phase 3 — Opportunistic / spares / checkpoint / vision
- **Tracks:** CASCADE-4 (opportunistic spare use), CASCADE-3 checkpoint-restore hardening, CASCADE-5 ETA SDK, ROLES-9/10 LWS-serving + cross-role addressing, DRA-first resource model (PLUGIN-7 branch).
- **Exit:** a second unfunded Run actually runs on an idle spare and is *evicted* (not just planned) the instant the owner's swap needs it; the ETA annotation has a real writer proven live; serving roles resize live via LWS/HPA; no audit finding and no borrow-vs-build decision remains unclosed or contradicted by docs.

---

## 5. Anti-Fake Discipline

The audit named four systemic failure patterns. Each gets a concrete rule and a mechanical CI gate (from Track F) so it cannot recur.

| Audit pattern | The rule | The CI check |
|---|---|---|
| **P1 — one fake trunk sterilizes downstream** | No feature is "done" until a real kind/minikube e2e (real kubelet, real container exit) exercises it. envtest is a *plumbing* check, never end-to-end proof. | `make e2e` / `.github/workflows/e2e.yaml` (TESTINFRA-4/7) — a named, required-on-main job; **provably red on a clean checkout today**, green only when the trunk is real. |
| **P2 — tests inject the exact output state** | No `_test.go` may set a workload Pod's `.Status.Phase` to a terminal value (or a terminal `Lease.Status`, or `spec.nodeName`) to fake an outcome the system was meant to derive. | `hack/check-no-fake-terminal-status.sh` (TESTINFRA-5) — grep/AST scan; shrink-only ratcheted allowlist (`wc -l` baseline); the two `scenario_test.go` sites seeded so the gate is provably active. |
| **P3 — docs market intent as present-tense reality** | Every doc claim about a feature must point to real code at file:line, or be deleted/reworded; no internal doc may contradict a user-facing one; superseded plan sections marked closed, not left side-by-side. | Cross-doc keyword sweep (TRUTH-12) + `doc_examples_test.go`-style checks that fail if the four CLI-mode doc surfaces disagree (CLI exit criteria); each mechanism's doc fix ships in the *same PR* as the code (CASCADE-6, JOBSET-12, ROLES-12). |
| **P4 — API accepts fields no controller reads** | No CRD field ships schema-validated + deep-copied but read by nothing. A new field lands *with* its consumer or not at all. | `hack/check-crd-fields-read.sh` (TESTINFRA-6) — every exported api/v1 field must have a non-test non-generated reader; `Runtime.Checkpoint` + `AutoRenew` seed the shrink-only allowlist (red without it). Enforced additionally per-track: JOBSET-1b (criticality), ROLES exit criteria (every RunRole field), TRUTH-3/7 (build-or-delete). |

**The single discipline, restated:** *build-or-delete, never accept-and-ignore; derive-don't-inject; e2e-or-it-isn't-real.*

---

## 6. Coverage Map

### Every audit finding (#1–#26)

| # | Finding | Closed by |
|---|---|---|
| 1 | Real job execution / workload container | **JOBSET-5/6**, PLUGIN-7; e2e via TESTINFRA-4, JOBSET-11 |
| 2 | Binder materializes real workload pods | **JOBSET-5/6/9**; PLUGIN-2 |
| 3 | Node-failure swap "resumes without losing state" | **CASCADE-3** (build one honest mechanism + rewrite docs) |
| 4 | `kubectl runs` as live plugin | **CLI-1/2/3..6**, CLI-8 |
| 5 | Run/gang completion on Succeeded | **CASCADE-1**, JOBSET-8, TESTINFRA-4 |
| 6 | ETA is a hardcoded now+15min constant | **TRUTH-1** |
| 7 | Opportunistic fill of spare capacity | **CASCADE-4** |
| 8 | `conflictSet`/`killProbability` exist on no type | **TRUTH-4** (build or delete) |
| 9 | "Event streams" — no event emission | **TRUTH-5**; PLUGIN-6 (plugin's own actions) |
| 10 | `checkpoint` hint read nowhere | **TRUTH-3** (grace window or delete) |
| 11 | submit accepts YAML | **CLI-3/CLI-10**; TESTINFRA-5 forbids the input-faking test pattern |
| 12 | `plan` shows fabricated conflictSet/remedies | **TRUTH-2/TRUTH-4** (build real rows) + **CLI-11** (delete fabrication) |
| 13 | Static `defaultRemedies` sold as computed | **TRUTH-2** |
| 14 | Grow/shrink materializing real capacity | **CASCADE-2** |
| 15 | `runtime.checkpoint` active safe-requeue | **TRUTH-3** |
| 16 | "Kill probability" field/computation | **TRUTH-4** |
| 17 | CLI live-cluster (dup vantage) | **CLI-11**; same root as #4 |
| 18 | `RunSpec.Runtime.Checkpoint` declared-unused | **TRUTH-3** + **TESTINFRA-6** lint |
| 19 | Elasticity metrics (`elastic_*`), M9 marked done | **TRUTH-10** |
| 20 | Workload-reported ETA has no writer | **CASCADE-5** (sidecar/SDK+RBAC); unblocked by JOBSET |
| 21 | `reservations_backlog_seconds` staleness/leak | **TRUTH-13 (added — was uncovered by the original 7-track breakdown; flagged and assigned here)** |
| 22 | `Budget.AutoRenew` read nowhere | **TRUTH-7** (wire or delete) |
| 23 | Attested lottery seed never logged | **TRUTH-6**; PLUGIN-6 (real Event) |
| 24 | Phantom "forecast controller" + metric | **TRUTH-9** |
| 25 | Spares "accounted at a discount" | **TRUTH-8** (build or delete) |
| 26 | Static shell completions | **TRUTH-11** / **CLI-13** (dedupe — one owner) |

*Systemic patterns #1–#4 → closed generally by the Track F CI gates (see §5), not just per-finding.*

**One honest gap flagged:** finding **#21** appears in **no** track's `auditFindingsClosed` in the supplied breakdown. It is control-plane observability honesty, so it belongs in Track D — added above as **TRUTH-13**. Do not let it drop.

### Every borrow-vs-build decision

| Decision (borrow-vs-build.md) | Track / task |
|---|---|
| #1 — become a scheduler-framework plugin, stop pinning `nodeName` | **Track A (PLUGIN)**, esp. PLUGIN-1/2 |
| #2 — roles formalized (gang of gangs) | **Track E (ROLES)**; single-role seam JOBSET-1 |
| **Option C** — lower Run→JobSet, place with the plugin | **Track B (JOBSET)** + Track A jointly |
| Q1 — gang gating via Permit (coscheduling, NOT Kueue suspend) | PLUGIN-4 |
| Q2 — elastic width = one Indexed Job's live `parallelism` (KEP-463) | JOBSET-4, CASCADE-2, ROLES-8 |
| Q3 — spare-swap ↔ FailurePolicy, plugin steers replacement to held slot | CASCADE-3, PLUGIN-2/3 |
| Q4 — per-slice Lease minted at Bind, idempotent | PLUGIN-5 |
| Q5 — reclaim=preemption but demote-not-kill stays a controller action | PLUGIN-6 |
| Q6 — DRA-first resource model, `nvidia.com/gpu` fallback | PLUGIN-7 |
| §6.2 — mirror TAS conventions (topology annotations), don't adopt Kueue | PLUGIN-2/3 (annotation UX), pack policy in Filter/Score |
| §6.3 — build our own Permit gate now, track native WAS (KEP-5732/4671) behind a gate | PLUGIN-4 |
| §6.1 Q2 — LWS for persistent serving/sampler roles | ROLES-9 |
| "if JobSet lacks something, fork it" | JOBSET-4 spike decision gate |
| §8 sequencing — prove plugin on single gang *first*, then JobSet spike, then lowering, then roles | Phase 0 → Phase 1 → Phase 2 ordering |
| "still ours regardless" — funding/topology brain, per-role criticality, follow, leases, zero-GPU CPU path, RL model | Kept intact across all tracks; zero-GPU = ROLES-6; criticality = ROLES-5; CompPath = ROLES-7 |

---

*Nothing dropped: all 26 findings mapped (with #21's coverage-gap fixed via TRUTH-13), every locked decision and validated §6 answer assigned to a track, and every track ending in a real-cluster e2e that forbids re-faking.*