# Adversarial review playbook

> **Mandatory reading for any agent reviewing a change to the funding engine, the
> scheduler plugin, or another sole-committer path.** `.claude/workflows/adversarial-review.js`
> injects a pointer to this file into every lens and every skeptic. Read it in full
> before you begin. It is not background colour; it is the distilled record of six
> real defects found on this exact path, in a row, and it tells you where to look.

## Why this exists

Adversarial review has caught a real, merge-blocking defect on **six consecutive
changes** to the node-failure and funding paths. Not six sloppy changes — six careful
ones, each written after reading the last one's post-mortem. That rate is not a
statement about the authors. It is a statement about the *shape of the system*: it
has a small number of load-bearing invariants that the type system does not encode,
the compiler does not check, and a passing test suite does not establish.

A reviewer who reads a diff and asks "does this look right?" will approve all six.
A reviewer who knows *where the invariants live* and *what their violations look
like* will find them. This file is the second reviewer's map.

## The one fact that generates most of the defects

**An open Lease is a charge and a capacity claim.**

`pkg/funding.Evaluate` derives every funding class (`Owned`, `Shared`, `Borrowed`,
`Unfunded`) from the set of **open** leases, plus budgets, plus the clock. The class
is never stored. So:

- A lease that is never closed **bills its budget forever** and **holds its GPUs forever**.
  We call this an **immortal lease**. It is the single highest-severity defect class
  in this repo, and it is *invisible* — no pod crashes, no error is logged, nothing
  turns red. The cluster just quietly gets smaller and someone quietly gets poorer.
- Closing a lease is therefore an **obligation**, and obligations are discharged on
  *code paths*. Every new `return`, every new `continue`, every new early exit is a
  new opportunity to leave one undischarged.

Read `controllers/run_controller.go:1384` — the comment there was written by the fix
for specimen 1 and states the stakes precisely:

```
// The group is not runnable, so the spare it was holding can never cover it.
// Leaving that lease open charges the run's budget for GPUs it will never use
// and keeps the ledger marking them occupied, blocking re-admission — the
// immortal-lease class R25 exists to kill, reached by a new door.
```

The phrase **"reached by a new door"** is the whole problem. The class was known.
The fix for the old door was correct. A new door opened anyway.

## Where to look

Ranked by defects-per-line historically. Start at the top and do not skip.

| Rank | Location | Why |
|---|---|---|
| 1 | `controllers/run_controller.go` — `HandleNodeFailure`, `applyResolution`, `failGroupWithoutSpare`, `failRun`, `completeRun`, `shrinkRun` | Every one of the six specimens lived in this file. Multi-branch functions that close leases. |
| 2 | Any function that writes `Lease.Status.Closed` | Only `closeLease` (`run_controller.go:2601`) may. Two sites still bypass it: `applyResolution:1566` and `controllers/kube/reconcilers.go:103`. A third would be a finding. |
| 3 | Any function that writes `run.Status.Phase` | Phase is a lattice, not a variable. See class **LAST-WRITER-WINS**. |
| 4 | `controllers/kube/reconcilers.go` — `NodeReconciler` | Where a Kubernetes signal is translated into a claim about physical reality. See class **SIGNAL ≠ REALITY**. |
| 5 | `pkg/resolver/` and its caller `applyResolution` | The resolver decides *who dies*. Its `Scope` filter means it sees a subset of leases; the caller then reasons about the whole run. |
| 6 | The test that "proves" the change | See class **THE TEST ASSERTS THE BUG**. This is the one that gets everyone. |

Two structural facts make confirmation cheap, and you are expected to use them:

- **The engine is pure.** `RunController` performs no I/O. `ClusterState` + a static
  clock *is* a simulator. Any hypothesis about engine behaviour can be turned into a
  compiled, running test in minutes. **Do not speculate when you can execute.**
- **There is one choke point.** Every production mutation goes through
  `Bridge.WithWorld` (`controllers/kube/bridge.go:101`), which already deep-copies a
  before-snapshot for its diff. Before/after invariants are free there.

## The taxonomy

Eight classes. For each: the **tell** (what you can mechanically scan for), **where**
it lives in this repo, **how to confirm** it, and the **specimen** that proves the
class is real.

---

### 1. IMMORTAL LEASE — an exit path that does not discharge its obligation

**Tell.** A function with more than one `return`, where *some* returns are preceded by
`closeLease`/`closeRunLeases` and others are not. Or: a status assignment to a terminal
phase (`RunPhaseFailed`, `RunPhaseComplete`) that is not accompanied by a closure call.
Or: a new caller added to an existing function whose closure behaviour was correct only
for the old callers.

