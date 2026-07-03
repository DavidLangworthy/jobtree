# Plan: run dependencies (`follow`) and ETA status

> **Status: plan (design + work breakdown), no code in this change.** Landed alongside the
> fundamentals reconciliation at the project owner's request. Internal (`docs/project/`, not
> published to the site).

## 0. Why

The home page promises “job forests,” “job trees,” and “dependent stages start together or not at
all” (data prep → training → evaluation with shared quotas). The implementation does not have this:
a **Run is a single gang** of pods (groups + optional elasticity/spares/borrowing); there is no
run-dependency graph and no workflow composition (`fundamentals.md` §10, and `RunSpec` carries no
dependency field). The genuinely strong, tested product is budget-correct, topology-aware gang
scheduling with metered quotas, derived funding classes, and recall.

This plan closes that gap the researcher-friendly way — a minimal **`follow`** dependency primitive
that makes “job forests” real without a heavyweight combinator/DAG language — and adds an optional
**ETA** status field for dashboards. Both are designed researcher- and operator-first. Where a
requirement would be nonsensical to implement literally, we push back to the simpler requirement that
serves the same need (§5). The whole point of the system is to keep things easy for researchers and
operators; the design below does not cheat on that to make the implementation smaller.

> **Load-bearing finding (from grounding this plan in the code): the system has no run *completion*
> concept yet.** `RunPhaseComplete` is defined but never assigned anywhere in production or tests
> (`controllers/run_controller.go:28` is only ever read at `:93`/`:404`); workload pods are `pause`
> containers with `RestartPolicy: Never` that never exit (`controllers/kube/bridge.go`), nothing
> watches pod completion, and a run that *were* Completed does not close its leases, so it would keep
> charging its budget and holding GPUs forever (`run_controller.go:93-96`). The only reachable
> terminal phase today is `Failed`. **`follow` waits on completion, so it cannot ship until completion
> exists** — this plan therefore scopes run completion as prerequisite **B0** (§2.0). It is a real
> feature in its own right (a training job that finishes should release its GPUs), not just plumbing
> for `follow`.

---

## 1. Feature A — ETA status (optional, observability only)

### Requirement
A run may carry an estimated completion time, optionally reported by the workload itself. **No
penalty** for omitting it, and **nothing in scheduling depends on it**; dashboards and the CLI
surface it when present.

### Surface
`Run.status.eta` (optional):

- `estimatedCompletion` — RFC3339 timestamp.
- `reportedAt` — when it was last reported (lets dashboards detect staleness).
- `source` — `job` | `controller` (who set it).

### How the job reports it (low friction)
The workload should **not** need Run-status write RBAC — that is operator toil and blast radius for a
purely informational field. The job sets a pod annotation `rq.davidlangworthy.io/eta: <RFC3339>` on
its own pod (the `rq.davidlangworthy.io/` prefix matches the existing label/annotation convention);
the run controller mirrors it into `Run.status.eta` (`source: job`). For humans/demos,
`kubectl runs eta <run> <time>` writes `status.eta` directly (`source: controller`) — no annotation
round-trip.

- **Decision to confirm:** annotation-mirror (recommended) vs. a status endpoint vs. granting jobs
  status-write RBAC.
- **Plumbing this needs (does not exist today):** `binder.PodManifest` has no `Annotations` field and
  `Bridge.load` discards every pod annotation except the GPU count, so the engine literally cannot see
  a `…/eta` annotation. A2 must (a) add `Annotations` to `PodManifest`, (b) load them in the Bridge,
  and (c) add a Run↔Pod watch so an annotation change triggers a reconcile at all (the primary Run
  watch is generation-gated and will not fire on a pod annotation — see B3).

### Non-goals
ETA never affects admission, funding, reservations, or cuts. It may inform a *display-only* “expected
start” for followers (§2) but gates nothing.

### Work
- **A1. API** — `RunStatus.ETA` struct; deepcopy; regenerate CRDs into both copies; no webhook (status
  is not validated).
- **A2. Controller + plumbing** — add pod annotations to the binder/Bridge model; a Run↔Pod watch;
  mirror the annotation → `status.eta` each reconcile; clear when the source pods are gone. Tie-break
  when the gang’s pods disagree: take the **latest `estimatedCompletion`**, set `reportedAt = now` at
  mirror time. The CLI/`source: controller` path writes `status.eta` directly.
