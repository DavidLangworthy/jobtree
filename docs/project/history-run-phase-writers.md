# Who may write `run.Status.Phase`

*A history of getting one thing wrong, seven times, in `HandleNodeFailure`.*

> **Provenance.** Researched from source, `git log`, and PR bodies. Every `file:line` and commit SHA in
> this document was independently verified against the tree at `98b602d` before it was committed. Where
> the record could not establish something, the text says so rather than guessing.
>
> Companion reading: `docs/project/adversarial-review-playbook.md` (the taxonomy) and
> `docs/project/reviews/2026-07-09-r27-invariant-oracle-98b602d/` (the review that confirmed specimen 7).

## Summary

`run.Status.Phase` has **twenty write sites** in `controllers/run_controller.go`. Nineteen are safe, by
two different structural arguments that have nothing to do with discipline. `Reconcile`, `completeRun`,
`setWaiting`, `failRun`, `activateReservation`, and `planReservation` each write the field at most once
per call, because every write is immediately followed by a `return`. `applyResolution` writes it once
per run because its loop ranges over a **de-duplicated map key** (`for runKey := range affectedRuns`,
`run_controller.go:1664`), not a lease list.

The twentieth is `reclaimSquatter`, added in `98b602d` on 2026-07-09, which writes
`victim.Status.Phase = RunPhasePending` directly at `run_controller.go:1474` — inside
`HandleNodeFailure`'s pass-2 lease loop, the one place in this file that can visit the *same run* twice
in a single call. `runPhaseTracker`, the severity-lattice fold that exists precisely to make multiple
writes to one run's phase commutative, was built **four hours earlier the same day** (`ae6c69e`) to kill
exactly this bug. `reclaimSquatter`'s signature —
`func (c *RunController) reclaimSquatter(lease *v1.Lease, victimKey string, now time.Time)` — takes no
`phases runPhaseTracker` parameter at all, so it **structurally cannot** participate in the fold even if
its author had remembered to try.

An adversarial review confirmed the consequence by running all 24 orderings of a four-lease node
failure. An unfunded run that both squats on a funded run's spare slot and holds its own rank on the
failing node ends `Failed` in roughly half the orderings and `Pending` in the other half. The
difference is the storage order of `c.State.Leases`.

**Which one is right?** `Failed` — and the review's own prose got this backwards, as did the first
version of this document. R14's *demote-not-kill* governs **reclamation**: unfunded work that lost
capacity it never paid for should requeue. It does not govern **destruction**: a rank that died on a
fenced node with no cover kills the gang whatever its funding class. A run that suffers both is dead
for the reason that has nothing to do with the reclaim. (The correction came from Fable, writing the
TLA+ brief, which could not state the phase-join invariant while two documents disagreed about what
the join should produce. That is model-checking earning its keep before a single line of TLA+ was run.)

So the defect is not "killed when it should have been demoted." It is that **the answer was
nondeterministic**, and `Failed` is terminal while `Pending` is not. The fix restores the
deterministic answer — which is also, exactly, what the code did before `reclaimSquatter` existed.

This is playbook class 3, LAST-WRITER-WINS, reintroduced by a new writer in the very commit that
shipped the 24-way permutation test meant to retire the class. The test missed it because its squatter
fixture held only one lease, so the tracked writer and the untracked writer never touched the same run.
**The rail was right; the fixture was too small.**

---

## Timeline

### Specimen 0 — the origin. `32c852c`, 2025-10-28

*"Implement spare handling and failure swaps."* The first implementation of the swap path. It contains
`leasesOverlap`, which compares `nodeFromSlot(slot)` — a helper that **strips the `#ordinal`** — rather
than the full slot identity. That is R22, seven months before anyone named it. The function stayed
compiled and callable until `98b602d` finally deleted it.

This commit predates `runPhaseTracker` entirely. Each group in `HandleNodeFailure` wrote
`run.Status.Phase` directly, with no coordination. That is the origin of the class-3 defect.

### Specimens 1–3 — R21, R22, R25. `a5c8eef`, 2026-07-09 06:40 UTC, PR #72

Three defects fixed at once, and a fourth introduced.

- **R21** — the swap fired on `!nodeUsable`, which is false for a merely **cordoned** node. A cordon
  stops nothing, so the original pod and its replacement ran simultaneously: two live copies of one
  distributed-training rank.
- **R22** — the reclaim sweep compared bare node names, so any lease sharing the *machine* with the
  spare was closed as `ReclaimedBySpare`, silently killing an unrelated co-located run's funded work.
- **R25** — the loop `continue`d on `Role == Spare` *before* testing whether the lease named the failed
  node, so a node hosting only a held spare matched nothing. The caller then swallowed the resulting
  error by `strings.Contains`. Fixed with the typed sentinel `ErrNoLeaseOnNode`.
- **Introduced, and asserted by a test.** Declining a swap now called `failGroupWithoutSpare`, which
  closed only the failed *active* lease — the run's own **spare** stayed open forever. An immortal
  lease. The test read:

  ```go
  t.Errorf("the spare must not be consumed when its slots are unavailable")
  ```

  The test literally asserted the bug as correct behaviour. This is the canonical specimen for playbook
  class 8.