**Where.** Every branch of `HandleNodeFailure`. Every arm of `applyResolution`'s final
`switch` (`run_controller.go:1612-1627`). `failRun`. `completeRun`. Any new helper
factored out of one of these.

**How to confirm.** Build a `ClusterState` in which the run reaches that branch. Drive
`Reconcile` twenty times over twenty simulated hours. Assert the lease is closed. If it
is still open, you have it — and note that *no test in the repo will have failed*.

**Specimen.** The decline-the-swap path called `failGroupWithoutSpare`, which closed the
failed active lease and left the run's **own spare** open forever. Reproduced by a judge:
20 reconciles, 20 simulated hours, still open, still deriving `Owned`.

**Live suspects when this was written.** `applyResolution`'s terminal branch
(`run_controller.go:1623`) sets `RunPhaseFailed` and never calls `closeRunLeases`.
`activeGPUsForRun` (`:1685`) *skips spares*, so a run whose last open lease is a spare
takes that branch. Confirm or refute it; do not assume someone else did.

---

### 2. THE COMMENT IS THE ONLY ENFORCEMENT

**Tell.** A comment in the indicative mood asserting a global property: *"It never holds
leases at this point."* *"This is always non-nil here."* *"Callers guarantee X."* Prose
is an assertion nothing runs. It was true when written. It is a **claim about all present
and future callers**, and nothing checks it.

**Where.** Grep for `never`, `always`, `cannot`, `guaranteed`, `by construction`,
`callers must` in `controllers/`. Each hit is a candidate: is the property *enforced*,
or merely *described*?

**How to confirm.** Find the newest caller of the function. Check whether the asserted
property holds for *that* caller. If the property is real, ask why it is not asserted in
code — a two-line check, a `panic`, or making the statement true by construction.

**Specimen.** `failRun` carried: *"It never holds leases at this point, so there is
nothing to close."* True when written. A new caller made it false. The fix was not to
update the comment — it was to make `failRun` **close the leases**, so the sentence
became true by construction. That is the correct shape of every fix in this class.
See `run_controller.go:702`.

---

### 3. LAST-WRITER-WINS — an outcome that depends on iteration order

**Tell.** An assignment to a status field, a phase, or a decision variable **inside a
loop**, with no comparison against what is already there. The final value is whatever
the last iteration wrote. If the loop iterates a Go map, the outcome is *randomized per
process*, and your test passes 90% of the time.

**Where.** Any `for i := range c.State.Leases` that writes `run.Status.*`. Any
`for runKey := range affectedRuns` (a **map** — Go randomizes this).
`applyResolution:1606`. `HandleNodeFailure`'s lease loop.

**How to confirm.** The metamorphic test: **shuffle `state.Leases`, replay, assert the
outcome is byte-identical.** If it is not, the system's answer depends on storage order,
which is not part of its specification. This single test retires the whole class rather
than one instance of it. For map iteration, run the same test 100 times in one process.

**Specimen.** Run phase was assigned by whichever lease the loop visited last, so a run
with a dead, uncovered rank could report `Running`. The fix was a severity-ranked
`runPhaseTracker` (`Running` < `Pending` < `Failed`) plus an order-independent post-loop
sweep — i.e. make the fold **commutative** rather than make the order deterministic.
Deterministic order would still have been a coincidence.

---

### 4. SIGNAL ≠ REALITY — a Kubernetes condition read as a physical fact

**Tell.** Code that treats a Kubernetes API object's *condition*, *taint*, or *absence*
as evidence about what is **physically happening on a machine**. The API server knows
what it has been told. It does not know whether a container is running.

**Where.** `controllers/kube/reconcilers.go`'s `NodeReconciler`. Anything reading
`NodeReady`, `Unschedulable`, `LastTransitionTime`, or pod phase.

**How to confirm.** For each signal, ask: *"if I am wrong about this, what runs twice?"*
For a gang scheduler, two live copies of one distributed-training rank is silent data
corruption — both write checkpoints, both join the collective.

Then ask the second question: **who can write this field, and can they lie?** A field
carrying a wall-clock timestamp written by the very node whose health is in question is
not a trustworthy input.

**Specimen (R21).** The swap fired on `!nodeUsable`, which is false for a **cordoned**
node. A cordon means "schedule nothing new here". It does not stop a running pod. The
fix's first draft then used "NotReady for 2 minutes" — *also wrong*, because NotReady
means the control plane cannot **hear** the kubelet, not that its containers stopped; a
partitioned kubelet keeps running them. Kubernetes itself waits 50s to mark NotReady and
then issues an ordinary **graceful** delete at +300s that an unreachable kubelet never
acts on.

