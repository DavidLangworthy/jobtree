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