### Specimen 4, and the birth of the tracker. `ae6c69e`, 2026-07-09 07:42 UTC, PR #72

Written after an adversarial review of `a5c8eef` caught the immortal spare. Four things happen at once:

1. **R21's own premise was found unsound** — not by review, by research. "NotReady past a grace window"
   is false as a failure signal: NotReady means the control plane cannot *hear* the kubelet, not that
   its containers stopped. A 2-minute grace swapped a rank *before* Kubernetes even begins graceful
   eviction. `nodeFailed` was rewritten to require a **fencing assertion** — the Node deleted, or
   tainted `out-of-service` — and takes no clock at all.
2. The immortal spare is closed with `SwapDeclined`, and the test's assertion is **inverted**, with a
   comment recording that an earlier version of that very test asserted the opposite.
3. `failRun`'s comment had asserted, in the indicative mood, *"It never holds leases at this point…so
   there is nothing to close."* True when written; falsified by a new caller; enforced by nothing.
   `failRun` now calls `closeRunLeases`. This is the specimen for playbook class 2.
4. **`runPhaseTracker` is born.** The commit message flags it as item 4:

   > *Each group wrote `run.Status.Phase` directly, so the last group in `c.State.Leases` won: a run
   > with one group swapping and another dead without coverage reported whichever came last. A run with
   > a dead, uncovered rank could report `Running`.*

   The comment it left behind, still current at `run_controller.go:1359`:

   ```go
   // runPhaseSeverity orders the phases HandleNodeFailure can write, worst last. A run
   // may hold several active leases on the failed node; each group reaches its own
   // verdict, but they share one Status.Phase.
   ```

   The fix was deliberately **not** "sort the leases." A deterministic order that happens to give the
   right answer is a coincidence, not a fix. `apply` makes the fold **commutative** — worst wins.

### Specimen 5 — the fenced node stayed in the capacity pool. `59e9a2c`, 2026-07-09 08:22 UTC, PR #75

`nodeFailed` ("is this machine dead?") and `nodeUsable` ("may we place work here?") are deliberately
separate questions. But `nodeUsable` checked only `Unschedulable` and `Ready`, never the fencing taint —
so a fenced node stayed in the capacity pool, and the engine could admit and *charge* a run for GPUs a
`NoExecute` taint made permanently unusable. Found by reading, while diagnosing an unrelated CI failure.

### The playbook. `833f7bd`, 2026-07-09 14:56 UTC, PR #79

Not a code fix. It distils the specimens into eight classes and injects them into every review lens. Its
rank-3 "where to look" entry reads:

> *Any function that writes `run.Status.Phase` — Phase is a lattice, not a variable.*

That sentence was in the repository **thirty-eight minutes** before the commit below wrote a new function
that violated it.

### Specimens 6 and 7 — R27. `98b602d`, 2026-07-09 15:34 UTC, PR #80 *(open, unmerged)*

Installs `pkg/invariant`, an oracle hooked into every engine entry point. Fixes three defects it
surfaced, one of which is the relevant one: the **ledger-only reclaim**. The old code closed a squatting
lease and left the container running on the exact `node#ordinal` slot the swap targeted one line later.
The new `reclaimSquatter` evicts the pod too, and **demotes rather than kills** — writing
`victim.Status.Phase = RunPhasePending` directly.

The same commit shipped `order_independence_test.go`: a 24-ordering permutation test and a
64-repetition map-iteration test, built explicitly to retire class 3 for good. And
`hack/antifake/soleclose.go`, a zero-tolerance lint making `CloseLease` the sole writer of
`Lease.Status.Closed`.

**The commit claimed class 3 was retired. It was not.** A skeptic reproduced the divergence and then
reverted the change to prove it was newly introduced:

> *I patched the scratch `run_controller.go` to replace `c.reclaimSquatter(other, otherKey, now)` with
> the pre-change `CloseLease(other, "ReclaimedBySpare", now)` and reran: both orderings gave
> `filler.phase=Failed` — deterministic. So the change INTRODUCED the order-dependence.*

Open as **task #50**, unfixed as of `98b602d`.

---

## Every `run.Status.Phase` writer

| Function | Sites | Tracked? | Why it is safe, or isn't | Verdict |
|---|---|---|---|---|
| `Reconcile` | 7 | no | every write is the last statement before a `return`; one run per call | correct |
| `completeRun` | 1 | no | single write, one run per call | correct |
| `setWaiting` | 1 | no | single write, trivial helper | correct |
| `failRun` | 1 | no | single write; also closes the run's leases | correct |
| `activateReservation` | 2 | no | mutually exclusive branches, each followed by `return` | correct |
| `planReservation` | 3 | no | each write is the last statement on its path | correct |
| `applyResolution` | 3 | no | loop ranges over a **map key**, so each run is visited once | **correct, but non-obviously so** |
| `HandleNodeFailure` swap branch | 1 | **yes** | `phases.apply` | correct |
| `failGroupWithoutSpare` | 2 | **yes** | `phases.apply` | correct |
| **`reclaimSquatter`** | **1** | **no** | called from inside the pass-2 **lease** loop, where one run can be visited twice; its signature has no `phases` parameter | **BUG — task #50** |