The shipped rule requires a **fencing assertion** — the Node object deleted, or tainted
`node.kubernetes.io/out-of-service` — both of which cause Pod GC to *force*-delete.
`nodeFailed` therefore takes **no clock at all**, which also removes the clock-skew
hazard and stops a compromised kubelet from backdating `LastTransitionTime` to
manufacture a failure. See `docs/project/remediation/R21-cordon-not-failure.md`.

**The generalization:** *"the control plane cannot observe X"* is a much stronger and
more useful prior than *"the control plane observed not-X"*.

---

### 5. IDENTITY COARSENING — comparing a coarser key than the one that matters

**Tell.** A comparison that strips or ignores part of a compound identity. Look for a
helper whose name says it discards information — `nodeFromSlot(slot)` strips the
`#ordinal` — used inside an equality test.

**Where.** Anywhere `node#ordinal` slot strings are handled. `buildSlotSet`,
`leaseOccupiesSlots`, `leasesOverlap`, `leaseContainsNode`. Also: run keys
(`namespace/name`) versus run names, and envelope names, which are unique only within a
Budget.

**How to confirm.** Construct two entities that share the coarse key and differ in the
fine key — two runs on the same node, different GPU ordinals — and check whether one
change affects both. **Sharing a machine is not sharing a slot.**

**Specimen (R22).** The reclaim sweep compared `nodeFromSlot(slot)` against the spare's
node set, so a node-failure swap for run A closed run B's funded, co-located lease
unconditionally. See the comment now at `run_controller.go:1240-1252`, which also records
the correct ruling: an exact-slot conflict with *funded* work means **decline the swap**,
because choosing between funded runs belongs to `pkg/resolver`, which ranks by class.

---

### 6. GUARD BEFORE PREDICATE — a `continue` that runs before the test that makes it relevant

**Tell.** A `continue` / `skip` / early-`return` filter placed **before** the predicate
that decides whether this item is even ours to consider. The filter then silently
suppresses items that should have set a "we handled it" flag.

**Where.** Any loop with both a role/type filter (`if lease.Spec.Slice.Role == RoleSpare
{ continue }`) and a match predicate (`if !leaseContainsNode(lease, nodeName) { continue }`).
**Order matters.** Also any `handled = true` / `found = true` flag: prove it is set on
every path that in fact handled something.

**How to confirm.** Feed the loop an input matched *only* by the skipped category. Does
the function report "nothing found"? Now check what the caller does with that report.

**Specimen (R25).** `continue` on `Role == Spare` sat before the node-match test, so a
node holding **only** a spare matched nothing, `handled` stayed false, and the function
returned `fmt.Errorf("no active lease found on node %s")`. Compounding it, the caller
**string-matched that error text and swallowed it**. The spare's lease stayed open
forever, charging a budget, pointing at a deleted node. Two bugs, one line apart, and
the composite was invisible.

**Corollary — SWALLOWED SENTINEL.** Grep for `strings.Contains(err.Error(), ...)`. An
error compared by its *text* is not an error; it is a coupling. Errors must be typed
sentinels compared with `errors.Is`, and every swallow site must enumerate exactly what
it swallows.

---

### 7. CLONED OBLIGATION — the same duty implemented twice, drifting

**Tell.** Two code sites that perform the same multi-step obligation with slightly
different steps. One gets fixed; the other does not.

**Where.** `closeRunLeases` (`run_controller.go:1425`) closes a run's open leases. So
does `cleanupDeletedRun` (`controllers/kube/reconcilers.go:98-105`) — hand-rolled, three
raw field assignments, bypassing `closeLease` entirely. So does `applyResolution:1566`.
Three implementations of "close a lease"; only one of them can be instrumented, metered,
or fixed in a single place.

**How to confirm.** Add a side effect (a metric, a log, an event) to the canonical
implementation. Grep for state changes that do not produce it. Anything that mutates
`Status.Closed` without going through `closeLease` is a clone.

**Specimen.** Both rogue sites above exist in `main` today. This is why the sole-closer
AST lint (`hack/antifake/`) is a rail and not a style preference: it makes the clone
*impossible to add*, rather than merely *discouraged*.

---

### 8. THE TEST ASSERTS THE BUG

This is the class that defeats every other rail, so it gets a procedure rather than a
paragraph.

