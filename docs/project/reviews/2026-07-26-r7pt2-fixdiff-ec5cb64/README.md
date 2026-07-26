# R7 pt2 — the FIX-DIFF review (the one that had never happened)

**Reviewed commit:** `ec5cb64` on `r7pt2/tenancy-owner-from-namespace`, PR #127.
**Target:** `git diff 0b77fbe..HEAD -- controllers pkg cmd api internal test hack/e2e` — **22 files,
+1096/−55**: the remediation made in response to three earlier panels, not the feature.
**Run:** `wf_5a636517-097`, harness from `main` (`#136` + `#137`) with one local patch (below).
`skepticQuorum: 2`, `commit: ec5cb64`, Judge bounded by `judgeOnly`.
**Date:** 2026-07-25/26.

## Why this review exists

Every earlier panel judged findings raised against `0b77fbe` — **pre-fix code**. Nothing had ever
reviewed the repairs. The two times a lens glanced at one it found a defect: a `critical` inverted
provenance assertion, and a `high` guard that made `failReservationNoEnvelope` unreachable and left
reservations immortal. Two for two, so this panel was pointed at the fixes and told so.

It found more. **The fixes are the least reviewed and most defective code on the branch.**

## VERDICT: not green, and the most important finding is that a fix introduced a DoS

Attest passed cleanly on the second attempt — **five lenses, 56 citations, all verified against
`git show ec5cb64:<path>`** (`"Checked all 8 citations against the ec5cb64 snapshot…"`, `missing: []`
×5). 50 raised findings across 20 sites (some duplicated by re-shapes; ~25 distinct).

### The headline: the terminal-reservation fix created a cross-tenant denial of service

The 2026-07-25 panel confirmed a HIGH defect — a non-terminal "hold" left reservations immortal
where `main` terminated at tick 1 — and ruled `fixIsReaper=false`. The fix made it terminal. This
panel then executed what terminal costs:

> Namespace `default` is correctly bound to `org:team` with a valid Budget and a due reservation. A
> Budget named `squatter` is created in namespace `attacker` whose only distinguishing feature is
> `owner: org:team`. **Nothing in `default` changes.** `OwnerOf("default")` flips `org:team` → `""`
> and one `ActivateReservations` tick marks `default/res` **Failed**. The same foreign Budget makes
> `promiseProvenanceValid` refuse `default`'s own top-up naming `default`'s own budget and envelope.

`Budget.Spec.Owner` is a free-form string the victim does not control. Before the fix that state
stalled; after it, it destroys. The irony is exact: R7 pt2 exists because `Run.Spec.Owner` was
forgeable, and closing that weaponised `Budget.Spec.Owner`.

A second door needs no adversary at all: **zero Budgets plus one due reservation is terminal in a
single tick**, and that window is any `kubectl delete && apply`, `replace --force`, a helm upgrade
recreating Budgets, or a GitOps sync that prunes before applying — against a one-minute resync.

**Where that leaves the earlier ruling, precisely:** `fixIsReaper=false` was established for an
*admin typo*. Generalising it to "terminating is safe" was the autopilot's error, not the seat's. It
is safe for the scenario tested and unsafe for two that were not.

**Parked as P8.** Both endpoints are now proven defective — holding gives immortal reservations,
terminating gives adversarially-inducible destruction — and choosing between them is a tenancy
policy question. **The unsafe option is what is committed today**, in force by accident rather than
by decision. That is stated here so nobody reads this archive as a clearance.

### The refusal that was KEPT strands partial gangs (critical)

`gang.go:756`, executed: a 4-GPU Promise gang with 2 of 4 ranks minted, admin adds a second-owner
Budget → 20 reconciles over 20 simulated hours → leases still open, `Unfunded: 40.03 GPU-hours` and
climbing, pods re-emitted forever, sweep empty. A live 4/4 gang that loses a rank to node failure
cannot replace it: `topUpActiveGang` re-emits it and PreBind refuses, ending at `Unfunded: 63.08`.

This is the refusal an earlier reproduce seat **confirmed as a genuine regression fix**. It reaps
through a different field than the `seg.Owner` pin the veto named — so removing the pin did not
remove the reaping.

### The fix does not fully repair the defect it was written for (high)

The new terminal guard sits behind two earlier returns, so it is reached only when
`baseGangGPUsForRun == 0`. Executed: a run holding one open 4-GPU Promise lease, 20 ticks → the
reservation is still `Pending`, countdown still set, gauge still `{H100-80GB 1020}`, lease still
open. A second door is *worse*: reservation Pending forever with the gauge **cleared**, so the
immortal reservation is invisible rather than merely frozen.

