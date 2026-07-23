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
| P1 | **R7 pt2** — delete `Run.Spec.Owner` / tenancy authz | Whether to delete the field now | Deferred by owner ruling (Codex-2 / #63). The playbook's park list names it explicitly. |
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
