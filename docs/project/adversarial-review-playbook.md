# Adversarial review playbook

> **Mandatory reading for any agent reviewing a change to the funding engine, the
> scheduler plugin, or another sole-committer path.** `.claude/workflows/adversarial-review.js`
> injects a pointer to this file into every lens and every skeptic. Read it in full
> before you begin. It is not background colour; it is the distilled record of seven
> real defects found on this exact path, in a row, and it tells you where to look.

## Why this exists

Adversarial review has caught a real, merge-blocking defect on **seven consecutive
changes** to the node-failure and funding paths. Not seven sloppy changes — seven careful
ones, each written after reading the last one's post-mortem. The seventh was written by
the author of this file, thirty-eight minutes after committing it, and it violated the
rule on line 59 below. That rate is not a
statement about the authors. It is a statement about the *shape of the system*: it
has a small number of load-bearing invariants that the type system does not encode,
the compiler does not check, and a passing test suite does not establish.

A reviewer who reads a diff and asks "does this look right?" will approve every one of them.
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

Read the comment in `failGroupWithoutSpare` (`controllers/run_controller.go`). It was written
by the fix for specimen 1 and states the stakes precisely:

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
| 1 | `controllers/run_controller.go` — `HandleNodeFailure`, `applyResolution`, `failGroupWithoutSpare`, `failRun`, `reclaimSquatter`, `releaseRun` | Every specimen but one lived in this file. Multi-branch functions that close leases. |
| 2 | Any function that writes `Lease.Status.Closed` | Only `controllers.CloseLease` may, and `hack/antifake/soleclose.go` fails the build otherwise. Zero allowlist. If you are here to add an exception, you are the finding. |
| 3 | Any function that writes `run.Status.Phase` | Phase is a lattice **inside `HandleNodeFailure`** and a state machine everywhere else. That asymmetry has now produced the same bug seven times: see [history-run-phase-writers.md](history-run-phase-writers.md) and class **LAST-WRITER-WINS**. |
| 4 | `controllers/kube/reconcilers.go` — `NodeReconciler` | Where a Kubernetes signal is translated into a claim about physical reality. See class **SIGNAL ≠ REALITY**. |
| 5 | `pkg/resolver/` and its caller `applyResolution` | The resolver decides *who dies*. Its `Scope` filter means it sees a subset of leases; the caller then reasons about the whole run. |
| 6 | The test that "proves" the change | See class **THE TEST ASSERTS THE BUG**. This is the one that gets everyone. |

**Cite by name, not by line.** Every line number in an earlier draft of this file had rotted within a
day. Name the function and quote the comment; both survive a rebase, and a reader who cannot find them
has learned something too.

Two structural facts make confirmation cheap, and you are expected to use them:

- **The engine is pure.** `RunController` performs no I/O. `ClusterState` + a static
  clock *is* a simulator. Any hypothesis about engine behaviour can be turned into a
  compiled, running test in minutes. **Do not speculate when you can execute.**
- **There is one choke point.** Every production mutation goes through
  `Bridge.WithWorld` (`controllers/kube/bridge.go:101`), which already deep-copies a
  before-snapshot for its diff. Before/after invariants are free there.

## The taxonomy

Nine classes. For each: the **tell** (what you can mechanically scan for), **where**
it lives in this repo, **how to confirm** it, and the **specimen** that proves the
class is real.

---

### 1. IMMORTAL LEASE — an exit path that does not discharge its obligation

**Tell.** A function with more than one `return`, where *some* returns are preceded by
`CloseLease`/`releaseRun` and others are not. Or: a status assignment to a terminal
phase (`RunPhaseFailed`, `RunPhaseComplete`) that is not accompanied by a closure call.
Or: a new caller added to an existing function whose closure behaviour was correct only
for the old callers.

**Where.** Every branch of `HandleNodeFailure`. Every arm of `applyResolution`'s final
`switch`. `failRun`. `completeRun`. `reclaimSquatter`. Any new helper factored out of one
of these.

**How to confirm.** Build a `ClusterState` in which the run reaches that branch. Drive
`Reconcile` twenty times over twenty simulated hours. Assert the lease is closed. If it
is still open, you have it — and note that *no test in the repo will have failed*.

