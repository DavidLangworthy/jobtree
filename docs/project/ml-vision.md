<!-- Internal vision doc (docs/project/ is excluded from the built site). The "why jobtree for ML"
ranked opportunities from the 2026-07-04 research/ideation pass. Companion to plan-workload-podspec.md. -->

# Why jobtree for ML — Ranked Opportunities

A note on stance: jobtree's genuine, already-built moat is the **RQΛ funding calculus** — derived (not stored) funding classes, owner-recall, demote-not-kill, unfunded-first reclaim, immutable leases. SLURM has fairshare-as-black-box; Kueue has quota-admission with no lifecycle; the frontier schedulers (Pollux, Gavel, Sia, Determined) have brilliant ML mechanics but no auditable economic model underneath. **The thesis is not "jobtree does goodput too" — it's "jobtree is the only system where the ML-smart decisions and the money are the same object."** Every idea below is ranked by how much it exploits that unique fusion versus just re-implementing something the frontier already has.

---

## 1. Migration-as-reclaim: make "quota is a claim, not a wall" physically cheap

**(a)** Generalize the existing `HandleNodeFailure` swap (close-old-lease + mint-new-lease) into a first-class *migration* primitive that every reclaim path — unfunded, shrink, lottery — invokes, so preemption relocates a checkpoint instead of destroying progress.

**(b)** This is the keystone. jobtree's entire pitch is demote-not-kill and "your over-quota work runs and gets gracefully cut" — but today a cut is a *kill*, and a killed 40-GPU run that loses an hour of progress makes over-quota execution economically irrational. Make reclaim cost ≈ one checkpoint flush and suddenly opportunistic/family/borrowed capacity is genuinely free money. **Operators** feel it as utilization that doesn't come with angry researchers; **researchers** feel it as "running unfunded actually finishes."

**(c)** Builds directly on immutable leases + the swap machinery already in `run_controller.HandleNodeFailure`, plus the accepted-but-inert `RunRuntime.Checkpoint`.

**(d)** Near-term for the *lease-ledger* half (close+mint with a reason is mechanical). The *transparent* half (CRIU/CUDA live migration, Singularity-style device proxy) is research and fragile — treat it as best-effort with kill+checkpoint-from-last-boundary as the honest fallback.

**(e)** True live migration of CUDA state is version-sensitive and mid-collective migration corrupts in-flight all-reduces; you must quiesce at a minibatch boundary. Ship the *requeue-from-checkpoint* version first (mount storage, preStop drain, honor `terminationGracePeriod`, resume) and be honest that "live" is aspirational. Also: the ledger fold must stay a close+mint pair — never mutate a lease's slice — or audit-by-replay breaks.

---

## 2. Goodput-driven elasticity that is *also* budget-aware — the fusion no one else has

**(a)** Drive `spec.malleable.desiredTotalGPUs` and the cover planner's flavor choice from a per-run **goodput** model (throughput × statistical efficiency), but gate every grow/shrink through the funding calculus so a run only expands into GPU-hours it can actually pay for at positive marginal goodput.

**(b)** Pollux/Sia prove goodput-aware sizing beats mechanical scaling — but they optimize goodput in a fairness/quota vacuum. jobtree can answer the question researchers *actually* ask: "given my budget, what width finishes soonest?" and the operator question "shrink the run whose goodput curve is flattest, not the one with the lowest priority number." That coupling — marginal goodput per *funded* GPU-hour — is the unique object. **Researchers** get faster JCT without over-provisioning their envelope; **operators** get contention resolved by real training progress, not guesswork.

**(c)** Malleable width + partial-funding machinery already exist; the cover planner already selects flavor by label. Only the objective function (goodput signal) is missing.

**(d)** Near-term for throughput-only goodput (jobs report samples/sec; grow while marginal throughput/GPU-hr > 0). Statistical-efficiency (gradient-noise-scale) co-adaptation with batch-size injection is research and needs job cooperation.

**(e)** Jobs that don't report gradient noise scale can only be throughput-optimized, and naive width growth then *hurts* convergence (large-batch generalization loss) while looking like higher utilization. Signal is noisy → you must damp with hysteresis and honor `stepGPUs/min/max` or the controller thrashes leases (and thrashing leases means thrashing the ledger). The goodput number must stay **derived** per reconcile — storing it on a CRD reintroduces the staleness the whole design avoids.

---

## 3. Heterogeneous RL/RLHF gangs funded and reclaimed as ONE unit

**(a)** Let a Run own a *set* of typed role-gangs (vLLM/SGLang rollout actors + FSDP/Megatron learners + reward/reference models) that are admitted atomically across roles, share one budget, and get a cross-role weight-sync rendezvous — the thing every single-shape scheduler structurally cannot express.

