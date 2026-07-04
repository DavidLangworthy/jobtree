<!-- Internal design doc (docs/project/ is excluded from the built site). Produced from a
grounded multi-agent research pass (codebase + PyTorch/RL/datagen/eval + SLURM/Kueue + frontier
systems + academic ML schedulers) on 2026-07-04. Owner decision: embed a full corev1.PodTemplateSpec. -->

# jobtree Workload Materialization: Embedding a PodTemplateSpec

*Design + work plan. Grounded in `controllers/kube/bridge.go`, `api/v1/run_types.go`, `pkg/binder/binder.go`, `controllers/run_controller.go`. Owner decision (fixed): the Run embeds a full `corev1.PodTemplateSpec`; jobtree overlays scheduling-derived fields.*

---

## 1. Current state

jobtree's scheduling brain is complete but its body is a mannequin. A bound slice becomes a `binder.PodManifest{Namespace,Name,NodeName,GPUs,Labels}` (`pkg/binder/binder.go:60-67`, built at `:224-240`), and `controllers/kube/bridge.go:281-298 buildPod()` renders it as a **placeholder**: one container named `workload` running `registry.k8s.io/pause:3.10` (`bridge.go:35`), `spec.nodeName` pinned, `RestartPolicy=Never`, three labels, and a single annotation `rq.davidlangworthy.io/gpus=<count>` (`bridge.go:287`). `RunSpec` (`api/v1/run_types.go:23-32`) carries **only** `{owner, resources{gpuType,totalGPUs}, locality, runtime, malleable, funding, spares, follow}` — no image, command, env, volumes, or CPU/mem. The load path reconstructs GPU usage by *parsing that annotation back* (`bridge.go:160`), never from a real request. **The critical gap:** `buildPod` never sets `resources.limits["nvidia.com/gpu"]`. `GPUCapacityResource` (`bridge.go:30`) is used only to *read* node capacity on load (`bridge.go:148`), never to *request* GPUs. On a real cluster the NVIDIA device plugin therefore injects **zero** GPUs into the container, and nothing stops two jobtree pods (or a jobtree pod and a foreign pod) from landing on the same physical devices. jobtree today cannot run any real training job.

---

## 2. API change: `RunSpec.Template`

### 2.1 The shape

Add a single field to `RunSpec` (`api/v1/run_types.go:23-32`):

```go
import corev1 "k8s.io/api/core/v1"

type RunSpec struct {
    Owner     string           `json:"owner"`
    Resources RunResources     `json:"resources"`
    // Template is the researcher's workload pod. jobtree deep-copies it per
    // materialized slice and overlays scheduling-derived fields (nodeName,
    // GPU limit, gang labels, rendezvous env). See the injection contract.
    Template  corev1.PodTemplateSpec `json:"template"`
    Locality  *RunLocality     `json:"locality,omitempty"`
    // ... unchanged
}
```

`k8s.io/api/core/v1` is already an indirect dependency (`k8s.io/api v0.36.2` in `go.mod`), so no new module. The CLI already round-trips `RunSpec` as JSON (`cmd/kubectl-runs`), so a submitted Run carries `spec.template` for free — no CLI schema work beyond documenting the field.

### 2.2 Single template vs. per-role (the taxonomy question)

The taxonomy facet is right that RL/RLHF (vLLM/SGLang rollout actors + FSDP learners + reward models), Ray head/worker, and LWS leader/worker are **irreducibly multi-shape** and cannot be expressed by one homogeneous template — while `PyTorchJob` Master/Worker is a *false positive* that collapses to a homogeneous gang + rank-0. **The recommendation is least-regret, and it splits the decision in two:**

