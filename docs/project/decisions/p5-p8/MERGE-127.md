# Getting #127 in — the shortest safe path

Note requested by David, 2026-07-26: *"What does this mean for #127? We've been on that a long time.
I'd like to get it in."*

**Answer: #127 can land without any of the new mechanisms** — no `Spec.Grants`, no rooted trace, no
subtree conservation, no CRD change. It needs **three small substitutions**, all of which the final
recommendation already specifies and none of which is new design.

## Why it cannot land exactly as it stands

The branch trades one vulnerability for another rather than removing one:

- **What it closes (the point of R7 pt2):** `Run.Spec.Owner` was a self-asserted, forgeable string. On
  main a Run can name any owner. #127 derives the owner from the namespace and deletes the field.
- **What it opens:** the terminal-reservation path committed on the branch is **foreign-inducible** —
  a Budget created in *any* namespace naming your owner flips `OwnerOf(yours)` to `""` and fails your
  reservations. Reproduced by execution. And that path **contradicts ratified text** —
  `quota-semantics.md:27` (exhaustion demotes) and `:128` (*"never as a lottery over funded runs"*) —
  which outranks `R7:126-127` under `AGENTS.md:176`. It is in force by accident rather than by
  decision.

So the merge question is not "is the design finished" but "does the branch strictly improve on main".
With the three substitutions below, it does.

## The pre-merge list

### 1. Terminal reservation → blocked and visible
`controllers/run_controller.go:1216-1226`. Stop calling `failReservationTerminally`. Set an explicit
blocked state instead: durable cause, durable onset, **countdown and backlog gauge cleared**.

This is the smallest change that fixes *both* known defects at once — the original immortal-reservation
bug was **invisibility** (Pending forever with the gauge frozen at 1020), not waiting. Cheap because
the activation loop already filters on `Pending` (`run_controller.go:1066-1068`), so a blocked state is
inert for free, and `ReservationStatus.State` is an unvalidated free string, so adding a value costs
nothing. `ReservationStatus` needs one field for onset (`reservation_types.go:54-70` has none).

### 2. PreBind must stop refusing a gang that already holds a lease
`cmd/scheduler/plugin/gang.go:755-758`. Refusing on `derived == ""` strands a partially minted gang:
leases open, GPUs held, nothing runnable, pods re-emitted forever, hours accruing Unfunded. This was
the CRITICAL finding, and the code's own comment at `:752-754` already concedes the completed leases
bill nobody. Allow completion up to the run's recorded width; refuse only *growth*.

Without a `U` deadline this leaves a below-minimum gang holding GPUs indefinitely on an idle cluster.
That is acceptable for the merge because it is **strictly better than the branch's current state**
(which strands it *and* refuses completion), and `U` is owner residue R3. Note it in the PR as a known
gap rather than pretending it is closed.

### 3. Delete "and resubmit" from the operator message
`controllers/run_controller.go:1216-1226`. The message instructs a human to do the one thing the design
promises they never have to: it contradicts `quota-semantics.md:38-39` (*"Nothing to resubmit, nothing
to approve"*) and the code's own comment thirty lines above claiming recovery is autonomous. One
sentence, and it is a ratified-text violation shipping in a tenant-facing string.

## What the branch still carries after that, stated honestly

**A denial-of-new-work vector remains.** A squatter Budget still flips `OwnerOf(victim)` to `""`, so the
victim cannot fund *new* work until the object is removed. Nothing is destroyed, running work coasts,
and the condition is (once R26 is wired) alarmed.

**This is the trade the design already prefers**, in Fable's words: *fail toward denial, away from
theft.* Blocking new admissions with an alarm is strictly better than silently charging another
tenant's budget. But it is a real exposure that main does not have, because main has no namespace
derivation at all — so it must be **recorded in the PR as accepted and temporary**, with its closure
named: P5's rooted grant trace (owner residue R1). Do not merge this describing the DoS as fixed.

## What is explicitly NOT required for the merge

- `Budget.Spec.Grants`, the rooted trace, root-set config — P5, needs a CRD change, batch it with the
  lease-schema outage `R7:473-475` already schedules.
- `INV-SUBTREE-CONSERVE` and the hierarchical cap — the largest piece of unscheduled work
  (`FINAL-RECOMMENDATION.md` §3.3), independent of this merge.
- The grandparent tier / N-generation family graph — goes onto the roles-generalization track.
- Forward-only accrual anchoring and P3 — P6/Ruling 6; #127 does not make retroactivity worse than
  main, so it is not a merge blocker.
- Wiring R26 to `Conflicts()` — the alarms are all no-ops today (`evaluate.go:176-182`). Needed for the
  *story* above to be true operationally; schedule it next, not before merging.

## Gate

Unchanged: `make verify` green including envtest, the 800-seed quiescence/eviction fuzzer under
`-race`, and each substitution mutation-verified. Add one test per substitution:

1. reservation with no funding principal ⟹ blocked, cause and onset set, **gauge absent** (assert the
   absence — that is what the 2026-07-24 defect was);
2. partial gang below minimum, unbound ⟹ completion to recorded width permitted, growth refused;
3. the operator message contains no "resubmit".

Mutation-verify each by deleting the fix and confirming red. The recovery test on this branch already
had to be repaired once for passing with its repair deleted (`37270af`), so this is not a formality.