**(b)** RL/RLHF/agentic-RL is *the* 2025–26 workload and it is irreducibly multi-shape with a learner→rollout weight broadcast. Kueue-wrapping JobSet/LWS gives you the pods but not one funded, co-reclaimed economic unit; SLURM gives you neither. The killer property is **funding coherence**: when contention hits, jobtree can shrink rollout actors (fungible, elastic) while protecting learners (the leader), and charge the whole thing to one envelope with per-role GPU-hour attribution. No other system reclaims a heterogeneous gang as one budgeted object. **Researchers** running verl/OpenRLHF/NeMo-Aligner feel it hardest; **operators** get RL jobs that don't half-admit and burn GPUs making zero progress.

**(c)** Builds on gang admission + the vestigial `LeaseSpec.compPath` (repurpose it as the funding role tag), plus the binder's group loop (extend from groups to roles).

**(d)** Research-to-medium. The *schema* decision is near-term and urgent: shape the Run as "a set of role-gangs, v1 validates cardinality==1" now, so RL is a fast-follow, not a breaking CRD change. Actually landing multi-role rendezvous + weight-sync is a real project.

**(e)** The atomicity boundary must move from placement-group to *Run-across-roles* or you get half-live RL jobs. Cross-role weight-sync needs a rendezvous group spanning roles — a per-gang model doesn't provide it. Strongly consider materializing onto JobSet+LeaderWorkerSet and owning only quota/funding/topology on top, rather than reinventing per-framework rendezvous (maintenance load) — but that cedes some of the "jobtree generates the pods" thesis. Agentic rollouts also inject variable-duration, network-bound work into a "GPU gang," making the GPU-hours look-ahead integral noisy.

---

## 4. Spot + productive-spares + checkpoint-restart as ONE fault-tolerance story

**(a)** Treat a cloud spot-termination notice as identical to a node failure: trigger the same swap into a productive hot-spare (which was running opportunistic work until that instant), and if no in-domain spare exists, checkpoint-requeue — one mechanism, three triggers.

**(b)** Everyone else bolts these on separately: SkyPilot does spot, Determined does checkpointing, k8s does nothing coherent for spares. jobtree already has *productive spares* (hot standby that runs opportunistic work until needed) — that's a genuinely novel primitive that turns the cost of fault tolerance negative (your insurance is doing useful unfunded work). Unifying spot-preemption into that same swap path means a spot fleet becomes as safe as reserved capacity. **Operators** buy spot without operational fear; **researchers** stop babysitting.

**(c)** Productive spares + failure-swap + leases; needs the checkpoint mount from #1.

**(d)** Near-term for in-cluster spare-swap on spot notices. Cross-region failover (SkyPilot FAILOVER/EAGER_NEXT_REGION) is a larger effort tied to the multi-cluster AggregateCaps roadmap.

**(e)** Depends entirely on #1's checkpoint path existing; without it a spot kill with no spare is still terminal Failed. Spot notices give ~30–120s — your `terminationGracePeriod` floor must actually fit a large-model checkpoint flush, and jobtree must *wait it out* before reclaiming the lease. Spare productivity assumes the opportunistic work migrates cleanly, which loops back to migration fragility.

---

## 5. Budget-driven fair-share a researcher can actually explain

**(a)** Replace SLURM's opaque multi-factor fairshare priority with a *readable* ranking — (proximity-to-owner, admission-time, name), owner-recall absolute, ties broken by a **published-seed lottery** — so any scheduling decision is reproducible by replay and explainable in one sentence.

**(b)** SLURM fairshare is a black box of decaying half-lives and weight knobs nobody on the research side understands; when your job doesn't run, no one can tell you why. jobtree's ranking is auditable-by-replay: "your run is unfunded and 3rd in line; here's the seed." That's a *support-ticket eliminator* and a trust primitive. **Researchers** stop filing "why is my job stuck" tickets; **operators** stop being fairshare oracles. This is arguably jobtree's most *shippable-today* differentiator because it already exists.

**(c)** The `pkg/funding` `rankLess` + seeded lottery in `pkg/resolver` — already built and TLA-specced.

**(d)** Near-term / done. The opportunity is *packaging and surfacing* it (expose ρ finish-time-fairness and attained-GPU-time as derived `Run.status` explainers).

**(e)** Honest gap: this is a *priority order*, not proportional/DRF fairness — a busy owner is not throttled toward an equal share of contested capacity the way SLURM fairshare or Kueue weighted cohorts intend. Teams that specifically tuned `PriorityWeightFairshare` will find *no equivalent knob* (by design — no user-set priority). You can add attained-GPU-time (Tiresias LAS) or long-term-share (Shockwave) as **derived** anti-starvation tiebreaks, but keep them pure functions of immutable facts or you lose replay. Be upfront: this is "legible ordering," not "proportional fairness."

