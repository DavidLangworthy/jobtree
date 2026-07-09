# R28 — Placement-group identity never reaches the ledger or the pods

**Priority:** R28a P0 (a ledger-only eviction, silently) · R28b P1 (a shipped feature that does nothing)
**Status:** R28a **fixed** (`fix/r27-invariant-oracle`) · R28b **open**, needs design
**Found by:** the adversarial review of `98b602d` raised it as *critical* and its three skeptics all died on the session quota before voting. Confirmed by hand, then reproduced.

## The one-line version

`spec.locality.groupGPUs` places a run's ranks in distinct fast-fabric groups, and then the
system throws the group identity away — twice. **Every Lease in a real cluster carries no group
index, and every pod carries `"0"`.**

## Evidence

The **sole committer** mints leases through exactly one function, and it stamps two labels:

```go
// pkg/admission/admission.go:216-224
return v1.Lease{
    ObjectMeta: v1.ObjectMeta{
        Namespace: run.Namespace,
        Name:      name,
        Labels: map[string]string{
            binder.LabelRunName: run.Name,
            binder.LabelRunRole: role,
        },
    },
```

`binder.buildLease` (`pkg/binder/binder.go:328`) *does* stamp `LabelGroupIndex` — and is dead
post-cutover, called only from `pkg/binder`'s own materializer.

On the pod side, `emitCohortPods` and `emitSparePods` **hardcode** the label:

```go
binder.LabelGroupIndex: "0",
```

and `emitIntentPods` flattens the packer's groups before it ever gets there:

```go
// controllers/run_controller.go:1877
created := c.emitCohortPods(run, flattenPackNodes(packPlan), gpusPerPod, width, "0", "Start", nil)
```

`flattenPackNodes` walks `plan.Groups` and returns a flat `[]string` of node names. The packer
computed the groups. The emitter discards them.

## R28a — the half-plane eviction (FIXED)

Five consumers read a group index off a Lease. Three of them **defaulted a missing label to `"0"`**:

| Consumer | Behaviour on a missing label |
|---|---|
| `pkg/resolver/resolver.go:273` `leaseGroupIndex` | defaults to `"0"` |
| `controllers/run_controller.go` `collectElasticGroups` | defaulted to `"0"` |
| `HandleNodeFailure`'s `groupIndex` | read raw → `""` |
| `findSpareLease`'s comparison | read raw → `""` (compared `""` to `""`, so it worked by accident) |
| `reclaimSquatter` | read raw → `""` |

The mismatch is invisible until a *lease*-derived group is used to select *pods*, whose label
really is `"0"`. That is exactly what `reclaimSquatter` does:

```
reclaimSquatter removes pods for group ""   ->  matches nothing
                                            ->  the ledger frees the slot,
                                                the container keeps running.
```

So the both-planes eviction added in `98b602d` — written precisely to stop a ledger-only eviction —
**was itself a ledger-only eviction in production.** Its test passed because the fixture set a label
the real system never sets. *A test whose fixture is richer than reality proves nothing about reality.*

This is the **cloned-obligation** class (playbook 7): three implementations of one question, two of
which defaulted and one of which did not.

**The fix.** One `leaseGroupIndex(lease *v1.Lease) string` in `controllers`, defaulting to `"0"`,
used at every lease-side read. Plus `prodLease()` in the tests — a fixture shaped like what
`PodLeaseWithRole` actually mints, with no group label — and a regression test driven by it.

Mutation-tested: reverting to the raw read turns the production-shaped test red, and leaves the
older fixture-rich test **green**. That contrast is the whole point.

## R28b — group identity is degenerate (OPEN)

`leaseGroupIndex` makes every consumer agree. It does not make them *right*: they now all agree that
every lease is in group `"0"`.

The consequences, each verified by reading:

1. **The resolver cannot cut a group.** `gatherCandidates` buckets a run's leases by
   `leaseGroupIndex`, so a multi-group run yields **one** token. The lottery cuts whole runs. The
   per-group lottery, the shrink guard's group arithmetic, and `ActionShrink`'s granularity are all
   nominal.
2. **`collectElasticGroups` sees one group**, so `shrinkRun` gives up capacity in whole-run units.
3. **`emitSwapPod` labels the swap pod with the failed lease's group** — now `"0"` rather than `""`,
   which is at least consistent, but a multi-group run cannot target one group's replacement.
4. **`removePodsForGroups` cannot address a group**, so any per-group pod eviction is either
   everything or nothing.

None of this corrupts the ledger today, because everything is uniformly `"0"`. It means the
multi-group feature is **vestigial in two of the three planes**. `docs/user-guide/` and
`docs/concepts/` describe per-group spares and per-group swaps as shipped behaviour.

### What the fix has to do

Carry the packer's group through both planes:

- `emitIntentPods` must emit per group, not over `flattenPackNodes`, labelling each pod with its
  real group index. `emitSparePods` likewise (the pack plan's `SparePlacements` are already
  per-group).
- The plugin must stamp `LabelGroupIndex` onto the minted Lease from the pod's label. Fail closed at
  PreBind if the pod carries none — every controller-emitted pod will — on the model of the existing
  R5/R6 provenance gates, which already `Error` on missing or forged provenance.
- `leaseGroupIndex`'s `"0"` default then becomes a migration shim, and should be deleted once no
  unlabelled leases can exist. **It must not survive as a permanent silent default**, or it will hide
  the next instance of this bug exactly as it hid this one.

### Breaking-change note

Per the project's standing policy — *never complicate the implementation to support side by side; if
there is a breaking change, schedule it, stop the jobs, and restart* — this is a clean break. Leases
minted before the change carry no group index; after it, every lease carries one. Do not write a
compatibility path.

### The rail

An invariant, once the mint is fixed: **every open Lease carries a group index, and it matches the
group index of the pod that caused it to be minted.** That is a two-plane correspondence — playbook
class 9 — and it belongs in R26's ledger auditor, which sees both planes. A unit test on
`PodLeaseWithRole` is the cheap half; the auditor is the real one.

## Why nobody noticed

Because everything defaulted to the same wrong answer. `"0"` is right for a single-group run, and
single-group runs are what every test fixture and every demo builds. The one consumer that did *not*
default was the one that crossed from the ledger plane into the pod plane, where the disagreement
finally had a consequence — and that consumer was three hours old.
