# Decisions needed from David

Opened by the unattended autopilot run of 2026-07-23
(`docs/project/autonomous-run-playbook.md`). Two kinds of entry live here, and they
want different things from the reader:

- **PARKED** — the run hit an owner decision and stopped rather than guessing. Nothing
  was implemented. These block the item named.
- **FLAG FOR THE MILESTONE REVIEW** — the run *did* implement something on the
  sole-committer / funding path. Nothing is blocked; these are the changes the
  end-of-milestone adversarial review should be pointed at first. The playbook forbids
  the autopilot from launching that review itself.

---

## PARKED — owner decisions, not implemented

| # | Item | The decision | Why it is not an implementation detail |
|---|---|---|---|
| ~~P1~~ | ~~**R7 pt2** — delete `Run.Spec.Owner` / tenancy authz~~ | **RESOLVED — APPROVED & UNPARKED by David (2026-07-24).** Implemented per `remediation/R7-tenancy-amendment.md`; see F7 below. | — |
| P2 | **R4 pt1b reader-swap** | The acceptable informer-**staleness bound** | The correctness core landed (#99); the reader swap is a perf change whose safety is defined by a number only the owner can set. |
| P3 | **R4 pt2b** — the settlement **store** | Whether to build it | A feature deferral, not correctness (see `correctness-closeout-plan.md` §Out of scope). |
| P4 | **ROLES track** (elastic / multi-role gangs) | Whether to schedule the XL | Out of scope for this milestone. |

---

## FLAG FOR THE MILESTONE REVIEW

| # | Change | Why it wants the review's attention |
|---|---|---|
| F1 | **R11 rewired all 25 `run.Status.Phase` writes** in `controllers/run_controller.go` through `setState(run, v1.RunState…, msg)`. | It is a wide, mechanical edit to the controller that sits beside the funding path. The phase VALUES are unchanged — the whole existing suite, the golden oracle and the 800-seed eviction fuzzer pass untouched, and `INV-PHASE-DERIVED` now fails any state where phase and conditions disagree. What deserves a second pair of eyes is the **mapping**: whether each site's chosen state (and therefore its reason) is the right one. A wrong reason is not a wrong decision, but it is a wrong explanation, and the researcher-facing value of R11 is exactly that explanation. |
| F2 | **Lease conditions are derived in `Bridge.apply`, not at the mint.** | The first draft stamped them in `admission.PodLeaseWithRole` and was inert (Status is a subresource; the plugin's `Create` drops it). The fix deliberately does NOT add a status write to the sole committer's hot path. Worth confirming that the one-pass lag between mint and `Active=True` is acceptable, and that nothing reads lease conditions as authority for the closure fact (`status.closed` remains the fact; conditions are the projection). |
| F3 | **R13 renamed the sole committer's minted object** (`Lease` → `GPULease`) and **R14 put lease immutability and closure monotonicity into apiserver CEL.** | Two things deserve the review's eyes. First, the rename crosses `pkg/funding`, the plugin's PreBind mint and the controller in one pass; it is mechanical and `make verify` + the 800-seed fuzzer are green, but the RBAC half fails *silently* (a stale `resources: ["leases"]` grant parses fine and grants nothing, so PreBind 403s and nothing is ever funded). A helm-assertion rail now catches that; the review should ask whether it catches every shape of it. Second, `status.closed` is now monotone **at the apiserver**: the sole closer's write path is fine, but any future code that reconstructs a lease status wholesale will now be rejected rather than silently reopening one. That is the intent — worth confirming nothing legitimate does it. |
| F4 | **R18's uninstall order is a claim about the funding path**, not just docs: delete Runs *while the controller manager is still up*, because the `rq.davidlangworthy.io/funding-closure` finalizer is what closes leases. | No code changed, so this is not a code review item — but the ordering is the difference between a closed ledger and an audit trail whose last statement is that work is still running and still charging. `make e2e-runbook` proves the happy path on kind. The review should ask about the unhappy one: a manager that is *itself* the faulting component. The runbook tells the operator to scale it down by hand and says what that costs; there is no automated path that closes leases without it. If you want one (a `--close-leases` mode, or the auditor of R26 doing it), that is a new decision. |
| F5 | **R20 wired an EventRecorder into the scheduler plugin** (the sole committer) and emits a fixed Event vocabulary at Permit/PostFilter/Unreserve/PreBind, plus per-gang caches (`runUIDs`, `lastForming`, `parkedAt`). | It is **observe-only** — no emission site changes a verdict, the recorder is nil-safe, and the hot path gains no synchronous API write on the decision path — so the risk is not correctness but two second-order things a review should confirm. First, the new maps live beside the `gangs` map on the funding hot path and are reaped by the same sweep; the review should confirm none can grow unbounded under gang churn (the intended bound: `runUIDs` keyed by run and reaped when no gang for that run remains, `lastForming`/`parkedAt` dropped with the gang / on `forget`). Second, `classifyRefusal` now decides unfundable-vs-unplaceable off `errors.As(&pack.PlanError)`; the review should confirm the planners' error types actually carry that distinction end to end (a mislabelled refusal is a wrong *explanation*, not a wrong decision, but it is the whole point of R20). The plugin runs inside the scheduler binary, which envtest does not stand up, so the emission is proven by unit tests + the `main_test.go` scheme-registration guard rather than a live assertion — the review may want a kind live-proof that an unfundable gang produces a `GangUnfundable` Event on the Run. |
| F7 | **R7 pt2 deleted `Run.Spec.Owner` and derives the funding owner from the run's NAMESPACE** (`funding.Evaluation.OwnerOf`), per `remediation/R7-tenancy-amendment.md` §4. This is a SECURITY + FUNDING-PATH change on the sole-committer path and MUST get the sole-committer adversarial review BEFORE it is merged (the run implemented it but, per the playbook, does not run that review itself). Scrutinize by name: **(1)** the `deriveOwners` fail-safes — a multi-owner namespace and a leaf owner spanning two namespaces both fail safe to *unbound* (empty owner → cover refuses fresh runs, pre-existing leases coast Unfunded); confirm no configuration lets a Run charge Owned across a namespace boundary (the S-1 hazard), and that the *interior-tier* exemption cannot be abused (a pool owner is exempt from injectivity). **(2)** the **empty-borrower guard** in `lendingAllows` — confirm an unbound/conflicted namespace can borrow from *nothing*, including `To:["*"]` sponsors, so a prior Borrowed lease demotes when its namespace becomes conflicted. **(3)** the plugin's `promiseProvenanceValid` now gates on **namespace equality** (`b.Namespace == run.Namespace`) instead of the two deleted owner-string agreements; confirm it still refuses a cross-namespace charge and that `seg.Owner` being cosmetic introduces no laundering path. **(4)** a SUB-QUESTION for the owner, deliberately NOT decided by the run: the amendment §7 specifies `PaidByNamespace` as a **required** field with a **loud rail** (a live lease with an empty payer-namespace surfaced as a defect), but the shipped **pt1** landed it as `PaidByBudgetNamespace` **optional/omitempty with a silent legacy-empty fallback** (Codex #1 back-compat). This PR stamps it at all three mint sites and adds the *conflict* surfacing on the Evaluation, but does NOT change pt1's optional-field/legacy-fallback semantics or add the empty-`PaidByNamespace` loud rail (doing so would reclassify legacy-empty leases — an owner-facing change to an already-ratified pt1 decision). The review should decide whether to reconcile pt1 to the amendment's required+loud-rail design in a follow-up. Also NOT in this PR: R26 alarms 3/4/5 (the auditor consuming `Evaluation.Conflicts()` / interior-owner Runs) — the engine surfaces the data (`Evaluation.Conflicts()`); wiring R26 is a separate small change. |
| F6 | **R26 added the `LedgerAuditor`, a NEW lease-closing actor on the sole-closer path.** It periodically closes leases `Orphaned` when the work behind them is gone (lease on a deleted node, or an Active lease of a live run whose pod is gone), after a grace window. It never deletes pods (alarm-only for a pod running without a lease). | This is the item on this milestone that most wants the review's eyes — a wrong auditor is a **reaper**, worse than the leak it chases, and the playbook forbids the autopilot from running the adversarial review that this class of change normally gets. Three things to scrutinize by name. **(1) Reaper safety of the no-pod rule:** it fires on an Active lease of a present, non-terminal run whose annotated pod is absent, sustained past grace. It defers absent runs (finalizer), terminal runs (SettleLeases), and legacy leases without a pod-name; it counts a Pending pod as live (recoverEvictedRanks re-provisions in place). The review should try to construct a healthy state it closes anyway — the grace window is the only thing separating "genuinely stuck rank" from "mid-recovery." **(2) The grace-vs-swap-window bound:** grace defaults to 2× the sweep interval and is clamped up; the claim is that this always exceeds the failed-lease→swap-pod-minted window. Confirm that holds at the configured interval. **(3) A separate observation worth a look, surfaced not hidden:** while building the envtest, a manually-closed lease was observed re-reading as `closed=false` once, under heavy concurrent RunReconciler activity against the shared suite manager. It was not reproduced deterministically and may be a test artifact (the manager deleting+re-planning, or a stale-snapshot apply that R14's CEL should reject), but because it touches the closer path the review should confirm a busy controller cannot transiently rewrite or reopen a lease a closer just closed. The auditor's own close is proven accepted by the fake-client test + R14's monotonicity envtests; this is about the *interaction* under load. |

---

## Answered here, deliberately, and recorded rather than asked

These looked like they might be owner calls and are not — each is already settled by a
decision on the record, so the run implemented it and logged the reasoning in
[`remediation/IMPLEMENTATION-LOG.md`](remediation/IMPLEMENTATION-LOG.md).

- **R15: delete the `notifier` rather than defaulting it off.** The audit's own
  remediation text offers both ("default `notifier` off — or delete it entirely (no
  source exists)"), so choosing between them is inside the sanctioned set. There is no
  `cmd/notifier`, no job that ever built the image, and the repo's standing rule is that
  a shipped-but-nonexistent feature is a fake to be removed, not hidden behind a flag.
- **R15: no Helm repository.** The audit says "publish (or fix) the helm repo index".
  Publishing to GitHub Pages would need Pages enabled and served, which an unattended
  run cannot verify — and an unserved index is the same 404 promise in a new place. The
  packaged chart is already a release asset, so the docs now install from that. If you
  want a real Helm repo later, that is a new (small) piece of work, not a correction.

- **R13: the kind name is `GPULease`.** R13's spec flags "name + migration mode" as
  David's. The migration mode was decided project-wide (clean break, 2026-07-09), and the
  name already had a written recommendation from the design layer — which `README.md` says
  is settled unless reopened with a stated reason — while the playbook's suggested order
  files `R13`+`R14` under "all unparked, no decisions". A kind name with a recorded
  recommendation is an implementation choice, and parking it would have blocked two
  milestone items. If you want `FundingLease` instead, say so: it is the same mechanical
  pass, and the clean-break rule already schedules breaking changes rather than
  accommodating them.

- **R14: `validate()` was NOT trimmed to cross-object checks.** The spec's step 2
  suggests moving field checks out of the webhook now that CEL holds them. I added the
  apiserver rules and kept the Go ones: R14's stated invariant is that the checks hold
  *without* the webhook, which addition achieves, and deleting the Go copies would only
  remove validation from the pure-engine tests, which never reach an apiserver. The drift
  risk is pinned by an envtest asserting both layers reject the same objects.
