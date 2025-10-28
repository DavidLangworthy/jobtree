# Runs, groups, and topology-aware packing

This document explains how Run specifications translate into placement groups and how the
pack-to-empty planner chooses fast-fabric domains.

## Topology vocabulary

Nodes contributing GPUs must expose the following labels:

| Label key            | Meaning                                 |
|----------------------|-----------------------------------------|
| `region`             | Geographic region of the cluster        |
| `cluster`            | Logical cluster identifier              |
| `fabric.domain`      | Fast-fabric island (e.g., NVSwitch pod) |
| `rack` (optional)    | Rack identifier for tie-breaking        |
| `gpu.flavor`         | GPU flavor provided by the node         |

The topology cache (`pkg/topology`) ingests these labels, groups nodes by
`(region, cluster, fabric.domain)`, and tracks free versus used GPUs per node
and per domain. Each domain exposes deterministic ordering so the planner can
prefer existing allocations and deliver reproducible plans.

## From Run spec to placement groups

A Run specifies total GPU demand and optional locality hints:

```yaml
spec:
  resources:
    gpuType: H100-80GB
    totalGPUs: 128
  locality:
    groupGPUs: 32          # optional
    allowCrossGroupSpread: true
```

The planner interprets the spec as follows:

- When `groupGPUs` is set, it creates fixed-size groups (with a smaller final
  group if the total is not a multiple). Each group must remain within a single
  fast-fabric domain.
- When `groupGPUs` is omitted, the planner forms chunks dynamically while
  traversing domains. It fills one domain to empty before spilling into the
  next, minimizing cross-domain cuts.
- `allowCrossGroupSpread=false` forces all groups to reside in the same domain;
  the planner fails fast if no domain has enough free GPUs.

Elastic runs extend the same translation: when `spec.malleable` is present the
controller keeps group semantics identical while deciding whether to materialise
additional groups or end high-index groups. `desiredTotalGPUs` acts purely as a
target—funding and placement remain per-group decisions.

## Funding & borrowing

Runs can optionally describe how additional GPUs should be funded:

- `funding.allowBorrow: true` enables the scheduler to look beyond the owner's
  budgets once family sharing is exhausted.
- `funding.maxBorrowGPUs` caps how many GPUs may be borrowed in a single
  admission or elastic growth step.
- `funding.sponsors` lists the preferred lending teams; each lending budget must
  set `spec.envelopes[].lending.allow: true` and authorise the borrower via
  `lending.to`.

Every Lease records the paying envelope's owner, and `Run.status.funding`
summarises the live split between owned and borrowed GPUs/GPU-hours so chargeback
and dashboards remain accurate.

## Pack-to-empty heuristics

The packing engine (`pkg/pack`) uses three rules:

1. **Match flavor and location.** Only nodes whose `gpu.flavor` equals the Run
   flavor participate; domains lacking required labels are rejected.
2. **Fill one domain before moving on.** Domains are sorted by free GPUs
   (descending, ties broken deterministically). For group-aware runs the planner
   keeps assigning to the same domain until it no longer has capacity, then
   moves on.
3. **Assign GPUs to nodes deterministically.** Within a domain, nodes are sorted
   by free GPUs and then by name. Allocations consume the largest available node
   first so the residual fragment is always well defined.

The resulting `Plan` object reports:

- groups with their domain and per-node allocations,
- total GPUs requested, and
- residual free GPUs per domain (helpful for forecasting and reservations).

Unit tests in `pkg/pack` mirror the worked examples to ensure the heuristics are
stable as topology edge cases are added.

## Worked examples

See [`docs/examples/worked-examples.md`](../examples/worked-examples.md) for
end-to-end scenarios. The planner unit tests reuse the same shapes so failures
point back to user-visible stories.
