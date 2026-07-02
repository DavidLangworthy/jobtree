# Testing & simulation plan

*Companion to the July 2026 design review (`design-review-2026-07.md`).*

This document recommends a ladder of test environments for jobtree, starting with something that
runs in milliseconds on a single machine and stepping up to live systems testing. It takes one
hard constraint as given: **no live GPUs will be available at any tier.**

## Why no-GPU testing works here

Nothing in jobtree touches a GPU. The scheduler consumes *labels and integers*: nodes advertise a
`gpu.flavor` label and a GPU count; the engine plans against that capacity and emits pods and
leases. The device itself — NVML, driver, fabric — never appears in the code. That means synthetic
GPU capacity has full fidelity at every tier of this plan, and the only things that can never be
tested without hardware are the things jobtree deliberately does not do (see "The fidelity
boundary" below). This is a strength of the current design and worth preserving: keep the engine
ignorant of devices, and the entire test ladder stays GPU-free.

## The invariant library: one set of checks, every tier

Before adding environments, extract the properties that define correctness into a reusable
checker package (e.g. `pkg/invariant`) that takes a `ClusterState` (later: an API snapshot) and
returns violations. Every tier below runs the same checks, so a bug found in production-like
testing can be replayed down the ladder.

Core invariants, each of which maps to a real finding from the design review:

1. **Placement validity** — the sum of pod GPUs on a node never exceeds the node's capacity, and
   every pod's node actually hosts the GPUs its lease claims. *(Would have caught the confirmed
   binder segment-splitting bug mechanically.)*
2. **Conservation** — lease GPUs per run equal the pack plan total; cover segments sum to the
   requested quantity; nothing is double-counted across budgets.
3. **Budget safety** — active concurrency per envelope ≤ `spec.concurrency`; aggregate caps hold;
   borrowed usage never exceeds lending sub-caps.
4. **Name uniqueness** — pod and lease names are unique per namespace. *(Would have caught the
   lease-collision bug.)*
5. **Determinism** — identical scenario + identical seed ⇒ byte-identical final state.
6. **Lease immutability** — closed leases never reopen; specs never mutate.
7. **Malleable floor** — a run's active width never drops below `minTotalGPUs` except on paths
   that explicitly mark it `Failed`.
8. **No orphans** — every pod maps to an open lease; every `PendingReservation` pointer resolves
   to a live Reservation; no Pending reservation exists for a Running run. *(Would have caught the
   stale-reservation/double-bind gap.)*

## The ladder

### Tier 0 — unit, property, and fuzz tests (milliseconds, every PR)

*What exists:* good table-driven unit tests in every core package.

*What to add:*

- **Property-based tests** over the engine's pure functions using Go's built-in fuzzing
  (`testing.F`), which keeps the zero-dependency stance (a test-only dependency such as
  `pgregory.net/rapid` is also acceptable and much more ergonomic — recommended). Generate random
  topologies, budgets, and requests; assert the invariant library instead of exact outputs.
  Highest-value targets, in order: `binder.Materialize` (invariants 1, 2, 4), `cover.Plan`
  (2, 3), `pack.Planner` (1, 2), `resolver.Resolve` (5, 7).
- **Round-trip fuzzing** of the CLI state snapshot (load → save → load ⇒ identical).
- Raise `api/v1` validation coverage (currently 15%) with a table of accept/reject manifests —
  this becomes the CRD schema conformance corpus in Tier 2.

*Exit criterion:* the two confirmed binder bugs, re-introduced deliberately, are caught by the
property suite within seconds of fuzzing.

### Tier 1 — deterministic scenario simulator (seconds, every PR)

This is the tier the project is closest to and the one with the highest payoff. Today the
in-memory `ClusterState` plus injected `Clock` is already a simulator; what is missing is a
driver: reconciliation only happens when a CLI command pokes it, and time only advances by wall
clock. Build a small **discrete-event scenario runner** (`cmd/jobtree-sim` or a `sim` subcommand):

- **Scenario files** (YAML/JSON, checked into `test/scenarios/`) describing an initial cluster and
  a timeline of events: `submit run`, `advance clock 2h`, `fail node`, `tighten budget`,
  `activate reservations`, `expect phase=Running`, `expect invariants`. The existing worked
  examples become executable scenarios.