- **The engine already lowers a Run to a *set* of groups.** `pkg/binder.Materialize` walks `req.PackPlan.Groups` (`binder.go:98-110`) and cover segments as two cursors. This is the natural seam for per-role gangs later — the atomicity boundary is already "the whole pack plan," not "one pod."
- **The API surface, however, should NOT pretend to be multi-role in v1.** The owner picked a singular `Template` and that is correct for the 90%. But do **not** hard-code "exactly one template" in a way that forces a breaking CRD change to add roles. Two honest options:

  | Option | v1 surface | Growth path | Verdict |
  |---|---|---|---|
  | **A. Singular `Template` (owner's pick)** | `spec.template` | Adding `spec.roles[]` later is *additive* (new optional field); a validating rule requires exactly one of `template`/`roles`. No breaking change. | **Recommended.** |
  | B. `spec.roles[]` now, v1 validates `len==1` | `spec.roles[0].template` | No schema change for multi-role, but every v1 user writes a list-of-one for a feature that doesn't exist. | Over-built for v1. |

  **Recommendation: ship Option A.** A singular `Template` covers homogeneous DDP/FSDP training, datagen, and eval (the real v1 90%). Multi-role RL is a genuine fast-follow, and because it arrives as a *new optional `roles[]` field* validated as mutually exclusive with `template`, it is not a breaking change. Reserving a list-of-one now buys nothing except confusing every early user. The load-bearing commitment is architectural, not syntactic: **keep the binder's group loop and the atomicity boundary role-extensible** (don't collapse `PackPlan.Groups` handling into a single-pod assumption), and **do not drop `LeaseSpec.compPath`** — it is the natural per-role funding tag when `roles[]` lands.

### 2.3 Defaulting, validation, deepcopy, CRD

- **Validation** (`Run.validate()`, `run_types.go:190`): require `len(Template.Spec.Containers) >= 1` and that the GPU-target container (see §5) has a non-empty `Image`. Reject template-set `spec.nodeName`, `spec.restartPolicy != ""`, and jobtree-reserved env names (or override them silently — decide in §3). Because pods are create/delete-only (`bridge.go:218-231`, no spec Update), **reject `Template` edits while the Run is `Running`** — a template change never propagates to live pods, so silently accepting it is a foot-gun.
- **Deepcopy:** `RunSpec.DeepCopyInto` is currently a shallow `*out = *in` (`zz_generated.deepcopy.go`). `corev1.PodTemplateSpec` has its own `DeepCopyInto`, so `make generate` (controller-gen `object`) will emit `in.Template.DeepCopyInto(&out.Template)`. **This must be regenerated before merge** — `run_controller` passes `run.DeepCopy()` into the binder (`run_controller.go:249,809,1367`); until regenerated, the template's maps/slices alias across copies.
- **CRD size:** `make manifests` inlines the *entire* PodTemplateSpec OpenAPI schema into `config/crd/bases/...runs.yaml` **and** the Helm copy `deploy/helm/gpu-fleet/crds/` (both diffed by `verify-generate`). The pod schema is hundreds of KB — it risks the 262144-byte `last-applied-configuration` annotation limit under `kubectl apply`. **Mitigation: mark the field `+kubebuilder:pruning:PreserveUnknownFields` (x-kubernetes-preserve-unknown-fields)** so the giant schema is not inlined; validate the template in the webhook instead. `allowDangerousTypes=true` is already set (needed for the float64 GPU-hours), so `resource.Quantity` fields need no Makefile change. Install CRDs `--server-side` as a belt-and-suspenders.

---

## 3. The injection contract (the heart of it)

`buildPod` is the single choke point every materialized pod flows through — both the binder path (`bridge.go:220`) and the swap path (`run_controller.go:1797` builds a `PodManifest` that also reaches `bridge.buildPod`). The overlay lives **only** in `buildPod`, rewritten from "synthesize a pause pod" to "deep-copy the researcher's template and overlay jobtree-owned fields." `apply()`'s create loop (`bridge.go:218-224`) must first resolve the owning Run from `state.Runs` by `keys.NamespacedKey(manifest.Namespace, manifest.Labels[binder.LabelRunName])` and pass `run.Spec.Template` into `buildPod`. Handle the deleted-Run race defensively (though `cleanupDeletedRun` already prunes orphan pods before apply).

**Precedence rule: jobtree wins on its fields, unconditionally. The researcher owns everything else.**

