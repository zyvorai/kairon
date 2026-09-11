# Getting started

## Requirements

- Kubernetes cluster
- Linux worker nodes with KVM available
- FluxVM installed and listening on `127.0.0.1:7788` on each virtualization node
- a host-local VM image path visible to FluxVM

## Install

```bash
kubectl label node worker-1 kairon.zyvor.dev/capable=true
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
```

Use immutable image tags rather than `latest` for production.

## Verify

```bash
kubectl -n kairon-system get pods -o wide
kubectl -n kairon-system logs deploy/kairon-controller
kubectl get machines -A
```

## Run a machine

Edit `examples/linux-machine.yaml` so `spec.image.path` exists on the selected host, then:

```bash
kubectl apply -f examples/linux-machine.yaml
kubectl get machine ubuntu-dev -w
```

## Stop/start

```bash
kubectl patch machine ubuntu-dev --type merge -p '{"spec":{"powerState":"Stopped"}}'
kubectl patch machine ubuntu-dev --type merge -p '{"spec":{"powerState":"Running"}}'
```

## Delete

```bash
kubectl delete machine ubuntu-dev
```

The finalizer makes the owning node agent delete the FluxVM runtime before Kubernetes removes the Machine object.
