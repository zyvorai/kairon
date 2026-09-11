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
| Disks / images / snapshot-clone | v0.2 |
| Multus / DRA / GPU | v0.3 |
| Live migrate / fencing / MDB | v0.4 |
| Admission / image policy / confidential | v0.5 |
| Conformance / must-gather / KubeVirt import | v1.0 |

## v0.2 — storage and images

- MachineImage and VirtualDisk CRDs
- CSI PVC -> block/file-backed FluxVM disks
- snapshot/clone controller
- OCI-distributed VM images with signatures and digests
- GuestKit preparation hooks

## v0.3 — networking and devices

- Multus attachment resolution
- PacketWolf/eBPF VM-edge policy API
- SR-IOV and VFIO
- Kubernetes ResourceClaim / Dynamic Resource Allocation binding
- topology-aware placement for GPUs and NICs

## v0.4 — availability

- QEMU pre-copy live migration
- shared-storage and block-migration modes
- node evacuation API
- PodDisruptionBudget-like MachineDisruptionBudget
- fencing and stale-runtime garbage collection
- lease/heartbeat-aware rescheduling

## v0.5 — security

- validating/mutating admission webhook
- Secure Boot + vTPM API
- SEV-SNP and TDX capability discovery
- image policy/provenance
- network-policy identities and audit stream

## v1.0 — production

- upgrade/rollback skew policy
- scale and chaos qualification
- conformance suite across k3s, kubeadm and OpenShift
- Windows/Linux matrix
- KubeVirt import/translation utility
- Transiva migration workflow
- supportability bundle and must-gather
