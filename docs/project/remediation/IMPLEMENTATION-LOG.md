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

> **Sequencing note (after R2 part 1):** I proceeded to **R5/R6** rather than
> immediately doing R2 part 2 (adopt-at-width). Rationale: part 1 already fixes the
> actual wedge *mechanism* (a lost member re-assembles and recovers on its own), so
> part 2's marginal value is honest-status + recovering *deleted* pod objects — and
> its re-emit is a no-op in the common case (part 1 recovers the still-existing
> pods). It also needs golden regen + a Degraded-status-clearing path. R5/R6 is a
> live, exploitable cross-tenant billing bypass (P1), so it is the better next unit
> of value. R2 parts 2 & 3 remain tracked follow-ups.


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
