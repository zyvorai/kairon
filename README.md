<div align="center">

# Kairon

### Kubernetes-native virtual machines without KubeVirt or libvirt

**Kubernetes declares. Kairon places and protects. FluxVM runs.**

</div>

Kairon is an Apache-2.0 Kubernetes control plane for virtual machines executed by [Zyvor FluxVM](https://github.com/zyvorai/fluxvm). It does not create a `virt-launcher` Pod for every VM and it does not use libvirt. Kubernetes stores desired state, `kairon-controller` handles placement/relocation/storage orchestration, and a `kairon-node` agent talks to the node-local FluxVM API.

## v0.2.0

This milestone adds:

- `MachineMigration` with `auto`, `cold`, and `live` strategies.
- Controlled cold evacuation: stop source, verify stopped, reassign, restart, verify target.
- FluxVM QMP live-migration start/status integration with pre-copy/post-copy, bandwidth, downtime, and multifd controls.
- An **adopt-only cutover guard**: after source migration completes, the target agent may only adopt the expected migrated runtime; it will not create a fresh duplicate VM.
- `MachineSnapshot` -> standard `snapshot.storage.k8s.io/v1` `VolumeSnapshot` orchestration for PVC-backed Machine volumes.
- Kubernetes DRA `resource.k8s.io/v1` `ResourceClaim` -> FluxVM `vfio_devices` bridge with allocation checks, PCI-BDF validation, and an explicit per-node allowlist.
- `kaironctl migrate`, `evacuate`, `snapshot`, plus views for migrations/snapshots.
- Static Go binaries, raw Kubernetes manifests, Helm chart, CI, race tests, and security-focused tests.

## Architecture

```text
kubectl / GitOps / kaironctl
             |
             v
+--------------------------------------------+
| Kubernetes API                             |
| Machine | MachineMigration | MachineSnapshot|
| ResourceClaim | VolumeSnapshot             |
+---------------------+----------------------+
                      |
             +--------+---------+
             |                  |
             v                  v
 +--------------------+  +--------------------+
 | kairon-controller  |  | kairon-node        |
 | placement          |  | one per VM node    |
 | migration state    |  | FluxVM lifecycle   |
 | CSI snapshots      |  | live migration     |
 +--------------------+  | DRA -> VFIO guard  |
                         +---------+----------+
                                   |
                            localhost:7788
                                   |
                                   v
                               FluxVM
                                   |
                                  KVM
```

## Install

Each virtualization node needs FluxVM running on the configured endpoint and should be explicitly labeled:

```bash
kubectl label node worker-1 kairon.zyvor.dev/capable=true
kubectl label node worker-2 kairon.zyvor.dev/capable=true

kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
```

Or install with Helm:

```bash
helm upgrade --install kairon ./charts/kairon \
  --namespace kairon-system --create-namespace
```

For CSI snapshots, install your CSI driver's snapshot support and the Kubernetes external snapshot CRDs/controller. For DRA/VFIO, configure `KAIRON_VFIO_ALLOWLIST` (raw manifest) or `node.vfioAllowlist` (Helm) with only the PCI BDFs that the node administrator authorizes, for example `0000:65:00.0,0000:65:00.1`. Empty means no VFIO device may be attached.

## Basic VM lifecycle

```bash
kaironctl create demo \
  --image /var/lib/fluxvm/images/ubuntu.qcow2 \
  --cpu 2 --memory 2Gi --backend qemu

kaironctl get machines
kaironctl stop demo
kaironctl start demo
```

## Cold migration / evacuation

```bash
# Explicit cold move to an eligible target.
kaironctl migrate demo --strategy cold --target-node worker-2

# Queue controlled cold migrations for every Machine currently on worker-1.
kaironctl evacuate worker-1
```

Cold migration does **not** copy a node-local VM image in v0.2. The target must be able to use the declared image and other runtime dependencies. Shared storage or identically provisioned images are required for portable workloads.

## Live migration

```bash
kaironctl migrate demo \
  --strategy live \
  --target-node worker-2 \
  --destination tcp:10.0.0.12:4444 \
  --mode pre-copy \
  --bandwidth-mbps 800 \
  --max-downtime-ms 200 \
  --multifd-channels 4
```

Kairon calls FluxVM's source-side migration endpoints and projects migration progress into `MachineMigration.status`. Kairon deliberately accepts only `tcp:host:port` destinations; command-backed or arbitrary QEMU migration transports are rejected.

**Current runtime boundary:** FluxVM exposes the source migration API but Kairon v0.2 has no verified FluxVM API for creating/authenticating the incoming destination runtime. Therefore the incoming QEMU target must already be prepared before an explicit live migration is started. When FluxVM reports completion, Kairon switches assignment using `kairon.zyvor.dev/adopt-only=true`. If the migrated runtime is not discoverable by Kairon's stable name, the target is marked `Blocked` and no replacement VM is created.

`strategy: auto` chooses live only when a destination is supplied and the backend is live-migration eligible; otherwise it uses controlled cold migration.

## CSI snapshots

A Machine may declare PVC-backed volumes for snapshot orchestration:

```yaml
spec:
  volumes:
    - name: data
      claimName: database-data
```

Then:

```bash
kaironctl snapshot database --name database-before-upgrade --class csi-snapclass
kubectl get machinesnapshots,volumesnapshots
```

Kairon creates standard CSI `VolumeSnapshot` objects and mirrors readiness into `MachineSnapshot.status`. v0.2 does not yet attach arbitrary CSI PVCs as FluxVM disks and it does not quiesce the guest; those remain explicit future runtime/storage integrations.

## DRA -> VFIO

A Machine can reference same-namespace Kubernetes `ResourceClaim` objects:

```yaml
spec:
  deviceClaims:
    - name: gpu-claim
```

The node agent requires the claim to have a DRA allocation. It resolves a concrete PCI BDF from either:

- `kairon.zyvor.dev/vfio-bdf` on the allocated `ResourceClaim`, or
- an allocation result whose device identifier itself is a valid PCI BDF.

Every resolved BDF must also exist in that node's administrator-configured VFIO allowlist. Only then is it sent to FluxVM as `vfio_devices`. Missing allocation, invalid mappings, and unauthorized BDFs all fail closed.

## CLI

```text
kaironctl get [machines|migrations|snapshots] [-n NAMESPACE]
kaironctl describe NAME [-n NAMESPACE]
kaironctl create NAME --image PATH [flags]
kaironctl start NAME [-n NAMESPACE]
kaironctl stop NAME [-n NAMESPACE]
kaironctl delete NAME [-n NAMESPACE]
kaironctl migrate MACHINE [--strategy auto|live|cold] [--target-node NODE] [--destination tcp:host:port]
kaironctl evacuate NODE [--strategy cold|auto]
kaironctl snapshot MACHINE [--name NAME] [--class CSI_CLASS]
kaironctl version
```

`kaironctl version` intentionally does not initialize Kubernetes credentials.

## Development

```bash
make all
make test-race
```

`make all` checks formatting, runs `go vet`, executes all tests, builds three static binaries, validates the repository/manifests, and smoke-tests all version commands.

Supportability bundle:

```bash
./scripts/must-gather.sh
```

## Production gaps

Kairon v0.2 is pre-GA. Important remaining work includes target-side live-migration preparation/authentication, storage/network migration preflight, fencing and rollback, PVC-to-FluxVM disk attachment, DRA topology-aware scheduling, admission policy, quotas, signed image policy, confidential-compute enforcement, and large-scale/real-hardware qualification. See [ROADMAP.md](ROADMAP.md) and [SECURITY.md](SECURITY.md).
