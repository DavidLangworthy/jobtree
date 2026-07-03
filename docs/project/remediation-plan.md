# Remediation plan: mocked and broken implementation

*Derived from the July 2026 design review (`design-review-2026-07.md`, reviewed at `16fc4f1`).
This page is the tracker for that work: every mocked or broken area identified in the review is a
numbered item below with concrete steps and a done-when criterion. Check items off here, or —
better — convert each to a GitHub issue titled with its ID (e.g. `R1: binder segment split`) and
link it from this page, following the convention in `first-class-readiness.md`.*

**Suggested sequencing:** Workstreams A and B are ordinary bug fixes — do them now, in any order;
R1/R2 first. Workstream C's decisions were made on 2026-07-02 and recorded in
`quota-semantics.md`; R14/R15 are now implementation work that should land before or with
Workstream D, because the port bakes the semantics in. Workstream D is the Kubernetes port
(Phase 3 of the testing & simulation plan) and should not begin until A is done — porting
known-broken code just moves the bugs. Workstream E is independent housekeeping.

---

## Workstream A — broken: engine correctness

### R1 — Binder splits funding segments across node boundaries *(critical)*

- [x] `pkg/binder/binder.go` — `Materialize`, `buildPod`

**Problem.** When a cover segment boundary does not align with a node allocation chunk, the pod
built for the slice takes `slots[0]`'s node and claims all the slice's GPUs on it — producing
pods with more GPUs than the node has, on the wrong node, and (with reversed segment order)
duplicate pod names. Confirmed by execution.

**Steps.**

1. Rewrite the assignment loop as a two-cursor walk: advance over node chunks and cover segments
   simultaneously, emitting one pod/lease slice per *(chunk ∩ segment)* intersection so no slice
   ever spans two nodes.
2. Delete the false "all slots belong to the same node allocation chunk by construction" comment
   and the `slots[0].node` assumption in `buildPod`.
3. Add the placement-validity property test (invariant 1 of the testing plan): for random pack
   plans and cover plans, per-node pod GPU totals never exceed the node's allocated GPUs.
4. Re-run the review's reproduction (1 group of 4 GPUs over two 2-GPU nodes, segments 3+1 and
   1+3) as a regression test.

**Done when.** The property test passes under `go test -fuzz` for a sustained run, and the
regression case produces four correctly-attributed pod slices.

### R2 — Lease names collide *(critical)*

- [x] `pkg/binder/binder.go` — `buildLease`

**Problem.** Names are `run-gNN-<envelopeName>-<UnixNano>`; `Now` is fixed per materialization
and envelope names are only unique within a budget, so two segments can yield identical lease
names. Confirmed by execution. A real API server would reject the second create.

**Steps.**

1. Include the budget name and a per-materialization monotonic index in the lease name; drop the
   reliance on `UnixNano` for uniqueness.
