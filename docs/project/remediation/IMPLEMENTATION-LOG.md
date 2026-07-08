# Remediation implementation log

Running record of implementation decisions made while executing the R-specs, so
they can be reviewed later. (David asked not to be interrupted for decisions
during the work; each judgment call is recorded here with its rationale.)

## Sequencing

**Chosen order: R1 → R2 → R5/R6 → R3 → R4 → P2–P5 (roughly by priority).**

The README compose note lists R5/R6 first. I moved the two P0 correctness bugs
(R1 phantom-lease leak, R2 gang wedge) ahead of R5/R6 because:

- They are the headline P0 defects (one live-reproduced), and delivering them
  first fixes the most-serious correctness problems soonest.
- They are pure-Go changes to the plugin/controller, fully unit-testable in this
  repo with the fake client — no live cluster needed. R5/R6 is a
  ValidatingAdmissionPolicy whose enforcement (userInfo gating) can only be
  truly verified on a kind cluster, so it is a heavier, less-immediately-testable
  first step.
- R1/R2 do **not** depend on R5/R6. Only R3 hard-depends on R5/R6 (its `Promise`
  marker must be forgery-proof), and R5/R6 still lands before R3.
- R1 is done before R2 (swapped from the note's "R2 → R1") because R1 is the
  smaller, self-contained, live-reproduced change; it introduces the shared
  `PostBind` + stale-gang sweep that R2 then builds on.

## Decisions (chronological)

### R5 + R6 — provenance trust anchor + mandatory scheduler (merged #TBD)
- **VAP, not a webhook.** The mandatory-scheduler + controller-only-fields rules
  ship as one `ValidatingAdmissionPolicy` (CEL, GA in the cluster's 1.36) rather
  than a webhook server — less code, no serving cert, no availability tail.
- **Two CEL rules, one binding.** (R6) a pod requesting `nvidia.com/gpu` must set
  `schedulerName: jobtree`; (R5) any pod setting a jobtree-owned field
  (`schedulerName: jobtree`, an `rq.davidlangworthy.io/*` annotation, or the role
  label) must be created by the controller SA (`request.userInfo.username`). The
  binding exempts the release namespace + operator-listed infra namespaces.
- **Default OFF (`podPolicy.enabled: false`).** Mirrors `scheduler.enabled: false`
  — a bare install must not suddenly gate every GPU pod in the cluster. Documented
  that *enabling it is what closes the opt-in-budget hole*. This is the one place I
  chose availability-of-the-default over closing-the-hole-by-default; flip the
  value (and it's in the operator guide / R18 break-glass) when ready.
- **failurePolicy `Fail`** (per the R6 recommendation), release namespace always
  exempt so the control plane comes up even under Fail.
- **Plugin defense-in-depth (the *tested* security win).** PreBind now refuses a
  swap whose carried provenance matches no real Spare lease the run held
  (`spareLeaseProvenanceValid`). This closes the sharpest exploit (mint against an
  arbitrary victim envelope) at the plugin level *even if the VAP is not enabled*,
  and it is unit-testable here; the VAP's CEL enforcement itself needs a kind
  cluster to verify (templating is checked; enforcement is a Sonnet live-verify
  follow-up).
- **OwnerReference on emitted pods** (`buildPod`): the Run is now the pod's
  controller owner — the provenance anchor R5 wants and the GC edge R12 needs
  (done once, here). Requires the Run UID (real path always has it; pure-engine
  Runs without a UID get none, backward compatible).
- **Tests:** `spareLeaseProvenanceValid` (accepts a matching spare, refuses a
  forged victim envelope, rejects an Active lease); `buildPod` owner reference (+
  no-UID fallback). Green under `-race`; full suite + antifake + helm template OK.

### R3 — refined scope (NOT yet implemented; recommendation logged)
On starting R3 I found the spec under-framed it. The "opportunistic / promised-
but-unfunded start" is **not** an incidental behavior to drop — it is a
**documented quota semantic** (`quota-semantics.md`, the source of truth) with
pure-engine tests that assert it: a shortfall run starts **Running, Unfunded**,
and is **re-funded when quota returns**
(`reservation_semantics_test.go:TestActivateReservationBudgetOnlyShortfallAdmitsOpportunistically`,
`quota_semantics_test.go` window-close/coast cases). So:
- **"Drop it" is OFF the table** — it would delete a documented semantic. My
  spec's earlier "drop it" fallback is withdrawn now that this is clear.
- **The fix is the `Promise` path** (spec's primary rec): the controller stops
  Materializing the opportunistic lease and instead emits intent pods marked
  `lease-reason=Promise` + payer provenance; the **plugin** mints the (naturally
  Unfunded) lease from that provenance, skipping the funding gate like a swap.
  The `Promise` marker is already forgery-protected — the R5/R6 VAP gates every
  `rq.davidlangworthy.io/*` annotation (incl. `lease-reason`) to the controller
  SA. Add a plugin owner cross-check (provenance owner == run owner) as
  defense-in-depth for when the VAP is off.
- **Why not rushed here:** this is a controller cutover of the opportunistic mint
  that must **migrate the pure-engine quota-semantics tests** to the intent-pod +
  simulated-plugin-mint pattern (as the PLUGIN-2 cutover did the others via
  `seedRunning`) and regenerate the affected golden scenarios. It touches the
  quota source-of-truth, so it deserves a careful, dedicated pass — not the tail
  of a long batch under a token budget. Left as the next unit of work with this
  design pinned. Nothing about it is blocked; it is scoped, not stuck.

### R3 — Promise path IMPLEMENTED (2026-07-08)
Executed the pinned design. The controller's opportunistic mint is gone; the
budget-only activation now emits a promised intent gang and the plugin is the
sole committer. Judgment calls made without interrupting, per standing
instruction:

1. **Promise branch keeps the run Pending; adoption flips Running.** The
   opportunistic branch (`activateReservation`) emits Promise pods and releases
   the reservation, but does **not** set `Phase=Running` and does **not** clear
   `CheckpointDeadline` — exact parity with the *funded* activation path, which
   also lets the plugin's leases land and the adoption block flip Running. Setting
   Running here would resurrect the old "Running with zero bindable pods" lie.
2. **New Reconcile guard `runHasPromisePods` short-circuits admission.** A
   promised run's cover is *expected* to keep failing until quota returns (that is
   why the promise fired), so re-entering `planPlacement`/`planReservation` would
   plan a spurious **second** reservation on every tick. The guard parks it Pending
   with a "promised start: scheduling N GPUs" message instead. It sits **after** the
   open-lease adoption block, so once the plugin mints the leases the run adopts to
   Running normally and never reaches the guard again.
3. **Per-pod leases replace the old per-group `Materialize` lease** — a pure
   mint-site move. The legacy Roles-less path emits one 1-GPU pod per requested GPU
   (`intentPodShape`), so a 4-GPU run now yields four per-pod Promise leases where
   the old `binder.Materialize` minted one 4-wide group lease. `funding.Evaluate`
   classes by envelope quota, not lease count, so the classification is identical
   (all Unfunded until quota returns); the golden oracle is unchanged.
4. **`promiseProvenanceValid` charge validation (defense-in-depth for VAP-off).**
   The plugin refuses to mint a Promise lease unless the **charged** envelope
   belongs to the run's own owner. First cut of this check compared only
   `seg.Owner == run.Spec.Owner` — an adversarial review (workflow, 2026-07-08)
   caught that this pins the wrong field: `funding.Evaluate` resolves every charge
   by `EnvelopeKey{PaidByBudget, PaidByEnvelope}` and takes the owner from the real
   Budget object, never from the lease's cosmetic `Spec.Owner`. So a pod that owns
   its own run could set `payer-owner` to itself (passing the naive check) while
   pointing `payer-budget/envelope` at a **victim's** budget, minting a gate-free
   cross-tenant charge. Fixed: resolve the named Budget, require `b.Spec.Owner ==
   run.Spec.Owner` **and** that it carries the named envelope — the exact invariant
   `opportunisticCoverPlan` upholds (it only attributes a promise to an envelope the
   run's owner owns). This matches the rigor of the swap's `spareLeaseProvenanceValid`
   (owner AND budget AND envelope); both flow through one PreBind carried-provenance
   branch that picks the check by marker (`Swap` vs `Promise`). With the R5/R6 VAP
   on, the payer annotations are already controller-only; this holds even with it off.
5. **Deleted the controller's orphaned `leaseSeqBase` copy.** It was dead after the
   mint-site move; the canonical copy stays in `pkg/admission/admission.go`.
6. **Test-migration scope was far smaller than feared.** Only one pure-engine test
   drives the controller's opportunistic mint
   (`TestActivateReservationBudgetOnlyShortfallAdmitsOpportunistically`); migrated
   it to the intent-pod + simulated-plugin-mint pattern with a new
   `seedPromiseLeases` helper (mirrors `seedSwapLease`). It now asserts the full
   promise lifecycle: controller mints nothing → 4 Promise pods carrying payer
   provenance → run stays Pending → re-reconcile does **not** re-reserve (guards the
   new guard) → plugin mints → adoption flips Running at 4 Unfunded → hog completes
   → **re-funded to 4 Owned with no new mint** (R14). Added `TestPromiseProvenanceValid`
   (plugin). **No golden scenario exercises opportunistic activation**, so the oracle
   needed no regeneration — verified it passes unchanged. Full suite green under
   `-race`; antifake + helm template OK.

This makes index.md's "sole committer" claim TRUE — R24 should drop its "false
until R3 lands" caveat when it does the doc-honesty pass.

> **Sequencing note (after R2 part 1):** I proceeded to **R5/R6** rather than
> immediately doing R2 part 2 (adopt-at-width). Rationale: part 1 already fixes the
> actual wedge *mechanism* (a lost member re-assembles and recovers on its own), so
> part 2's marginal value is honest-status + recovering *deleted* pod objects — and
> its re-emit is a no-op in the common case (part 1 recovers the still-existing
> pods). It also needs golden regen + a Degraded-status-clearing path. R5/R6 is a
> live, exploitable cross-tenant billing bypass (P1), so it is the better next unit
> of value. R2 parts 2 & 3 remain tracked follow-ups.


### Funding-model review (2026-07-08) — David's design challenge

David asked whether funding-class-on-the-GPU is the right design, whether the
ledger's allocs/frees can be trusted, and pointed out quota and capacity are
independently variable, reconciling only at scheduling instants. Ran a four-way
evidence sweep (funding engine, ledger lifecycle, quota↔capacity coupling, doc
claims); full analysis pinned in `../funding-model-review.md`. Outcomes:

- **Class is derived, never stored — confirmed clean.** Exhaustive grep: status
  class fields are write-only cache; no control path reads them back. The
  design's Decision 3 holds in the code.
- **Frozen-payer consequence documented** (re-funding is arithmetic within the
  minted envelope only; no re-point path exists). Accepted as a feature
  (predictable attribution); now written down instead of implicit.
- **New bug → [R25](R25-spare-node-lease-leak.md):** deleting a node hosting
  only a held spare leaks an open lease forever (`HandleNodeFailure` skips
  spares before node-match; caller swallows the error). Lands with R21/R22.
- **New structural item → [R26](R26-ledger-auditor.md):** runtime ledger
  auditor (open lease ↔ live pod on live node; jobtree pod ↔ open lease;
  `Orphaned` closure reason; violation metrics). Decision made without asking,
  per standing instruction: destructive repair is limited to closing leases
  (budget-safe direction); pod-without-lease only alarms.
- **R20 gains `GangUnplaceable`:** Permit currently labels pure physical
  failures "not fundable" (pack/cover errors collapsed to one string).
- **R24 expanded:** index.md budget-as-gate framing + "sole committer" claim
  (false until R3), dead `Fail` enum in leases.md, role/class conflation,
  and an explicit three-plane / quota-may-over-or-under-commit statement.
- **R3 spec note added:** opportunistic lease bakes `Slice.Nodes` from the pack
  plan while the pod gets only soft affinity → ledger/placement divergence; the
  Promise path fixes it by minting from the actual bind node — verify that.

### Leftover test fix (before P0) — `make e2e-image` scheduler image
Fixed the pickup-notes "Monday item #1": `e2e-image` now builds+loads the
scheduler image too. Done by a Sonnet agent; merged as #45. Not a remediation
spec, just the outstanding item.

### R1 — phantom lease clearing + gang GC (merged #TBD)
- **Retirement point:** a pod's phantom `pending[i]` is retired in **PreBind,
  right after the real lease `Create` succeeds** (`notifyMinted`), not at claim
  and not at PostBind. Rationale: the double-count window opens the instant the
  real lease exists in the API (another gang's `decide` would then see real +
  phantom), so it must close there. Retiring at claim would be too early (a failed
  mint must keep the guard); at PostBind too late (bind can lag).
- **GC point:** the whole `gangCommit` is dropped in **PostBind, only when every
  pod is fully minted** (`fullyMinted`). PostBind fires only after a *successful
  bind*, so a gang with a bind-failed / still-unbound member is deliberately kept
  alive — that surviving state is exactly what R2's recovery will read. This is
  why GC is in PostBind and not folded into `notifyMinted`.
- **Sweep backstop:** a `sweep(now)` drops any gang idle past `gangTTL = 15m`
  (> the 2m Permit timeout so an actively-forming gang is never reaped), driven by
  a ticker (`sweepInterval = 5m`) started in `New` off the scheduler context. This
  reclaims abandoned commits (member never bound, unfundable gang nobody retried,
  deleted run) that PostBind never reaches. TTLs are consts for now; make them
  config if a deployment needs it.
- **Extension point:** `postBind` was not enabled in the scheduler profile;
  added it to both `config/scheduler/jobtree-config.yaml` and the helm ConfigMap.
- **Tests:** double-count-after-mint (the headline, mirrors the live repro),
  guard-held-pre-mint (overspend still prevented before mint), PostBind-GC, and
  TTL-sweep. All green under `-race`; full suite + antifake + helm template green.

### R2 — gang recovery: SPLIT into three increments
R2's spec has four pieces; I split it so each lands small, green, and testable
rather than as one large controller+plugin change.

**R2 part 1 (this PR — pieces 1 + 4, plugin-side):**
- **Piece 1 — Permit counts committed siblings.** The gate now passes when
  `waiting + committedCount(gang) >= expected`, where `committedCount` = pods that
  already claimed a payer (`g.claimed`). This de-wedges the *common* failure: a
  member whose PreBind/bind fails transiently re-enters Permit alone; its bound
  siblings are gone from the waiting set, so the old `waiting >= expected` gate
  could never re-form and the gang looped to timeout forever at N-1 width.
  `committed` is 0 until a gang funds, so the *first* funding decision is
  unchanged (still needs the whole active set waiting).
- **Piece 4 — ABA lease-name nonce.** `buildPod` stamps `run-nonce` (a 12-char
  prefix of the Run UID); PreBind folds it into the lease name. A delete+resubmit
  of a same-named Run (new UID) now mints a fresh OPEN lease instead of colliding
  with the prior incarnation's closed lease and being swallowed by
  `IsAlreadyExists`. Same-incarnation retries keep the same nonce → still
  idempotent. No UID (pure-engine tests) → legacy name, backward compatible.
- Tests: `committedCount` accounting; `buildPod` nonce stamp (+ empty-UID
  fallback). Green under `-race`; full suite green.

**R2 part 2 (next PR — piece 3, controller-side):** adopt-at-correct-width —
the controller currently flips a run to Running on *any* open lease > 0
(`run_controller.go:197`), so a partial gang reports healthy while charging
budget. Will compare open leases to expected active width, mark Degraded + re-emit
missing active pods when short. Deferred here because it needs golden regen and
controller-test updates — kept as its own increment.

**R2 part 3 / R2b (documented follow-up — piece 2):** full scheduler-restart
reconstruction (rebuild gang commits from open leases on startup and delta-fund
un-minted survivors). Rarer than the transient-failure wedge that part 1 already
fixes, and the most complex sub-part (needs cohort-labelled leases + delta
re-funding). Left as a precise design note in the R2 spec for a later pass;
part 1's in-memory committed-count does NOT survive a process restart.
