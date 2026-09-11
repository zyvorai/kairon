# Unreleased

Operational tooling and a web dashboard, built on top of v0.3.0's secure migration control plane. No CRD version bump; `status.dataPlaneEncrypted` and the concurrency-quota fields are additive.

## Added

- `kairon-ui`: an optional web dashboard (Go backend in `cmd/kairon-ui` + `internal/uiapi`, a React/TypeScript SPA in `web/`) for Machines, Migrations, Snapshots, and operator-attested `NeedsRecovery` recovery. Static bearer-token auth, opt-in via `ui.enabled` in the Helm chart (first Service/Ingress in this chart).
- Prometheus metrics (`internal/metrics`) on `kairon-controller`'s existing health port: migration counts by phase, phase age, transfer/downtime duration histograms, completion counters, and a data-plane-encryption gauge.
- Example Prometheus alert rules (`charts/kairon/alerts.yaml`), optionally rendered as a `PrometheusRule` via `metrics.prometheusRule.enabled`.
- `migration.maxConcurrentPerNode` / `migration.maxConcurrentCluster`: an opt-in per-node/cluster concurrency quota on non-terminal migrations, admitted through the existing `Blocked` phase.
- `MachineMigration.status.dataPlaneEncrypted`: whether the live-migration RAM/state stream (not the always-on mTLS control-plane RPCs) was actually encrypted, set from the adapter's own `prepare()` response. A loud startup warning logs from `kairon-migration-adapter-fluxvm` whenever data-plane TLS is disabled.
- `internal/integration`: a CI-runnable test package driving the controller and node agent together through a real migration pipeline (Starting → Running → Cutover → Adopting → Succeeded, and the `NeedsRecovery` path), against one shared fake Kubernetes backend.
- `docs/runbook-migration-failures.md`: how to diagnose and resolve `NeedsRecovery`, cross-referenced from the new alerts.
- `docs/runbook-multi-host-migration-test.md` and `docs/runbook-recovery-drill.md`: real two-host live-migration testing and a live `NeedsRecovery` drill, as runbooks and helper scripts (not yet exercised against real hardware in this repository's own CI, which has no second host).
- `systemd/kairon-migration-adapter-fluxvm.service` and `scripts/gen-migration-mtls-certs.sh`: closes a gap in `scripts/deploy-remote.sh`, which previously only supported installing the test-double adapter stub, not the real one.

## Changed

- `go.mod`'s `go` directive is now `1.27.1` (previously `1.23`); CI's `go-version` pins updated to match.
- `Dockerfile` gained a `ui` build target (a Node stage building `web/dist`, copied into the same distroless image as the Go binary) and its base Go image was bumped to `golang:1.27-bookworm`.
- CI gained a `web` job (typecheck/test/build the dashboard) and a fourth `container` build step for the `ui` image target.

# Kairon v0.3.0

Kairon v0.3.0 replaces the provisional live-migration transport from v0.2 with a secure backend-neutral control plane.

## Added

- TLS 1.3 mutual-authentication node peer service on port 9443.
- Target-first `prepare -> transfer -> commit` migration protocol.
- Atomic per-node session journal with idempotency/conflict detection.
- Unix-socket migration adapter contract for VMM-specific implementation.
- Rollback/abort when source transfer fails.
- `NeedsRecovery` state for ambiguous post-transfer target commit failures.
- Target node `InternalIP` discovery from the Kubernetes Node API.
- Helm settings for migration TLS, state storage and optional adapter socket.

## Changed

- `MachineMigration.spec.destination` was removed. Users select only a target node.
- `auto` uses cold migration until cluster-wide live-backend capability discovery exists.
- Live status uses backend-neutral `sessionID`, `transferID`, `transferPhase`, and `backend` fields.
- The node RBAC role can read Node addresses for peer discovery.

## Removed

- Unverified FluxVM `/migration/start`, `/migration/status`, and `/migration/cancel` client calls.
- User-provided raw `tcp:host:port` migration transport.

## Runtime boundary

The current FluxVM source tree does not expose a verified migration/QMP API. Therefore Kairon's secure orchestration is implemented and tested, but real VM memory-state live transfer requires a migration adapter. Without one, live migration is blocked before source transfer; cold migration is unaffected.