## Three decorative tests — all in code written to answer earlier panels

This is the part worth internalising. Every one of these was a test the autopilot wrote *in response
to a panel*, and each certified its own fix.

| Test | Mutation | Result | Status |
|---|---|---|---|
| `TestRunRecoversAfterTerminalFailureWhenBindingIsRepaired` | delete the repair line | **PASSED**, log printing `msg="owner and flavor must be set"` — the very message the fix claims to remove | **fixed** `37270af`, now asserts `activeIntentPods == 4` |
| both backlog-gauge assertions | delete `ClearReservationBacklog` entirely | `ok ./controllers` — the fixture's `EarliestStart` is in the past so the gauge was `map[]` before *and* after; `len(backlog) != 0` was vacuously true | **fixed** `9ed3193`, gauge now seeded so "cleared" is observable |
| `failReservationNoEnvelope` call site at `run_controller.go:1357` | replace with `return nil` | whole suite green | **OPEN** — no test exists for that call site |

The gauge one is the sharpest: the autopilot's own repeated claim was *"the state was wrong and the
metric lied about it in the same breath"* — and the test written to guard that claim proved nothing.

## Open findings, not fixed here

- **`run_controller.go:1357`** — the second `failReservationNoEnvelope` call site has no test.
  Neutered it produces the original defect exactly: the resolver has already evicted victims to make
  room, the reservation stays Pending forever, and `ActivateReservations` returns nil so nothing is
  even logged. Not faked under time pressure; recorded instead.
- **`run_controller.go:1217`** — `run.Status.Message` has two disagreeing writers for this state, and
  the panel reports the durable winner is forbidden by the fix's own test assertion, i.e. the
  asserted message may only ever be transient.
- Three **comment-as-enforcement** hits on comments the autopilot wrote: `Evaluate` never reads
  `Lease.Spec.Owner`; the resolver key "never escapes this package"; `OwnerOfNamespace` meaning the
  two derivations "can never drift". All true today, all enforced by nothing.
- Findings in the new tests across four packages (`tenancy_buckets_test.go`, `tenancy_conflict_test.go`,
  `tenancy_r7_conflicts_test.go`, `tenancy_reservation_test.go`) — see `findings.json`.

## What went right, mechanically

**Pinned-commit attestation works.** `#137`'s `ATTEST_SOURCE` was exercised for real: five lenses,
56 citations, `git show ec5cb64:<path>`, zero missing. The earlier run's five "fabricated citation"
charges were pure tree drift and are now structurally impossible when `commit` is passed **and the
tree is checked out at it**. Both halves matter: this run only worked because the tree was detached
to `ec5cb64` before resuming.

**The lesson the autopilot had to learn twice:** a branch under review is frozen, and "it is only a
comment" is not an exemption — a comment moves line numbers exactly as much as code. Two commits
landed mid-review here before that rule was adopted.

**Resume is cheap and real.** The run survived three segment boundaries and two quota walls. Scout,
five lenses and five attestations replayed from cache; only the dead consequence lens and Judge ever
re-ran.

## The codex seat: not an auth failure, a model-name mismatch

Diagnosed by forcing a filesystem read rather than trusting an exit code:

```
-m gpt-5.6 / gpt-5-codex / gpt-5  → 400 "not supported when using Codex with a ChatGPT account"
no -m flag                        → ran `sed -n` on a real file and echoed the exact lines back
model: gpt-5.6-sol
```

`auth.json` is good; the subscription simply serves **`gpt-5.6-sol`** and rejects every explicitly
named model. The harness hardcodes `-m gpt-5.6` in both its preflight probe and its trace-seat
prompt, so on this account a **working** cross-vendor seat is silently downgraded to the announced
Opus fallback. `harness-codex-model.patch` (in this directory) drops the flag; it belongs in the
harness's own PR, like the attest fix before it.

## Files

| File | What |
|---|---|
| `findings.json` | Every shaped lens report, the five attestations, and the raised set grouped by site |
| `harness-codex-model.patch` | Let the ChatGPT subscription choose its own model |

## Standing items

- **P8** (terminate vs hold, and whether a foreign Budget may induce it) is the merge-blocking
  decision. Neither endpoint is safe as-is.
- **P5**, **P6**, **P7** remain parked and were deliberately out of scope.
- `#127` must not merge on the strength of this record. It is not a clearance.
