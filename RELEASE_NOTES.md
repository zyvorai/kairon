# Kairon v0.2.0

Kairon v0.2.0 adds relocation, snapshot orchestration, and guarded device passthrough on top of the v0.1 VM lifecycle.

## Added

- `MachineMigration` CRD and controller state machine.
- Cold migration and multi-Machine node evacuation.
- FluxVM live-migration start/status integration using the current `destination`, `mode`, `bandwidth_mbps`, `max_downtime_ms`, and `multifd_channels` contract.
- Migration RAM/downtime status projection.
- Target adopt-only guard to prevent accidental double boot.
- `MachineSnapshot` and CSI `VolumeSnapshot` orchestration.
- Kubernetes DRA `ResourceClaim` resolution to allowlisted PCI BDFs and FluxVM `vfio_devices`.
- CLI migration, evacuation, snapshot, and status commands.
- Typed Kubernetes API errors for idempotent controllers.
- Security tests for migration URI validation, adopt-only behavior, and VFIO fail-closed behavior.

## Explicit boundary

Source-side FluxVM live migration is integrated. Automatic creation/authentication of the destination incoming QEMU runtime is not claimed because no verified target-preparation FluxVM API is available in the runtime contract used by this release. Operators must prepare that endpoint before `strategy: live`; `strategy: auto` can use cold relocation when no live destination is supplied.