| Field | Owner | Overlay behavior |
|---|---|---|
| `spec.nodeName` | **jobtree** | Force-set to `manifest.NodeName`. Overrides any template `nodeName`/`nodeSelector`/`affinity`/`schedulerName` — jobtree placed it; kube-scheduler never runs. |
| `metadata.name` | **jobtree** | Force-set per slice (`run-gNN-role-node-seq`, `binder.go:235`). Template must not set a name. |
| `metadata.namespace` | **jobtree** | Force-set to `manifest.Namespace`. |
| `metadata.labels` | **jobtree (merge)** | Merge in `run`/`group-index`/`role` (`binder.LabelRunName` etc.); do not clobber researcher labels. |
| `metadata.annotations` | **jobtree (merge)** | Merge in `rq.davidlangworthy.io/gpus=<GPUs>` — the load path parses it back (`bridge.go:160`). |
| `spec.restartPolicy` | **jobtree** | Force `Never`. A `Succeeded` pod is the gang-completion signal (`run_controller.go:386`); any other policy breaks the contract. |
| `resources.limits["nvidia.com/gpu"]` | **jobtree** | Inject `= manifest.GPUs` on the target container (§5). THE missing piece. |
| GPU nodeSelector / tolerations / `runtimeClassName` | **jobtree** | Stamp from the resolved `gpu.flavor`/taints of the pinned node (defer auto-config, §11). |
| rendezvous env (`MASTER_ADDR`, `WORLD_SIZE`, `RANK`/`NODE_RANK`, …) | **jobtree** | Append per pod (§4), only when enabled. Reserved names — reject or override if researcher sets them. |
| `/dev/shm` emptyDir (`medium=Memory`) | **jobtree default, researcher override** | Inject a memory-backed `/dev/shm` volume+mount unless the template already mounts `/dev/shm`. |
| `image`, `command`, `args` | **researcher** | Untouched. |
| researcher `env` (dataset paths, `HF_TOKEN`, `WANDB_*`) | **researcher** | Untouched; jobtree *appends* its vars, never overwriting a researcher key of the same name (except reserved rendezvous names). |
| `volumes` / `volumeMounts` (dataset/checkpoint PVCs) | **researcher** | Untouched. |
| `imagePullSecrets`, `serviceAccountName`, `securityContext` | **researcher** | Untouched. |
| `resources.requests/limits` cpu/memory | **researcher** | Untouched. |
| `initContainers`, sidecars, probes | **researcher** | Untouched (guidance: no aggressive livenessProbe — it kills a healthy rank mid-allreduce). |

**Overlay = additive + decision-only.** It copies the template, force-sets the ~6 jobtree-owned fields, merges labels/annotations, and appends env — it never drops a researcher container, volume, secret, or SA.

---

## 4. Distributed env injection

The binder knows, at materialize time, the full gang: every group's node, GPUs-per-pod, group ordinal, and ordering. That is exactly the rendezvous. jobtree injects the **node-level** vars; the in-pod launcher (`torchrun`) derives per-*process* ranks — jobtree must **not** set `RANK`/`LOCAL_RANK` when `torchrun` is used (one pod == one node; `torchrun --nproc-per-node` forks one rank per GPU).

| Var | Value | Source |
|---|---|---|
| `MASTER_ADDR` | Stable DNS of the rank-0 (group-0) pod via a per-Run **headless Service** — never a pod IP (IPs are unknown pre-schedule and change on restart). | jobtree |
| `MASTER_PORT` | Fixed/derived per Run (e.g. 29500). | jobtree |
| `WORLD_SIZE` | Total ranks = `Run.Resources.TotalGPUs` (1 rank/GPU). | jobtree |
| `NNODES` | Number of pods in the gang. | jobtree |
| `NPROC_PER_NODE` | This pod's GPU count (`manifest.GPUs`) — must equal its `nvidia.com/gpu` limit. | jobtree |
| `NODE_RANK` | This pod's group ordinal (`LabelGroupIndex`), distinct per pod, exactly one node-0. | jobtree |
| `RANK` / `LOCAL_RANK` | **Not set** when `torchrun` is used. Set `RANK` per-pod *only* in the one-process-per-pod (no-torchrun) case. | launcher |

**Making it optional.** Rendezvous is meaningless for single-pod datagen/eval and independent shards. Gate injection on a signal:
- **Simplest v1:** inject rendezvous **only when the gang has >1 pod** (`NNODES > 1`). A single-pod Run gets no `MASTER_ADDR`/headless Service. This is a pure function of the pack plan — zero new API.
- **Explicit alternative (defer):** a `spec.workloadClass: training|batch` enum. Cleaner intent, but adds surface; the pod-count heuristic covers v1.

For batch/eval, inject only a per-pod shard index (`RQ_SHARD_INDEX`/`RQ_SHARD_COUNT`, `JOB_COMPLETION_INDEX`-compatible) plus `RQ_RUN_NAME`/`RQ_GROUP_INDEX` — surfaced from the labels the binder already stamps. Never `MASTER_ADDR`/`WORLD_SIZE` on a single-pod job.

