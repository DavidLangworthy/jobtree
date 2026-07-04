# Pickup notes — where we are, where to start next

_Last updated: 2026-07-04 (end of week). Author: single-committer / CASCADE work._

## TL;DR

The single-committer cutover is **done and live-proven end to end**. All controller
mint sites (admission, reservation activation, elastic grow, node-failure swap)
and now **held spares** go through the scheduler plugin — the sole committer.
Everything below is merged to `main`.

## Landed this week (all merged to `main`)

- **PLUGIN-2** — controller stops minting; plugin is the committer (#35).
- **CASCADE-1** reservation activation via intent pods (#36).
- **CASCADE-2** elastic grow via cohort intent pods, incremental delta funding (#37).
- **CASCADE-3** node-failure swap through the plugin, provenance-preserving (#38).
- **CASCADE-3b** held spares emitted + funded + minted live; swap lands on them (#39).
  - Live proof: `hack/e2e/swap-smoke.sh` (2-node kind, cordon active → plugin
    mints `Swap` lease on the former spare node, provenance preserved, Running).
- **CI fix** — `kind e2e` Install-kind step installs kind as root (#40). This job
  had been red on `main` for *every* run (chmod on the runner's root-owned
  `/usr/local/bin/kind`); see the "CI status" section for the post-merge result.

Green locally at merge time: `go test -race ./...`, `make antifake`,
`make envtest`, `make verify-generate`, plus `grow-smoke.sh` and `swap-smoke.sh`.

## Live proof scripts (run any of these on a machine with kind + go)

- `hack/e2e/plugin-smoke.sh` — plugin binds a GPU pod + mints its lease.
- `hack/e2e/fullstack-smoke.sh` — manager + scheduler, run → container exit 0 → Completed.
- `hack/e2e/grow-smoke.sh` — malleable run grows 2→4, plugin funds the delta cohort.
- `hack/e2e/swap-smoke.sh` — node-failure swap onto a held spare, provenance preserved.

## CI status — #40 worked, and it surfaced the NEXT failure (Monday item #1)

The post-merge `e2e` run on `main` (`cb5d938`) confirmed the #40 fix: the job now
gets **past the previously-broken Install-kind step for the first time ever**.
Steps that had never run in CI now pass:

- ✅ Install kind · ✅ kind-up · ✅ Build and load the **manager** image into kind

It then fails at the next step — **newly-visible real signal, not a regression**:

- ❌ **Install the real chart** — `helm ... Error: context deadline exceeded`,
  because the **scheduler** pod is stuck `ErrImagePull` on
  `jobtree-scheduler:e2e-local`. That image is never built/loaded into kind.
- ⏭️ `test/e2e` skipped (chart install failed first).

**Root cause + fix (small, well-scoped):** the CI workflow's "Build and load the
manager image" step runs `make e2e-image`, which builds + `kind load`s only
`$E2E_IMAGE` (the manager). It does **not** build/load `$E2E_SCHEDULER_IMAGE`
(`jobtree-scheduler:e2e-local`, built from `Dockerfile.scheduler`) — even though
`hack/e2e/run-e2e.sh:42-47` does exactly that. Fix: add the scheduler build +
`kind load` to the `e2e-image` Makefile target, mirroring `run-e2e.sh`:

```make
e2e-image:
	@set -a; . hack/e2e/versions.env; set +a; \
	echo "Building $$E2E_IMAGE"; \
	docker build -t "$$E2E_IMAGE" .; \
	echo "Building $$E2E_SCHEDULER_IMAGE"; \
	docker build -f Dockerfile.scheduler -t "$$E2E_SCHEDULER_IMAGE" .; \
	kind load docker-image "$$E2E_IMAGE" --name "$$KIND_CLUSTER_NAME"; \
	kind load docker-image "$$E2E_SCHEDULER_IMAGE" --name "$$KIND_CLUSTER_NAME"
```

Expect this to advance the job to `test/e2e`, which then runs in CI for the first
time — it may reveal a further never-run failure (fix as its own small PR). Note
`test/e2e` has 3 documented expected skips (blocked on JOBSET — see
`test/e2e/completion_test.go`, `follow_test.go`); those are fine.

## Where to pick up next (ranked by value)

1. **Fix any newly-visible `e2e` failure** (see CI status above) — cheap, and it
   restores the structural anti-fake guard.
2. **ROLES (Track #21) — the one substantive unbuilt feature.** Today the
   controller honors only `run.Spec.Roles[0]` (see `intentPodShape` and `buildPod`
   in `controllers/kube/bridge.go`). A Run with multiple roles, per-role
   elasticity, and per-role criticality is not implemented. The cohort-key
   groundwork from CASCADE-2 (`gangKey` includes cohort) is the stepping stone.
   Start by making `emitIntentPods`/`buildPod`/`intentPodShape` iterate all roles
   and gang each role as its own cohort.
3. **CASCADE-4 (optional consolidation).** One combined live proof extending
   `fullstack-smoke.sh`: a run that grows, loses a node and swaps to a spare, and
   a follower that admits after completion — all in one cluster, no injected
   state. Each behavior is already individually proven; this just proves they
   compose.

## Deliberately deferred (not gaps — design says so)

- **PostFilter reclaim** (`cmd/scheduler/plugin/plugin.go`, "reclaim not wired
  PLUGIN-6"). Reclaim/eviction already works controller-side per borrow-vs-build
  §9; plugin-driven preemption is an optional optimization.
- **JOBSET lowering** (`pkg/lowering/lowering.go`, guarded `ErrNotImplemented`).
  Real workloads already run via direct bridge pod emission; JobSet lowering is
  an alternative architecture, only worth doing if we want JobSet semantics.

## Working agreements / gotchas

- David merges PRs (branch protection); the repo merges via merge commits.
- **Live proofs catch what unit/envtest can't.** They exercise `buildPod` + real
  scheduler timing + the actual CRD field names. Two real bugs this week were
  caught only live: `buildPod` dropping the cohort annotation (CASCADE-2), and
  the run-level spare field being `sparesPerGroup` not `spares` (CASCADE-3b).
- Background shells: use `run_in_background`, never `&` (gets SIGTERM'd). Never
  `pkill` in the same command as a `git commit`.
- If multi-tracking with agents: commit WIP first, keep file-scopes disjoint, and
  forbid agents from spawning sub-agents or running `git checkout`/`restore`/
  `revert` (an agent's rogue sub-agent wiped uncommitted work once this week).

## Source of truth

- Design: `docs/project/cascade-plan.md`, `docs/project/borrow-vs-build.md` §9,
  `docs/project/plugin-cutover-plan.md`.
- Parity rail: the frozen `legacy/nodename-binder` worktree +
  `controllers/golden_test.go`.
