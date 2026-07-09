# Hot spares & opportunistic fill

This guide explains how to request per-group spares, how the controller binds them, and what happens when a node fails while opportunistic work is using those resources.

## Requesting spares

Set `spec.sparesPerGroup` to the number of additional GPUs each placement group should reserve in the same fast-fabric domain.

```yaml
apiVersion: rq.davidlangworthy.io/v1
kind: Run
metadata:
  name: train-64-with-spares
  namespace: demo
spec:
  owner: org:demo:research
  resources:
    gpuType: H100-80GB
    totalGPUs: 64
  locality:
    groupGPUs: 32
  sparesPerGroup: 4
```

The packer allocates those four extra GPUs alongside each 32-GPU group. The cover planner funds both the active ranks and the spares, and the binder emits:

- a regular `Active` lease and pod for the group, and
- a `Spare` lease and pod anchored to the reserved nodes.

A spare is **charged at the full rate**, like any other GPU: the funding derivation
attributes every GPU-hour to a class, spares included. It is reported separately
(`SpareWidth`, `SpareGPUs`) so a future finance policy could discount it, but no such
policy exists today. A spare you hold is a spare you pay for.

## Opportunistic fill

Spare pods are labelled `rq.davidlangworthy.io/role=Spare`, and they are **held live** —
a real pod occupying the reserved slots. Nothing else can be scheduled onto them.

There is no `Borrowed` *role*. `Borrowed` is one of the four funding **classes**
(`Owned`, `Shared`, `Borrowed`, `Unfunded`), derived from budgets and leases, never
stored. It says who paid, not what the GPU is doing.

So "opportunistic fill" is not a special kind of lease. It is ordinary work whose lease
derives the class `Unfunded`: nobody's envelope covers it. That is what makes it
reclaimable, and the funding derivation — not a label — is what decides.

When a node-failure swap needs the exact `node#ordinal` slots some other run occupies:

* if that run's lease derives `Unfunded`, it is reclaimed, closing with
  `closureReason=ReclaimedBySpare`;
* if it derives **any funded class**, jobtree **declines the swap** rather than evict
  it. Choosing between funded runs is the resolver's job, and the resolver ranks by
  class. A failure in one run is not a licence to kill another's funded work.

Sharing a machine is not sharing a slot: only a lease holding the same `node#ordinal`
is a conflict at all.

## Failure swap lifecycle

When a node that backs an `Active` lease fails:

1. `HandleNodeFailure` locates the matching spare lease for the run and group.
2. Opportunistic tenants on those spare nodes are closed with reason `ReclaimedBySpare`.
3. The spare lease closes with reason `Swap`, the failed lease closes with `NodeFailure`, and a new `Active` lease is minted on the spare nodes.
4. Pod manifests are refreshed so the group now runs on the spare node, and `Run.status.message` reports the swap (for example, `group 0 swapped to spare after node failure`).

If no spare is available, the run transitions to the `Failed` phase, signalling that checkpoints should be used to recover work.

## Observability & CLI

The following commands highlight the spare lifecycle:

```bash
kubectl runs submit --file config/samples/runs/run-with-spares.json
kubectl runs watch train-64-with-spares    # shows active + spare leases
kubectl runs explain train-64-with-spares # details swap reasons after a failure
```

The worked examples (scenario 3) walk through a failure with opportunistic fill and show the resulting ledger entries.