- **A3. CLI** — `explain` shows ETA; an `eta` subcommand writes `status.eta` (controller source).
- **A4. Dashboards** — Grafana ETA column/panel; observability docs note it is optional and may be stale.
- **A5. Tests** — mirror present/absent/stale + multi-pod tie-break; assert no scheduling effect.

---

## 2.0 Prerequisite B0 — run completion (a real feature, and the gate for `follow`)

### Requirement
A run whose workload finishes should reach a terminal **`Completed`** phase and **release its GPUs**.
Today it does not: `Completed` is never set, and an open lease keeps charging the budget and holding
capacity forever. This is worth doing on its own (a finished training job should free its fleet), and
`follow` depends on it.

### Semantics
- **What completes a gang**: its workload pods finish. In a real deployment the workload container
  runs the job and exits `Succeeded` (the current `pause`-container pods are a dev/demo artifact and
  never exit). A run is `Completed` when all of its active (non-spare) pods have `Succeeded`.
- **On completion**: set `RunPhaseComplete` and **close the run’s open leases** (reason `Completed`) so
  the funding derivation stops counting them — otherwise the budget and GPUs never free.
- **Workload failure** (a pod `Failed`, not the fleet): **a single pod failure does not fail the run**
  (owner decision). v1 surfaces it and leaves the run as-is; retry/backoff is a separate future
  feature. Only node-loss-without-spare and resolver kills produce run `Failed` today, unchanged. (So
  completion is “all active pods `Succeeded`”; a pod `Failed` neither completes nor fails the gang in
  v1.)
- **Dev/CLI path**: for the snapshot simulator, completion is signaled explicitly
  (`kubectl runs complete <run>` or a snapshot phase) since there are no live pods to watch.

### Work
- **B0a. Pod-completion watch** — a Pod reconciler (or extend the node watch) that watches workload
  pods labeled to a run for `Succeeded`, aggregates across the gang, sets `RunPhaseComplete`, and
  closes the run’s open leases. Wire it in `cmd/manager/main.go` (only Run/Reservation/Node/Budget
  reconcilers exist today).
- **B0b. Lease closure on Completed** — ensure the Completed branch (`run_controller.go:93`) closes
  leases rather than just recomputing width.
- **B0c. CLI `complete`** for the snapshot path; envtest for the pod-Succeeded → Completed + leases-
  closed transition.
- **Decision to confirm:** completion signal (pod `Succeeded` aggregate, recommended) and whether a
  workload-pod `Failed` should fail the run or be left to policy.

## 2. Feature B — `follow` (run dependencies)

> Gated on **B0** — a follower waits on `Completed`, which does not exist until B0 lands. Shipping
> `follow` before B0 would mean followers can only ever *fail* (the sole terminal today), which is
> actively misleading; do not ship B before B0.

### Requirement
“Start this run after the followed run(s) complete.” A researcher expresses a workflow as a set of
runs joined by follow edges (`train` follows `data-prep`; `eval` follows `train`), and each run
starts only after its upstreams finish. This is the researcher-facing realization of “job forests.”

### Surface
- `Run.spec.follow` — the common case is just a list of upstream run names in the **same namespace**;
  **all** must complete (AND semantics, matching “after the followed job **or jobs** completes”). To
  keep the policy out of the way of the 90% case it is a small struct:
  `follow: { after: [names…], onUpstreamFailure?: wait | fail, upstreamFailureGrace?: duration }`
  (the CLI’s `--follow <name>` fills `after`; policy fields default). 
- New phase **`Waiting`** — blocked on dependencies, distinct from `Pending` (admitted, no capacity),
  with a message listing the outstanding upstreams. A distinct phase is a clearer researcher signal
  than overloading `Pending`.

### Semantics
- A run with unmet `follow` stays **`Waiting`** and does **not** run cover/pack/admission or reserve
  capacity — cheap for operators (no zombie reservations).
- When **all** followed runs reach `Completed`, the run proceeds to normal admission
  (`Pending → Running`, with reservations/funding exactly as today).
