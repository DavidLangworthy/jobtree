# Jobtree concepts

This repository implements the Budget, Run, Reservation, and Lease custom resources that underpin the GPU/HPC scheduler described in the golden-thread document. Milestone M1 extends the initial type definitions with live accounting for Budgets—see [`docs/concepts/budgets.md`](budgets.md) for details on envelope headroom, aggregate caps, metrics, and lending. Milestone M2 adds topology awareness and group packing; the planner behavior and heuristics are documented in [`docs/concepts/runs.md`](runs.md).

## Three planes, deliberately not aligned

Most quota confusion comes from collapsing these into one. They are separate, and they
are allowed to disagree.

| Plane | What it is | Resource |
|---|---|---|
| **Entitlement** | what an owner may *claim* | `Budget`, its `envelopes` |
| **Ledger** | what is *actually consumed*, as immutable fact | `Lease` |
| **Capacity** | what physically *exists* | GPUs on `Node`s |

**Quota may over-commit or under-commit the hardware, and both are fine.**

*Over-commit* — the envelopes sum to more GPUs than the cluster has. Not everyone can run
at once; the ledger and the resolver decide who does, ranked by funding class.

*Under-commit* — the cluster has more GPUs than anyone's quota covers. The surplus does not
sit dark. Work runs on it as `Unfunded`, and yields the moment a funded claim needs it.

This is the whole design, in one sentence from the internal decision record:

> **Quota is a claim, not a wall — and claims are ranked, not labeled.**

A Budget therefore never *blocks* a Run. It determines what the Run's GPU-seconds are
*classified as* — `Owned`, `Shared`, `Borrowed`, or `Unfunded` — and that class, derived
fresh on every evaluation and never stored, is what decides who yields to whom.
