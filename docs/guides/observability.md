# User guide: metrics and alerts

Until now, Kairon's Prometheus metrics were `kairon-controller`-only and
migration-lifecycle-only (`internal/metrics`'s original scope) -- a real
observability gap this page closes the worst of: there was no signal at
all if `kairon-controller`/`kairon-node` themselves were unhealthy or
wedged, no metrics from `kairon-node`/`kairon-ui` at all, and every alert
in `charts/kairon/alerts.yaml` was migration-specific.

## What each component exposes

Every metric name and its exact meaning lives in `internal/metrics`'s
package doc and `Observe*` method comments -- this table is a map to that,
not a duplicate of it.

| Component | Port | New in this pass | Already existed |
|---|---|---|---|
| `kairon-controller` | `controller.healthPort` (`/metrics`) | `kairon_reconcile_duration_seconds`, `kairon_reconcile_errors_total`, `kairon_webhook_decisions_total` (only if `webhook.enabled`), `kairon_apiserver_request_duration_seconds` | `kairon_migration_phase_count`, `kairon_migration_phase_age_seconds`, `kairon_migration_completed_total`, `kairon_migration_transfer_duration_seconds`, `kairon_migration_cutover_downtime_seconds`, `kairon_migration_dataplane_encrypted` |
| `kairon-node` | `node.healthPort` (`/metrics`) | `kairon_reconcile_duration_seconds`, `kairon_reconcile_errors_total`, `kairon_apiserver_request_duration_seconds` | (none -- `internal/health.Server.Metrics` existed but was never wired to anything, so `/metrics` 404'd) |
| `kairon-ui` | its own listen port (`GET /metrics`, unauthenticated -- see `SECURITY.md`) | `kairon_ui_request_duration_seconds`, `kairon_apiserver_request_duration_seconds` | (none -- no health/metrics endpoint of any kind existed) |

`kairon_apiserver_request_duration_seconds` is the one metric all three
share, by design: every component talks to the Kubernetes API through the
same `internal/kube.Client`, so instrumenting it once (`Client.Observe`,
wired to each component's own `Recorder` in `cmd/*/main.go`) gives every
component apiserver call visibility for free, labeled `method`
(GET/POST/PATCH/PUT/DELETE) and `outcome` (ok/error).

`kairon-node`'s metrics needed no chart changes -- its health port was
already exposed as a container port, just never serving `/metrics`.
`kairon-ui`'s did: it has no separate health-check port at all, so its
`/metrics` route shares the same listen port as the dashboard and its
`/api/v1/...` routes, on the same "unauthenticated, like `/healthz`"
convention -- see `SECURITY.md`'s `kairon-ui` section for the trust
boundary that implies (aggregate request counts/latencies, not sensitive
data, but reachable wherever the dashboard itself is).

## Alerts

`charts/kairon/alerts.yaml` now has two rule groups:

- **`kairon-migrations`** (unchanged) -- the original four alerts, all
  pointing at `docs/runbook-migration-failures.md`.
- **`kairon-health`** (new) -- three cross-component alerts with no
  runbook of their own (check the named component's own logs):
  - `KaironReconcileErrorRateHigh` -- more than 5 failed reconcile ticks
    in 15 minutes, on `kairon-controller` or `kairon-node`.
  - `KaironAPIServerErrorRateHigh` -- more than 10 failed apiserver calls
    in 15 minutes, from any component.
  - `KaironWebhookDenyRateHigh` -- more than 20 admission-webhook denials
    in 15 minutes (`webhook.enabled` only). `severity: info`, not `warn`
    -- a denial is the webhook doing its job; a spike usually means a
    `MachineQuota`/`MachineDisruptionBudget` was set stricter than
    expected, not that something is broken.

Neither rule file distinguishes *which* `kairon-controller` or
`kairon-node` instance fired, since `kairon_reconcile_*`/
`kairon_apiserver_*` use the same metric names on every instance's own
`/metrics` -- that's your scrape config's `job`/`instance` labels to add,
same as any other multi-target Prometheus rule.

If you run prometheus-operator, `metrics.prometheusRule.enabled=true`
renders `charts/kairon/alerts.yaml` as a real `PrometheusRule` named
`kairon-alerts` -- **renamed from `kairon-migrations`** now that it covers
more than migrations. On `helm upgrade` from an older release, the
old-named object is orphaned (Helm creates the new name rather than
renaming in place): `kubectl delete prometheusrule kairon-migrations -n
<namespace>` cleans it up. Otherwise, copy the rule groups into your own
Prometheus `rule_files` config directly.

## Real limits today (first cut)

- No distributed tracing -- flagged deliberately in this project's own
  production-readiness review as a design decision to make explicitly if
  it comes up, not something to add by default (it would be a third
  deliberate exception to Go-stdlib-only, after OIDC and CSI).
- `kairon-ui`'s `kairon_ui_request_duration_seconds` now carries a
  `route` label -- the registered mux *pattern* a request matched (e.g.
  `/api/v1/machines/{namespace}/{name}`), never the raw request path, so
  cardinality stays bounded regardless of how many distinct
  Machines/namespaces are ever actually requested, the same way
  `status_class` already bounds it against the full HTTP status range
  instead of the raw numeric code. A request matching nothing registered
  under `/api/v1/` (a genuine 404) reports `route="unmatched"`.
- `kairon_reconcile_errors_total` counts a whole reconcile tick failing,
  not which specific Machine/migration/snapshot inside that tick caused
  it -- check the accompanying "reconcile failed"/"agent stopped" log
  line for that.
