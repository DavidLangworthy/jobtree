# Correctness closeout plan

**Status:** plan of record (2026-07-11). Sequences the remaining *correctness* work to
a provably-correct engine. Supersedes the ad-hoc task tracking for these items; each
phase points at the R-doc or code path it touches.

**Scope:** only issues that produce (or risk) *wrong runtime behavior* — a wrong
funding decision, a lease billing for no work, a gang that wedges, a run that finishes
missing a rank. Features (the settlement *store*, elastic roles), performance, and the
tenancy authz decision (owner validation) are explicitly **out of scope** here; where a
larger item has a correctness *sub-part*, only that sub-part is in this plan.

---

## What "correct" means here — the acceptance bar

A change reaches "done" in this plan only when all of the following hold:

1. **The metamorphic oracle is green with EVERY legal event.** The quiescence driver
   (`controllers/quiescence_test.go`, `pkg/invariant`) runs its full 800 seeds green
   with the external pod-deletion (eviction) event **enabled** — `step()` case 10,
   `w.deletePod()`, currently disabled. Turning this on is both a fix (Phase 1) and the
   standing regression net for every phase after it.
2. **Each confirmed or plausible correctness bug is fixed with a mutation-verified
   test** (revert the fix → the test goes red), or is *executably cleared* (a
   reproduction attempt shows the feared behavior cannot occur).
3. **The restart-wedge liveness bug has a kind live-proof** — kill the scheduler
   mid-gang, the run returns to full width with no operator action.
4. **No new fake-green.** Every fix ships with a test that fails without it; the
   anti-fake allowlists do not grow; the sole-closer / sole-committer lints stay green.
5. **Verified against the real cluster.** `go test ./...` silently skips envtest —
   every engine/plugin change is run under `KUBEBUILDER_ASSETS=… JOBTREE_REQUIRE_ENVTEST=1
   go test ./controllers/...`, and touching the plugin or funding path also runs the
   eviction fuzzer from Phase 1.

---

## Inventory — status today

