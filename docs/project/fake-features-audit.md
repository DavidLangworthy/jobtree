<!-- Internal audit (docs/project/ is excluded from the built site). Produced 2026-07-04 by a
9-subsystem adversarial audit (each finding survived a refutation pass) after the pause-pod discovery.
Headline findings independently spot-checked by the maintainer session: CLI has no client-go (local
simulator); ETA capacity-shortfall path is a hardcoded now+15min (forecast.go:18,131); conflictSet/
killProbability exist on zero CRD types; no event emission anywhere. -->

# jobtree Fake-Features Audit

*Definitive honest inventory of claimed-but-unwired capabilities. Verified against source at commit-level, branch `docs/ml-workload-plan` (with `follow`/`eta` commits verified via `git show`). Every finding grounded in file:line.*

> **Status update (feat/workload-trunk, PLUGIN-2 cutover):** the load-bearing
> "fake trunk" is cut out. `buildPod` now renders a **real** workload container
> (role `Template` or a real terminating default) with a real `nvidia.com/gpu`
> request and **no pre-pinned nodeName**; the scheduler plugin schedules it and
> mints the Lease. **#1 (real job execution)** and **#2 (real workload pods /
> PodTemplate)** are RESOLVED, and the whole downstream cascade that hung off
> them is no longer inert: **#5 (completion on pod `Succeeded`)** and **#14
> (grow/shrink real capacity)** now fire on real pods. Proven end-to-end on a
> live cluster — a real container runs to exit 0 and the Run reaches `Completed`
> with the plugin as sole committer — by `hack/e2e/fullstack-smoke.sh` and
> `hack/e2e/plugin-smoke.sh` (no hand-injected pod phase or lease). #4/#17 (CLI
> simulator) were already addressed by Track CLI; the `--local` path is honestly
> gated and now models the plugin offline. Remaining LOW items (#3 rank/world-
> size checkpoint wiring, #20 workload ETA SDK, #22 AutoRenew, #23 seed logging)
> are separate tracks (CASCADE/ROLES), not the product-premise fakes.

## 1. Executive Summary

**jobtree is a scheduling *simulator* wearing a product costume — and the costume is elaborate.** The entire quota/funding/reservation/lease/lottery *decision engine* is real, tested, and genuinely sophisticated: it correctly computes who pays, who gets preempted, how much capacity is short, and in what order to reclaim. But everything that would make those decisions *touch a real cluster and run a real training job is a mannequin*. The workload-execution layer is the load-bearing fake: `RunSpec` has no container/image/command/resources field anywhere (`api/v1/run_types.go:23-31`), and every pod jobtree creates runs `registry.k8s.io/pause:3.10` with zero GPU requests (`controllers/kube/bridge.go:35,291-294`). Because no pod ever runs a real workload, an entire cascade of downstream features is *structurally inert*: completion detection, `follow` dependencies, workload-reported ETA, node-failure "model-state" preservation, and checkpoint-restart can never fire in production even though their controller code is correctly written — the precondition (a pod reaching `Succeeded`, a workload writing an annotation, a training process holding state) can never occur. On top of that sits a second, independent layer of fakes: the `kubectl runs` CLI is a local-JSON simulator with zero cluster connectivity marketed as a live `kubectl` plugin, and several forecast/observability fields (`conflictSet`, `killProbability`, computed `remedies`, elasticity metrics, a "forecast controller") are documented but do not exist in the API or code at all. The fakes are **not contained** — they sit directly under the product's headline promise ("run GPU gangs"). What *is* contained is the honesty gap: the project's own internal docs (`docs/fundamentals.md`, `docs/project/fundamentals-gap-analysis.md`) already confess most of these gaps, while the user-facing guides and migration docs continue to market them as working.

## 2. Ranked Table of Confirmed Fakes

| # | Feature | Claimed where | Reality | Severity | Fakeness class |
|---|---------|---------------|---------|----------|----------------|
| 1 | **Real job execution / workload container** | Product premise; `docs/index.md`; every migration doc | `buildPod` hardcodes `Image: pauseImage` (`bridge.go:293`); `RunSpec` has no image/command/env/resources field (`run_types.go:23-31`); `nvidia.com/gpu` used only to *read* node capacity (`bridge.go:148`), never requested. **Cannot run any real job.** | **CRITICAL** | Fully-fake (unwired) |
| 2 | **Binder materializes real workload pods / PodTemplate** | Binder design; `docs/user-guide/*` | `binder.PodManifest` carries only Namespace/Name/NodeName/GPUs/Labels (`binder.go:52-58`); no layer anywhere knows a user image. GPU count is a *string annotation* (`bridge.go:286`), not a resource request. | **CRITICAL** | Fully-fake (unwired) |
| 3 | **Node-failure swap "resumes without losing model state / zero topology change"** | `docs/user-guide/spares-and-fill.md`; migration docs | The lease/pod *bookkeeping* swap is real and non-trivial (`run_controller.go:663-716, 1518-1588`), but no `RANK`/`WORLD_SIZE`/`MASTER_ADDR`/checkpoint/restore wiring exists (zero hits repo-wide); the pod it swaps to is another pause container. **No model state exists to preserve.** | **CRITICAL** | Inert-without-dependency (real swap logic, fake workload) |
| 4 | **`kubectl runs` as a live-cluster plugin** | `researcher-guide.md:8-10`; `admin-setup.md:56-76`; krew `runs.yaml`; helm `NOTES.txt`; kueue/slurm migration docs | No `client-go`/`rest.Config`/kubeconfig usage anywhere in `cmd/kubectl-runs/` despite client-go being a dep. Every subcommand loads/saves a local `cluster-state.json` (`state.go:23-90`) and reconciles in-process (`helpers.go:14-16`). It self-identifies as "the local simulator" in an error string (`submit.go:32`). | **CRITICAL** | Fully-fake (simulator mislabeled as live) |
| 5 | **Run/gang completion on pod `Succeeded` (gates `follow` deps)** | `docs/roadmap`; commits 8075b15, 5693ec4 | Detection/propagation machinery is **fully real and wired** — real `podSucceeded` watch predicate (`reconcilers.go:120-135`), real `completeRun` lease-closure. But pause pods never exit, so `Status.Phase` never reaches `Succeeded` in production. Both "passing" tests **hand-inject** the terminal state ("No kubelet in envtest, so drive the workload pod to Succeeded by hand", `scenario_test.go`). | **HIGH** | Inert-without-dependency; tests inject output state |
| 6 | **`earliestStart` / ETA as a real forecast of when capacity frees** | `reservations.md:3-5,34-36`; `researcher-guide.md:87-88` | Plumbing is real end-to-end (`forecast.Plan`→Reservation→Run status→CLI→metric). But the capacity-shortfall branch is a hardcoded `now + 15min` (`forecast.go:131,143`, `DefaultActivationLead`). A 1-GPU and a 500-GPU deficit yield the identical ETA; `estimateDeficit`'s real number is never fed into `conservativeEarliest`. | **HIGH** | Works-but-claim-overstates (constant masquerading as forecast) |
| 7 | **Opportunistic fill of spare (hot-spare) capacity** | `spares-and-fill.md:32-34` | No node-selector/affinity/target field exists on any type (zero repo-wide hits); and `computeUsage` counts spare-held nodes as fully consumed (`run_controller.go:965-983`→`snapshot.go:226-228`), so no other run could ever be placed there even if targeting existed. **Structurally impossible.** | **HIGH** | Fully-fake (unwired + structurally blocked) |
| 8 | **Reservation `conflictSet` / `killProbability`** | `researcher-guide.md:89,93`; `kueue.md:93` | The fields **do not exist** on any CRD type (`reservation_types.go:44-60`, `run_types.go:66-73`); zero hits for `ConflictSet`/`Probability`/`Risk`/`Odds` outside docs. `Confidence` is a 2-value string label, not a probability. | **HIGH** | Fully-fake (nonexistent field) |
| 9 | **"Event streams" observability** | `docs/index.md:11` | No `EventRecorder`/`Broadcaster`/`corev1.Event`/`events.k8s.io` anywhere. The only "stream" is the CLI polling a local JSON file on a timer (`watch.go:12-62`). Internal gap-analysis already admits "There is no event ledger". | **HIGH** | Fully-fake |
| 10 | **`checkpoint` hint tells system when safe to requeue** | `researcher-guide.md:126`; `slurm.md:81` | `Spec.Runtime.Checkpoint` is **read nowhere** — only the struct def (`run_types.go:47`) and generated deepcopy. Node-failure-without-spare goes straight to terminal `Failed` (`run_controller.go:691`) with no checkpoint consultation. | **HIGH** | Fully-fake (accepted-but-unused field) |
| 11 | **`kubectl runs submit -f x.yaml` accepts YAML** | `slurm.md:33-52,85`; `researcher-guide.md:25-41` | Hard byte-check rejects any non-`{` input (`submit.go:32-35`); no YAML decoder imported. Doc-example test silently rewrites `.yaml` args to JSON fixtures (`doc_examples_test.go:58-69`); `root_test.go` writes JSON content into a file named `run.yaml`. Tests inject the accepted format. | **HIGH** | Fully-fake; tests inject valid input |
| 12 | **`kubectl runs plan` shows `conflictSet` / `intendedSlice` / computed `remedies`** | `researcher-guide.md:86-91`; `kueue.md:66-72`; `slurm.md:66-70` | `buildPlanPayload` emits only Run/Phase/Message/EarliestStart/Deficit/Confidence (`plan.go:40-73`); no such rows. The fabricated transcripts aren't even in the file the doc-example test reads (`doc_examples_test.go:38`), so nothing checks them. | **MEDIUM** | Fully-fake (fabricated transcript) |
| 13 | **"Remedies" as a computed situation-specific plan** | `researcher-guide.md:90` ("drop spares (32), shrink malleable (16)") | `defaultRemedies()` returns an identical hardcoded 4-string slice on every call, ignoring all inputs (`forecast.go:291-300`); test pins the constant (`forecast_test.go:117-127`). A real reclaim *engine* exists (`resolver.go`) but never feeds this field. | **MEDIUM** | Works-but-claim-overstates (static list sold as computed) |
| 14 | **Grow/shrink materializing "real" capacity change** | `elastic-runs.md:56-73` | Lease/pod-count accounting is genuinely wired (`run_controller.go:1095-1228`, `bridge.go` real Create/Delete). But the "capacity" grown/shrunk is pause pods with no GPU request — no training job's real footprint ever changes. | **MEDIUM** | Inert-without-dependency |
| 15 | **`runtime.checkpoint` as active safe-requeue control** | `researcher-guide.md:37,60,126` | Same as #10; the project's own `fundamentals.md:209` calls it "the one gap with clear product value" — not yet built. | **MEDIUM** | Fully-fake |
| 16 | **Reservation/Run status "kill probability"** | `researcher-guide.md:93`; `kueue.md:93`; `product/researcher-budget-ux.md:22` | No probability field or computation anywhere; only an aspirational design doc (`M4-...md:10`). | **MEDIUM** | Fully-fake |
| 17 | **`kubectl runs plan/submit` live-cluster (dup vantage)** | helm `NOTES.txt:7-8`; krew description | Same simulator as #4; honestly disclosed only in `docs/cli/kubectl-runs.md:19-21`. | **MEDIUM** | Fully-fake |
| 18 | **`RunSpec.Runtime.Checkpoint` runtime hint** | CRD schema; `fundamentals.md` | Accepted by API, read by nothing (`run_types.go:47` sole non-generated ref). | **LOW** | Fully-fake (declared-but-unused) |
| 19 | **Elasticity observability (`elastic_grows_total` etc.)** | `elastic-runs.md`; M9 design doc; `milestones.md:68` marks M9 done | No `elastic_*` metric defined (`metrics.go`); `growRun`/`shrinkRun` contain zero metrics calls; no Grafana panel. Doc admits "will follow in M9" while roadmap marks M9 complete. | **LOW** | Fully-fake |
| 20 | **Workload-reported ETA (source "job")** | commit 7c2d8d3; ETA design | Mirror pipeline (annotation→watch→`mirrorETA`) is real and envtest-verified. But no SDK/sidecar ships (`find` for sdk/sidecar returns nothing) and the pause container can't call the API — nothing can ever produce the annotation. | **LOW** | Inert-without-dependency |
| 21 | **`jobtree_reservations_backlog_seconds` as live per-flavor forecast** | `admin-setup.md` | Real forecast-derived value (`run_controller.go:771`), but frozen between reservation creation and activation (no timer requeue while `PendingReservation != nil`), keyed by flavor only (concurrent reservations collapse), and never cleared on activation/release — persists forever. | **LOW** | Works-but-claim-overstates |
| 22 | **AutoRenew rotation of open-ended envelopes** | CRD schema; `budgets.yaml:74-85` | `Spec.AutoRenew` read nowhere; `validate()` ignores it; `BudgetStatus` has no renewal field (`budget_types.go`). Self-disclosed inert in `fundamentals.md:210`. | **LOW** | Fully-fake (declared-but-unused) |
| 23 | **Attested lottery seeds discoverable via controller logs** | `admin-setup.md:72-76` (`kubectl logs ... \| rg RandomPreempt`) | Seed lives only in `Action.Reason`→`lease.Status.ClosureReason` (CRD, `run_controller.go:834`); metrics record only action *kind* (`:838`). No `.Info`/`.Error`/event-recorder call exists in the reconcile path — the seed is never logged. | **LOW** | Fully-fake |
| 24 | **Dedicated "forecast controller" + `jobtree_forecast_latency_seconds`** | `admin-setup.md:93` | No `ForecastReconciler` type; forecast is an inline library call (`run_controller.go:740`). The metric string appears in zero Go files. | **LOW** | Fully-fake (nonexistent) |
| 25 | **Spares "accounted at a discount"** | `researcher-guide.md:116` | `accrue`/`commit` charge spare leases at full rate with no Role branch (`evaluate.go:667-673,523`); spare width is only a *reporting* counter. Design docs (`M6-...md:77`) confirm discount is "policy pending", never built. | **LOW** | Fully-fake (contradicted by own design docs) |
| 26 | **Shell completions reflect the command tree** | `completions.go` | Static hand-written map literals (`completions.go:31-59`), not generated from Cobra's tree — silently rot when subcommands change (already stale on sibling branches missing `complete`/`eta`). | **LOW** | Works-but-brittle-and-static |

## 3. The Pattern — Why These Slipped

Four systemic causes, in order of blast radius:

1. **One fake load-bearing layer sterilizes everything downstream.** The missing workload container (`RunSpec` has no image/command/resources; `buildPod` hardcodes `pause`) is not just one fake — it is the *root* that makes findings #3, #5, #14, #15, #20 inert. Their controller code is often genuinely correct, but the precondition they wait on (a pod reaching `Succeeded`, a training process holding rank/state, a workload calling the API) can never occur. This is the pause-pod discovery generalized: **an entire tree of real code hangs off a fake trunk.** A reviewer reading any single downstream file sees plausible, well-tested logic and moves on.

2. **Tests inject the exact output state the feature is supposed to produce.** This is the single most effective camouflage. `scenario_test.go` hand-sets `pod.Status.Phase = Succeeded` with the comment "No kubelet in envtest… drive the workload pod to Succeeded by hand"; `forecast_test.go:117-127` pins the hardcoded `defaultRemedies` constant as expected output; `doc_examples_test.go:58-69` rewrites `.yaml` args to JSON before running; `root_test.go` writes JSON into a file named `run.yaml`. Each of these is a *green test* that proves the plumbing *given the output*, never that the feature *derives* the output. A passing suite actively hides the gaps.

3. **Docs describe intent as present-tense reality, and the doc set disagrees with itself.** The honest disclosures exist — but in the *internal* docs (`fundamentals.md`, `fundamentals-gap-analysis.md`) and one CLI reference (`kubectl-runs.md:19-21`) — while the *user-facing* researcher/operator/migration guides market the same features as working. `milestones.md:68` marks M9 "done" while `elastic-runs.md:86-87` says its metrics "will follow in M9". The project literally documents both the truth and the lie in different files.

4. **API surface accepts fields no controller reads.** The cheapest fake: add a CRD field, ship the schema, never wire it. `runtime.checkpoint` and `budget.autoRenew` are accepted, schema-validated, deep-copied — and read by nothing. The API *shape* implies a capability the *behavior* never delivers, and CRD-schema review can't catch a field that's simply ignored.

## 4. What Is Actually Real and Solid

To be fair — the *brain* of jobtree is genuine engineering, and it is the majority of the differentiated value:

- **Funding / quota calculus** — `pkg/funding/evaluate.go` genuinely replays GPU-hour accrual against envelope caps (`ConsumedGPUHours`, `:669-673`), enforces concurrency-width admission (`:523`), and handles borrowing/lending/aggregate caps. Real, tested arithmetic.
- **Lease lifecycle & the K8s bridge** — `bridge.go apply()` does real `client.Create`/`Delete` diffing of Pods and Leases against a live snapshot; `load()` reads real pod/node status. The *object choreography* is production-grade — it's only the pod *contents* that are fake.
- **Reservation forecasting deficit math** — `estimateDeficit` (`forecast.go:146-191`) computes a genuinely data-driven shortfall from free-GPU counts and funding headroom (even though the *ETA* attached to it is a constant).
- **The resolver / reclaim engine** — `pkg/resolver/resolver.go` implements a real, situation-specific unfunded→spares→shrink→lottery cascade with actual per-run GPU quantities and a seeded fair lottery, wired into the reconcile loop (`run_controller.go:280,542`). (Ironically, the docs attribute this real engine's output to the *fake* `remedies` field.)
- **Elastic grow/shrink accounting** — driven from `Spec.Malleable.DesiredTotalGPUs` through real planner/binder/lease code (`run_controller.go:1037-1228`); the *bookkeeping* is real, only the underlying pods are hollow.
- **Node-health-driven failure handling** — `NodeReconciler` watches real `corev1.Node` health and drives a real lease/pod swap (`reconcilers.go:208-233`).

**The honest one-liner: jobtree is a correct and fairly advanced GPU *scheduling and accounting engine* that has never been connected to a GPU or a workload.**

## 5. Remediation Priority

**Tier 0 — Must fix or jobtree is not a product (blocks everything):**
1. **Add a workload spec to `RunSpec`** — container image/command/args/env/volumes, and plumb it through `PodManifest` (`binder.go:52-58`) into `buildPod` (`bridge.go:280-297`). This is the trunk; #2, and the preconditions for #3/#5/#14/#20 all depend on it.
2. **Request real GPUs** — put `nvidia.com/gpu` into the container's `Resources.Limits` in `buildPod`, and set nodeSelector/runtimeClass from `GPUType` (currently read-only). Without this the device plugin never allocates and pods never land on real GPU nodes.
3. **Re-validate the downstream cascade against real pods** — once #1/#2 land, #5 (completion), #14 (elastic), #20 (workload ETA) should become genuinely live; delete the hand-injection lines in `scenario_test.go` and require the tests to drive pods to `Succeeded` via a real (or realistically faked) exit.

**Tier 1 — Fix to make advertised control-plane features true:**
4. **Wire `runtime.checkpoint`** (#10/#15) into `HandleNodeFailure` so a checkpoint window produces a requeue instead of terminal `Failed` (`run_controller.go:691`). The docs already promise this as fact.
5. **Implement, or stop claiming, node-failure state preservation** (#3) — inject `RANK`/`WORLD_SIZE`/`MASTER_ADDR` and a checkpoint-restore path, or delete the "resumes without losing model state" claim.
6. **Make the ETA data-driven** (#6) — feed `estimateDeficit`'s result into `conservativeEarliest` instead of the hardcoded 15-minute constant, or relabel the field as a fixed activation lead, not a forecast.

**Tier 2 — Truth-in-labeling (cheap; mostly doc/CLI fixes):**
7. **Relabel the CLI everywhere** (#4/#17) — it is a local simulator; propagate the honest `kubectl-runs.md:19-21` caveat into researcher/operator/migration docs, krew, and helm `NOTES.txt`. Either add real client-go wiring or stop calling it a live plugin.
8. **Delete fabricated CLI transcripts** (#12) and non-existent fields from docs (#8 `conflictSet`, #16 `killProbability`), or build the fields.
9. **Either compute `remedies` per-situation** (#13) or change docs to show the static list honestly.
10. **Accept YAML in `submit`** (#11) or remove `.yaml` examples; fix the tests that inject JSON into `.yaml` files.

**Cosmetic / low-stakes (fix opportunistically, or just correct the docs):**
- #7 opportunistic fill, #18 checkpoint field doc, #19 elasticity metrics (unmark M9 "done"), #21 backlog gauge staleness/leak, #22 AutoRenew, #23 seed-in-logs, #24 phantom forecast controller/metric, #25 spare discount, #26 static completions. Most of these are best resolved by **deleting the claim** rather than building the feature — they promise polish on a product whose core doesn't run yet.

**Bottom line for the owner:** the alarm is justified but the situation is recoverable. The expensive, hard part — the scheduling/funding/reservation brain — is real and good. The gap is that it was never connected to a runnable workload, and the docs/tests papered over that gap rather than exposing it. Fix Tier 0 and jobtree becomes a real scheduler; the rest is honesty cleanup.