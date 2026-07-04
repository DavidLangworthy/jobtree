# PLUGIN-2 cutover plan — controller commit → scheduler-plugin commit

The rip-and-replace that makes jobtree's scheduling **real**: today `run_controller`
pins `spec.nodeName` and mints the `rq.davidlangworthy.io/leases` CRD itself, in the
same synchronous call that decides admission — the scheduler is bypassed entirely and
workload pods are `pause:3.10` mannequins. This plan cuts over to the corrected
single-committer design (`borrow-vs-build.md` §9): the **scheduler plugin is the sole
committer**; the controller only *requests* width (unscheduled real pods), forecasts,
and runs lifecycle.

Transition rule (David, 2026-07-04): **new is new, old goes away — no dual path.** The
old path stays runnable at the frozen sibling worktree `/workspaces/jobtree-legacy`
(`legacy/nodename-binder`); the parity oracle is `controllers/golden_test.go`.

---

## 1. Decisions (settled from existing docs — flag to veto)

**D1 — Placement lives in the plugin; the controller emits unscheduled pods.**
The plugin, on first encounter of a gang, lists the gang's Active sibling pods from its
informer, resolves the owning Run, builds a `topology.Snapshot` from nodes, and runs
`pack.Planner` **once** over the whole gang; it caches the group→node assignment and
`Filter` then admits each member only on its planned node(s). `Score` is trivial (single
feasible node). This keeps jobtree's whole-run, topology-aware, pack-to-empty +
spares placement (the moat) intact instead of re-deriving it as per-node logic, while
satisfying the PLUGIN exit criterion: *no `NodeName` assignment outside the plugin pkg*
(`make-it-real-plan.md`). The reservation fallback stays controller-side and is
self-sufficient (`forecast.Plan` re-derives fit independently of the plugin).

**D2 — Gang atomic unit = the whole Run's Active pod set (all groups).**
Per `quota-semantics.md` ("gang allocation is all-or-nothing per width"). `LabelGroupIndex`
constrains *where* a subset lands (one fabric domain each), it does not shrink the atomic
admission unit. Spares (`LabelRunRole=Spare`) sit out Permit — they are held capacity, not
gang members, and only enter on a swap. (v1 has one RunRole, so gang=Run=role coincide;
ROLES adds a third key later.)