**Specimen.** The decline-the-swap path called `failGroupWithoutSpare`, which closed the
failed active lease and left the run's **own spare** open forever. Reproduced by a judge:
20 reconciles, 20 simulated hours, still open, still deriving `Owned`.

**The suspects named here when this file was written were both real.** `applyResolution`'s
terminal branch set `RunPhaseFailed` and never swept the run's leases; `activeGPUsForRun`
skipped spares, so a run whose last open lease was a spare took that branch and left it open
forever. Both fixed. Neither had a test. *Write the suspect down, then go and settle it — the
note is not the work.*

**And a later specimen, worth more than the first.** The fix for a *different* immortal-lease
finding made a dead pod-eviction live — and being live, it deleted the victim's whole pod set
while closing one lease. The run was left `Running` with an open lease and **zero containers**,
still open after twenty simulated hours. `INV-WIDTH-ASSEMBLED` counts *leases*, not pods, so the
oracle was structurally blind to it. **A fix for this class can create this class.**

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
See `failRun`.

**Variant — a re-read that does not hold across its use.** A check can *look* like
enforcement — it re-reads the very thing it guards — and still be prose, because its
result is consumed after the code crosses a lock or a blocking call the read did not
hold across. The world moves in that window and the stale answer drives a real
action. A check that races the thing it guards is a comment, not a guard.

*Where.* Grep for a `Get`/`List` whose result is *used after* a `WithWorld`, a mutex
acquire, or any blocking call the read did not span. In this engine the bridge mutex
serializes every decision, so a read taken before it is a read about a world that can
have moved by the time it is used.

