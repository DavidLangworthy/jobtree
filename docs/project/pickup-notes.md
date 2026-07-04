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

## CI status to verify Monday morning

The #40 fix unblocks the `kind e2e` job, which had **never run to completion in
CI** before. So downstream steps (`kind-up`, image build, chart install,
`test/e2e`) are running in CI for the first time. **Check the latest `e2e`
workflow run on `main`:**

- If green: the whole e2e rail is finally live — nothing to do.
- If Install-kind now passes but a *later* step fails: that is newly-visible real
  signal (not a regression), previously masked by the broken install step. Fix
  it as its own small PR. `test/e2e` has 3 documented expected skips (blocked on
  JOBSET — see `test/e2e/completion_test.go`, `follow_test.go`); those are fine.

_(This section's result was being watched at hand-off; if the run had finished it
would be recorded here — otherwise `gh run list --workflow=e2e.yaml --branch=main
--limit=1` shows it.)_

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