2. Do the same review for pod names from `buildPod` (they collide today via R1's path).
3. Add the name-uniqueness invariant (invariant 4) to the binder property tests.

**Done when.** No name collision is producible by the fuzzer across random plans.

### R3 — Binder panics instead of erroring on exhausted segments

- [x] `pkg/binder/binder.go` — `Materialize` (`segments[0]` indexing)

**Problem.** If a cover plan covers the first group exactly but more groups remain, the next
group's loop indexes into an empty slice: panic rather than error.

**Steps.**

1. Guard the top of the per-group loop: empty `segments` with work remaining returns the existing
   "cover plan exhausted" error.
2. Add unit tests for under- and over-provisioned cover plans against multi-group pack plans.

**Done when.** All mismatched cover/pack combinations return typed errors; none panic.

### R4 — Group indices sorted as strings break at 10

- [x] `pkg/resolver/resolver.go` — `shrinkMalleable` sort

**Problem.** Descending *string* sort on group index cuts group "9" before "10", contradicting
the documented highest-index-first shrink order once a run has ≥10 groups.

**Steps.**

1. Parse indices to integers for ordering (the label parse already exists elsewhere —
   `collectElasticGroups` does it correctly); fall back deterministically for non-numeric labels.
2. Regression test with 12 groups asserting cut order 11, 10, 9…

**Done when.** Shrink order is numeric and covered by test.

### R5 — Three disagreeing `namespacedKey` implementations

- [x] `controllers/run_controller.go`, `pkg/resolver/resolver.go`, `cmd/kubectl-runs/cmd/state.go`

**Problem.** Empty namespace yields `"/name"`, `"name"`, or `"default/name"` depending on the
package. An empty-namespace `RunRef` makes the resolver's run lookup miss, silently exempting
those leases from preemption.

**Steps.**

1. Create one shared helper (e.g. `pkg/keys`) with a single defaulting rule; use it everywhere.
2. Add a resolver test with an empty-namespace `RunRef` proving the lease is now considered.

**Done when.** One implementation, all callers migrated, exemption bug covered by test.

### R6 — Resolver metrics count actions that never happen

- [x] `pkg/resolver/resolver.go` (`IncResolverAction` during planning), `controllers/run_controller.go` (`applyResolution`)

**Problem.** Spare-drop and shrink metrics increment while *planning*; if the subsequent lottery
errors, the whole result is discarded but the metrics remain.

**Steps.**

1. Remove metric increments from `Resolve`; emit them from `applyResolution` per applied action.
2. Test: failed lottery leaves resolver-action counters unchanged.

**Done when.** Metrics reflect applied actions only.

### R7 — Resolver preempts to fix budget shortfalls

- [x] `controllers/run_controller.go` — `activateReservation` deficit fallback

**Problem.** When activation fails on *cover* (budget) rather than capacity, `computeDeficit`
can return 0 and the code forces `deficit = totalNeeded`, running the lottery — preempting other
tenants to free physical capacity that was never the problem. Preemption cannot create budget
headroom.

**Steps.**

1. Distinguish the two failure classes at the call site: invoke the resolver only for a positive
   *capacity* deficit; for pure budget shortfalls, re-forecast and reschedule the reservation
   instead.
2. Scenario test: reservation blocked only by a future budget window must not trigger any
   preemption at activation.

**Done when.** No resolver actions occur in budget-only scenarios; capacity scenarios still clear.

### R8 — One bad reservation blocks all; activation order nondeterministic

- [x] `controllers/run_controller.go` — `ActivateReservations`

**Steps.**

1. Iterate reservations in sorted key order.
2. Collect per-reservation errors (mark the reservation's status) and continue; return an
   aggregate error at the end.
3. Test: a reservation referencing a deleted run does not prevent a later reservation from
   activating.

**Done when.** Activation is deterministic and fault-isolated.

### R9 — Reservation double-bind: no phase guard, no cleanup on direct bind *(critical once R21 lands)*

- [x] `controllers/run_controller.go` — `Reconcile` bind path, `activateReservation`

**Problem.** The direct-bind path clears neither `Status.PendingReservation` nor the stored
Reservation, and `activateReservation` never checks the run's phase — a run that reserves, then
binds directly, double-materializes on activation. Unreachable today only because nothing calls
`ActivateReservations` (see R21); wiring that without this fix creates a double-spend.

**Steps.**

1. In the direct-bind success path: delete any pending Reservation for the run and clear
   `PendingReservation`/`EarliestStart`.
2. In `activateReservation`: skip (and mark Released/Superseded) if the run is already Running
   or Completed.
3. Add the "no Pending reservation exists for a Running run" invariant (invariant 8) to the
   invariant library; scenario test for reserve → capacity-frees → direct-bind → activation tick.

**Done when.** The scenario runs clean with the invariant enforced at every step.

### R10 — Validation gaps in `api/v1`

- [ ] `api/v1/budget_types.go`, `api/v1/run_types.go`

**Steps.**

1. Reject duplicate envelope names within a Budget (today they silently collapse in
   `BuildBudgetState`'s map; a same-named envelope in another budget of the same owner
   double-counts leases — decide and document cross-budget scoping as part of the fix).
2. Validate `AggregateCap.Envelopes` references against declared envelope names.
3. Call `Run.Default()` at every ingestion point (CLI submit, future webhook) instead of
   re-implementing defaults in consumers; delete the duplicated defaulting logic.
4. Raise `api/v1` test coverage (15% at review) with an accept/reject manifest table — this
   corpus becomes the CRD schema conformance suite in the testing plan's Tier 2.

**Done when.** Invalid manifests above are rejected with actionable messages; coverage of the
validation paths is meaningfully complete.

### R26 — HandleNodeFailure breaks on multi-lease nodes *(critical, pre-existing)*

- [ ] `controllers/run_controller.go` — `HandleNodeFailure`, `findSpareLease`, `createSwapLease`, `updatePodsAfterSwap`

**Problem.** The swap path assumes one lease/pod per (group, node). Any co-funded run violates
that (funding segments split leases on one node). Confirmed by execution on main AND the PR
stack: a 2-GPU co-funded group with 2 spare GPUs available ends up Failed with 1 GPU — the
first lease consumes the spare, the overlap loop discards the second spare lease as
ReclaimedBySpare, the second failed lease finds no spare and marks the run Failed, overwriting
the successful-swap status.

**Steps.**

1. Rework the swap to group level: collect ALL active leases on the failed node per
   (run, group), collect ALL spare leases for the group, and swap capacity-for-capacity.
2. Regression test: co-funded 2-GPU group (two 1-GPU leases on one node) + 2 spare GPUs →
   run stays Running at full width.
3. Property test over random lease splits.

**Done when.** Node failure with sufficient spares never reduces width or fails the run,
regardless of how funding segments split leases.

### R27 — Resolver double-counts dropped spares in shrink accounting

- [ ] `pkg/resolver/resolver.go` — spare-drop phase vs `shrinkMalleable`

**Problem.** A spare freed in phase 1 (DropSpare) still counts inside its group's `grp.GPUs`
when phase 2 shrinks that group, so the deficit is decremented twice for the same GPUs:
Resolve reports a cleared deficit while actually freeing fewer.

**Steps.**

1. Exclude marked leases from group totals (or subtract dropped-spare GPUs from
   `grp.GPUs`/`st.Remaining` when marking).
2. Test: malleable run, spare in the shrunk group, deficit sized to expose the gap — assert
   real freed GPUs ≥ deficit.

**Done when.** Sum of action GPUs with no double-count ≥ cleared deficit in all resolver tests.

---

## Workstream B — broken: CLI

### R11 — Every CLI error is silent *(critical, trivial fix)*

- [ ] `cmd/kubectl-runs/main.go`

**Problem.** `main` discards the error (`os.Exit(1)` without printing) and the root command sets
`SilenceErrors`. Confirmed: all failures exit 1 with zero output.

**Steps.**

1. Print the error to stderr in `main` before exiting.
2. Golden test asserting stderr content for a missing-run invocation.

**Done when.** Every failure mode prints a one-line actionable error.

### R12 — Documented invocations do not parse

- [ ] `cmd/kubectl-runs/internal/cobra/command.go`, `docs/cli/kubectl-runs.md`

**Problem.** The stdlib-`flag`-based clone stops parsing at the first positional argument, so the
docs' own example (`watch train-128 --watch-count 3`) fails. Confirmed by execution.

**Steps.**

1. Preferred: fold into R20 (adopt real cobra, which interleaves flags and positionals).
2. If R20 is deferred: teach `parseFlags` to interleave, or fix every doc example to place flags
   first — and add a CLI golden test that executes each documented example verbatim.

**Done when.** Every command line shown in the docs runs successfully in CI.

### R13 — State snapshot: mutation-by-read, no locking, `.yaml` that is JSON

- [ ] `cmd/kubectl-runs/cmd/state.go`, `helpers.go`, read-path commands

**Steps.**

1. Make `plan`/`explain`/`budgets usage` read-only (no reconcile-and-save side effects); keep
   mutation in `submit`/`shrink`/`sponsors add`/`watch` and say so in help text.
2. Iterate runs in sorted key order in `reconcileAll` so competing admissions are deterministic.
3. Add advisory file locking (or write-to-temp + atomic rename with a lock file) around
   load-modify-save.
4. Either accept YAML input or change the default state filename to `cluster-state.json`.

**Done when.** Two concurrent CLI invocations cannot lose writes; read commands leave the file
byte-identical; the format/extension mismatch is gone.

---

## Workstream C — quota semantics (decided 2026-07-02; see `quota-semantics.md`)

### R14 — GPU-hour caps are dead at admission

- [ ] `pkg/cover/cover.go`, `controllers/run_controller.go` (all `cover.Request` sites), `pkg/budget`, `api/v1`

**Problem.** `ExpectedDuration` is only ever set in tests, so every `MaxGPUHours` check
(envelope, aggregate, lending) is skipped in the real path. Runs also have no
duration/completion concept, so the integral-budget half of the design exists only in status.

**Decision (2026-07-02).** Metered evaluation with demote-not-kill: admission checks
`width × period` against the remaining integral; exhaustion re-evaluates the run as
opportunistic (derived, never stored — see Decision 3 in `quota-semantics.md`); opportunistic
work is reclaimed only when funded work needs the capacity, by lottery, and recovers
automatically when quota returns. No envelope overdraft — unfunded hours are metered separately.

**Steps.**

1. Add the cluster accounting `period` configuration and plumb `width × period` hours into every
   cover request (this finally exercises the GPU-hour math at admission).
2. Implement the deterministic ranking/evaluation function from `quota-semantics.md` (shared
   with R15): per-envelope greedy fill over concurrency + integral, stable tiebreaks.
3. Add the unfunded bucket to budget accounting and the metrics class label; enforce the
   no-overdraft invariant.
4. Resolver: unfunded-first reclaim phase; funded admissions reclaim opportunistic capacity
   before falling back to a reservation.
5. Scenario tests: zero-hour envelope cannot fund an admission; exhaustion demotes without
   killing; a reopened budget window re-funds; overdraft is unrepresentable.

**Done when.** The quota-semantics invariants hold in the Tier 1 simulator, and status surfaces
the derived classes without the control path ever reading them back.

*Known edge (accepted 2026-07-02):* activation for a run short on both capacity and budget still
preempts for the capacity half and then reschedules (`activateReservation`); funded victims can
die for a run that does not start. Accepted until this rework dissolves the path.

### R15 — Family sharing vs. lending semantics are inconsistent

- [ ] `pkg/cover/cover.go` (phases), `pkg/binder/binder.go` (role assignment), `pkg/budget/usage.go`, `controllers/run_controller.go` (`summarizeRunFunding`)

**Problem.** Family members (siblings/parents/cousins) consume each other's envelopes with no
lending gate, and those leases are `Role: Active` — bypassing lending sub-caps in budget
accounting — yet Run status reports the same GPUs as "borrowed" because the payer differs. The
same GPUs are borrowed in one ledger and not the other.

**Decision (2026-07-02).** Proximity-ordered family sharing of excess with owner recall
expressed as claim ranking; lending policy governs sponsors/strangers only; four derived classes
(owned / shared / borrowed / unfunded), never stored on a CRD. Full semantics in
`quota-semantics.md`.

**Steps.**

1. Untangle lease *role* (Active/Spare — a fact on the lease) from *funding class* (derived);
   remove `Role: Borrowed` semantics from accounting.
2. Align cover phases with the ranking function's proximity order; drop the lending gate for
   family; add the `sharing: none` envelope opt-out; apply lending caps to the sponsor class
   only.
3. Implement recall as ranking — no demotion writes; resolver and admission consume the
   derivation directly.
4. Point all ledgers at the one derivation: budget usage buckets, `summarizeRunFunding`
   four-way split, metrics class label.
5. Tests: conservation across the four classes; owner-recall scenario (owner's admission
   displaces a sibling to opportunistic without eviction when capacity exists elsewhere);
   lending caps unaffected by family usage.

**Done when.** One derivation function feeds lease accounting, run status, budget status, and
resolver candidate selection; the ACL-bypass finding is replaced by passing recall tests.

---

## Workstream D — mocked: the Kubernetes layer (the port)

*These items are one coordinated effort — Phase 3 of `testing-and-simulation.md`. Entry gates:
Workstream A complete; C decided (done — `quota-semantics.md`); the TLA+/P specs (reservation
lifecycle, budget conservation, and the quota evaluation semantics) check clean.*

### R16 — `cmd/manager` is a stub

- [ ] `cmd/manager/main.go`

**Steps.**

1. Adopt controller-runtime; build a real manager wiring the Run and Budget reconcilers, leader
   election, and health probes.
2. Serve the existing `pkg/metrics.Handler()` on the metrics port (it is currently never served
   by anything).
3. Drive reservation activation from a requeue/timer source rather than ad-hoc calls (see R21).

**Done when.** The manager runs against envtest and a kind cluster, reconciling real CRs
end-to-end.

### R17 — No CRD manifests exist

- [ ] new: `config/crd/`

**Steps.**

1. Generate CRDs from the API types with controller-gen (the kubebuilder markers are already in
   place); commit under `config/crd/` and ship them in the Helm chart.
2. Add a CI step asserting generated CRDs are up to date (`git diff --exit-code` after
   regeneration).

**Done when.** `kubectl apply -f config/crd/` succeeds and M0's definition-of-done is actually
met.

### R18 — Webhook validators/defaulters are never wired

- [ ] `api/v1/*_types.go`, manager wiring

**Steps.**

1. Register the existing `ValidateCreate/Update/Delete` and `Default` methods as admission
   webhooks in the manager (they were written for this and are currently dead code paths).
2. envtest coverage: invalid manifests rejected by the API server, defaults applied on create
   (closes the R10.3 duplication permanently).

**Done when.** The api/v1 accept/reject corpus passes through a real API server with webhooks on.

### R19 — Hand-mimicked meta types

- [ ] `api/v1/meta.go`, `api/v1/runtime.go`

**Steps.**

1. Replace `TypeMeta`/`ObjectMeta`/`Time`/`Duration`/`RuntimeObject` mimics with
   `k8s.io/apimachinery` equivalents; regenerate deepcopy with controller-gen instead of the
   ~1,000 lines of hand-written `DeepCopy*`.
2. Mechanical sweep of the engine packages for type changes (the engine logic itself should not
   change — a good test of the layering).

**Done when.** `api/v1` contains no hand-rolled Kubernetes machinery.

### R20 — Hand-rolled cobra clone

- [ ] `cmd/kubectl-runs/internal/cobra/`

**Steps.**

1. Replace with real `spf13/cobra`; delete the clone. This fixes flag interleaving (R12) and the
   `SilenceErrors` foot-gun (R11) idiomatically, and restores `--help` behavior.
2. Keep the CLI golden tests; they should pass unchanged except for help/error text.

**Done when.** The clone is deleted and documented examples pass verbatim.

### R21 — `ActivateReservations` and `HandleNodeFailure` are unreachable

- [ ] `controllers/run_controller.go`, simulator (Phase 2), manager (R16)

**Problem.** Their only callers are tests: reservations can never activate and spare swaps can
never trigger in the shipped system.

**Steps.**

1. Near-term (pre-port): the Tier 1 scenario simulator drives both on every tick — making them
   reachable, testable, and regression-guarded (this is Phase 2 of the testing plan).
2. Port: the manager drives activation via requeue-at-`EarliestStart` and node-failure handling
   via node watch events.
3. Sequence R9 (double-bind guard) and R8 (error isolation) *before* this lands.

**Done when.** Both paths execute in the simulator's default loop and, post-port, in the manager;
no feature exists without a driver.

---

## Workstream E — packaging, CI, docs honesty

### R22 — Helm RBAC is wildcard cluster-admin

- [ ] `deploy/helm/gpu-fleet/templates/rbac.yaml`

**Steps.** Scope the ClusterRole to the jobtree CRDs plus read on nodes and CRUD on pods/events;
add `helm lint`/template assertions that no wildcard rules ship.

**Done when.** The chart grants only what the manager uses.

### R23 — Krew manifest is unbuildable

- [ ] `plugins/krew/runs.yaml`, `.github/workflows/release.yaml`

**Steps.** Point the manifest at the artifact the release workflow actually produces (an archive
of `dist/kubectl-runs`), add darwin/arm64 + linux/arm64 platforms, and validate with
`kubectl krew install --manifest` against a release candidate.

**Done when.** A tagged release yields a krew-installable plugin on the named platforms.

### R24 — CI toolchain and coverage

- [ ] `.github/workflows/ci.yaml`

**Steps.** Bump from Go 1.22 (EOL; its test binaries abort on current macOS) to a supported
version; add `-race` to the test step; add `helm lint` (M9 claims CI validates the chart; it does
not); add `go vet`.

**Done when.** CI runs vet + race tests + helm lint on a supported Go.

### R25 — Roadmap checkboxes overstate what exists

- [ ] `README.md`, `docs/roadmap/milestones.md`

**Steps.** Re-badge M0 (no CRDs/webhooks exist), M6 (no e2e failure injection, no `pkg/policy`),
and M9 (Helm not validated in CI) as partially complete with a one-line note of the gap, linking
the relevant R-items here — or complete the missing artifacts via Workstream D and close them
honestly.

**Done when.** Every checked milestone's definition-of-done is verifiable in the repository.