- **Virtual clock.** The runner owns time; nothing sleeps. A multi-day soak (budget windows
  opening, GPU-hours accruing, auto-renew) executes in milliseconds. This is the only way to test
  the GPU-hour/integral-budget semantics at all, and it removes the current blind spot where
  `ActivateReservations` and `HandleNodeFailure` have no non-test caller.
- **Full-loop reconciliation.** Each tick reconciles all runs *in deterministic order* and
  activates due reservations — i.e., the simulator drives the control loop the way a real manager
  would, rather than the way the CLI happens to.
- **Golden outputs + seed reporting.** Each scenario records a digest of final state; lottery
  seeds are fixed by scenario so preemption outcomes are reproducible. Failures print the seed and
  the event index for replay.
- **Monte-Carlo mode.** A workload generator (random arrival of runs/failures/shrinks under a
  seed) runs N randomized episodes per CI job, asserting invariants after every event. This is
  FoundationDB-style deterministic simulation scaled down; it is cheap because the engine is pure.
- **Exploration mode (model checking the implementation).** Because the engine is pure,
  single-threaded, and clock-injected, the simulator can do better than sampling: it can
  *enumerate*. Define tiny universes — 2 owners, 2–3 nodes, 3–5 events (submit, grow, node-fail,
  budget-window-open, activate) — and exhaustively explore every event ordering up to a depth
  bound, asserting the invariant library at each state. Almost every scheduler bug of this kind
  has a minimal witness at this scale, and exhaustive-at-small-scale beats random-at-large-scale
  for finding it; the counterexample that comes back is already minimal.
- **Fault-point enumeration.** Take every passing trace and systematically re-run it with a
  failure injected at each step (node death, manager crash/restart between events, partial state
  write). This is lineage-driven fault injection: it converts Tier 4's probabilistic chaos into
  deterministic, replayable coverage at millisecond cost.
- **Boundary-biased generators.** Bias inputs toward fringe values: zero-headroom envelopes,
  exact-fit capacities, windows expiring at `now`, group counts crossing 9→10, borrow limits of
  exactly the request size. Several design-review findings are precisely fringe-value bugs (the
  string-sorted group index breaks at 10; the binder bug needs segment sizes that misalign with
  node chunks) — random generation finds these eventually, biased enumeration finds them
  immediately.

*Exit criterion:* every user-guide walkthrough exists as a scenario; nightly Monte-Carlo runs
10k+ episodes and the exhaustive small-universe exploration (including fault-point enumeration)
completes, both with zero invariant violations.

### Tier 2 — envtest: a real API server, no kubelet (seconds–minutes, every PR once the port lands)

This tier only makes sense after (and motivates) the controller-runtime port recommended in the
design review. `sigs.k8s.io/controller-runtime`'s **envtest** runs a real `kube-apiserver` +
`etcd` on the test machine — no containers, no kubelet, still single-machine and CI-friendly.

- Register the real CRDs; run the real reconcilers against a real API.
- Nodes are just `Node` objects with topology labels and capacity set in status — no kubelet
  needed, because jobtree only reads labels/capacity and creates pods that never have to run.
- What this uniquely catches that Tiers 0–1 cannot: CRD schema/OpenAPI validation drift, webhook
  wiring, **name uniqueness enforced by a real API server** (the lease-collision bug becomes a
  hard create failure), status-subresource semantics, optimistic-concurrency conflicts and retry
  behavior, watch/informer event ordering.
- Reuse Tier 1 scenario files: the same runner drives events through the API instead of directly
  mutating `ClusterState`.

*Exit criterion:* the Tier 1 scenario corpus passes unmodified against envtest.

### Tier 3 — kind + KWOK: real cluster mechanics on one machine (minutes, nightly)

Two complementary single-machine environments:

- **kind** (Kubernetes-in-Docker) for *behavioral* fidelity on small clusters (3–10 nodes).
  Fake GPUs come from a device plugin advertising phantom `nvidia.com/gpu` (or
  `jobtree.io/fake-gpu`) resources — either a ~150-line in-repo fake device plugin or the
  off-the-shelf **fake-gpu-operator** (Run:ai), which also fakes per-GPU metrics for dashboard
  testing. Node labels model regions/clusters/fabric domains. Workload pods run `pause`
  containers. This is where eviction actually evicts, preemption races the scheduler, and the
  Helm chart, RBAC (currently wildcard — see the review), metrics endpoint, Prometheus scrape,
  and Grafana dashboards get exercised for real.
