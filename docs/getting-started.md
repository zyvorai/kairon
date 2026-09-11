# Getting started

## Prerequisites

- Kubernetes cluster with at least one Kairon-capable node.
- KVM and a reachable FluxVM service on each VM node.
- VM image paths available under the node agent's configured `--image-root`.
- For snapshots: CSI snapshot CRDs/controller and a capable CSI driver.
- For DRA/VFIO: Kubernetes DRA plus administrator-approved PCI BDFs in `KAIRON_VFIO_ALLOWLIST`.

## Install

```bash
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
kubectl label node worker-1 kairon.zyvor.dev/capable=true
```

## Create a Machine

```bash
kubectl apply -f examples/linux-machine.yaml
kubectl get machines -A -w
```

## Migrate

```bash
kaironctl migrate demo --strategy cold --target-node worker-2
```

For live migration, prepare a compatible incoming QEMU target first, then supply its address:

```bash
kaironctl migrate demo --strategy live --target-node worker-2 \
  --destination tcp:10.0.0.12:4444 --mode pre-copy
```

## Snapshot

See `examples/snapshot-machine.yaml`, then:

```bash
kaironctl snapshot database --name database-before-upgrade --class csi-snapclass
kaironctl get snapshots
```
