# Metrics inventory

| Metric | Type | Labels | Description |
| ------ | ---- | ------ | ----------- |
| `jobtree_runs_admission_latency_seconds` | histogram | `flavor`, `result` | Time to admit or reserve a run. `result` ∈ {`bound`,`reserved`,`waiting`,`error`}. |
| `jobtree_forecast_latency_seconds` | histogram | `flavor` | Time spent in `forecast.Plan` — an inline library call inside the `run` reconciler, not a separate controller. |
| `jobtree_reservations_backlog_seconds` | gauge | `reservation`, `flavor` | Forecasted backlog until *this specific* pending reservation can activate. Keyed by reservation (not just flavor) so concurrent reservations of the same flavor do not collapse onto one series; refreshed on every resync while Pending and cleared on activation/release. |
| `jobtree_resolver_actions_total` | counter | `kind` | Structural actions performed by the resolver. |
| `jobtree_budgets_concurrency_gpus` | gauge | `owner`,`budget`,`envelope`,`flavor`,`class` | Current concurrency split by derived funding class (`owned`/`shared`/`borrowed`/`unfunded`) plus the `spare` role, per envelope. |
| `jobtree_spares_concurrency_gpus` | gauge | `flavor` | Aggregate spare usage across envelopes. |
| `jobtree_elastic_grows_total` | counter | `flavor` | Successful elastic grow steps applied by `growRun`. |
| `jobtree_elastic_shrinks_total` | counter | `flavor` | Successful elastic (voluntary or resolver-driven) shrink steps applied by `shrinkRun`. |
| `jobtree_elastic_width_current` | gauge | `run` | A malleable run's current allocated (non-spare) width. |

The in-process controllers expose these metrics via the standard Prometheus text exposition. The helper `pkg/metrics.Handler()` returns an `http.Handler` that can be wired to `/metrics` on the manager so existing Prometheus scrapers keep working. `pkg/metrics.Snapshot()` is the same data in Go form, used by tests to assert metrics vary with real inputs instead of being pinned constants.

The helm chart surfaces these metrics via a ServiceMonitor; see [docs/operator-guide/observability.md](../operator-guide/observability.md).

## Events

Beyond metrics, the manager emits real Kubernetes `Events` (via `client-go`'s `EventRecorder`,
wired in `cmd/manager/main.go` as `mgr.GetEventRecorderFor("jobtree")`) on the affected `Run` at
admission, reservation, activation, resolver action (including the attested lottery/reclaim
seed), node-failure swap, and completion. These are real `corev1.Event` objects — visible via
`kubectl get events` / `kubectl describe run <run>` — not just log lines or a CLI polling loop.
