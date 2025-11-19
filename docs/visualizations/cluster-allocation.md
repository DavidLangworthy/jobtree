# Cluster allocation visualizations

High-signal dashboards make jobtree feel first-class for operators and researchers. This guide
shows how to visualize cluster allocation across Budgets, Runs, Reservations, and Leases.

## 1. Grafana dashboard

The repository ships a base dashboard under `deploy/grafana/dashboards/observability.json`. Extend
it with the following panels:

1. **Fabric-domain heatmap**
   * Query: `sum by (fabric_domain)(jobtree_active_gpu{role="active"})`
   * Visualization: heatmap with domains on the Y axis and time on the X axis.
   * Purpose: highlights saturation and hot spots across islands.

2. **Budget headroom table**
   * Query: `jobtree_budget_headroom`
   * Transform: pivot by envelope.
   * Purpose: lets SREs spot impending quota exhaustion.

3. **Reservation countdown list**
   * Query: `jobtree_reservation_seconds_until_start`
   * Visualization: table sorted ascending.
   * Purpose: surfaces which Runs are about to activate and whether cuts are expected.

4. **Lottery outcomes**
   * Query: `increase(jobtree_lease_preemptions_total[30m])`
   * Visualization: bar chart stacked by `reason` and `owner`.
   * Purpose: audits fairness and the rate of structural cuts vs. lotteries.

Include screenshots of the dashboard in user-facing docs so researchers see the tooling before they
log into Grafana.

## 2. Time-lapse topology map

To make island usage intuitive, generate a topology map that overlays GPU usage on the physical
domain layout.

* Export topology from `pkg/topology/snapshot` as JSON via a debug endpoint.
* Feed it into a D3.js visualization (e.g., simple hex grid) hosted behind the ops dashboard.
* Color nodes by active Lease density, optionally animating reservations.
* Highlight spare pools and borrowed capacity using distinct borders.

Document the map and link to it from onboarding materials.

## 3. CLI snapshots for researchers

Researchers often lack Grafana access. Enhance the `kubectl runs` plugin:

* `kubectl runs state --owner <team>` already emits JSON. Pipe it into `jq` + `gnuplot` to produce
  quick sparkline plots. Document the commands.
* Provide a `--render svg` flag (future work) that produces a simple bar chart per fabric domain.

## 4. Data export

Add a `/metrics/allocations` endpoint that streams newline-delimited JSON snapshots of
`{timestamp, domain, active, spare, borrowed}`. This enables lightweight dashboards (e.g., Superset
or Observable notebooks) without Grafana.

## 5. Next steps

* Capture pre- and post-cut states during reservations to visualize fairness decisions.
* Integrate with an internal data warehouse for long-term trend analysis (GPU-hours per team).
* Provide sample notebooks under `examples/visualization/` that demonstrate the API.
