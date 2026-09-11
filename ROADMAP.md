# Roadmap

## v0.3 — secure migration control plane

- mTLS node-to-node prepare/commit/abort protocol
- atomic destination session journal and idempotency
- no user-supplied migration transport URI
- local Unix-socket migration adapter contract
- rollback before commit and `NeedsRecovery` after ambiguous commit
- adopt-only target cutover
- cold migration, CSI snapshots, DRA/VFIO guard retained

## Network Fabric — FluxVM eBPF edge (in progress)

Kairon declares VM-edge networking; FluxVM owns TAP/TC/eBPF; Fabric proxies dataplane UX.
See [`docs/network-fabric.md`](docs/network-fabric.md).

- **N1** Machine network create parity (`forwards`, `macvtapMode`, `staticNetwork`, `podUID`) + dataplane status projection
- **N2** `MachineNetworkPolicy` + `NetworkSecurityGroup` → FluxVM policy/groups/CNP
- **N3** Live-migration network quiesce / export / restore / resume
- **N4** Service Fabric VIP membership + `dataplaneRequired` fail-closed readiness

## v0.4 — FluxVM live-migration backend

- [x] implement the Kairon migration adapter in FluxVM or a companion host service (`cmd/kairon-migration-adapter-fluxvm`)
- [x] QEMU incoming destination lifecycle and QMP transfer implementation
- [x] authenticated/encrypted data-plane transport (`migration.dataplaneTls`, `status.dataPlaneEncrypted`)
- [x] cancellation and operator recovery commands (`kaironctl recover`, `docs/runbook-migration-failures.md`)
- [ ] shared-storage and block-migration preflight
- [x] migration network selection and bandwidth policy

## Operational tooling (shipped ahead of schedule)

Not originally scoped for a specific version, but small enough to land alongside the v0.4 work above:

- Prometheus metrics + example alert rules (`internal/metrics`, `charts/kairon/alerts.yaml`)
- `kairon-ui` web dashboard (`cmd/kairon-ui`, `internal/uiapi`, `web/`)
- `internal/integration`: a CI-runnable controller+agent pipeline test
- multi-host migration test and `NeedsRecovery` drill runbooks (`docs/runbook-multi-host-migration-test.md`, `docs/runbook-recovery-drill.md`) — documented and scripted, not yet run against real hardware in this repo's own CI

## v0.5+

- PVC -> FluxVM block-device lifecycle
- DRA topology-aware scheduler scoring
- SR-IOV/GPU migration capability checks
- guest quiesce hooks for snapshots
- admission policy, quotas and multi-tenant controls -- a narrower, single-purpose piece of this (`migration.maxConcurrentPerNode`/`maxConcurrentCluster`, a per-node/cluster concurrency cap) shipped early; real multi-tenant admission policy is still open
- per-node certificate automation/rotation and SPIFFE support
- confidential VM policy (SEV-SNP/TDX)
- upgrade/scale/failure-injection test suites
