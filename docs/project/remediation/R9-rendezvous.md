# R9 — Distributed-training rendezvous on the live path

**Priority:** P2 · **Design:** complete (Fable), **architecture decision for David** · **Next:** Opus implements, Sonnet verifies
**Depends on:** R8 (failure/restart semantics travel with whichever path is chosen).

## Problem (evidence)

`buildPod` injects **zero** environment and creates **no** Service
(`controllers/kube/bridge.go:327-409`): no `MASTER_ADDR`/`WORLD_SIZE`/`RANK`, no
headless DNS, no stable pod identity (pod names embed pack's advisory node + a
sequence number — `pkg/binder/binder.go:280`). The only rendezvous code lives in
`pkg/lowering/lowering.go` behind `ErrNotImplemented`. An API comment
(`api/v1/run_types.go:67`) **falsely** claims the overlay injects rendezvous env.
So a `width>1` gang cannot form a process group — the headline "real GPU
workloads / multi-thousand-GPU training" holds only for single-pod or
embarrassingly-parallel jobs. (R10 fixes the false comment immediately.)

## Root cause

Rendezvous was designed to arrive with the JobSet lowering (Option C), which was
deferred; the direct-bridge emit path that shipped instead never grew the
rendezvous wiring.

## Design decision

Two viable architectures; pick one. Both must deliver: a stable per-rank identity,
a discoverable master address, `WORLD_SIZE`/`RANK` (or the framework's expected
env), and headless DNS.

- **Option A — Finish JobSet lowering (`pkg/lowering`), recommended long-term.**
  Lower a Run's role to a JobSet `ReplicatedJob`; JobSet provides the headless
  Service, stable pod hostnames (`<job>-<replica>`), the rendezvous env, and — for
  free — the `failurePolicy`/restart semantics R8 needs and gang co-termination.
  Cost: it re-plumbs the emit path through JobSet objects, and the jobtree plugin
  must schedule JobSet-created pods (they carry `schedulerName=jobtree`), so the
  gang key / provenance annotations must survive JobSet's pod template. Larger, but
  it is the architecture the design always intended and it subsumes R8.
- **Option B — Direct-inject in `buildPod`, recommended if A is too big now.**
  Create a per-run headless Service; give pods a stable ordinal identity (index the
  gang, put the ordinal in the pod name/hostname/subdomain); inject
  `MASTER_ADDR=<run>-0.<svc>`, `MASTER_PORT`, `WORLD_SIZE=width`, `RANK=<ordinal>`
  (and `NODE_RANK`/`LOCAL_WORLD_SIZE` for multi-GPU pods). Smaller and stays on the
  current emit path, but re-implements a slice of what JobSet already does and does
  **not** give you failure/restart semantics (so R8 remains fully separate).

**Recommendation:** if the team wants distributed training to actually work soon
and is willing to take on one substantial change, do **A** (it also closes R8 and
part of the gang co-termination gap). If bandwidth is tight, do **B** now as a
bridge and migrate to A later — but then finish R8 independently.

### Decision for David (flagged)

Option **A (finish JobSet lowering)** vs **B (direct-inject + headless Service)**.
This is the single biggest fork in the P2–P5 set; it also decides whether R8 is
separate (B) or subsumed (A).

## Invariant

A `width=N` role starts N pods that can complete a collective rendezvous
(torch/NCCL) with correct `WORLD_SIZE`/`RANK` and a resolvable master, on the live
path, with no researcher-side discovery plumbing — fulfilling index.md's
"without making researchers think about schedulers".

## Implementation spec (Opus)

- **Option A:** implement `pkg/lowering` (remove `ErrNotImplemented`); emit a JobSet
  per role; ensure JobSet's pod template carries `schedulerName=jobtree`, the gang/
  cohort/flavor annotations, the GPU request, and (post R5) is created by the
  controller SA. Verify the plugin gangs JobSet pods identically. Retire the direct
  `buildPod` workload path (keep it for the legacy Roles-less run only, or delete).
- **Option B:** in `bridge.go` `buildPod`, add ordinal identity + env injection;
  add a per-run headless `Service` created/GC'd by the controller; thread the
  ordinal from the emit loop (the gang already knows width and index). Update the
  pod naming to expose a stable hostname/subdomain.
- Either way: **fix `run_types.go:67`** to describe what actually happens (or defer
  to R10 which does exactly this).

## Verification spec (Sonnet)

1. **Live (`rendezvous-smoke.sh`, the real proof).** 2-node kind, a `width=2`
   role running a tiny `torch.distributed.init_process_group("nccl"/"gloo")` that
   all-reduces one tensor and exits 0. Assert both ranks rendezvous and the run
   Completes. This is the test that actually proves the promise.
2. **Env assertion.** Unit/e2e: assert each pod has correct `RANK`/`WORLD_SIZE` and
   a resolvable `MASTER_ADDR`.
3. **Option A extra:** assert JobSet-created pods are gang-scheduled and funded by
   the plugin exactly like direct-emit pods (provenance/annotations survive).
4. **Golden.** Regenerate; audit that only the emit shape changes, not funding.

## Interactions

- **R8** — subsumed by A (JobSet failurePolicy) or complementary to B.
- **R10** — the false-comment fix; do it now regardless of A/B.
- **R2** — gang recovery must understand whichever pod identity model is chosen.