**Elastic re-injection caveat:** pods are create-only. On a malleable grow/shrink or spare-swap, only *newly* materialized pods carry the new `WORLD_SIZE`. A static rendezvous world therefore does **not** auto-resize. v1 honest stance: rendezvous reflects the width at materialization; true elastic re-rendezvous (torchelastic c10d `--nnodes=MIN:MAX`, `rdzv-backend=c10d`) maps cleanly onto `Malleable`/`Spares` and is the right long-term target (§9), but is deferred.

---

## 5. The GPU-resource fix

The one change that turns the mannequin into a runnable body: **inject `resources.limits["nvidia.com/gpu"] = manifest.GPUs`** onto the workload container in `buildPod`. Extended resources require `request == limit` and are integer/non-overcommit; set both. Once present, kubelet's Device Manager allocates the devices and injects `NVIDIA_VISIBLE_DEVICES` **even though jobtree pinned `nodeName`** — device-plugin admission runs at the kubelet regardless of scheduler bypass. jobtree does *not* set `NVIDIA_VISIBLE_DEVICES` itself; it appears as a side effect. Do **not** hard-set `CUDA_VISIBLE_DEVICES` (it fights `torchrun`'s per-rank `LOCAL_RANK` device selection).

**Target container:** keep the convention of the container named `workload` (`bridge.go:293`), and validate in the webhook that it exists (or fall back to `Containers[0]`) so a template can't silently produce a zero-GPU pod.

**New failure mode to own:** because `nodeName` is pinned, kube-scheduler never verifies free `nvidia.com/gpu`. Adding a real limit makes **kubelet admission the first-ever fit check** — if jobtree's ledger races the device plugin (stale node view), the kubelet rejects the pod with `UnexpectedAdmissionError` and there is no scheduler to retry. Today the pause pod requests nothing and never fails admission; this change introduces the failure. Reconcile jobtree's annotation ledger against `node.Status.Allocatable[nvidia.com/gpu]` and treat admission rejection as a NodeFailure-style event.

**GPU-ordinal pinning:** not needed for v1. jobtree's model is count-based (`topology.Node{Capacity,Used}`; lease `node#ordinal` is *logical* accounting, `binder.go:245`). The device plugin picks physical GPUs from the count. Revisit only for MIG/MPS/deterministic slice-to-GPU mapping — which needs a physical-GPU-identity concept that does not exist today.

---

## 6. Non-training cases (datagen / eval)

The same template + overlay serves these once two things are optional:

1. **GPU optional.** Relax `totalGPUs > 0` (`run_types.go:197`) to allow `0`, or gate it behind a workload type. A zero-GPU Run must **skip GPU Cover/Pack/lease/charge entirely** and schedule by pod count + the template's cpu/mem requests, and **emit no `nvidia.com/gpu` limit**. This unblocks CPU datagen and API-based graders, which today cannot even be *expressed* — and it matters for `follow`: a CPU datagen stage holding a GPU lease defeats the datagen→train→eval handoff (`follow` closes upstream leases on Completed before the follower admits).
2. **Rendezvous optional** (§4): single-pod eval / independent shards get only the shard-index env.

**Completion policy gap (must fix for batch).** `runGangComplete` (`run_controller.go:375-391`) returns true only when *every* active pod is `Succeeded`; a `Failed` pod is "not Succeeded," so it **neither completes nor fails the run** — the run hangs forever holding capacity. That is tolerable-ish for a training gang you'll restart, but wrong for a datagen shard or eval with no retry. Add a per-workload-class failure policy: for batch/eval, an active pod `Failed` should fail the Run (or bounded retry). Keep the process-exit-0 → `Succeeded` → aggregate signal as the universal completion contract; just make a real container (not `pause`) the thing that exits, and ensure a Failed pod is terminal.

---

## 7. Replacing SLURM and Kueue