- **Upstream failure** (owner decision — *wait a bit, overridable*): a followed run reaching terminal
  `Failed` will never complete (no resurrection). The follower stays `Waiting` with reason
  `upstream <X> failed` for a **grace period** so the researcher can fix and resubmit just the failed
  stage; if it is not resolved within the grace, the follower `Failed`s with a clear reason (no silent
  zombie). Controlled by `spec.follow` policy fields with sensible defaults, both overridable:
  `onUpstreamFailure: wait | fail` (default `wait`) and `upstreamFailureGrace` (default e.g. 30m).
  A followed run *deleted* during the grace is treated the same as failed (`upstream <X> deleted`).
- **Deleted upstream**: a followed run *deleted* before it completes (not Completed/Failed) would
  otherwise strand the follower in `Waiting` forever. The Run→Run watch must therefore also fire on
  upstream **delete** and re-evaluate dependents (fail with `upstream <X> deleted`, or, under a `wait`
  policy, keep waiting with that reason).
- **Cycles / existence**: the validating webhook is **clientless** (it calls the pure `Run.Validate*`
  methods with no API access), so it can only enforce field-level rules — non-empty names, no
  self-follow, dedupe, a max-follow count. **Existence** (dangling refs) and **cycle detection** are
  controller concerns: `Reconcile` already holds the whole world (`ClusterState.Runs`), so a
  per-reconcile edge-walk is cheap and marks a cycle- or dangling-blocked run `Failed`
  (`follow cycle: A→B→A` / `unknown upstream <X>`).
- **Trigger**: a **Run→Run watch** with a *custom* predicate — enqueue a run’s followers only on a
  transition **into** a terminal phase (non-terminal → `Completed`/`Failed`) or on delete. It cannot
  reuse the `Budget→Run` `GenerationChangedPredicate`: phase lives in **status**, which does not bump
  `metadata.generation`, and a naïve “any Run update” watch would fire on every status write and, with
  `MaxConcurrentReconciles=1`, churn/livelock. Transitive chains resolve one completion at a time.
- **Scope**: same-namespace follow only in v1 (researcher-scoped, simple visibility; the world model
  is namespace-agnostic internally so this is a policy choice, not a code limit). Cross-namespace
  follow needs a visibility/permission model — future work.

### UX
- Submit: `kubectl runs submit … --follow data-prep` (repeatable).
- `explain` / `watch`: show `Waiting — after: data-prep, train`; normal status once eligible.
- Optional `kubectl runs tree`: render the follow forest with each run’s phase and ETA, so a
  researcher sees the whole workflow at a glance — this is where `follow` + ETA compose into the “job
  forest” view the home page promises.

### Non-goals / explicitly out of scope
- No `AND`/`SEQ`/`SHARD` **combinator language** or compiled job-DAG (`fundamentals.md` §2/§10): a
  `follow` edge list plus the existing grouping and malleability already deliver “dependent stages in
  order” and gang scheduling, without a DAG-rewrite engine (§5).
- No speculative pre-reservation of a follower timed to an upstream’s ETA in v1 (start on completion
  via the normal admission path). Future.
- No data passing between runs — that is the workload’s concern (shared storage, etc.).

### Work
- **B1. API** — `RunSpec.Follow []string`; `RunPhaseWaiting`; *field-level* webhook validation only
  (non-empty names, no self-follow, dedupe, max count); deepcopy; CRD regen; corpus + webhook cases.
- **B2. Controller** — a **Waiting gate** placed early in `Reconcile`: after the not-found and
  `Complete` short-circuits but **before** `topology.BuildSnapshotForFlavor` and, critically, before
  the open-lease **adoption** step (so a stray open lease cannot flip a Waiting run to Running behind
  the gate). While any dep is unmet, set `Waiting` and return with a `RequeueAfter` backstop (the
  current requeue only covers `parked`/`running`). Proceed to normal admission on all-`Completed`;
  upstream-`Failed`/deleted and cycle/dangling → `Failed(reason)` (or `wait` per policy). This leaves
  the funding/reservation path untouched — a Waiting run never enters admission and never has a
  reservation for `ActivateReservations` to touch.
- **B3. Watch** — a Run→Run watch with the custom terminal-transition/delete predicate above (enqueue
  followers via a reverse index or a scan); resync backstop. **Must not** reuse the generation gate.
