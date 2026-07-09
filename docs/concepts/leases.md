# Leases

A **Lease** is the immutable fact that a Run consumed a slice of the cluster for a period of time.
Every controller, report, and audit folds over Leases to reconstruct history.

## 1. Schema recap

```yaml
apiVersion: rq.davidlangworthy.io/v1
kind: Lease
metadata:
  name: lease-abc-0007
spec:
  owner: org:ai:rai:sys
  runRef:
    namespace: default
    name: train-128
  compPath: ["SHARD[1]", "D"]
  slice:
    nodes: [n17-04#0, n17-04#1]   # node#ordinal — a slot, not a machine
    role: Active                  # Active | Spare
  interval:
    start: "2025-11-01T00:00:10Z"
  paidByBudget: rai-sys          # scopes the envelope name below
  paidByEnvelope: west-h100
  reason: Start                  # why it was MINTED, below
status:
  closed: true
  ended: "2025-11-01T03:33:29Z"
  closureReason: Completed       # why it was CLOSED, below
```

Key rules:

* A Lease never changes once written. Ending it sets `status.closed=true`, `status.ended`,
  and `status.closureReason`.
* `slice.nodes` holds **slots**, not machines: `node#ordinal` names one GPU. Two runs may
  share a node and never share a slot.
* Each Lease lists exactly one payer. `paidByEnvelope` names the envelope;
  `paidByBudget` scopes it, because envelope names are unique only within a Budget and
  one owner may hold several.

`slice.role` says how the GPU is used. There are exactly two:

| Role | Meaning |
|---|---|
| `Active` | contributes to the Run's desired width |
| `Spare` | hot standby, held live, reserved for a node-failure swap |

**`Borrowed` is not a role.** It is a *funding class* — derived, never stored — and it
describes who paid, not what the GPU does. An `Active` lease can be `Borrowed`. See
[budgets](budgets.md) for the four classes (`Owned`, `Shared`, `Borrowed`, `Unfunded`).

## 1a. The two reason fields

They are different questions, and conflating them is how the old version of this page
listed lottery seeds as mint reasons.

`spec.reason` — **why this Lease was minted**. The scheduler plugin writes it once, from
the `rq.davidlangworthy.io/lease-reason` annotation the controller stamped on the pod.
There are four:

| Value | Meaning |
|---|---|
| `Start` | the run's initial gang (the default when no annotation is set) |
| `Grow` | an elastic widening, funded as its own delta |
| `Swap` | a rank re-placed onto a spare after its node was fenced |
| `Promise` | a promised-but-not-yet-funded opportunistic activation |

`status.closureReason` — **why it ended**:

| Value | Meaning |
|---|---|
| `Completed` | the gang finished |
| `NodeFailure` | its node was fenced (deleted, or tainted out-of-service) |
| `Swap` | a spare, consumed to cover a failed rank |
| `SwapDeclined` | a spare, released because the swap it was held for could not proceed |
| `ReclaimedBySpare` | it held the exact slots a swap needed, and was `Unfunded` |
| `RunFailed` | the run went terminal; a failed run holds no open leases |
| `Shrink` | given up by an elastic narrowing |
| `DropSpare` | the resolver converted this spare into active capacity |
| `RandomPreempt(0x…)` | the resolver's lottery chose it; the seed is the attestation |

Note `Swap`, `Shrink`, and `DropSpare` are not symmetric across the two fields. `Swap` is
both: the *new* lease is minted `Swap`, and the *spare* it consumed is closed `Swap`.
`Shrink` and `DropSpare` only ever close a lease — the resolver writes them as it gives
capacity up.

There is no `Fail` reason, and `RandomPreempt` is a closure reason, never a mint reason.
Both were listed as mint reasons in an earlier version of this page; neither has ever
existed in the code as one.

You will not see `Hypothetical` on a real Lease. The engine builds such leases in memory
to ask the funding derivation a what-if question, and never writes them.

## 2. Derived views

Controllers maintain indices on top of Leases:

* **Active index:** Leases with `status.closed=false` per node.
* **Usage index:** owner/envelope summaries for `u_e(t)` (concurrency) and `U_e(t)` (GPU-hours).
* **Run status:** aggregated width, payer split, and lifecycle events used by `kubectl runs watch`.

## 3. Auditing workflows

* “Who used GPU `n17-04` last night?” → `kubectl get leases --field-selector spec.slice.nodes=n17-04`.
* “How many GPU-hours did org:ai:mm:vision spend?” → integrate Lease durations where
  `paidByEnvelope` matches the sponsor.
* “Why was my job preempted?” → find the Lease that ended with reason `RandomPreempt(seed)` and
  correlate with the Reservation activation log (seed + conflict set).

## 4. Failure and spare swaps

When a node fails, spares take over without changing the Run topology:

1. Spare Lease ends with reason `Swap`.
2. Active Lease starts on the spare node with reason `Swap` and the same payer.
3. Opportunistic Leases running on that spare end with reason `ReclaimedBySpare`.

Every step is recorded so SREs can replay the sequence later.

## 5. Best practices

* Treat the Lease set as the source of truth—do not rely on pod logs alone.
* Retain Leases for as long as your compliance program requires (they are lightweight CR objects).
* Emit events or metrics derived from Leases rather than maintaining separate mutable state.

Leases keep the ledger honest and make it possible to answer “who ran where, when, and why?” years
after the fact.
