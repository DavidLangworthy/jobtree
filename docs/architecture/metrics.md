# Metrics inventory

| Metric | Type | Labels | Description |
| ------ | ---- | ------ | ----------- |
| `jobtree_runs_admission_latency_seconds` | histogram | `flavor`, `result` | Time to admit or reserve a run. `result` ∈ {`bound`,`reserved`,`waiting`,`error`}. |
| `jobtree_reservations_backlog_seconds` | gauge | `flavor` | Forecasted backlog until pending reservations can start. |
| `jobtree_resolver_actions_total` | counter | `kind` | Structural actions performed by the resolver. |
| `jobtree_budgets_concurrency_gpus` | gauge | `owner`,`budget`,`envelope`,`flavor`,`class` | Current concurrency split into owned/borrowed/spare. |
| `jobtree_spares_concurrency_gpus` | gauge | `flavor` | Aggregate spare usage across envelopes. |

The helm chart surfaces these metrics via a ServiceMonitor; see [docs/operator-guide/observability.md](../operator-guide/observability.md).