This is the competitive frame, not a footnote. jobtree already owns the *hard* semantics both systems are known for — gang allocation (Pack is all-or-nothing per width, each group pinned inside one fabric domain), quota/fairshare (Budget envelopes + family DAG + aggregate caps + proximity-then-recency ranking), reservations, and dependencies (`follow.after` = `afterok` AND-semantics with a fix-and-resubmit grace strictly nicer than SLURM's silent `DependencyNeverSatisfied` cancel). The blocker for both migrations is the **workload materialization layer** this doc builds.

### 7.1 SLURM

SLURM is what ML researchers already know: job scripts, `SLURM_*` env, `--dependency=afterok` ≈ our `follow`, fairshare ≈ our budgets. The migration blocker is not scheduling — it's that a bound slice is a `pause` pod with no image, no env, no `/dev/shm`, no shared FS, no peer DNS.

**Directive mapping:**

| `#SBATCH` / srun | jobtree | Notes |
|---|---|---|
| `--gres=gpu:N` / `--gpus-per-node` | `template` container `nvidia.com/gpu` limit (jobtree-injected, §5) + `locality.groupGPUs` | Per-pod GPU count derived by the binder. |
| `--nodes=K` | gang width (`resources.totalGPUs` / per-node GPUs) | Pack packs K nodes in one fabric domain. |
| `--ntasks-per-node` | `NPROC_PER_NODE` (= pod GPU limit) | Consumed by `torchrun --nproc-per-node`. |
| `--cpus-per-task`, `--mem` | template `resources.requests` cpu/memory | Researcher-owned; not modeled in `RunResources` today. |
| `--dependency=afterok:jobid` | `follow.after: [run]` | **Already built** (`run_types.go:46`). AND-semantics + grace. |
| `--partition` | Budget envelope selector (`Flavor`/`Selector`) | |
| `--qos` | family / aggregate-caps | |
| `--array` | *no primitive* (defer, §11) | Many Runs or an external indexed wrapper for now. |
| `--time` | *no analogue* | jobtree leases are open-ended (accounting horizon ≠ walltime kill). Optional `activeDeadlineSeconds` if a hard cap is wanted. Flag loudly in any translator. |
| `--requeue` | *checkpoint-requeue not built* (§9) | Node loss without an in-domain spare is terminal Failed today. |

**Recommendation on `SLURM_*` env: yes, inject the compat set alongside the torch vars.** It is the single biggest "unmodified script runs as-is" lever and is fully derivable from gang membership jobtree already computes: `SLURM_JOB_ID` (Run UID), `SLURM_JOB_NODELIST` (from gang pod DNS — needs the headless Service), `SLURM_NNODES`, `SLURM_NODEID` (group ordinal), `SLURM_NTASKS`/`SLURM_NTASKS_PER_NODE`, `SLURM_GPUS_ON_NODE` (`manifest.GPUs`), `SLURM_JOB_NAME`. **Do not** inject per-*task* `SLURM_PROCID`/`SLURM_LOCALID` — one pod == one SLURM node; those are stamped by the in-pod launcher exactly as `srun` does inside an allocation. Pretending to set them from the controller is wrong when `nproc-per-node > 1`. (For v1 leanness, the torch vars are the must-have; `SLURM_*` is a cheap, high-value follow — see §11.)

**Concrete before/after:**

```bash
#!/bin/bash
#SBATCH --job-name=llama-ft
#SBATCH --nodes=2
#SBATCH --gres=gpu:8
#SBATCH --ntasks-per-node=8
#SBATCH --dependency=afterok:4213      # the datagen job
srun torchrun --nproc-per-node=8 train.py --data /lustre/ds
```

becomes a jobtree Run (the binder injects `MASTER_ADDR/NNODES/NODE_RANK/NPROC_PER_NODE` + `SLURM_*`; `torchrun` reads them):

```yaml
apiVersion: rq.davidlangworthy.io/v1
kind: Run
metadata: { name: llama-ft, namespace: ml }
spec:
  owner: org:ai:rai
  resources: { gpuType: h100, totalGPUs: 16 }
  locality: { groupGPUs: 8 }          # 8-GPU NVLink node per group
  follow: { after: [datagen] }        # replaces --dependency=afterok
  template:
    spec:
      containers:
      - name: workload
        image: ghcr.io/acme/llama-ft:cu124
        command: ["torchrun", "--nproc-per-node=8", "train.py", "--data", "/mnt/ds"]
        resources: { requests: { cpu: "48", memory: 384Gi } }
        volumeMounts: [{ name: ds, mountPath: /mnt/ds }]
      volumes: [{ name: ds, persistentVolumeClaim: { claimName: lustre-ds } }]
```

`follow` replaces `--dependency`; budgets replace fairshare/QOS; the researcher deletes `#SBATCH` scheduling lines and keeps their `torchrun` line verbatim. What does *not* translate (say it out loud in docs): `--time`, `--array`, and fairshare priority knobs (jobtree has no user-set priority by design).

### 7.2 Kueue

Kueue queues/suspends *existing* workload types (`batch/v1 Job`, JobSet, PyTorchJob, RayJob, LeaderWorkerSet) by flipping `.spec.suspend`; it never owns a pod template. jobtree instead owns the whole lifecycle.

**Pressure-testing "embed" vs. Kueue's "wrap": the embed decision HOLDS, for a specific reason.** jobtree's differentiators — per-slice topology packing (`pkg/pack`), elastic width grow/shrink, spare-swap on node failure, per-slice Lease attribution — all require **pod-granular** control. Wrapping a single `batch/v1 Job` would *fight* this: a Job owns and re-creates its own pods and would relocate them out from under the binder. Document *this* as the rationale, not "it's simpler." The honest cost of embedding: jobtree must reimplement the rank/rendezvous env, headless DNS, and success/failure policy that JobSet/PyTorchJob give Kueue for free — which is exactly §3–§4 and §6 of this doc.

**Concept mapping (mostly already present):**

| Kueue | jobtree | Status |
|---|---|---|
| ResourceFlavor (node labels + tolerations) | `topology/labels.go` (`gpu.flavor`/`region`/`fabric.domain`) + `BudgetEnvelope.Flavor/Selector` | labels done; **tolerations injection is a gap** — add so tainted GPU pools work. |
| ClusterQueue quota | `BudgetEnvelope` concurrency + `MaxGPUHours` | done (GPU-only). |
| Cohort borrowing / lendingLimit | family proximity sharing + `LendingPolicy` caps | done; family sharing needs *no* lending gate because owner-recall protects the lender. |
| Suspend / admit | `Pending`/`Waiting` phases; pods created only at admit | done — but jobtree *deletes* pods on reclaim (destructive) vs Kueue's non-destructive `.spec.suspend`. |
| Preemption / reclaim | `pkg/resolver` lottery (unfunded → spares → shrink → lottery) | done. |

**What jobtree must MATCH:** borrowing semantics (map `borrowingLimit`/`lendingLimit` precisely onto `LendingPolicy` for strangers) and gang admission (all-or-nothing width — already done). **What it ADDS beyond Kueue:** topology-aware pack-to-empty, the derived RQΛ funding calculus (owner-recall, demote-not-kill, unfunded-first reclaim — TLA-specced), immutable auditable Lease ledger, `follow`-forests, and productive spares. **Honest gaps vs Kueue** (flag, don't hide): GPU-only quota (no cpu/mem/DRA), no workload PriorityClass, no weighted/DRF fair-share, no ProvisioningRequest autoscaler hook, and destructive (non-suspend) reclaim. **Doc correction:** `docs/migrations/kueue.md`'s "Jobtree handles PodTemplate generation automatically" is currently *false* (it generates a pause placeholder) — rewrite once §3 lands.

---

## 8. Precedent (brief)

Every established operator embeds a full `PodTemplateSpec` per role and overlays a small identity+rendezvous set — validating the owner's decision:

- **Kubeflow PyTorchJob:** `ReplicaSpec.Template` is a full `v1.PodTemplateSpec`; injects `MASTER_ADDR/PORT/WORLD_SIZE/RANK` + a headless master Service; per-role `RestartPolicy` overrides the template. Default success = leader (rank-0) Succeeds; `SuccessPolicy=AllWorkers` is the opt-in "all must succeed" mode — jobtree's `runGangComplete` is exactly `AllWorkers`, a real precedent but not the common default.
- **JobSet:** `ReplicatedJob.template` = full JobTemplateSpec; Indexed hostnames + headless Service; `SuccessPolicy(operator=All|Any)`, `FailurePolicy(maxRestarts, rules)`.
- **Volcano vcjob:** `tasks[].template`; env/svc plugins inject `VC_TASK_INDEX`/host lists; gang via PodGroup `minMember`.

jobtree mirrors the embed-and-overlay pattern; what it must not copy blindly is "all workers Succeeded" as the *only* completion rule (§6).

---

## 9. The broader ML vision

Why the podspec work matters and where it leads. jobtree's genuine moat is the **RQΛ funding calculus** — funding class is *derived, not stored* (recall = re-ranking, preemption = arithmetic, every decision reproducible by replay). SLURM has fairshare-as-black-box; Kueue has quota-admission with no lifecycle; the frontier (Pollux, Gavel, Sia, Determined) has brilliant ML mechanics but no auditable economic model. **The thesis: jobtree is the only system where the ML-smart decision and the money are the same object.** Ranked:

**Near-term (builds directly on this podspec work):**

1. **Budget-driven, explainable fair-share — shippable today.** The `(proximity, admission-time, name)` ranking + published-seed lottery (`pkg/funding rankLess`, `pkg/resolver`) is already built and TLA-specced. The opportunity is *surfacing* it (expose finish-time ρ and attained-GPU-time as derived `Run.status` explainers). Honest caveat: this is legible *ordering*, not proportional/DRF fairness — no `PriorityWeightFairshare` knob, by design.
2. **Opportunistic/unfunded execution as free ML throughput.** Unfunded tier + no-lending-gate family sharing already exist; over-quota work runs and is cut first. Perfect for preemption-tolerant datagen/eval/sweeps. **Blocked on the zero-GPU path (§6)** so CPU datagen isn't shoehorned into the GPU allocator.
3. **Topology-aware collectives with in-domain guarantees.** Cover→Pack→Bind is already deterministic pack-to-empty (stronger than Kueue TAS), but it never *grants the fabric*. Near-term: inject `NCCL_IB_*`, request `rdma/hca`, add `IPC_LOCK`, mount memory-backed `/dev/shm`. The deepest gotcha in the whole corpus is the §5 fix — topology placement is worthless until real `nvidia.com/gpu` requests exist.
4. **`follow`-forests as the native pipeline object.** datagen→train→eval as Runs joined by `follow`, one family budget, capacity handed down as each stage closes its leases — dependency graph + funding in one object, no external orchestrator. Already built for `afterok`; needs `afterany`/`afternotok` and the batch failure policy (§6).

**Research / forward-looking (honest about fragility):**

5. **Migration-as-reclaim.** Generalize the existing `HandleNodeFailure` close-old-lease+mint-new-lease swap (`run_controller.go:930`) into a first-class migration every reclaim path invokes — so "quota is a claim, not a wall" costs ≈ one checkpoint flush, not a kill. **Near-term half:** requeue-from-checkpoint (mount storage, `preStop` drain, honor `terminationGracePeriod`, resume). **Research half:** transparent CRIU/CUDA live migration (version-sensitive; mid-collective migration corrupts in-flight all-reduces — must quiesce at a minibatch boundary). Ship requeue first; be honest "live" is aspirational. The ledger fold must stay close+mint pairs — never mutate a lease's slice — or audit-by-replay breaks.
6. **Goodput-driven, budget-aware elasticity.** Drive `malleable.desiredTotalGPUs` + cover-planner flavor choice from a per-run goodput model (throughput × statistical efficiency), gated through the funding calculus so a run only grows into GPU-hours it can pay for at positive marginal goodput. The fusion no one else has: marginal goodput per *funded* GPU-hour. Signal must stay **derived** (never a stored CRD field) and damped with hysteresis or it thrashes leases.
7. **Heterogeneous RL gangs as one funded unit.** A Run owning a *set* of typed role-gangs (rollout actors + learners + reward models), admitted atomically across roles, one budget, per-role GPU-hour attribution via `compPath`. The schema decision (§2.2) is the near-term, urgent part; landing cross-role weight-sync rendezvous is a real project.
8. **Spot + productive-spares + checkpoint-restart as one story.** Treat a spot-termination notice as identical to a node failure → same swap into a productive hot-spare; if none, checkpoint-requeue. Turns fault-tolerance cost *negative* (your insurance runs unfunded work). Depends entirely on #5's checkpoint path.

---

## 10. Work breakdown (ordered)

1. **API + codegen.** Add `RunSpec.Template corev1.PodTemplateSpec` with `PreserveUnknownFields` (`run_types.go:23`). Run `make generate manifests`; commit `zz_generated.deepcopy.go` + **both** CRD copies. Validate `>=1` container + non-empty image on the GPU-target container; reject template `nodeName`/`restartPolicy`/reserved env; reject `Template` edits on a `Running` Run.
2. **The overlay (`buildPod`).** Rewrite `bridge.go:281` to deep-copy `run.Spec.Template`, force-set namespace/name/nodeName/restartPolicy, merge labels + GPU annotation, **inject `nvidia.com/gpu` limit** (§5), default memory-backed `/dev/shm`. Change `apply()` create loop (`bridge.go:218`) to resolve the Run from `state.Runs` and pass the template in.
3. **GPU-resource reconciliation.** Reconcile the annotation ledger against node allocatable; handle `UnexpectedAdmissionError` as a node-fit failure.
4. **Rendezvous env + headless Service**, gated on `NNODES > 1` (§4). Compute from gang membership in the binder/overlay.
5. **Non-training path.** Relax `totalGPUs > 0` to allow 0 with a GPU-free schedule path; add shard-index env; add the batch failure policy so a `Failed` pod is terminal (§6).
6. **Tests.** Update `scenario_test.go` pod-shape assertions (name/nodeName at `:199`, GPU annotation at `:202`) for the template-derived shape; add a `buildPod` unit test asserting the GPU limit + label/annotation merge (envtest has no kubelet/device-plugin, so the real GPU path needs a unit/fake).
7. **Docs.** Rewrite `docs/migrations/kueue.md` (remove the false "generates PodTemplate" claim) and `docs/migrations/slurm.md` (add the `SLURM_*`/torch env table, rendezvous mechanism, walltime/array caveats). Update `docs/project/quota-semantics.md` with the injected-vs-provided contract.
8. **Fast-follow (not v1):** `SLURM_*` compat env; `afterany`/`afternotok`; sbatch→Run translator.

**Open decisions for the owner:**

- **A.** Rendezvous gating: pod-count heuristic (`NNODES>1`, recommended) vs. explicit `spec.workloadClass` enum?
- **B.** Reserved rendezvous env names: **reject** at admission (strict, clear errors) or **silently override** (lenient)? Recommend reject.
- **C.** Zero-GPU path in v1, or defer datagen/eval to a later cut? (Blocks opportunity #2; low code cost.)
- **D.** Ship `SLURM_*` compat env in v1, or fast-follow? (High migration value, mechanical.) Recommend fast-follow.
- **E.** GPU-target container: enforce a container named `workload`, or `Containers[0]`? Recommend named + webhook check.
- **F.** Batch failure policy shape: a `spec` field now, or infer from zero-GPU/single-pod? Recommend minimal `spec` field.

---

## 11. Keep it simple — do not over-build

v1 must credibly replace SLURM/Kueue **for the real 90%: PyTorch DDP/FSDP training + a simple eval.** That needs exactly: the `Template` field, the overlay with the **real GPU limit**, torch rendezvous env for multi-pod gangs, a memory-backed `/dev/shm`, and the zero-GPU/batch-failure path for eval. Everything below is explicitly **deferred** — resist the pull to build it now:

- **Per-role / multi-role gangs (`roles[]`).** Reserve the *architecture* (§2.2) but ship the singular `Template`. RL is a fast-follow, unblocked by an additive field.
- **GPU-ordinal / MIG / MPS / fractional pinning.** The count-based model is correct; physical-GPU identity doesn't exist and isn't needed.
- **RDMA/InfiniBand auto-config.** Stamp GPU tolerations/`runtimeClassName`; let researchers opt into `rdma/hca`/`IPC_LOCK`/`NCCL_IB_*` via their template. Auto-injecting fabric env that's correct on one node class and wrong on another is a trap.
- **Full `SLURM_*` parity.** Ship torch vars in v1; `SLURM_*` is a mechanical follow. Never inject per-task `SLURM_PROCID`/`LOCAL_RANK` (wrong for `nproc-per-node>1`).
- **Migration / checkpoint-restart / live CUDA migration.** Keep `RunRuntime.Checkpoint` inert for now; requeue-from-checkpoint is the honest first step (§9 #5), later.
- **Elastic re-rendezvous, goodput elasticity, spot integration, non-destructive suspend.** All valuable (§9), all post-v1. Rendezvous reflecting materialization-time width is an acceptable v1 limitation.
- **`--time`/`--array` primitives.** No walltime analogue; arrays as N Runs for now — flag both honestly rather than fake them.

The single unglamorous change that unlocks opportunities 1–8 is the same: a `PodTemplateSpec` seam, the injected GPU limit + rendezvous env, and peer DNS. Build that lean, ship the 90%, and every ML-distinctive capability becomes an additive follow — not a rewrite.