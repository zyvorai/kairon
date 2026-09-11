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
