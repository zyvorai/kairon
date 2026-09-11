# Roadmap

## v0.3 — secure migration control plane

- mTLS node-to-node prepare/commit/abort protocol
- atomic destination session journal and idempotency
- no user-supplied migration transport URI
- local Unix-socket migration adapter contract
- rollback before commit and `NeedsRecovery` after ambiguous commit
- adopt-only target cutover
- cold migration, CSI snapshots, DRA/VFIO guard retained

## v0.4 — FluxVM live-migration backend

- implement the Kairon migration adapter in FluxVM or a companion host service
- QEMU incoming destination lifecycle and QMP transfer implementation
- authenticated/encrypted data-plane transport
- cancellation and operator recovery commands
- shared-storage and block-migration preflight
- migration network selection and bandwidth policy

## v0.5+

- PVC -> FluxVM block-device lifecycle
- DRA topology-aware scheduler scoring
- SR-IOV/GPU migration capability checks
- guest quiesce hooks for snapshots
- admission policy, quotas and multi-tenant controls
- per-node certificate automation/rotation and SPIFFE support
- confidential VM policy (SEV-SNP/TDX)
- upgrade/scale/failure-injection test suites
