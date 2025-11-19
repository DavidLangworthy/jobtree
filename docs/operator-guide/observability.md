# Observability and Alerts

This milestone introduces first-class metrics, dashboards, and alerting for the Jobtree scheduler.

## Prometheus metrics

The controller manager exposes Prometheus metrics on the configured metrics port (default `:8080`). Key series include:

| Metric | Description |
| ------ | ----------- |
| `jobtree_runs_admission_latency_seconds` | Histogram of run admission attempts labelled by outcome (`bound`, `reserved`, `waiting`, `error`). |
| `jobtree_reservations_backlog_seconds` | Gauge tracking forecasted backlog per GPU flavor. |
| `jobtree_resolver_actions_total` | Counter of resolver actions segmented by `kind` (`DropSpare`, `Shrink`, `Lottery`). |
| `jobtree_budgets_concurrency_gpus` | Gauge for owned, borrowed, and spare concurrency per envelope. |
| `jobtree_spares_concurrency_gpus` | Aggregate spare usage per flavor. |

### Scraping

* Helm users can enable the built-in ServiceMonitor via `monitoring.enabled=true`.
* Kustomize overlays under `deploy/kustomize/` integrate with the same chart values to configure scrape intervals.

## Grafana dashboards

A curated dashboard (`deploy/grafana/dashboards/observability.json`) highlights:

* p95 admission latency (histogram quantile).
* Reservation backlog accumulation.
* Resolver action rates broken down by action kind.

When deploying via Helm the dashboard is automatically packaged into a ConfigMap labelled `grafana_dashboard=1`.

## Alerting

PrometheusRule definitions in `deploy/prometheus/rules.yaml` ship two early-warning alerts:

* **JobtreeReservationDelay** – backlog exceeding 30 minutes for 10m.
* **JobtreeResolverStorm** – more than five resolver actions per minute sustained for 5m.

Tune thresholds as you gather production telemetry.

## CLI-based spot checks

For quick diagnostics without Grafana access, the `kubectl runs` plugin surfaces the same information:

```bash
kubectl runs --state cluster.yaml plan train-128
kubectl runs --state cluster.yaml explain train-128
kubectl runs --state cluster.yaml budgets usage
```

Combine these with metrics to triangulate bottlenecks quickly.