| Item | Kind | Status | Where |
|---|---|---|---|
| Duplicate spare mint → `INV-CLOSED-MONOTONE` | confirmed | **fixed** (PR #93, `emitSparePods` name-keyed) | this doc, Phase 0 |
| Evicted rank never re-emitted (workless lease bills forever) | confirmed | **core fixed** (PR #93, `recoverEvictedRanks`) | this doc, Phase 0/1 |
| Run that loses ALL ranks sits `Running`-empty (`INV-WIDTH-ASSEMBLED`) | confirmed | **open** — blocks the eviction fuzzer | Phase 1 |
| `AggregateCap` flavor-blind attach → cross-flavor mis-count | plausible | **open, unproven** | Phase 2, `R4-plugin-hotpath.md` / `quota-semantics.md` |
| `TestETAMirroredFromPodAnnotation` intermittent hang | liveness? | **open, undiagnosed** | Phase 3 |
| Scheduler-restart gang wedge (R2 pt3) | liveness (confirmed) | **open, blocked on identity** | Phase 5, `R2-gang-recovery.md` |
| Safe cached reads (R4 pt1b) | perf w/ correctness hazard | **open** (first draft reverted; uncached today = correct) | Phase 6, `R4-plugin-hotpath.md` |

---

## The two things that shape the ordering

**A. The eviction fuzzer is the acceptance gate.** Once Phase 1 closes the last tail
and `deletePod` is re-enabled, 800 seeds exercise external pod loss across the whole
state space on every run of the suite. Turning it on *early* means Phases 4–6 get their
regressions caught for free. So Phase 1 comes first even though #90's core already
landed.

**B. Durable lease identity is a shared foundation.** A minted lease carries
`LabelRunName`, `LabelRunRole`, `LabelGroupIndex` (R28) — but **no cohort label and no
pod-name annotation** (`admission.leaseLabels`; the code notes "a Lease records no
cohort"). Both R2 pt3 (rebuild gang state after a restart) and R4 pt1b (fold safely
against a cache) need to answer *"is the real minted lease present, and which gang
member is it?"* from the lease itself, not from an in-memory flag. Build that identity
**once** (Phase 4) before either. Building #34 without it re-introduces exactly the
read-your-write double-fund hazard #37 has to solve anyway.

---

## The sequence

### Phase 0 — done (PR #93)
- **#91** `emitSparePods` name-keyed top-up (was raw-count → duplicate-named spare →
  two leases of one name → `INV-CLOSED-MONOTONE`). Mutation-verified.
- **#90 core** `recoverEvictedRanks` + `closeWorklessSpareLeases` at the top of
  `Reconcile`: GPU-sum, lease-relative detection; re-emit an evicted active rank in
  place from its still-open lease; reap an evicted spare's lease. Unit-tested,
  full suite + envtest green.

### Phase 1 — close the eviction tail, turn the fuzzer on *(highest leverage)*
**Goal:** the last confirmed eviction bug, and with it the strongest oracle.

**The bug:** a run that loses ALL its ranks stays `Running` with zero open leases and
zero pods → refutes `INV-WIDTH-ASSEMBLED` (a *different* invariant than #90's
workless-lease reaper). Reached by the fuzzer (seed 86) when a node-failure **swap** pod
is evicted *before* it mints and every node is deleted — there is no open lease for
`recoverEvictedRanks` to re-emit from.

**Approach:** a complementary rule to the eviction edge — a non-terminal run whose
runnable active GPUs have dropped to 0 with **no pending pod awaiting a mint** is not
`Running`; demote it to `Pending` so the assembly path re-provisions it (or fail it if
it cannot). This is the inverse of the adoption "adopt at width" rule and must
coordinate with the node-failure and resolver paths (do not double-fire against
`failGroupWithoutSpare`). Keep it GPU-sum based and gated to *already-Running* runs.

**Then:** re-enable `deletePod` (`quiescence_test.go` `step()` case 10) and run the full
800 seeds. Iterate on any remaining seed the same way — each is a real state, fix the
engine, never weaken the driver.

**Exit:** 800 seeds green with eviction enabled; a unit test for the loses-all-ranks
transition, mutation-verified.

### Phase 2 — prove or clear the aggregate-cap flavor bug *(fast, funding engine)*
**Goal:** resolve the one *plausible* correctness bug in the sole-committer path.

**The claim (Codex #3):** `AggregateCap.Flavor` (`api/v1/budget_types.go`) is consumed
at `pkg/funding/admission.go`, but the `evaluate.go` attach loop
(`pkg/funding/evaluate.go` ~189-196) links a cap to its named envelopes *regardless of
the envelope's flavor vs `cap.Flavor`*. If accrual does not re-filter downstream, a
flavored aggregate cap mis-counts width/hours across envelopes of different flavors.

**Approach:** a bounded reproduction — two envelopes of different flavors under one
flavored `AggregateCap`; trace `aggWidth`/`aggHours` accrual. If it mis-counts, filter
at attach (skip envelopes whose flavor ≠ `cap.Flavor`) or enforce `cap.Flavor` at
accrual. If it re-filters correctly downstream, write the test that proves it and close
the item as cleared.

**Exit:** either a mutation-verified fix, or a test that pins the correct behavior and a
one-line note in `quota-semantics.md` marking the claim refuted.

### Phase 3 — diagnose the ETA-test hang *(liveness or infra)*
**Goal:** know whether `TestETAMirroredFromPodAnnotation`'s one observed 30s timeout
(run stuck `Pending` after `seedPluginLeases`) is a real adoption/liveness race or a
too-tight `eventually` under `-race`. **No timeout bump** — a real diagnosis.

**Approach:** reproduce under load (`-race`, `-count`, parallelism) and instrument the
adoption path. If it is a race, fix it (likely an ordering in the half-applied adoption
readmit); if it is test timing, make the wait condition robust and document why.

**Exit:** a written root cause; the test is either fixed or provably not flaky. The
weekly report may not call the suite flake-free until this is closed.

### Phase 4 — durable lease identity *(the shared foundation)*
**Goal:** make gang membership recoverable from a lease alone. Unblocks Phase 5,
de-risks Phase 6.

**Approach:** stamp two things at mint (`admission.PodLeaseWithRole` / `leaseLabels`):
a **cohort label** (base gang = "0"; each elastic-grow step "1","2",… — today only
`Spec.Reason=="Grow"` proxies this) and a **pod-name annotation** (so a member maps to
its rank without string-parsing `<pod>[-<nonce>]-lease`). Introduce one predicate used
by both later phases: *"is the real, open, this-gang lease present in the snapshot?"* —
identity-based, not flag-based.

**Exit:** new leases carry the identity; a test that a lease round-trips to
`(gang, cohort, member)`; the shared predicate landed with its own test. No behavior
change yet.

### Phase 5 — restart reconstruction (R2 pt3) *(real liveness bug)*
**Goal:** a scheduler restart no longer wedges a partially-bound gang.

**The bug:** `gangManager` state is in-memory, so a restart resets `committedCount` to
0; `Permit`'s `waiting + committed >= expected` degrades to `waiting >= expected`, and a
lone surviving member parks, times out at `permitTimeout` (2m), and loops forever.

**Approach:** `Reconstruct(ctx)` in `gang.go`, called from `New` before serving. List
open leases, group by (runRef, cohort) using Phase 4's identity, rebuild one
`gangCommit` per gang: `claimed = len(open leases)`, `assigned[podName]=idx` and
`minted[]=true` from the pod-name annotation with each payer taken from the lease's own
provenance, and leave `decided=false` so the first `Permit` funds only the **delta**
(`expected − claimed`) against the live ledger — never the full width on top of already-
charged leases (the `world.Quantity` override the grow path already uses,
`gang.go` ~159-161). Also tighten R2 step 4: `PreBind` swallows `IsAlreadyExists`
unconditionally (`plugin.go` ~295) — treat it as success only if the existing lease is
open and owned by this gang.

**Exit:** unit (restart → late sibling still admits, no double-mint), unit (ABA: closed
leases + resubmit → fresh leases adopted), and a **kind live-proof** (kill the scheduler
mid-gang; the run returns to full width). Full envtest. Eviction fuzzer green.

### Phase 6 — safe cached reads (R4 pt1b) *(perf, done last on purpose)*
**Goal:** re-introduce the informer cache without the double-fund that reverted the
first draft.

**Why last:** today's uncached reader is *correct* (just slower), so there is no live
bug to race; and this is the riskiest change (its first attempt double-funded a gang).
It must run against the strongest net — the eviction fuzzer (Phase 1) and the restart
tests (Phase 5) — which exist by now.

**Approach:** make the cross-gang pending fold and `PostBind` GC key off Phase 4's
*"is the real lease actually in the snapshot?"* predicate (dedup by lease identity), not
the in-memory `minted` flag — the read-your-write assumption both snapshot-before-lock
and an eventually-consistent cache break. *Then* back `m.reader` with an informer cache;
fix the `newCachedReader` startup-goroutine leak + sync-wait race. Watch the fold's
interaction with R1's double-count test.

**Exit:** the reverted double-fund reproduction stays green; a kind live-proof of
sync/staleness; full envtest; eviction fuzzer green.

---

## Ordering rationale (one paragraph)

Phase 1 first because the eviction fuzzer is the acceptance gate for everything and
#90's tail is a confirmed bug — turning the net on early makes Phases 4–6 self-checking.
Phases 2 and 3 are independent quick prove-or-clears; do them before the heavy
foundation so the board is unambiguous. Phase 4 (identity) precedes 5 and 6 because both
need it, and building it once stops #34 from re-introducing #37's hazard. #37 before #34
because it is a live liveness bug and #34 is only performance. #34 last because it is the
riskiest and wants the strongest net, which exists by then.

---

## Verification discipline (every phase)

- **Mutation-verify** each fix: revert it, watch the test go red, re-apply.
- **Full envtest**, not `go test ./...` (which skips it).
- **Eviction fuzzer** (once Phase 1 lands) for any engine/plugin/funding change.
- **No allowlist growth**; sole-closer and sole-committer lints stay green.
- Each phase lands as its own PR with the test that proves it.

---

## Out of scope (named so they are not forgotten)

- **Owner validation / tenancy** (self-declared `run.Spec.Owner` unvalidated against
  namespace/identity) — an authz *policy* decision deferred by owner ruling, not an
  engine-correctness bug. Revisit under `R7-tenancy-*`.
- **The settlement *store*** and aggregate-cap *summary* (the feature half of R4 pt2b) —
  only the aggregate-cap correctness sub-part (Phase 2) is here.
- **Elastic roles / gang-of-gangs**, and any performance work beyond Phase 6.
