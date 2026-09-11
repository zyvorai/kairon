<div align="center">

# Kairon

### Kubernetes-native virtual machines without KubeVirt or libvirt

**Kubernetes declares. Kairon places. FluxVM runs.**

[Architecture](docs/architecture.md) · [Getting started](docs/getting-started.md) · [Roadmap](ROADMAP.md) · [Security](SECURITY.md)

</div>

---

Kairon is a lightweight, Apache-2.0 virtual-machine orchestration layer for Kubernetes. It deliberately does **not** launch a per-VM `virt-launcher` Pod and does not require libvirt. A small cluster controller assigns `Machine` resources to capable nodes; a node-local agent reconciles those machines into the existing [Zyvor FluxVM](https://github.com/zyvorai/fluxvm) REST API.

This repository is an **MVP/reference implementation**, designed so the core control loop is real and testable today while advanced enterprise features can land incrementally without changing the API boundary. It is not yet a claim of feature parity with KubeVirt.

## What works

- `Machine` CRD with a status subresource and printer columns.
- Deterministic, least-loaded scheduling across Ready Kairon-capable Kubernetes nodes.
- Node-local reconciliation to FluxVM `POST /v1/vms`, `GET /v1/vms?name=...`, `GET /v1/vms/{id}`, and `DELETE /v1/vms/{id}`.
- QEMU, Cloud Hypervisor, Firecracker and FluxVM backend selection exposed through the Machine API; Firecracker supports an explicit kernel path.
- CPU and memory quantity conversion into FluxVM's `vcpus` and `memory_mib` contract.
- `user`, `tap`, and `macvtap` network modes, including per-VM netns for TAP.
- Declarative start/stop and deletion cleanup with a finalizer.
- Multi-tenancy mapping: Kubernetes namespace is always the FluxVM tenant; Machine authors cannot override it.
- In-cluster Kubernetes client implemented with the Go standard library only: no client-go dependency and no generated code requirement.
- Controller and node health/readiness endpoints.
- `kaironctl` for create/get/describe/start/stop/delete through the Kubernetes API.
- Helm chart, raw manifests, RBAC, GitHub Actions, container builds and test suite.

## Architecture

```text
kubectl / GitOps / API
        |
        v
+---------------------+
| Kubernetes API      |
| Machine CRD         |
+----------+----------+
           |
           v
+---------------------+       +----------------------+
| kairon-controller   |       | kairon-node          |
| placement/scheduler |------>| one per KVM node     |
+---------------------+       +----------+-----------+
                                         |
                                  localhost:7788
                                         |
                                         v
                              +----------------------+
                              | FluxVM               |
                              | QEMU / CH / FC /     |
                              | FluxVM hypervisor    |
                              +----------+-----------+
                                         |
                                         v
                                        KVM
```

Kairon never invokes `virsh`, libvirt or KubeVirt. FluxVM owns VM execution; Kairon owns desired state, placement and Kubernetes lifecycle semantics.

## Quick start

### 1. Prepare each virtualization node

Install and run FluxVM on the host, listening on `127.0.0.1:7788`, and label the node:

```bash
kubectl label node worker-1 kairon.zyvor.dev/capable=true
```

### 2. Install Kairon

```bash
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
```

Or use Helm:

```bash
helm upgrade --install kairon ./charts/kairon \
  --namespace kairon-system --create-namespace
```

### 3. Create a VM

```bash
kubectl apply -f examples/linux-machine.yaml
kubectl get machines -A -w
```

Example:

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: ubuntu-dev
spec:
  image:
    path: /var/lib/fluxvm/images/ubuntu-24.04.qcow2
  resources:
    cpu: "2"
    memory: 2Gi
  runtime:
    backend: qemu
  network:
    mode: tap
    netns: true
  powerState: Running
```

## CLI

For local development, expose your Kubernetes API using `kubectl proxy` and point the CLI at it:

```bash
kubectl proxy --port=8001
export KAIRON_KUBE_URL=http://127.0.0.1:8001

kaironctl create demo --image /var/lib/fluxvm/images/ubuntu.qcow2 --cpu 2 --memory 2Gi
kaironctl get
kaironctl describe demo
kaironctl stop demo
kaironctl start demo
kaironctl delete demo
```

Inside a cluster, all components automatically use the mounted service-account credentials.

## Design rules

1. **No VM wrapper Pod.** A Machine is scheduled by Kairon and executed directly by FluxVM on that node.
2. **No libvirt.** Runtime control is through the FluxVM API.
3. **Small control plane.** Kubernetes remains source of truth; Kairon has no external database.
4. **Node-local failure domain.** The node agent only mutates VMs assigned to its own node.
5. **API before implementation.** Migration, storage, devices, confidential compute and eBPF features extend the Machine API without coupling it to one VMM.

## Production gaps before v1.0

The current code is intentionally honest about its maturity. Before calling this a KubeVirt replacement in production, implement the milestones in [ROADMAP.md](ROADMAP.md), especially live migration, CSI snapshot/clone integration, failure fencing, ResourceClaim/DRA device binding, admission webhooks, VM image provenance, HA disruption controls, and upgrade/rollback testing.

## Development

```bash
make all
```

`make all` runs formatting, `go vet`, unit/integration-style HTTP tests, binary builds, and repository/manifest validation. The project has no third-party Go dependencies, making the control-plane bootstrap auditable and reproducible.

## License

Apache License 2.0. See [LICENSE](LICENSE).