Nineteen of twenty are correct. The twentieth is why this document exists.

---

## Why this function

Six decision points in `HandleNodeFailure`'s call tree can independently decide a run's fate on one
node-failure event: the spare swap, the declined swap under checkpoint grace, the declined swap without
it, the squatter reclaim, the post-loop terminal sweep, and — one level up — `Reconcile`'s adoption
logic on the next pass.

The reason the bug recurs *here* and nowhere else is structural, not personal. Everywhere else in this
file, a direct `run.Status.Phase = X` is safe, and it is safe for two different, non-obvious reasons
that **look identical from the call site**:

- *one write per call, then return* — seven functions; or
- *one write per run, because the loop ranges over a de-duplicated map key* — `applyResolution`.

`HandleNodeFailure` is the only function where **neither** property holds. It loops over a **slice**,
and one run can own several leases on the failed node — or own a lease on the failed node *and* be
reclaimed as a squatter elsewhere in the same pass.

`reclaimSquatter`'s own doc comment makes the miscalibration explicit. It justifies its direct write by
claiming parity with `applyResolution`'s demote branch — *"This is the same obligation, and it is now
discharged the same way"* — copying a pattern that is safe in `applyResolution` for a reason that does
not hold in `HandleNodeFailure`. Nothing in the code marks that distinction. A reader has to know, from
institutional memory alone, that this one loop is different in kind from every other loop in the file.

And `runPhaseTracker` is an **opt-in discipline**. It works perfectly for every writer that uses it and
enforces nothing on a writer that does not. That is playbook class 2 — *the comment is the only
enforcement* — one level up: the rule lives in a doc comment and in the reviewer's memory, not in the
compiler or a build gate.

There is a deeper asymmetry that makes a single global rule hard to write. Inside `HandleNodeFailure`,
`Phase` really is a lattice: `Running < Pending < Failed`, and the right answer for a run touched by
several branches is the **join**, never "whichever ran last." Outside it, in `Reconcile`, `Phase` is not
a lattice at all — it is a state machine with one live transition per call, whose branches are mutually
exclusive by construction rather than competing on severity. Encoding a lattice there would be actively
wrong. `reclaimSquatter` was written as if it were another state-machine writer when it needed to be
another lattice writer.

## What would actually stop it

Two mechanical rails of the right shape already exist in this tree.

**`hack/antifake/soleclose.go`** is the zero-tolerance model: no non-test file may assign
`Lease.Status.{Closed,Ended,ClosureReason}` outside `CloseLease`. No allowlist, no ratchet, permitted
count zero forever. It works because that field genuinely has exactly one legitimate writer.

**`run.Status.Phase` does not.** It has eight independent legitimate writers, each the sole owner of a
distinct transition. A soleclose-style rule would have to special-case seven functions immediately — at
which point it has degenerated into an allowlist, which is the *second* model already in the repo.

**`hack/antifake/terminalphase.go`** is that second model: an AST scanner with a small, explicitly
reasoned, ratcheted allowlist, requiring a matching inline annotation at every allowlisted line so the
code and the allowlist cannot drift. Notably, it already special-cased `run.Status.Phase` **out** of its
scope by design. *The scanning technology for exactly this field already exists, and was deliberately
pointed away from it.*

### The rail to build

Extend the `terminalphase.go` pattern, **inverted**. Instead of allowlisting exceptions to a
default-forbid rule, allowlist the *functions in scope* for a default-forbid-direct-writes rule:

- **Forbid** a direct `X.Status.Phase = RunPhaseXxx` assignment anywhere in `HandleNodeFailure`,
  `failGroupWithoutSpare`, `reclaimSquatter`, or any future function in that call tree, unless the
  function's parameter list includes a `runPhaseTracker` and the write goes through `.apply()`.
- **Allow, by name**, the nineteen sites verified correct above. Everything outside the node-failure
  call tree is left alone; those writers should not be forced through a fold they do not need.

A PR adding a new direct write inside the fan-out then fails CI with a clear message, forcing the author
to route through the tracker or justify the exception in the same review that introduces it.

### The cheaper fix, which should ship first

Stop passing `*v1.Run` into any function reachable from `HandleNodeFailure`'s lease loop, and thread the
`runPhaseTracker` instead. `reclaimSquatter`'s signature becomes:

```go
func (c *RunController) reclaimSquatter(lease *v1.Lease, victimKey string, now time.Time, phases runPhaseTracker)
```

mirroring `failGroupWithoutSpare` exactly. The type system then makes the **call site** wrong the moment
the fix ships — a missing argument, not a missing habit — and the lint becomes a backstop rather than
the primary defence.

That is the whole lesson of this file, stated once: *make the illegal state unrepresentable, or a
compile error, or a CI failure. Never a convention.* Seven specimens say a convention is not enough, and
the seventh was written by the same hand that wrote the convention down, thirty-eight minutes earlier.
