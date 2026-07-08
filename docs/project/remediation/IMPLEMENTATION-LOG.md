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