*Specimen.* `NodeReconciler` read a Node, decided *fenced*, then blocked on the bridge
mutex behind a slow admission pass; a Node recreated under the same name in that window
had its FRESH leases closed as a failure (#36/#53). The re-read that supposedly *closed*
#36 was itself outside the mutex — it narrowed the window, it did not close it. The fix
re-takes the verdict INSIDE `WithWorld`, the only read whose answer cannot change before
it is used; the outside read is explicitly "a filter, not a verdict". See
`NodeReconciler.fenced` and the deterministic `racyReader` test (first `Get` says gone,
every later `Get` says alive — no sleep, no envtest). **Audited 2026-07-10: this is the
only live instance in the reconcilers.** The budget reconciler reads lock-free but writes
only self-correcting status (funding is always re-derived from open leases, never read
back from `Budget.Status`), and R12's finalizer read drives resourceVersion-guarded
metadata while its one destructive act — closing the run's leases — runs under the lock.

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
with a dead, uncovered rank could report `Running`. It was then **reintroduced**, by the
same author, in the very commit that added the permutation rail above — see
[the full history of this one field](history-run-phase-writers.md), which is the best
single argument in this repo that a convention is not a rail. The fix was a severity-ranked
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
unconditionally. See the comment beginning *"R22 — reclaim at SLOT granularity"* in
`HandleNodeFailure`'s pass 2, which also records
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

**Where.** There were once three implementations of "close a lease": `closeLease`,
`applyResolution`'s inline field assignments, and `cleanupDeletedRun`'s. Only one could be
instrumented, metered, or fixed in a single place. All three are now `controllers.CloseLease`,
and `hack/antifake/soleclose.go` fails the build on a fourth.

Look next at the *other* cloned obligations: `leaseGroupIndex` existed twice (in `controllers`
and in `pkg/resolver`) and **both defaulted a missing group to `"0"`**, which hid R28b for
months. `groupIndexForPodIndex` is a second implementation of `pack.deriveGroups`, pinned to it
by a test. Two implementations of one rule always drift; the question is only whether something
notices.

**How to confirm.** Add a side effect (a metric, a log, an event) to the canonical
implementation. Grep for state changes that do not produce it. Anything that mutates
`Status.Closed` without going through `closeLease` is a clone.

**Specimen.** The two rogue closure sites shipped for months. This is why the sole-closer AST
lint (`hack/antifake/soleclose.go`) is a rail and not a style preference: it makes the clone
*impossible to add*, rather than merely *discouraged*. Zero allowlist — an allowlist is a door,
and this class exists because doors keep being found.

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

4. **Mutate the fix.** Delete the line you are relying on — the `CloseLease` call, the
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

---

### 9. HALF-PLANE ACTION — an eviction, or a release, performed in one plane only

**Tell.** Code that closes a Lease without deleting the Pod, or deletes a Pod without closing the
Lease. More generally: any state change that has a representation in *two* places — the **ledger**
(Leases, budgets, funding class) and the **workload** (Pods, containers, the kubelet's view of a GPU)
— but is written to only one.

Grep for `CloseLease(` and ask, at each site: *whose container is still running?* Grep for
`State.Pods = ` and ask: *whose lease is still open?*

**Where.** `HandleNodeFailure`'s reclaim of a squatter. `applyResolution`'s terminal branch (it drops
the pods of `closedGroups`, but the sweep closes leases whose groups are not in that set).
`failRun`. `releaseRun`. `removePodsForGroups`. `removeRunPodsOnNodes`. `Bridge.apply`, which deletes
exactly the Pods absent from `state.Pods` — that is the mechanism, and the reason forgetting is silent.

**Why it is invisible.** Nothing crashes in either direction, and each direction lies in a different
way:

| Direction | The lie | The consequence |
|---|---|---|
| Lease open, pod gone | the ledger says a GPU is busy | an **immortal lease**: a budget is charged forever for nothing |
| Pod running, lease closed | the ledger says a GPU is free | the engine plans new work onto an occupied GPU; kube-scheduler can never bind it, and the new gang wedges assembling forever while the old containers burn power |

**How to confirm.** After the call, walk both planes for the affected run. The engine is pure, so:
assert on `state.Leases` *and* `state.Pods`. A test that only checks closure reasons will pass through
either lie. Every eviction test in this repo must assert on both.

**Specimen.** `HandleNodeFailure` reclaimed an unfunded squatter with `CloseLease(other,
"ReclaimedBySpare", now)` and nothing else. Its container kept running on the exact `node#ordinal` that
`emitSwapPod` targeted one line later. And a second, in the other direction: a terminally Failed run
released every lease via `closeRunLeases` while `failRun` touched no pods at all, so its ranks kept
holding GPUs the ledger had just handed back.

**The eviction you cannot always perform.** A `PodManifest` names a machine, not a `node#ordinal`,
so "delete the pods of this lease" is not expressible. Evicting at machine granularity means deleting
containers that back the victim's *other* leases — and a run's funding class is per-**lease**, so one
of those may be `Owned`. The correct move is then to **decline the swap**, exactly as the engine
already does for a funded conflict: choosing between funded runs belongs to `pkg/resolver`. A repair
that closes "all the victim's leases" is a reaper, and a judge's fix-probe caught it before it shipped.

**The legal exception, which you must not reap.** The checkpoint-grace window is a *deliberate*
half-plane state: `failGroupWithoutSpare` closes the dead group's lease, parks the run `Pending` with
a `CheckpointDeadline`, and leaves the surviving containers running **so they can write a checkpoint**.
The run is paying nothing and holding GPUs, on purpose, for a bounded time. Any sweep that closes the
gap between the planes must exempt it — and must key that exemption on the deadline, not on the phase.

---

## Refutation rules

The skeptic's job is to refute, and the default under uncertainty is `refuted=true`.
But the following are **not** valid refutations, and a skeptic offering one has failed
the task:

- **"The test suite passes."** See class 8. The suite passed for every specimen on this list.
- **"Pre-existing, therefore the change does not worsen it."** Valid for a *regression*
  review; dangerous for a *correctness* review. On 2026-07-09 this refuted two true findings
  while concealing that the fix under review was **inert in production** — it keyed on a
  label the sole committer never sets. A change that fails to achieve its stated purpose is
  a finding in its own right, whatever the code did before. Ask: *does this fix do anything?*
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
| 9 Half-plane action | Assert on BOTH `state.Leases` and `state.Pods` in every eviction test; R26's ledger auditor |

If you find a defect whose class is not on this list, **add the class**. This file is
append-only in spirit: the taxonomy grows by exactly one entry each time the system
surprises us.