**D3 — Expected gang width = an annotation, `rq.davidlangworthy.io/expected-width`.**
Stamped once at pod emission from `RunRole.Width` (pre-Roles: the pack plan's pod count).
Permit reads it off the pod — self-contained, no per-pod Run/JobSet lookup, no
count-what-exists race (the failure mode that would let a partial gang admit). Matches the
`PodGPUAnnotation`/`EtaAnnotation` precedent; width is not a selector dimension so it is not
a label. **No PodGroup CRD** — `borrow-vs-build.md` §6.1/§6.3 already settled this
(reimplement `minMember`'s *purpose*, not its machinery; `RunRole.Width` is the one
versioned source of truth).

**D4 — The commit mints jobtree's own `rq.davidlangworthy.io/leases` CRD**, not the built-in
`coordination.k8s.io/v1` Lease. `fwk.Handle` exposes `ClientSet()`/`KubeConfig()`; the
plugin builds a controller-runtime client from `KubeConfig()` with our scheme registered
(plus informers for Run/Lease/Budget) to read the funding ledger and create Leases.

**D5 — Side effects go in `PreBind`, placement stays with `DefaultBinder`.**
PreBind is the framework-blessed, retry-safe place for side effects (a PreBind failure
backs off and retries; it does not mark the pod Unschedulable). The plugin mints the Lease
in PreBind and lets `DefaultBinder` bind the pod to the Filter-approved node — no custom
`Bind`. Mint is **idempotent**: Lease name = `run+group+seed`, `OwnerReference` the pod, so
concurrent per-pod PreBinds and retries converge to one Lease per group-slice.

**D6 — Funding atomicity preserved.** Permit is an *optimistic* gang+funding gate
(`funding.Evaluate` → `cover.NewInventory` → `Plan` over the gang's width vs. the live
lease ledger). PreBind is the *authoritative atomic* commit: it re-runs
`funding.Evaluate`+`cover.Plan`+`binder.Materialize` under a plugin-level mutex and creates
the Leases — so no two gangs overspend an envelope between gate and commit (the
`BudgetConservation.tla` invariant the current bridge mutex protects). If the optimistic
gate passed but the atomic re-check fails (someone raced), PreBind rejects → pods requeue →
controller forecasts a reservation.

**D7 — Lifecycle intent rides on pod annotations.** The controller distinguishes
Start / Grow / Swap / reservation-Activate by stamping `rq.davidlangworthy.io/lease-reason`
(and, for Swap, the original funding provenance) onto the intent pods. The plugin's one
commit path reads it and mints with the right `LeaseReason`. This is what collapses the
controller's **four** commit sites (admission, grow, activation, node-failure swap) into a
**single** plugin committer.

---

## 2. Move / stay split (from the call-site map)

**Moves into the plugin** (all four are `binder.Materialize` commit sites today):
| controller today | → plugin phase |
|---|---|
| `funding.Evaluate` + `cover` fit check (Reconcile:179, activate:786, grow:1515) | Permit (gate) + PreBind (atomic commit) |
| `planPlacement`/`pack.Planner` (Reconcile:216, activate:815, grow:1503) | PreFilter/Filter (plan once, enforce per-pod) |
| `binder.Materialize` lease mint (Reconcile:291, activate:938, grow:1543) | PreBind |
| `createSwapLease`+`updatePodsAfterSwap` (HandleNodeFailure:1057, 1910, 1946) | PreBind (swap = re-emit group pods + mint Swap lease) |
| `reclaimForAdmission` funding-fit precheck (Reconcile:228, 348) | PostFilter (deferred — see §5) |

**Stays in the controller** (never a second commit — mostly re-derivations that only *close*
leases or write status): reservation/forecast writing (`planReservation`, ETA); follow
gating; elastic desired/allocated/step **arithmetic** (only the commit tail moves); shrink
(closes leases only); completion detection; node-failure **parking**/checkpoint-deadline;
budget-status mirrors; reclaim **ranking + eviction** (`resolver.Resolve`/`applyResolution`
close loser leases); half-applied-admission lease **adoption** (read open leases, flip
status — this is in fact the post-cutover primary pattern).

**Pure funcs the plugin calls** (the moat, reused verbatim): `funding.Evaluate`,
`cover.NewInventory`/`Plan`, `pack.Planner`, `binder.Materialize`.

---

## 3. Sequenced increments (on `feat/workload-trunk`)

Intermediate commits may be red; the **end state is purely new** (single committer, no
nodeName pin, no pause image). Merge trunk→main as one honest increment at P2c exit.

- **P2a — the plugin gets real (additive, stays green).** Implement `Filter`/`Score`
  (pack-plan enforcement), `Permit` (gang assembly + funding gate + `IterateOverWaitingPods`
  Allow/cascade-Reject, <15m timeout), `PreBind` (atomic funding commit + idempotent Lease
  mint), `Unreserve` (gang-state cleanup), `EnqueueExtensions`. Build the plugin's client +
  Run/Lease/Budget informers from `handle.KubeConfig()`. Unit-test the whole decision surface
  in isolation (fake pods/nodes/runs/leases) — this is the 11 `migrate-to-plugin-test`
  scenarios re-homed as plugin tests. The no-op scaffold becomes real; existing envtests
  untouched → **green**.

- **P2b — controller emission cutover + delete the old commit path (the big one).**
  `bridge.go buildPod` emits real unscheduled pods: deep-copy `RunRole.Template`, overlay
  `nvidia.com/gpu` request==limit==GPUsPerPod on the GPU-target container, `schedulerName=jobtree`,
  drop `spec.nodeName`, add the expected-width + lease-reason annotations, force
  `RestartPolicy=Never`; legacy Roles-less runs keep a real default container (not pause).
  Convert all four controller commit sites to "ensure the right set of intent pods exists."
  **Delete** `binder.Materialize`-from-controller, `createSwapLease`, `updatePodsAfterSwap`,
  the nodeName pins, `pauseImage`. Migrate the 18 `rewrite-lifecycle-only` tests (replace
  "Reconcile-once-to-bind" setup with pre-seeded leases simulating "the plugin already bound
  this") + 4 `rewrite-assertion`. Regenerate goldens (`UPDATE_GOLDEN=1`) — review the diff:
  4 of 6 change (leases now minted by the plugin, not Reconcile), funding class / payer /
  deficit / remedies must **not** change. Single-committer reached.

- **P2c — real cluster proof.** Deploy the plugin as a second scheduler in kind (Deployment
  + ConfigMap from `config/scheduler/jobtree-config.yaml` + RBAC: `system:kube-scheduler`,
  `system:volume-scheduler`, `extension-apiserver-authentication-reader`, **plus** a dedicated
  grant to create `rq.davidlangworthy.io/leases` in the target namespace). Move the 2
  `move-to-e2e` tests. Flip `TestRunAdmitsAndBindsOnRealCluster` + the blocked
  `TestRunCompletesWithRealContainer`: a real container runs to **exit 0**, scheduled by the
  plugin, lease minted by the plugin, run Completed — **no hand-injected state**. This is the
  phase-1 exit criterion and the trunk→main merge gate.

---

## 4. Test migration ledger (46 affected of ~46 Test funcs)

- **keep-unchanged (11):** pure lifecycle/forecast/webhook — half-applied adoption, completion,
  zero-hour envelope, reforecast-fail, park-cleanly, resolution-metrics, invalid-lease webhook.
- **migrate-to-plugin-test (11):** the admission/commit decisions — become plugin unit tests in P2a.
- **rewrite-lifecycle-only (18):** pre-seed leases instead of Reconcile-to-bind, then test the
  unchanged lifecycle behavior — done in P2b.
- **rewrite-assertion (4):** follow-then-admit, isolation, goldens — assert "intent pods emitted"
  not "lease minted by Reconcile" — P2b.
- **move-to-e2e (2):** real-Admitted-event + manager-binds-end-to-end — need the running plugin
  binary — P2c.

---

## 5. Explicitly deferred (not this cutover)

- **PostFilter reclaim** — the reclaim funding-fit precheck maps to PostFilter, but the
  eviction *mechanism* stays controller-owned per §6.1 Q5; wiring PostFilter is a follow-on
  (CASCADE), not a P2 blocker.
- **Multi-role gangs** — the third gang key (role name) and disambiguating `LabelRunRole` is
  ROLES-2.
- **JobSet lowering** — P2 emits pods directly from `bridge.go` (legacy path made real). The
  Run→JobSet lowering (`pkg/lowering`) that makes `ReplicatedJob.replicas` the live width
  source is JOBSET-2/5, layered on after the plugin commit works.
