# Unreleased

A batch of fixes from a code-level production-readiness audit -- each backed by a specific file:line finding, not a guess -- plus real day-2 VM operations (cloud-init, SSH forwards, a graphical VNC console) and a real per-operator login for `kairon-ui`. See the "Production gaps" section of README.md for what's still open.

## Added

- **VNC console** (`console.enabled`, off by default): a "Console" button per Machine opens a real graphical VNC session in the browser (noVNC), relayed `browser -> kairon-ui -> a new kairon-node listener -> the VM's local QEMU VNC socket` -- FluxVM's own VNC is a Unix-socket-only, unauthenticated RFB server with no remote-facing endpoint of its own, so this is a real relay Kairon built end-to-end, not a wrapper around an existing FluxVM API. QEMU-backend Machines only (Cloud Hypervisor/Firecracker have no display device), and the dashboard now hides the button proactively (via the new `GET /api/v1/config`) rather than only failing after the click. Gated by kairon-ui's normal operator auth plus a single-use connection ticket plus a shared cluster-wide token to kairon-node -- read SECURITY.md's "VNC console" section before enabling on a shared cluster, since FluxVM's socket itself has no auth of its own. Every ticket is now bound to the username that requested it, and each session logs a `uiapi console opened`/`uiapi console closed` audit line (username, namespace, name, duration). The kairon-ui-to-kairon-node hop can now optionally run over one-way TLS (`console.tls.enabled`/`console.tls.secretName`) instead of plaintext HTTP. `scripts/deploy-remote.sh --with-ui --with-console` (`--console-port` to override the auto-picked port) wires the same feature end to end on the bare-metal install path.
- `kairon-ui` real username/password login: bcrypt-hashed accounts (`ui.auth.users`, or a pre-created Secret via `ui.auth.existingSecret`), a two-step "Apple ID style" sign-in screen (brand header, a one-line "what is Kairon" tagline, and a "Connecting to `<host>`" indicator so an operator juggling multiple deployments knows which one they're signing into), signed 12-hour session tokens, a working `POST /api/v1/auth/logout`, and per-operator attribution in the audit log -- closing this project's longest-standing known auth gap. A fresh `helm install` with no `ui.*` values now seeds one default `admin` account with a random, generated-once password (see NOTES after install/upgrade) instead of failing or coming up unauthenticated. New `kairon-ui -hash-password PASSWORD` mode generates a hash for `ui.auth.users`. The legacy shared `ui.token` keeps working unchanged, accepted alongside the new login. The login screen itself is now a two-column layout (brand/tagline panel + card) with a per-step transition, an avatar on the password step, and a proper loading/error state. Repeated failed logins against one username are now rate-limited (5 failures -> 5-minute lockout, tracked per requested username including unknown ones so the lockout itself can't be used to enumerate accounts).
- `spec.cloudInit` (`hostname`, `user`, `sshAuthorizedKeys`, `packages`, `runCmd`): forwards operator-supplied guest customization into FluxVM's own cloud-init seed image, previously entirely unreachable through Kairon (only `staticNetwork` was exposed). New `kaironctl create --hostname/--user/--ssh-key/--package/--runcmd` flags and matching dashboard create-form fields. Only applied at Machine creation -- see `docs/guides/machine-network.md`.
- `kaironctl create --forward=hostPort:guestPort[/proto]` (repeatable) and a dashboard create-form field for `spec.network.forwards` -- the SLIRP port-forward mechanism already worked end-to-end, it just had no way to set it besides hand-writing a Machine manifest.
- `kairon-ui` now logs every mutating `/api/v1/...` request (method, path, remote address, resulting status), including rejected-auth attempts against destructive routes -- a partial fix for having had no audit trail at all.
- `ui.existingSecret` / `ui.existingSecretKey`: reference a pre-created Secret for the dashboard token instead of passing it through `helm --set`/stored release values, mirroring the escape hatch `migration.tlsSecretName` already requires.
- CPU/memory `resources:` requests and limits on all three workloads (`controller.resources`/`node.resources`/`ui.resources` in `values.yaml`), in both the Helm chart and the raw `deploy/*.yaml` manifests.
- `govulncheck` in CI's `lint` job.

## Changed

- `kairon-controller`/`kairon-node` ClusterRoles no longer grant the `update` verb on Kairon CRDs -- `internal/kube.Client` only ever issues `PATCH`, never a full-object `PUT`.
- `controller.go`/`agent.go` no longer silently swallow a *secondary* status-patch failure (the follow-up write that records a reconcile error) -- both the primary and secondary failures are now logged.
- `go.mod` gained `golang.org/x/crypto` (bcrypt password hashing for `kairon-ui`'s new login) and `github.com/coder/websocket` (the VNC console relay; stdlib `net/http` has no WebSocket support) -- otherwise still a dependency-light module (Prometheus client + stdlib + these two additions).
- `web/package.json` gained `@novnc/novnc` (the browser VNC/RFB client the new Console view renders into a `<canvas>`).
- `kairon-node`'s VNC console listener no longer takes the whole node agent down if it can't bind its port (e.g. a port collision on a shared host) -- found via a real deployment, where this silently stopped all Machine reconciliation, not just the optional console feature. Fixed by checking the bind synchronously and logging-and-skipping on failure instead of canceling the process.
- Every listen port the Helm chart's workloads use is now a `values.yaml` setting, not a compiled-in default: `controller.healthPort` (8080) and `node.healthPort` (8081) join the already-configurable `migration.port`/`console.port`, and `ui.service.port` now also drives the container's actual internal `-listen` port, not just the Kubernetes Service. Prompted directly by a real port collision (`console.port`'s default 8090 was already in use by an unrelated Docker container on a real host).

# Kairon v0.4.0

Kairon v0.4.0 turns v0.3.0's secure migration *control plane* into a working live-migration *backend*, and adds the operational tooling (metrics, alerting, a concurrency quota, a web dashboard) needed to actually run it. No CRD version bump; every new field is additive.

## Added

- A real FluxVM-backed migration adapter (`cmd/kairon-migration-adapter-fluxvm`), implementing the destination/source adapter contract (`docs/migration-adapter.md`) against real FluxVM migration/QMP endpoints -- previously only a defined contract with a test-double stub (`cmd/kairon-migration-adapter-stub`).
- Per-migration migration network selection (`spec.migrationNetwork`) and adapter-side `-migration-network name=ip` address policy.
- Opt-in authenticated/encrypted migration data-plane transport (`migration.dataplaneTls`, adapter `-migration-data-tls`), with `MachineMigration.status.dataPlaneEncrypted` reporting whether a given migration's RAM/state stream was actually encrypted (distinct from the always-on mTLS control-plane RPCs). A loud startup warning logs from the adapter whenever data-plane TLS is disabled.
- Operator recovery commands for `NeedsRecovery` (`kaironctl recover`), with CRD schema for `spec.recovery`/`status.recovery`, and `docs/runbook-migration-failures.md` documenting the decision tree.
- `kairon-ui`: an optional web dashboard (Go backend in `cmd/kairon-ui` + `internal/uiapi`, a React/TypeScript SPA in `web/`) for Machines, Migrations, Snapshots, and operator-attested `NeedsRecovery` recovery. Static bearer-token auth, opt-in via `ui.enabled` in the Helm chart (first Service/Ingress in this chart).
- Prometheus metrics (`internal/metrics`) on `kairon-controller`'s existing health port: migration counts by phase, phase age, transfer/downtime duration histograms, completion counters, and a data-plane-encryption gauge. Example alert rules (`charts/kairon/alerts.yaml`), optionally rendered as a `PrometheusRule` via `metrics.prometheusRule.enabled`.
- `migration.maxConcurrentPerNode` / `migration.maxConcurrentCluster`: an opt-in per-node/cluster concurrency quota on non-terminal migrations, admitted through the existing `Blocked` phase.
- `internal/integration`: a CI-runnable test package driving the controller and node agent together through a real migration pipeline (Starting → Running → Cutover → Adopting → Succeeded, and the `NeedsRecovery` path), against one shared fake Kubernetes backend.
- `docs/runbook-multi-host-migration-test.md` and `docs/runbook-recovery-drill.md`: real two-host live-migration testing and a live `NeedsRecovery` drill, as runbooks and helper scripts.
- `systemd/kairon-migration-adapter-fluxvm.service` and `scripts/gen-migration-mtls-certs.sh`: closes a gap in `scripts/deploy-remote.sh`, which previously only supported installing the test-double adapter stub.
- Apache-2.0 SPDX headers across source and manifests; the project is now open-sourced under zyvor.dev.

## Changed

- `go.mod`'s `go` directive is now `1.27.1` (previously `1.23`); CI's `go-version` pins updated to match.
- `Dockerfile` gained a `ui` build target (a Node stage building `web/dist`, copied into the same distroless image as the Go binary) and its base Go image was bumped to `golang:1.27-bookworm`.
- CI gained a `web` job (typecheck/test/build the dashboard) and a fourth `container` build step for the `ui` image target.

## Runtime boundary

A real migration adapter now exists (see Added, above), but it is not installed automatically by `scripts/deploy-remote.sh` or the Helm chart today -- see `docs/runbook-multi-host-migration-test.md` for how to deploy and wire it up. Real two-host live migration and a live `NeedsRecovery` drill are documented as runbooks with helper scripts but have not yet been exercised against real hardware in this repository's own CI, which has no second host.

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
