# Kairon roadmap

## v0.1 — control-plane MVP (this repository)

- Machine CRD, status and finalizer
- least-loaded node placement
- FluxVM create/query/delete reconciliation
- declarative Running/Stopped state
- raw manifests + Helm + CI
- deterministic node naming and namespace-to-tenant isolation

## v0.1.1 — KubeVirt-feel control-plane polish

- `observedGeneration` and stable `Scheduled` / `Created` / `Ready` conditions
- Kubernetes Events for schedule / create / start / stop / fail
- `spec.cloudInit.userData` and `sshPublicKeys` forwarded to FluxVM
- `spec.image.digest` (`sha256:…`) verified before create
- `spec.placement.tolerations` and required `nodeAffinity`
- `kaironctl describe` shows conditions/events; `console` via `KAIRON_FLUXVM_URL`
- workload CPU/memory requests/limits; Ready printer column
- `secureBoot` / `tpm` passed through to FluxVM create payload

## KubeVirt job map

Same user jobs as KubeVirt, different execution model (no virt-launcher Pod; FluxVM owns VMs):

| Job | Status |
|---|---|
| Declare VM + start/stop | v0.1 |
| Status / Events / cloud-init / placement polish | v0.1.1 |
| Disks / images / snapshot-clone | v0.2 (hostPath/PVC bind + snapshot; OCI pull & CSI mount pending) |
| Multus / DRA / GPU | v0.3 |
| Live migrate / fencing / MDB | v0.4 (fencing + MDB + MachineMigration foundation) |
| Admission / image policy / confidential | v0.5 (validating webhook + digest; SNP/TDX pending) |
| Conformance / must-gather / KubeVirt import | v1.0 |

## v0.2 — storage and images

- MachineImage and VirtualDisk CRDs with status binding
- CSI PVC / hostPath / local PV → FluxVM boot disk path (CSI mount publish still pending)
- MachineSnapshot → FluxVM `POST /v1/vms/{id}/snapshot`
- VirtualDisk clone-from-disk path reference (bit-copy / GuestKit pending)
- OCI MachineImage source declared with digest (pull/stage pending GuestKit)
- Machine `diskSizeGiB`, `storage` (`default|lvm-thin|nbd|ceph-rbd`), `sharedFolders`
- FluxVM-aligned nested `cloud_init` payload

## v0.3 — networking and devices

- Multus attachment resolution
- PacketWolf/eBPF VM-edge policy API
- SR-IOV and VFIO
- Kubernetes ResourceClaim / Dynamic Resource Allocation binding
- topology-aware placement for GPUs and NICs

## v0.4 — availability

- QEMU pre-copy live migration via MachineMigration → FluxVM migration API
- MachineDisruptionBudget (voluntary evacuate / maxUnavailable)
- Node fencing after `--fence-grace` when Node NotReady/missing; clears placement for reschedule
- Evacuate annotation `kairon.zyvor.dev/evacuate=true` (MDB-gated)
- Shared-storage assumption for live migration (FluxVM contract); block-migration modes still pending
- Stale-runtime GC on fenced nodes still requires node-local cleanup when host returns
- lease/heartbeat-aware rescheduling refinements pending

## v0.5 — security

- Validating admission webhook for Machine (`internal/admission`, `deploy/webhook.yaml`)
- Prometheus `/metrics` on controller health port
- `scripts/must-gather.sh` supportability bundle
- Secure Boot + vTPM API fields forwarded (enforcement still FluxVM/backend dependent)
- SEV-SNP and TDX capability discovery pending
- Image policy/provenance beyond digest check pending
- Network-policy identities and audit stream pending

## v1.0 — production

- upgrade/rollback skew policy
- scale and chaos qualification
- conformance suite across k3s, kubeadm and OpenShift
- Windows/Linux matrix
- KubeVirt import/translation utility
- Transiva migration workflow
- supportability bundle and must-gather (`scripts/must-gather.sh` shipped; expand coverage)
