# Getting started

## Prerequisites

- Kubernetes cluster and Kairon-capable nodes.
- KVM and a reachable FluxVM service on each VM node.
- VM image paths available below `--image-root`.
- CSI snapshot stack for `MachineSnapshot`.
- Kubernetes DRA plus administrator-approved BDFs for VFIO.

## Install

```bash
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
kubectl label node worker-1 kairon.zyvor.dev/capable=true
kubectl label node worker-2 kairon.zyvor.dev/capable=true
```

## Create a Machine

```bash
kubectl apply -f examples/linux-machine.yaml
kubectl get machines -A -w
```

## Cold migrate

```bash
kaironctl migrate demo --strategy cold --target-node worker-2
```

## Enable the secure live-migration peer

Use the Helm chart and provide `kairon-migration-tls` with `ca.crt`, `tls.crt`, and `tls.key`, then enable `migration.enabled=true`. The chart credential must have `serverAuth` + `clientAuth` EKUs and DNS SAN `kairon-node` unless `migration.tlsServerName` is changed.

A real live transfer additionally requires a Kairon migration adapter on each node. Current FluxVM does not expose the verified adapter/API required by this release, so an explicit live request without an adapter is safely blocked before source transfer.

```bash
kaironctl migrate demo --strategy live --target-node worker-2 --mode pre-copy
```

## Snapshot

```bash
kaironctl snapshot database --name database-before-upgrade --class csi-snapclass
kaironctl get snapshots
```

## Network Fabric (eBPF edge)

Apply the example Machine + policy + security group, then follow the tutorial:

```bash
kubectl apply -f examples/network-fabric-machine.yaml
```

- Tutorial: [tutorials/network-fabric.md](tutorials/network-fabric.md)
- User guides: [guides/machine-network.md](guides/machine-network.md), [guides/network-policy.md](guides/network-policy.md)
- Reference: [network-fabric.md](network-fabric.md)