- **KWOK** (Kubernetes WithOut Kubelet, a kubernetes-sigs project) for *scale* fidelity: thousands
  of fake nodes with GPU capacity on a laptop, pods "run" without containers. This is the packing
  and topology stress rig — 5,000 nodes across 40 fabric domains, 100k leases, measured
  reconcile latency (the admission-latency histogram already exists in `pkg/metrics`).
- **M10 rehearsal:** two kind clusters on one machine cover the multi-cluster aggregate-caps
  stretch milestone without any new infrastructure.
- **Backend-agnostic harness.** Drive the e2e runner through a kubeconfig and nothing else, so
  any conformant cluster is a valid target. kind is the CI default (multi-node clusters are
  trivial, which jobtree needs for fabric-domain packing, and startup is CI-fast); minikube works
  unchanged for developers who already run it — its VM drivers even offer slightly higher node
  fidelity (real kernel and systemd per node), though nothing jobtree does depends on that — and
  k3d/k3s likewise. The same harness later points at the Tier 4 cloud clusters, so nothing is
  written twice.

*Exit criterion:* a nightly CI job stands up kind, installs the chart, replays a smoke subset of
the scenario corpus end-to-end (submit → bind → fail node → spare swap → preempt), and tears
down; a weekly KWOK job publishes scheduling-latency numbers at 1k/5k nodes.

### Tier 4 — live systems testing on CPU-only clusters (hours–days, pre-release)

Real multi-node clusters — **no GPUs** — running the operator exactly as production would, with
the fake device plugin advertising phantom GPU resources. The device plugin gRPC contract is
identical whether the resources are real or fake, so everything above the device driver is
production-faithful.

A managed cloud service such as **AKS** is the recommended concrete rig here (EKS/GKE are
equivalent), because it tests things no local or self-managed environment can:

- **Managed control planes behave differently.** API-server throttling and rate limits, admission
  latency, managed etcd — controller retry/backoff bugs that never appear against kind's idle API
  server show up here, and this is exactly the behavior class the controller-runtime port
  introduces.
- **Real topology labels for free.** Availability zones and multiple node pools map naturally
  onto the region/cluster/fabric-domain scheme, so placement is exercised against labels the
  platform maintains rather than ones a test fixture invents.
- **Real node churn.** Spot/low-priority CPU VMs are cheap and get genuinely preempted — free
  chaos for the spare-swap and resolver paths — and cluster-autoscaler interplay is only testable
  here.
- **Realistic upgrade drills.** Managed version upgrades rehearse Kubernetes version skew and CRD
  migration the way an adopter would experience them.

Clusters are ephemeral — Terraform up, run the gate, tear down — to keep cost bounded. Two AKS
clusters (or AKS + kind) also serve the M10 multi-cluster rehearsal at higher fidelity than
Tier 3.

One caveat cuts the other way: with a managed control plane you *cannot* kill the API server or
partition etcd deliberately. The chaos suite therefore splits: **control-plane chaos** (API-server
restarts, etcd disruption) runs on a self-managed cluster — kind is sufficient — while **node and
workload chaos** and soak run on AKS.

- **Chaos:** kill nodes (real kubelet death, not simulated), partition the network, kill the
  manager mid-reconcile; on the self-managed rig, restart the API server. Verify: spare swaps
  fire, leases close exactly once, no double-binds after leader failover, invariants hold on the
  live state (the invariant library runs as a read-only audit job against the cluster).
- **Soak:** multi-day runs with accelerated budget windows; watch for lease/GPU-hour drift,
  memory growth, metrics cardinality.
- **HA and lifecycle:** leader election, upgrade and rollback drills using the Helm chart, CRD
  schema migration rehearsal.
- **Observability acceptance:** Prometheus rules fire on induced conditions; dashboards render
  from real scrapes; the reservation-backlog and resolver-action metrics tell a coherent story
  during an induced capacity crunch.

*Exit criterion:* a release gate checklist — chaos suite green, 72-hour soak clean, upgrade drill
clean — attached to every tagged release.

## Design-level model checking (a gate for the Kubernetes port)