---

## 6. Opportunistic/unfunded execution as genuinely free ML throughput

**(a)** Let over-quota and idle-capacity work run UNFUNDED with no lending gate, first to be cut, so a cluster's stranded capacity becomes datagen/eval/sweep throughput at zero budget cost.

**(b)** Kueue borrowing requires explicit lending limits and cohort config; SLURM has no clean "run on scraps, get cut first" mode. jobtree's unfunded tier + no-lending-gate family sharing means a researcher can *always* make progress on idle GPUs without asking anyone, and the operator knows it's costless because it's cut first under pressure. For embarrassingly-parallel ML (datagen, eval, HP sweeps) this is a perfect fit — those workloads tolerate preemption. **Researchers** get "free" capacity for exploration; **operators** get near-100% utilization with a clean reclaim story.

**(c)** UNFUNDED/opportunistic execution + unfunded-first reclaim in `pkg/funding/evaluate.go`.

**(d)** Near-term for the *funding* semantics (exist today). The blocker is that unfunded work must be *cheap to cut*, which loops back to #1 (checkpoint-requeue), and to a real zero-GPU / free-spread scheduling path so CPU datagen and sharded eval don't get shoehorned into the GPU gang allocator or pinned into scarce NVLink islands they never use.

**(e)** Today `totalGPUs > 0` is required (`run_types.go:197`) so a CPU-only datagen shard can't even be *expressed* — the whole "free ML throughput" story is blocked on a zero-GPU path and a fire-and-forget K-of-N completion mode. And unfunded work that can't checkpoint just loses progress when cut, which for long jobs is worse than never starting. It shines for elastic/restartable workloads; it's a trap for non-elastic ones.

---

## 7. Topology-aware collective placement with in-domain gang guarantees

**(a)** Promote the flat `fabric.domain` into a real NVLink-domain→rack→block tree with a *minCount-in-one-domain* guarantee, and inject the matching NCCL/RDMA env so a tensor-parallel group is provably co-located on one NVSwitch island.

**(b)** GB200 NVL72-class training lives or dies on collective bandwidth; Kueue TAS and Volcano HyperNode do hierarchical topology, and jobtree's Cover→Pack→Bind is *deterministic and pack-to-empty* which is already stronger — but it's expressed as one flat domain + rack tiebreak and, critically, it never actually **grants the fabric** (no `rdma/hca`, no `IPC_LOCK`, no `NCCL_IB_HCA`). **Researchers** get advertised bandwidth instead of silent 10–50× TCP fallback; **operators** get their expensive fast fabric actually used.

**(c)** Cover→Pack→Bind + `pkg/topology` labels + binder env injection.

**(d)** Near-term for the *env/RDMA plumbing* (inject `NCCL_IB_*`, request `rdma/hca`, add `IPC_LOCK`, mount `/dev/shm` memory-backed). Medium for the hierarchical tree + intra-node NVLink-peer/rail-aware rank assignment.

**(e)** Two subtle correctness traps. First, jobtree packs at *GPU-slot granularity and shares nodes across runs* — but a tensor-parallel group must own a whole 8-GPU NVSwitch node, so slot-sharing collides with NVLink locality. Second, the deepest gotcha in the whole corpus: **the missing `resources.limits["nvidia.com/gpu"]`** — jobtree tracks GPUs only in an annotation and pins `nodeName`, so today pods get *zero real GPUs* and two runs can silently land on the same physical devices. Topology placement is worthless until real device-plugin requests exist, and adding them makes kubelet admission enforcement live for the first time (stale accounting → `UnexpectedAdmissionError` with no scheduler to retry).

---

## Non-obvious ideas

## 8. Funding-provenance env: workloads that checkpoint *because* they're about to be cut

**(a)** Inject `JOBTREE_FUNDING_CLASS` / `PAID_BY_ENVELOPE` / `RECLAIM_RISK` into every pod so training code can *self-defend* — an unfunded run checkpoints aggressively and an owner-funded run doesn't waste I/O.

**(b)** No scheduler tells the workload *why* it's running or how exposed it is. Because jobtree's funding class is derived every reconcile, it can expose "you are currently unfunded, reclaim-eligible" as a live signal, letting the *application* trade checkpoint frequency against reclaim risk — turning demote-not-kill from a scheduler-only contract into a scheduler↔workload contract. **Researchers** writing robust training loops feel it; it makes #1 and #6 dramatically cheaper because the app pre-flushes.

**(c)** Immutable leases + derived funding class + binder env injection.

**(d)** Near-term — it's env-var injection of facts jobtree already computes.