**The problem.** Specimen 1 shipped past a test literally named *"the spare must not be
consumed"*. Specimen 4 (R21) had an envtest scenario and an e2e smoke script that both
triggered the swap **by running `kubectl cordon`** — that is, the suite's *mechanism for
provoking the behaviour* was the corruption itself. A green suite was proof the
corruption worked.

You cannot detect a defective test from inside the test. It is internally consistent: it
asserts a state, and the state obtains. The only detector is **an oracle the test author
did not write**.

**The mechanical procedure. Run all five.**

1. **Read the assertions, not the name.** `TestNodeFailureSwapsToSpare` tells you what
   the author *believed*. Read what it asserts. Ask: "if the system were broken in the
   way I suspect, would this test still pass?" If yes, the test is not evidence.

2. **Find the trigger and ask whether the trigger is legitimate.** How does the test
   provoke the behaviour? R21's suite cordoned a node. Cordoning is not failing. **A test
   whose stimulus is invalid proves nothing about the response.** This is the single
   highest-yield question on this list.

3. **Read every assertion as a claim about the world, then check it against the domain,
   not against the code.** "The spare must not be consumed" — is that *desirable*? A run
   that has lost a rank and will never recover it should not also be sitting on a spare.
   The assertion was self-consistent and wrong. When code and test agree, they may simply
   share an author's misconception. Check both against `docs/project/quota-semantics.md`
   and the concept docs, which are binding.

4. **Mutate the fix.** Delete the line you are relying on — the `closeLease` call, the
   `handled = true`. Re-run the test that supposedly covers it. **If it still passes, the
   test does not test the fix.** This caught a real hole: an early version of the
   specimen-1 test passed against the bug, because a *different* post-loop sweep happened
   to close the spare. Only the checkpoint-grace variant (where the run parks `Pending`
   and is never swept) actually isolated the fix.

   Note the limit, and state it in your review if it applies: **mutation testing cannot
   find omissions.** There is no mutation operator for code that was never written.
   Specimen 1 was *missing* code. Mutation testing is how you check that a *defense* is
   load-bearing; the invariant oracle is how you find what is *absent*.

5. **Run the invariant oracle.** `pkg/invariant` asserts, at the end of every engine entry
   point, properties the test author never wrote: a terminal run holds no open leases; a
   `Running` run holds at least `minRunnableGPUs`; closure is monotone. With that hook
   installed, a test that asserts the bug goes red **inside the call**, before its own
   assertions run. *Green stops meaning "asserted" and starts meaning "asserted and
   legal."*

---

## Refutation rules

The skeptic's job is to refute, and the default under uncertainty is `refuted=true`.
But the following are **not** valid refutations, and a skeptic offering one has failed
the task:

- **"The test suite passes."** See class 8. The suite passed for all six specimens.
- **"This is pre-existing."** Say so explicitly and *keep the finding on the table* as
  pre-existing. A defect the change did not introduce is still a defect. Only refute as
  pre-existing when the change also does not **worsen its consequences or reachability** —
  and note that making a dead path reachable *is* worsening it. That is precisely what
  specimen 1 was: R25's immortal-lease class, newly reachable through a new door.
- **"The comment says it cannot happen."** See class 2.
- **"It would require an unusual sequence of events."** A gang scheduler runs for months.
  State the sequence and estimate whether a cluster sees it in a year. Node failures,
  cordons, budget-window rollovers, and controller restarts are all routine.
- **Silence.** A skeptic who returns no usable verdict is not a vote. The harness enforces
  quorum, and an under-quorum finding is surfaced as `UNRESOLVED`, never as refuted.

## The rails that make each class mechanical

Prose does not enforce anything — that is class 2, and this document is prose. Each class
above is therefore backed by something that **runs**:

| Class | Rail |
|---|---|
| 1 Immortal lease | `pkg/invariant` I1 (terminal cleanliness), hooked into every engine entry point; `settleLeases` sweep, where a sweep-closure **fails CI** |
| 2 Comment-as-enforcement | Make it true by construction, as `failRun` now does |
| 3 Last-writer-wins | The lease-shuffle permutation test |
| 4 Signal ≠ reality | `nodeFailed` takes no clock; requires a fencing assertion |
| 5 Identity coarsening | Slot-granular comparison; `buildSlotSet` |
| 6 Guard before predicate | Typed sentinel + `errors.Is`; no `strings.Contains` on errors |
| 7 Cloned obligation | The sole-closer AST lint in `hack/antifake/` |
| 8 Test asserts the bug | The invariant oracle — an assertion the test author did not write |

If you find a defect whose class is not on this list, **add the class**. This file is
append-only in spirit: the taxonomy grows by exactly one entry each time the system
surprises us.
