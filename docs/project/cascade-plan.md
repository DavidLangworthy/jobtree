# CASCADE plan — finish the single committer + un-fake "once pods run"

PLUGIN-2 cut the **normal admission** path over to the single-committer model
(`borrow-vs-build.md` §9): the scheduler plugin places pods and mints the Lease;
`run_controller` emits unscheduled intent pods and adopts the plugin's leases.
Three controller mint sites remain — they only fire on already-Running runs or
specific events, so they're a documented interim two-committer state, not a
regression. CASCADE moves them onto the same intent-pod → plugin-mint mechanism
and, with real pods now running, makes the downstream "once pods run" features
(completion, follow, elastic, node-failure, opportunistic fill) genuinely
exercised end-to-end.

## The three remaining mint sites

### 1. Reservation activation — `activateReservation` (run_controller.go:935)
When a reservation fires, the controller frees capacity (resolver eviction —
**stays**, per §9) then `binder.Materialize`s the full gang. Cutover: after the
resolver frees room, **emit the full-width intent pods** (same as initial
admission, `expected-width` = full width, `lease-reason` = Start); the plugin
funds + binds; the run's adoption path releases the reservation. No plugin
change — it's a fresh full gang, exactly what Permit already gates.

Two subtleties:
- **Once-per-activation eviction guard.** The resolver must not re-evict every
  tick while the freed capacity waits for the plugin's async bind. Guard: if the
  run already has intent pods out (emitted a prior tick, leases not yet
  adopted), skip the resolver and just wait.
- **Opportunistic / promised-unfunded activation — DESIGN FORK (decided).**
  `opportunisticCoverPlan` deliberately starts a *promised* reservation even when
  its envelope is exhausted (classed Unfunded, re-funded by arithmetic when quota
  returns). The plugin's Permit funding gate would **reject** an unfunded gang,
  so it cannot honor this promise. **Decision:** keep the opportunistic/promised
  start as a **narrow, explicitly-documented controller mint** — it is *not* a
  second funding authority, it is honoring a prior promise with a deliberately-
  Unfunded lease that the live gate is designed to refuse. Giving the plugin a
  "bypass the funding gate" flag (the alternative) would re-introduce exactly the
  dual-authority fuzziness §9 removed. So: funded activations flow through the
  plugin; the promised-unfunded escape-hatch stays controller-side and labeled.
  *(Flag to veto — this is the CASCADE equivalent of the advisory/authoritative
  fork.)*

### 2. Elastic grow — `growRun` (run_controller.go:1611)
A Running malleable run grows by `binder.Materialize`ing `delta` more GPUs.
Cutover: **emit `delta/gpusPerPod` more intent pods**; the plugin binds + mints
them; width updates from the leases (no adoption needed — the run is already
Running). Requires a plugin change: the grow delta is a **separate admission
unit**, not part of the base gang, so it needs a **cohort key**. Add a
`rq.davidlangworthy.io/cohort` annotation; the plugin gangs by
`(run, cohort)` with per-cohort `expected-width`. Base cohort `0` (initial
width), grow cohorts `1, 2, …` (each `delta`). Shrink is unchanged (it only
closes leases). This cohort key is also what a clean multi-role gang wants
later (ROLES).

### 3. Node-failure swap — `createSwapLease` / `updatePodsAfterSwap` (run_controller.go:1072, 1978)
On node failure the controller closes the failed leases and mints a `Swap` lease
onto a held spare, preserving the funding **provenance** — and note
`createSwapLease` keeps the **spare's** payer (owner/budget/envelope), not the
failed lease's, since the spare was the held, already-funded capacity
(`TestSwapLeaseKeepsFundingProvenance`). Cutover: close the failed + overlapping
+ spare leases (**stays** — reclaim/eviction is controller-owned), then **re-emit
the affected group's pod** as a swap cohort; the plugin re-binds onto the spare
and mints the `Swap` lease. This is the most intricate of the three — three real
wrinkles the design must handle:
1. **Provenance carried on the pod, not re-derived.** The swap pod is stamped
   with the spare's payer (owner/budget/envelope) via annotations; the plugin's
   PreBind mints with THAT payer instead of running cover, so sponsor-paid
   capacity keeps counting against the lender's caps.
2. **Hard node targeting.** A swap must land on the *specific* spare node (the
   held capacity), not "any fitting node" — so a swap pod needs a REQUIRED
   nodeAffinity/nodeSelector (buildPod's normal soft advisory affinity is not
   enough). Filter must enforce it (or rely on required nodeAffinity + the
   default plugins).
3. **Permit does not re-fund a swap.** The swap re-places already-funded work
   onto capacity the reclaim just freed/held, so its Permit ALLOWS without the
   funding gate (like the opportunistic-activation case) — the pod carries a
   swap marker so the plugin skips cover and mints from the stamped provenance.

Because the swap must place on a specific node with carried provenance and no
re-funding, it is closest in shape to the opportunistic escape hatch (§1) and
should be implemented and live-proven (swap-smoke.sh) as its own careful
increment, after CASCADE-2.

## Sequencing
1. **CASCADE-1** reservation activation (funded → emit; opportunistic → keep,
   documented; once-per-eviction guard). No plugin change. Migrate the
   reservation-activation tests.
2. **CASCADE-2** elastic grow via cohort intent pods + the plugin's cohort
   gang-key. Migrate elastic tests.
3. **CASCADE-3** node-failure swap via provenance intent pods + the plugin's
   provenance-preserving PreBind. Migrate swap tests.
4. **CASCADE-4** prove the whole cascade on a live cluster (extend
   `fullstack-smoke.sh`: a run that grows, a node that fails and swaps to a
   spare, a follower that admits after completion) — no hand-injected state.

Each lands as its own increment with pure-engine + envtest coverage and the
golden oracle regenerated; the parity rail stays the frozen
`legacy/nodename-binder` worktree.
