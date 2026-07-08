# Design vs. Implementation Audit — does jobtree fulfill the promise of `docs/index.md`?

_Date: 2026-07-08. Method: a 12-perspective code-grounded review panel plus a completeness
critic, with 44 adversarial verification passes (42 of 44 findings survived refutation; two were
downgraded and are noted inline). The most serious finding was live-reproduced; the largest
findings were independently discovered by three or four assessors reading the same code, which is
why confidence in them is high._

---

## Bottom line

The design is genuinely good and unusually honest. The implementation fulfills the **control-plane**
promise impressively, but not yet the **run-real-ML-training-reliably** promise — and the component
the recent single-committer cutover just made load-bearing (the scheduler plugin, the "sole
committer") is the least robust and least tested code in the repository.

Compressed to one sentence: jobtree is a well-architected budget-and-scheduling *brain* with a real
Kubernetes body attached last, and the seams where they join are where it will bleed. It is a strong
research prototype and a promising early product — not something to put a production training fleet
on this quarter.

### Scorecard

| Dimension | Grade | One line |
|---|---|---|
| Budgets & borrowing | **good** | The crown jewel — real, TLA-checked, wired end to end |
| Reservations & observability | **good** | Real lifecycle and events; the "forecast" is a linear heuristic |
| Code quality & architecture | **good** | Exceptional layering and test discipline — except at the new seam |
| Real GPU workloads (promise) | **mixed** | Happy path real and live-proven; failure/multi-role/distribution unhandled |
| Job forests & gangs | **mixed** | `follow` fully delivered; gang *commit bookkeeping* has correctness holes |
| K8s API conventions | **mixed** | Clean spec/status, but no Conditions, no ownerRefs, `Lease` name collision |
| K8s controller/scheduler practices | **mixed** | Idiomatic happy path; restart/retry/failure semantics under-built |
| Researcher UX | **mixed** | Day 1 delightful; day 2 (first crash) burns you silently |
| Admin UX | **mixed** | Good chart; the documented install path cannot work as written |
| Stability & robustness | **weak** | Controller is crash-tolerant; the plugin wedges/leaks under real failures |
| Security & multi-tenancy | **weak** | Forgeable funding provenance; opt-in; namespaces are not a boundary |
| ML workload fitness | **weak** | Not yet usable for real distributed training |

---

## What is genuinely good

The funding engine is the real differentiator, and it is well built. Time-scoped budget windows,
GPU-hour integrals, family sharing with owner recall, sponsor lending with ACLs and caps, and the
unfunded → spares → shrink → lottery reclaim cascade are all implemented as a single *pure derived
classification* (`funding.Evaluate`) that replays the lease ledger and is consumed identically by
admission, reclaim, and status. It is property-tested and TLA+ model-checked in CI. No mainstream
alternative — Kueue, Volcano, JobSet — ships this calculus.

The single-committer cutover is real, not cosmetic. Only two lease-mint sites exist outside tests:
the plugin's `PreBind` and one documented controller mint. A researcher's `roles[0].template` is
deep-copied verbatim (image, command, env, volumes) with only scheduling-owned fields overlaid, and
`hack/e2e/fullstack-smoke.sh` proves Run → plugin bind → plugin-minted lease → real container exit 0
→ Completed on a real kind cluster with no injected state. That is a genuine, dramatic recovery from
the pause-pod era the project's own `fake-features-audit.md` documents.

The engineering *discipline* is rarer than the code itself. The pure-engine layering is verified in
the import graph (zero Kubernetes dependencies in `pkg/funding`, `cover`, `pack`, `binder`); the
controller is drivable fully in memory; there is a golden parity oracle, an AST-based "antifake"
ratchet that fails CI on any test that hand-assigns a terminal pod phase, fuzz tests, and unusually
strong decision-traceability in the comments. The `follow` feature (job forests) is fully delivered
with exactly the semantics `index.md` promises: an AND of upstreams, a Waiting phase, a 30-minute
default grace on upstream failure, honest terminal failure, and cycle/dangling detection.

---

## The load-bearing problems

These are organized by root cause rather than by dimension, because the same few causes surface
across many dimensions.

### 1. The new "sole committer" has the worst failure semantics in the codebase

This is the highest-confidence cluster — four assessors and one live reproduction.

**Phantom-lease funding leak.** When a gang commits, its placeholder `pending` leases are folded
into every *other* gang's funding check to close the decide → mint window — but nothing ever clears
them. `forget()` is a no-op once a pod has claimed a payer, and there is no `PostBind` extension
point. So after any gang mints, every subsequent funding decision counts that run **twice**, forever,
until the scheduler process restarts. One assessor reproduced it directly: with envelope concurrency
8 and one 4-GPU gang minted, a second 4-GPU gang is falsely rejected as "insufficient capacity";
clearing the stale in-memory entry makes it fundable again. A busy scheduler monotonically
under-admits, and `m.gangs` is an unbounded memory leak besides. Evidence: `cmd/scheduler/plugin/
gang.go:117-122,136,184-191`.

**Partial-gang wedge.** After Permit allows a gang, members mint and bind independently. If one
member's `PreBind` or bind fails — a transient API error, or the fail-closed Lease webhook being
briefly unavailable — that lone pod can never re-pass Permit, because the gate requires the *full*
expected width to be simultaneously waiting and its already-bound siblings never wait again. It loops
on 2-minute timeouts indefinitely. Meanwhile the controller adopts the run as **Running on any open
lease count greater than zero, without checking width** (`run_controller.go:197-212`), so N−1
containers run and charge the budget forever — quietly violating "start together or not at all." The
identical wedge occurs on a **scheduler restart mid-gang**, because gang state is in-memory only and
is never reconstructed from existing Leases. Evidence: `cmd/scheduler/plugin/plugin.go:167-178`.

**Hot-path cost.** `Permit` performs four uncached full-cluster LISTs while holding the gang mutex,
inside the serial scheduling cycle, and each `Feasible` replays the entire lease ledger — which is
never compacted or deleted — making it effectively O(history²). Acceptable at demo scale; in tension
with the "keep large fleets busy" promise.

### 2. Workload lifecycle is unhandled — a failed pod becomes an immortal, budget-charging zombie

`RestartPolicy` is forced to `Never`, and the pod watch fires only on `Succeeded`, never on `Failed`.
So a rank-3 OOM an hour into a weekend run cascades (peers crash at the next NCCL collective), the run
stays **Running forever**, its leases never close, GPU-hours accrue until the envelope is drained, and
any `follow`-chained stage waits forever. There is no event, no status message, no retry. This is
*documented as deferred* under the JobSet-lowering track — but `index.md` markets "failure handling"
and the researcher guide promises work "will not silently hang forever," so today it is an overclaim,
not merely a gap. Evidence: `controllers/kube/bridge.go:358`, `controllers/run_controller.go:
139,448-469`, `controllers/kube/reconcilers.go:136-154`.

### 3. No distributed-training rendezvous on the live path

`buildPod` injects zero environment — no `MASTER_ADDR`, `WORLD_SIZE`, or `RANK` — and creates no
headless Service and no stable pod identity. The only rendezvous code lives in `pkg/lowering`, which
returns `ErrNotImplemented`. A `width: 8` run (the quick-start's own example) yields eight pods that
cannot discover each other. Worse, an API comment at `api/v1/run_types.go:67` *claims the rendezvous
overlay exists* — that comment is false. Combined with problem 2, this is why ML fitness graded
**weak**: the headline "real GPU workloads / multi-thousand-GPU training" holds only for single-pod or
embarrassingly-parallel jobs today.

### 4. Security holes that matter if this is multi-tenant

The plugin trusts unauthenticated pod metadata: it sets no `OwnerReference` on emitted pods and there
is no pod-level admission webhook. The sharpest hole is the swap path — a pod annotated
`lease-reason=Swap` **skips the gang and funding gate entirely**, and `PreBind` mints a Lease straight
from attacker-supplied `payer-*` annotations against *any* tenant's envelope, even an exhausted one,
needing only ordinary `create pods` RBAC. Separately, jobtree is **opt-in**: a raw `default-scheduler`
GPU pod runs with zero budget accounting, and `owner` is a free-form string with no namespace binding
(funding aggregates cluster-wide via an `EnvelopeKey` that has no namespace), so namespaces are not a
tenancy boundary. These are critical in a genuinely multi-tenant cluster and papercuts in a
single-trusted-team deployment. Evidence: `cmd/scheduler/plugin/plugin.go:144-146,213-223,236`;
`cmd/scheduler/main.go:30-32`; `controllers/kube/webhooks.go:18-22`.

### 5. The one remaining controller mint is now incoherent

Opportunistic "promised-but-unfunded" reservation activation still mints via `binder.Materialize`, but
post-cutover those pods have `nodeName` stripped and are routed to the plugin — whose gate is *designed
to refuse* an unfunded gang. The result is a run marked Running with leases charging the budget and
pods that can never bind. The design fork recorded in `cascade-plan.md` was never reconciled with the
cutover. This is a static-read conclusion (not live-reproduced) but was confirmed by verification.
Evidence: `controllers/run_controller.go:955-982`; `pkg/binder/binder.go:269-285`.

### 6. Kubernetes-convention misses that would fail an upstream API review

There is not a single `metav1.Condition` in the API — status is freeform phase strings whose canonical
values live *outside* the API package and are unenumerated in the schema. There are zero
`ownerReferences` and zero finalizers, so garbage collection is hand-rolled and manager-dependent. The
`Lease` kind **collides with `coordination.k8s.io/Lease`**, and a documented audit command targets the
wrong resource with a field selector CRDs do not support. CRD schemas carry near-zero validation (no
minimums, no enums, no CEL), so every invariant — including Lease immutability — hangs entirely on
webhook availability. And `v1` was cut while the contract is openly in flux ("Roles optional during
the JOBSET transition").

### 7. The open-project shell is missing

There is **no LICENSE anywhere** in the repository — as presented, it is not legally usable.
`MAINTAINERS.md`, linked from the front page "to understand ownership," is a fictional governance
document with a dead `.example` security contact. The documented `helm install` path **cannot work on
a fresh cluster**: the helm-repo URL 404s, no CI job builds or pushes any image, `--set image.tag` is
a silent no-op, and the default-on `notifier` component deploys an image the repo itself admits does
not exist. An admin who reverse-engineers `hack/e2e` gets a working system; one who follows the docs
gets `ImagePullBackOff`. There is also no ServiceMonitor that actually matches the metrics Service, no
leader election in the prod overlay (which runs three manager replicas), and no break-glass,
uninstall, or CRD-upgrade story.

_Two findings were downgraded during verification: "`roles[]` is plural but only one role is allowed"
became a docs papercut, since `index.md` does not actually promise multi-role; and "reservations are
not a real forecast" moved from major to minor, since the linear heuristic is a defensible floor
estimate rather than a false claim._

---

## Meta-observation

The most striking thing about jobtree is a pattern. It has invested more in *self-honesty
infrastructure* — the antifake ratchet, the golden oracle, TLA specs, an explicit
`fake-features-audit.md` — than almost any codebase of its size, and it works: where the discipline
has been applied (the controller and the pure engine), the fakes are gone and the claims hold. But
that discipline has not yet reached the plugin, and every serious *new* finding in this audit clusters
exactly at that seam. The live proofs that declared the cutover "done" only exercise one or two gangs
on fresh clusters with slack budget — which is precisely why the phantom-lease leak and the
partial-gang wedge slipped through. The gap here is not "honest versus fake"; the team has beaten
that. It is "live-proven-once" versus "robust-under-failure." The next unit of work is not more
features — it is a failure-semantics pass on the plugin plus a multi-gang / kill-a-pod /
restart-the-scheduler stress proof.

---

## Remediation list

### Model assignment

Your split is the right default; I am adopting it with one cost-motivated carve-out.

- **Fable — analysis and design.** Deciding *what* the behavior should be, reconciling design forks,
  and root-causing the hard concurrency bugs. The carve-out: for the gnarly races (items R1–R3), the
  root-cause and invariant-design step is Fable, **not** Sonnet, even though it is nominally
  "debugging" — a cheap model that mis-diagnoses a race wastes an entire Opus implementation cycle,
  which costs *more* tokens, not fewer. Mechanical debugging (charts, selectors, wiring) stays Sonnet.
- **Opus — writing the code** once the design is settled.
- **Sonnet — mechanical debugging, reproduction harnesses, and verification** that a fix actually
  holds.

Where a task has a hard design step and a mechanical code step, both models are listed in order.

### P0 — Correctness at the new committer (blocks "stable"; several are critical)

| # | Remediation | Root finding | Model(s) |
|---|---|---|---|
| R1 | Clear a gang's `pending` placeholder leases and GC the `gangCommit` once its real leases exist. Add a `PostBind` (or reconcile `pending` against the live ledger) so committed gangs stop double-counting funding, and prune `m.gangs` so it is not an unbounded leak. | Phantom-lease funding leak | **Fable** (settle the clear-invariant: on real-lease appearance vs. PostBind vs. periodic reconcile) → **Opus** (implement) → **Sonnet** (multi-gang repro + verify the double-count is gone) |
| R2 | Give the gang a rollback / re-formation / recovery path: compensating lease closure when a member fails after siblings bound; reconstruct gang state from existing Leases on scheduler startup; and make the controller check actual vs. requested width before adopting a run as Running (and re-emit or fail a wedged partial gang). | Partial-gang wedge; restart wedges gang; adopt-Running-on-any-open-lease | **Fable** (design the recovery + adoption invariant — hardest item) → **Opus** (implement) → **Sonnet** (kill-a-member and restart-mid-bind repro + verify) |
| R3 | Reconcile the opportunistic-activation fork with the cutover: either stop marking the run Running and emit properly-annotated pods the plugin can honor, give the plugin a *legitimate, authenticated* honored-promise path, or drop the feature. Do not leave leases charging for pods that can never bind. | Opportunistic activation incoherent post-cutover | **Fable** (decide the fork) → **Opus** (implement) → **Sonnet** (budget-shortfall activation e2e) |
| R4 | Move the plugin off four uncached full-cluster LISTs under the mutex; add a cache/informer-backed world read and ledger compaction so `Feasible` is not O(history²) on the hot path. | Hot-path relist cost; unbounded ledger replay | **Fable** (caching/compaction design) → **Opus** (implement) |

### P1 — Security & tenancy (blocks "multi-tenant safe"; critical if multi-tenant)

| # | Remediation | Root finding | Model(s) |
|---|---|---|---|
| R5 | Stop trusting pod annotations for funding. Before minting from carried Swap provenance, verify the pod carries a controller `OwnerReference` and that a real spare lease existed; require an authenticated trust anchor rather than non-empty annotations. | Swap-provenance forgery mints against any budget | **Fable** (trust-anchor design) → **Opus** (implement) → **Sonnet** (forged-pod exploit test proves it now rejects) |
| R6 | Make jobtree mandatory for GPU pods: ship a ValidatingAdmissionPolicy/webhook that requires `schedulerName=jobtree` for any pod requesting `nvidia.com/gpu`, so the budget cannot be skipped by opting out. | Budget is fully opt-in | **Fable** (enforcement model) → **Opus/Sonnet** (author + ship the policy) |
| R7 | Set `OwnerReference` on every emitted pod, and make tenancy a real boundary: bind `owner` to an authenticated identity/namespace and include namespace in `EnvelopeKey` so same-named budgets in different namespaces do not collide. | Unauthenticated pod metadata; owner is free-form; funding aggregates cluster-wide | **Fable** (tenancy model) → **Opus** (implement) |

### P2 — Workload lifecycle (blocks "usable for ML"; largely the deferred JOBSET track)

| # | Remediation | Root finding | Model(s) |
|---|---|---|---|
| R8 | Handle pod failure: watch `Failed`, close the run's leases, write status + emit an event, and apply a retry-or-fail policy (the `failurePolicy` the lowering skeleton already describes). Kill the immortal-zombie behavior. | Failed pod hangs run forever, charges budget | **Fable** (failure/retry policy design) → **Opus** (implement) → **Sonnet** (OOM-a-rank repro + verify lease closure) |
| R9 | Wire multi-node rendezvous on the live path: inject `MASTER_ADDR`/`WORLD_SIZE`/`RANK`, create a headless Service, and give pods stable identity — or finish the JobSet lowering that already models this. | No rendezvous env/DNS; width>1 gangs cannot form a process group | **Fable** (finish-lowering vs. direct-inject decision) → **Opus** (implement) → **Sonnet** (2-node torchrun e2e) |
| R10 | Fix the false API comment at `run_types.go:67` claiming a rendezvous overlay that does not exist (do this immediately even before R9 lands). | API comment falsely claims rendezvous overlay | **Sonnet** |

### P3 — Kubernetes conventions & API hardening

| # | Remediation | Root finding | Model(s) |
|---|---|---|---|
| R11 | Replace freeform phase strings with `status.conditions` (`metav1.Condition`, standard types/reasons) and move canonical values into the API package. | No conditions; values defined outside the API | **Opus** |
| R12 | Add `ownerReferences` and finalizers so garbage collection is API-native rather than hand-rolled. | Zero ownerRefs/finalizers | **Opus** |
| R13 | Rename the `Lease` kind to avoid the `coordination.k8s.io/Lease` collision (e.g. `GPULease`/`FundingLease`) and fix the broken audit command. | Lease name collision; broken audit command | **Fable** (naming + migration story) → **Opus** (implement) |
| R14 | Add CRD-level validation (minimums, enums, CEL) including Lease spec-immutability via CEL, so invariants do not depend solely on webhook uptime. | Near-zero CRD validation | **Opus/Sonnet** |

### P4 — Admin, release & project hygiene (blocks "installable" and "usable as OSS")

| # | Remediation | Root finding | Model(s) |
|---|---|---|---|
| R15 | Make the documented install real: build+push controller/scheduler images in `release.yaml`, publish (or fix) the helm repo index, wire `image.tag` (or correct the docs), and default `notifier` off — or delete it entirely (no source exists). | Documented install cannot work; phantom notifier | **Sonnet** |
| R16 | Fix the ServiceMonitor selector so metrics are actually scraped; make the Prometheus-Operator dependency optional so a bare `helm install` does not fail. | ServiceMonitor never matches; install fails without Prometheus Operator | **Sonnet** |
| R17 | Enable leader election in the prod overlay (three replicas today with it off = concurrent unsynchronized committers), and enable the sole-committer scheduler in both overlays. | Prod overlay unsafe; scheduler disabled in overlays | **Sonnet** |
| R18 | Write the missing operator day-2 docs: break-glass (disable plugin / fall back to default scheduler / drain), uninstall, and CRD-upgrade/migration. | No break-glass, uninstall, or upgrade story | **Fable** (docs/design) |
| R19 | Add a LICENSE; replace the fictional `MAINTAINERS.md` and dead security contact with real ones. | No license; fictional governance | **Fable/human** (choose license) → **Sonnet** (add files) |

### P5 — Observability & correctness papercuts

| # | Remediation | Root finding | Model(s) |
|---|---|---|---|
| R20 | Emit Kubernetes Events from the plugin on Permit refusals so `kubectl runs explain` can surface plugin-side causes (a gang stuck in Permit is invisible today). | Plugin-side refusals invisible to Run/explain | **Opus** |
| R21 | Fix cordon-treated-as-node-failure: a benign `kubectl cordon` currently drives a destructive swap plus duplicate workload execution. | Cordon = destructive swap | **Fable** (diagnose the trigger + correct semantics) → **Opus** (fix) |
| R22 | Fix the `ReclaimedBySpare` sweep closing innocent co-located runs' leases — reclaim at GPU-slot granularity, not whole-node. | Swap closes co-located runs' leases | **Fable** (diagnose) → **Opus** (fix) |
| R23 | Add a workload-observability story: `kubectl runs logs`/`pods`, and artifacts guidance (none exists today). | No logs/pods/artifacts | **Fable** (design) → **Opus/Sonnet** (implement) |
| R24 | Clear the remaining doc-honesty leftovers: the stale README claim that the real-workload feature does not exist, the `spares-and-fill.md` "opportunistic fill" fake, and the researcher-guide `spares` field-name error the API silently drops. | README/spares-and-fill/researcher-guide drift | **Sonnet** |

### Suggested sequencing

R1–R3 first (they close every plugin-side critical and move three "weak" grades), then R5–R8 (make it
multi-tenant-safe and stop the budget-bleed on failure), then R9 (unlock real multi-node training),
then the P3–P5 hardening and hygiene. R10, R15, R16, R19, R24 are cheap Sonnet wins that can land in
parallel at any time.