Tier 1's exploration mode model-checks the *implementation*, but it explores inputs to a
sequential engine. What it cannot cover is what the Kubernetes port introduces: concurrent
reconcilers, stale informer caches, and conflict-and-retry semantics. That is where a small
design-level specification pays off — written and checked *before* the port, when changing the
design is cheap.

Scope it to exactly two protocols; everything else is adequately covered by the ladder:

1. **Reservation lifecycle** — plan / direct-bind / activate racing each other. The double-bind
   defect found in the design review (a Pending reservation surviving a direct bind and
   re-materializing leases on activation) is a state-reachability result a model checker finds
   instantly.
2. **Budget conservation under concurrent admission** — can two racing reconciles overspend an
   envelope given optimistic concurrency and stale reads? This validates the retry/ownership
   design the port is about to commit to.

TLA+ with TLC is the default tool; Microsoft's P framework is the alternative if executable
state-machine specs feel more maintainable. Either way keep the spec deliberately tiny (tens of
lines of state, not a codebase shadow) so it cannot drift far from reality, and treat "specs
check clean" as an entry gate for Phase 3 below.

## The fidelity boundary (what no tier can test without GPUs)

Be explicit about what is out of scope so nobody mistakes green CI for hardware validation:

- Real device allocation (NVML, MIG partitioning, driver/device-plugin quirks on GPU nodes).
- Actual fabric performance and NCCL behavior — jobtree places by fabric-domain *label*; whether
  the label tells the truth is an operator problem.
- GPU health signals (ECC errors, thermal throttling) as failure triggers; the plan tests the
  *response* to node failure, not GPU-level detection.

Mitigations: keep device interaction behind the existing label/capacity seam; rely on the
standard device-plugin API contract (identical for fake and real plugins); ship a short
first-deployment conformance checklist for the first adopter with real GPUs (verify labels,
capacities, and one end-to-end run per flavor).

## CI mapping

| Tier | Environment | Trigger | Budget |
| ---- | ----------- | ------- | ------ |
| 0 | `go test` + fuzz corpus | every PR | < 2 min |
| 1 | scenario simulator + Monte-Carlo (small N) | every PR | < 2 min |
| 1 | Monte-Carlo (large N) + exhaustive exploration + fault-point enumeration | nightly | ~1 h |
| 2 | envtest | every PR (after the controller-runtime port) | < 5 min |
| 3 | kind e2e smoke | nightly | ~20 min |
| 3 | KWOK scale + latency report | weekly | ~1 h |
| — | TLA+/P spec check (reservation lifecycle, budget conservation) | on spec/design change; gate for the port | minutes |
| 4 | AKS chaos + soak (node/workload); self-managed control-plane chaos | pre-release / release branch | days |

Prerequisite housekeeping: bump CI from Go 1.22 (EOL) and add `helm lint` to the PR workflow.

## Phased implementation plan

**Phase 1 — invariants + properties (small).** Create `pkg/invariant`; add property/fuzz tests
for binder, cover, pack, resolver. No new infrastructure. *This phase alone would have caught the
worst bug in the design review.*

**Phase 2 — scenario simulator (medium).** Scenario schema + runner + virtual clock + golden
outputs; convert worked examples and user guides into scenarios; wire `ActivateReservations` and
`HandleNodeFailure` into the simulated loop (fixing the "no driver" gap in the process); add
Monte-Carlo mode, exhaustive small-universe exploration, fault-point enumeration, and
boundary-biased generators to nightly CI.

**Phase 3 — the Kubernetes port and envtest (large, but it is the M-next work anyway).** Entry
gate: the TLA+/P specs for the reservation lifecycle and budget conservation check clean. Adopt
controller-runtime; generate real CRDs; wire webhooks; port reconcilers. envtest lands as the
port's own test bed, reusing the Phase 2 corpus.

**Phase 4 — kind/KWOK harness (medium).** Fake GPU device plugin (in-repo or fake-gpu-operator),
kind config with labeled nodes, kubeconfig-driven e2e driver reusing the scenario corpus (kind by
default; minikube/k3d work unchanged), KWOK scale rig, nightly and weekly CI jobs.

**Phase 5 — live systems rig (medium, mostly ops).** Terraform for disposable AKS clusters
(spot CPU node pools, zone-derived topology labels), node/workload chaos suite plus self-managed
control-plane chaos, soak jobs, invariant audit against live state, release gate checklist.

Phases 1–2 need no architectural decisions and pay for themselves immediately; start there.