- **B4. CLI** — `--follow` on submit; Waiting display in `explain`/`watch`; optional `runs tree`.
- **B5. Dashboards** — Waiting/blocked state and a follow-graph panel; combine with ETA for expected
  start.
- **B6. Docs** — researcher-guide “chaining runs (follow)”; reframe `index.md` (job forests = runs +
  follow edges, funded by a hierarchy of budgets) and add `follow` to `fundamentals.md` §2 surface
  plus a Follow transition in §6 (and reconcile §10: the combinator engine is superseded by `follow`
  for the common case).
- **B7. Tests** — Waiting→admit on completion; multi-follow AND; upstream-failure; upstream-delete;
  cycle; dangling ref; transitive chain; watch re-trigger without storms; an envtest end-to-end
  (needs B0’s completion path).

---

## 3. Docs / home-page reconciliation

Once `follow` lands, “job forests” are real: a forest of runs joined by follow edges, funded by a
hierarchy of budgets. Reframe the home page and fundamentals to describe **that**, honestly, rather
than the unbuilt combinator DAG. Until then the home page overstates workflow orchestration; this plan
is the bridge, and `fundamentals.md` §10 stays accurate (the *combinator* engine is not built and is
largely superseded by `follow`).

---

## 4. Sequencing & effort

1. **ETA (A)** — small but not free: the status field is trivial, the annotation-mirror needs pod-
   annotation plumbing (A2). Ship first; independent of the rest.
2. **Completion (B0)** — prerequisite for `follow` and a feature in its own right (a finished run
   releases its GPUs). A pod-Succeeded watch + lease closure on Completed.
3. **Follow (B)** — the substantive feature, **gated on B0**: API + a new `Waiting` phase + a
   status-driven Run→Run watch + controller-side existence/cycle checks + CLI + docs. It leaves the
   admission path unchanged — a `Waiting` run never enters admission, so the funding/reservation
   machinery is untouched — but the `Waiting` gate placement and the watch predicate are the two spots
   that need care (§2).
4. **Docs reconciliation (3)** — lands with B.

---

## 5. Pushback / “back off one click”

Per the owner’s guidance — don’t cheat researcher/operator UX to make the implementation easier, but
push back when a requirement is genuinely nonsensical and offer the simpler requirement that does the
job:

- **Full job-DAG combinator language (AND/SEQ/SHARD compiler).** Not worth building. The researcher
  need is “run these stages in order” and “gang-schedule a stage.” **Simpler:** `follow` (ordering) +
  the existing groups/malleability (gang + elasticity).
- **Jobs writing Run status directly for ETA.** Pushes RBAC and blast-radius onto operators for an
  informational field. **Simpler:** a pod annotation the controller mirrors.
- **Auto-retry/resurrect a failed upstream so the follower eventually runs.** Conflicts with the
  no-resurrection model and hides failures. **Simpler and honest:** fail the follower with a clear
  reason; the researcher resubmits (optional policy later).
- **Cross-namespace / cross-team follow.** Needs a real multi-tenant visibility/permission model.
  **Back off:** same-namespace in v1; revisit with that story.
- **Speculative pre-reservation timed to an ETA.** Complex and error-prone. **Back off:** start on
  completion via the normal path; ETA informs display only.

**Decisions (settled by the owner, 2026-07-03):**

- (a) **ETA reporting mechanism** — annotation-mirror (no per-job RBAC).
- (b) **Completion signal** — pod `Succeeded` aggregate; **a single pod `Failed` does not fail the
  run** (surfaced, left to a future retry policy).
- (c) **Upstream failure** — `onUpstreamFailure` defaults to **`wait`** (a bounded grace so the
  researcher can fix and resubmit just the failed stage), **overridable** (policy + `upstreamFailure
  Grace`); on grace expiry the follower `Failed`s so it never becomes a silent zombie.
- (d) **`follow` same-namespace only in v1** — yes.

**A note on scope honesty:** grounding this plan in the code turned up that “completion” (B0) does not
exist yet — a good example of not cheating on requirements. The researcher-facing promise (“run these
stages in order”) is small; delivering it correctly means also making a finished run *finish*
(release GPUs, stop charging), which is the right thing regardless of `follow`.
