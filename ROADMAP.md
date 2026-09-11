# Kairon roadmap

## v0.2 — current

- MachineMigration: auto/cold/live
- controlled cold relocation and node evacuation
- FluxVM source live-migration API integration
- adopt-only live cutover guard
- CSI VolumeSnapshot orchestration
- DRA ResourceClaim -> allowlisted FluxVM VFIO bridge
- migration/snapshot CLI and status

## v0.3 — target preparation and portable state

- FluxVM target-side incoming migration API and signed/authenticated handoff
- automatic destination listener preparation
- storage/network migration preflight
- PVC/PV -> FluxVM disk attachment contract
- CSI attach/detach/clone integration
- DRA topology-aware scheduling and driver-specific device resolvers
- SR-IOV lifecycle integration

## v0.4 — high availability

- automated rollback and fencing
- node heartbeat/lease-aware recovery
- shared-storage and block-migration modes
- network identity handoff
- MachineDisruptionBudget
- migration policy/SLOs

## v1.0 — production qualification

- upgrade/rollback compatibility policy
- admission, quotas, provenance, audit guarantees
- confidential-compute capability enforcement
- Kubernetes distribution interoperability matrix
- large-scale chaos and real-hardware qualification
