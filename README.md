<div align="center">

# Kairon

### Kubernetes-native VMs — without KubeVirt, without libvirt, without a virt-launcher Pod

**Kubernetes declares. Kairon orchestrates. FluxVM executes.**

[![CI](https://github.com/zyvorai/kairon/actions/workflows/ci.yml/badge.svg)](https://github.com/zyvorai/kairon/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/github/license/zyvorai/kairon)](LICENSE)
[![Release](https://img.shields.io/badge/version-v0.3.0-blue)](VERSION)
[![Go](https://img.shields.io/badge/Go-stdlib%20only-00ADD8?logo=go)](go.mod)

[Architecture](docs/architecture.md) · [Getting started](docs/getting-started.md) · [Migration adapter](docs/migration-adapter.md) · [Roadmap](ROADMAP.md) · [Security](SECURITY.md) · [zyvor.dev](https://zyvor.dev)

</div>

---

Kairon is Zyvor’s Apache-2.0 control plane for running virtual machines on Kubernetes through [FluxVM](https://github.com/zyvorai/fluxvm). A `Machine` is desired state in the API. A small cluster controller places it. A node-local agent turns that into FluxVM — QEMU, Cloud Hypervisor, Firecracker, or FluxVM hypervisor — on real KVM.

No per-VM wrapper Pod. No libvirt. No guessed hypervisor migration endpoints in the Kubernetes API.

| | KubeVirt-style stacks | **Kairon** |
|---|---|---|
| Execution | virt-launcher Pod + libvirt | Node agent → FluxVM REST |
| Scheduling | Pod scheduler + virt extras | Kairon least-loaded placement |
| Live migrate | Built into the VMM stack | mTLS peer handshake + optional adapter |
| Dependencies | Large Go/operator surface | **Go standard library only** |

---

## What you get in v0.3

- **Machine CRD** — CPU, memory, image, network, power, volumes, DRA device claims
- **Placement** — Ready, capable-labeled nodes; least-loaded with deterministic tie-break
- **Cold migrate & evacuate** — stop → reassign → restart, restart-safe in the API
- **Secure live handshake** — TLS 1.3 mTLS `prepare → transfer → commit`; no user-supplied `tcp:` URIs
- **Split-brain guards** — rollback on transfer failure; `NeedsRecovery` when commit is ambiguous; adopt-only cutover
- **CSI VolumeSnapshot** — `MachineSnapshot` orchestrates standard snapshot objects
- **DRA → VFIO** — allocated `ResourceClaim` → PCI BDF → allowlisted `vfio_devices` (fail-closed)
- **kaironctl** — create, start/stop, migrate, evacuate, snapshot
- **Helm + raw manifests + CI** — auditable, reproducible builds

Live *memory* transfer still needs a node-local [migration adapter](docs/migration-adapter.md). Without one, live requests block **before** the source is touched. Cold migration works today.

---

## Quick start

**1. Label virtualization nodes**

```bash
kubectl label node worker-1 kairon.zyvor.dev/capable=true
kubectl label node worker-2 kairon.zyvor.dev/capable=true
```

**2. Install**

```bash
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
```

Or:

```bash
helm upgrade --install kairon ./charts/kairon \
  --namespace kairon-system --create-namespace
```

**3. Run a VM**

```bash
kaironctl create demo \
  --image /var/lib/fluxvm/images/ubuntu.qcow2 \
  --cpu 2 --memory 2Gi --backend qemu

kaironctl get machines
kaironctl describe demo
```

FluxVM must already be listening on each capable node (default `127.0.0.1:7788`), with the image path visible to the host.

---

## Architecture

```text
     kubectl / GitOps / kaironctl
                 │
                 ▼
┌────────────────────────────────────────────┐
│           Kubernetes API                   │
│  Machine · MachineMigration · Snapshot     │
│  ResourceClaim · VolumeSnapshot            │
└────────────────────┬───────────────────────┘
                     │
         ┌───────────┴───────────┐
         ▼                       ▼
┌─────────────────┐     ┌─────────────────┐
│ kairon-controller│     │   kairon-node   │
│ placement        │     │ FluxVM lifecycle│
│ migration FSM    │     │ DRA → VFIO      │
│ CSI snapshots    │     │ mTLS peer :9443 │
└─────────────────┘     └────────┬────────┘
                                 │
              ┌──────────────────┼──────────────────┐
              ▼                  ▼                  ▼
         FluxVM API      migration adapter    KVM / VMM
         (node-local)    (optional Unix sock)
```

Kubernetes is source of truth. FluxVM owns execution. Kairon owns placement, relocation policy, and Kubernetes lifecycle semantics.

More detail: [`docs/architecture.md`](docs/architecture.md).

---

## Relocate a Machine

**Cold** (proven path — shared or identically provisioned images required):

```bash
kaironctl migrate demo --strategy cold --target-node worker-2
kaironctl evacuate worker-1   # batch cold/auto migrations off a node
```

**Live handshake** (peer mTLS + adapter; you pick a *node*, never a raw destination):

```bash
kaironctl migrate demo \
  --strategy live \
  --target-node worker-2 \
  --mode pre-copy \
  --bandwidth-mbps 800 \
  --max-downtime-ms 200
```

```text
controller → source node ──mTLS──▶ target :9443
                      prepare + journal
             ◀── opaque transfer endpoint ──
source adapter transfers ──▶ target commit
                      adopt-only cutover
```

Enable peer TLS:

```bash
kubectl -n kairon-system create secret generic kairon-migration-tls \
  --from-file=ca.crt --from-file=tls.crt --from-file=tls.key

helm upgrade --install kairon ./charts/kairon -n kairon-system \
  --set migration.enabled=true
```

Certificates need `serverAuth` + `clientAuth` and the chart’s migration server name (default `kairon-node`). See [`docs/migration-adapter.md`](docs/migration-adapter.md) for the adapter contract and fail-closed behavior.

---

## Snapshots & devices

**CSI snapshot** of PVC-backed volumes:

```yaml
spec:
  volumes:
    - name: data
      claimName: database-data
```

```bash
kaironctl snapshot database --name before-upgrade --class csi-snapclass
```

**DRA / VFIO** (administrator allowlist on the node — empty allowlist denies all):

```yaml
spec:
  deviceClaims:
    - name: gpu-claim
```

Examples: [`examples/`](examples/).

---

## CLI

```text
kaironctl get [machines|migrations|snapshots] [-n NS]
kaironctl describe NAME
kaironctl create NAME --image PATH [--cpu N] [--memory SIZE] [--backend qemu|…]
kaironctl start|stop|delete NAME
kaironctl migrate MACHINE --strategy auto|cold|live --target-node NODE
kaironctl evacuate NODE [--strategy cold|auto]
kaironctl snapshot MACHINE [--name NAME] [--class CLASS]
kaironctl version
```

Point at a cluster with `KAIRON_KUBE_URL` (for example after `kubectl proxy`) or run in-cluster with the mounted service account.

---

## Develop

```bash
make all        # fmt · vet · test · build · validate · version smoke
make test-race
./scripts/must-gather.sh
```

Runtime code uses the **Go standard library only** — no client-go, no generated deep stacks. That keeps the control plane small enough to audit.

---

## Status

**v0.3.0** is open source and honest about maturity. Cold relocation, snapshots, DRA bridging, and the secure live *control plane* are real. Full live memory migration waits on a FluxVM (or other) adapter. HA fencing, PVC attach, admission, and qualification work are tracked in [`ROADMAP.md`](ROADMAP.md).

### Production gaps

Pre-GA gaps include a real hypervisor migration adapter, storage/network migration preflight, automatic fencing, PVC-to-FluxVM disk attachment, DRA topology-aware placement, admission/quotas, certificate rotation, confidential-compute enforcement, and large-scale hardware qualification.

Report vulnerabilities privately to **security@zyvor.dev** — see [`SECURITY.md`](SECURITY.md).

---

## License

Copyright 2026 [Zyvor](https://zyvor.dev).

Licensed under the [Apache License, Version 2.0](LICENSE). See [NOTICE](NOTICE).
