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

A real live transfer additionally requires a Kairon migration adapter on each node (`cmd/kairon-migration-adapter-fluxvm`; not installed automatically today -- see [`runbook-multi-host-migration-test.md`](runbook-multi-host-migration-test.md)). Without one configured, an explicit live request is safely blocked before source transfer.

```bash
kaironctl migrate demo --strategy live --target-node worker-2 --mode pre-copy
```

## Snapshot

```bash
kaironctl snapshot database --name database-before-upgrade --class csi-snapclass
kaironctl get snapshots
```

## Deploy the web dashboard

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system \
  --set ui.enabled=true \
  --set ui.token="$(openssl rand -hex 24)"
kubectl -n kairon-system port-forward svc/kairon-ui 8082:8082
```

Open `http://127.0.0.1:8082` and paste the token from `ui.token`. `ui.token` (or `ui.allowUnauthenticated=true`, local development only) is required -- the chart refuses to render without one, and `kairon-ui` independently refuses to start without one.

## Network Fabric (eBPF edge)

Apply the example Machine + policy + security group, then follow the tutorial:

```bash
kubectl apply -f examples/network-fabric-machine.yaml
```

- Tutorial: [tutorials/network-fabric.md](tutorials/network-fabric.md)
- User guides: [guides/machine-network.md](guides/machine-network.md), [guides/network-policy.md](guides/network-policy.md)
- Reference: [network-fabric.md](network-fabric.md)
