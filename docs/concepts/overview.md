# Jobtree concepts

This repository implements the Budget, Run, Reservation, and Lease custom resources that underpin the GPU/HPC scheduler described in the golden-thread document. Milestone M1 extends the initial type definitions with live accounting for Budgets—see [`docs/concepts/budgets.md`](budgets.md) for details on envelope headroom, aggregate caps, metrics, and lending. Milestone M2 adds topology awareness and group packing; the planner behavior and heuristics are documented in [`docs/concepts/runs.md`](runs.md).