**(e)** Requires re-injection/signal on every reclassification (a var can't update in a running pod — needs a downward-API file or a sidecar that watches the lease). And it's advisory: workloads that ignore it gain nothing. Value is proportional to how much training code opts in.

## 9. `follow`-forests as the native pipeline/experiment object — no Airflow, one budget

**(a)** Make datagen→train→eval pipelines (and HP sweeps as fan-out) first-class via `follow.after`, where each stage is a Run with its own shape/GPU-count/funding but the *whole forest* draws on one family budget and hands capacity down as each stage completes and closes its leases.

**(b)** The frontier makes you bolt an external orchestrator (Airflow/Argo) on top of the scheduler, so the pipeline and the quota live in different systems and fight. jobtree's `follow` already composes stages with afterok AND-semantics *and* a fix-and-resubmit grace that's strictly nicer than SLURM's silent `DependencyNeverSatisfied` cancel — and because completion closes upstream leases before the follower admits, a GPU train stage hands its fleet to eval *within the same budget envelope*. That unification — dependency graph + funding in one object — is unique. **Researchers** express a whole experiment as one artifact; **operators** see the whole DAG's GPU-hour cost in one ledger.

**(c)** `RunFollow` + leases-close-on-completion + family budgets.

**(d)** Near-term for the existing afterok path. Medium to add `afterany`/`afternotok` (cleanup/recovery stages) and a real `--array` primitive with `SLURM_ARRAY_TASK_ID` for sweeps.

**(e)** Today `follow` only advances on `Completed` (afterok only) — no "run regardless" or "run only on failure," which is exactly the pattern for cleanup jobs. And a single Failed pod currently leaves a run Running forever (neither completes nor fails), which for a datagen shard wedges the whole forest — needs a per-workload-class failure policy first.

## 10. sbatch-to-Run translator that markets its own honest gaps

**(a)** Ship `kubectl runs submit --from-sbatch job.sh` that mechanically maps `--gres`→resources, `--nodes`→width, `--dependency=afterok`→`follow.after`, `--qos`→family/caps — and *loudly flags* the three things that don't translate (`--time` walltime, `--array`, fairshare priority) plus rewrites `module load`/`srun`/`/lustre` lines.

**(b)** The #1 migration blocker off SLURM isn't scheduling semantics (jobtree already wins those) — it's that the workload-materialization layer (env injection, srun→torchrun, shared storage) is unbuilt, and researchers have thousands of sbatch scripts. A translator that gets 80% mechanical and is *honest* about the 20% is a faster on-ramp than any competitor, because the honesty itself builds trust. **Researchers** migrate in an afternoon; **operators** get a defensible migration story.

**(c)** New RunSpec workload template + the SLURM_* compat env block the binder can compute from gang membership it already knows.

**(d)** Medium — depends on the (currently missing) RunSpec pod template landing first. The env mapping itself is mechanical.

**(e)** You cannot inject per-*task* `SLURM_PROCID`/`LOCAL_RANK` from the controller (one pod == one SLURM node; ranks are spawned in-pod) — pretending to would be wrong for `nproc-per-node>1`. And `--time` has genuinely no analogue (jobtree leases are open-ended), so safety-cap users get different behavior. Sell the translator only alongside the shared-storage and rendezvous story, or migrated scripts hang on the first `srun`.

---

## The catch that underlies everything (say it out loud)

Half of these ideas presume a capability jobtree **does not have today**: a real workload. `RunSpec` carries no image/command/CPU/mem; `buildPod` emits a `pause` container with **no `nvidia.com/gpu` request** (GPUs are a bare annotation + `nodeName` pin). So jobtree currently cannot run *any* real training job, GPU isolation is unenforced, and there's no rendezvous env, `/dev/shm`, RDMA, or headless Service. **The scheduling brain is excellent and the body is a mannequin.** The single highest-leverage prerequisite for opportunities 1–4, 7, and 10 is the same: add a per-role `PodTemplateSpec` seam, inject the real GPU limit + rendezvous env at bind time, and provision peer DNS. Everything ML-distinctive is gated on that one unglamorous plumbing change.

---

## jobtree's ML thesis in 3 sentences

**jobtree is the only ML scheduler where the smart decision and the budget are the same object: funding class is derived, not stored, so recall is just re-ranking, preemption is just arithmetic, and every scheduling choice is reproducible by replay — a fairness model researchers can actually read and operators can actually audit.** On top of that ledger it uniquely fuses goodput-aware elasticity, heterogeneous RL gangs, and spot/spares fault-tolerance into *one funded, co-reclaimed unit*, turning "quota is a claim, not a wall" from a slogan into cheap, non-destructive migration that makes over-quota and idle capacity genuinely free ML throughput. The honest catch is that jobtree today schedules a mannequin — it must first grow a real workload body (pod templates, real GPU requests, rendezvous) — but its economic spine is something SLURM, Kueue, and the entire frontier conspicuously lack, and that spine is far harder to build than the plumbing that remains.