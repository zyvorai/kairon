<div align="center">

<img src="docs/assets/social-preview.png" alt="Kairon — Real VMs on Kubernetes without KubeVirt" width="720">

# Kairon

**Real VMs. Real Kubernetes. No virt-launcher.**

A `Machine` is desired state. `kairon-controller` places it.
`kairon-node` runs it on [FluxVM](https://github.com/zyvorai/fluxvm) / KVM —
QEMU, Cloud Hypervisor, Firecracker, or FluxVM's own hypervisor.
**Kubernetes stays the only source of truth.**

[![CI](https://github.com/zyvorai/kairon/actions/workflows/ci.yml/badge.svg)](https://github.com/zyvorai/kairon/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/zyvorai/kairon?display_name=tag)](https://github.com/zyvorai/kairon/releases/latest)
[![Go Report Card](https://goreportcard.com/badge/github.com/zyvorai/kairon)](https://goreportcard.com/report/github.com/zyvorai/kairon)
[![OpenSSF Scorecard](https://api.scorecard.dev/projects/github.com/zyvorai/kairon/badge)](https://scorecard.dev/viewer/?uri=github.com/zyvorai/kairon)
[![License: Apache-2.0](https://img.shields.io/github/license/zyvorai/kairon)](LICENSE)
[![Docs](https://img.shields.io/badge/docs-zyvor.dev-ff5a15)](https://zyvor.dev/docs/kairon?utm_source=github&utm_medium=kairon)

[Why](#why-kairon) · [vs KubeVirt](#vs-kubevirt) · [Install](#install) · [Architecture](#architecture) · [What ships](#what-ships) · [Operate](#operate) · [Status](#status)

</div>

---

## Why Kairon

KubeVirt makes a VM look like a Pod: `virt-launcher` → libvirt → QEMU, scheduled by the Pod scheduler. It works — and every layer is another thing you patch and debug at 2am.

Kairon starts from a different premise: **a VM is not a Pod.**

| You get | You don't get |
|---|---|
| A `Machine` CRD as desired state | A Pod wrapping a hypervisor |
| Capacity-aware placement in the controller | Pod-scheduler virt plugins as the core model |
| Node agent → FluxVM REST → KVM | libvirt in the hot path |
| **Stdlib-only** controller & node ([policy](docs/DEPENDENCIES.md)) | A sprawling client-go operator surface |
| Ambiguous live commits → `NeedsRecovery` | Silent split-brain hopes |
| FluxVM **eBPF** edge + `MachineNetworkPolicy` | “Just use NetworkPolicy on the launcher Pod” |

Small enough to read in an afternoon. Honest about what's green and what isn't — see [Status](#status).

---

## vs KubeVirt

<div align="center">
<img src="docs/assets/kairon-vs-kubevirt.jpg" alt="KubeVirt vs Kairon comparison" width="720">
</div>

| Dimension | KubeVirt | **Kairon** |
|---|---|---|
| Model | VM ≈ Pod (`virt-launcher`) | VM ≠ Pod (`Machine` CRD) |
| Execution | virt-launcher → libvirt → QEMU | kairon-node → FluxVM REST → KVM |
| Scheduling | Pod scheduler + virt extras | Capacity-aware Kairon placement |
| Control plane | Large operator / client-go surface | **Stdlib-only** controller & node |
| Hypervisors | Primarily QEMU via libvirt | QEMU, Cloud HV, Firecracker, FluxVM |
| Network | Pod CNI / NetworkPolicy | FluxVM TC/**eBPF** + `MachineNetworkPolicy` (opt. Cilium EW) |
| Live migration | In VMM / KubeVirt stack | mTLS peers + optional adapter |
| Ambiguous commit | Stack-dependent recovery | `NeedsRecovery` — no silent split-brain |
| Source of truth | K8s + virt abstractions | **Kubernetes API only** |
| Install | Operator + CDI (often) | Helm OCI / `kaironctl`, prod profile |
| Maturity | Battle-tested ecosystem | **v0.6 tagged · v0.7 shipping on main** |

---

## Install

**v0.6.0** — signed images, OCI Helm chart, CLI binaries, checksums, SBOM.
Production profile pins tags to `Chart.AppVersion` and turns on webhook, namespace isolation, network default-deny, and migration dataplane TLS.

```bash
# Label nodes where FluxVM listens (default 127.0.0.1:7788)
kubectl label node worker-1 kairon.zyvor.dev/capable=true

# Production install from OCI
helm upgrade --install kairon oci://ghcr.io/zyvorai/charts/kairon \
  --version 0.6.0 -n kairon-system --create-namespace \
  -f https://raw.githubusercontent.com/zyvorai/kairon/v0.6.0/charts/kairon/values-production.yaml

# CLI (linux amd64; also: kaironctl-linux-arm64 — or: kubectl krew install kairon)
curl -fsSL -o kaironctl \
  https://github.com/zyvorai/kairon/releases/download/v0.6.0/kaironctl-linux-amd64
chmod +x kaironctl && sudo mv kaironctl /usr/local/bin/

kaironctl create demo --image /var/lib/fluxvm/images/ubuntu.qcow2 \
  --cpu 2 --memory 2Gi --backend qemu
kaironctl get machines
```

Images: `ghcr.io/zyvorai/kairon-{controller,node,ui}` · Chart: `oci://ghcr.io/zyvorai/charts/kairon` · Release: [v0.6.0](https://github.com/zyvorai/kairon/releases/tag/v0.6.0)

<details>
<summary><strong>From source / kind / bare metal</strong></summary>

```bash
git clone https://github.com/zyvorai/kairon.git && cd kairon
make docker-build
helm upgrade --install kairon ./charts/kairon -n kairon-system --create-namespace
# or: kaironctl install   (embedded Helm SDK — no helm binary required)
```

Raw manifests: `kubectl apply -f deploy/crd.yaml -f deploy/rbac.yaml -f deploy/controller.yaml -f deploy/node.yaml`.

Bare-metal systemd (node + controller + UI): `scripts/deploy-remote.sh USER@HOST --with-controller --with-ui --with-console` — see [getting-started](docs/getting-started.md).

</details>

---

## Architecture

```text
                  kubectl / GitOps / kaironctl / kairon-ui
                                      │
                                      ▼
                 ┌────────────────────────────────────────┐
                 │             Kubernetes API             │
                 │ Machine · MachineMigration · Snapshot  │
                 │ MachineQuota · MachineDisruptionBudget │
                 │ MachineNetworkPolicy · NetworkSecurity │
                 └────────────────────────────────────────┘
                                      │
                  ┌───────────────────┴────────────────────┐
                  ▼                                        ▼
┌───────────────────────────────────┐      ┌───────────────────────────────┐
│         kairon-controller         │      │          kairon-node          │
│ placement · migration FSM · fence │      │ FluxVM lifecycle · DRA → VFIO │
│ CSI snapshots · admission webhook │      │ eBPF policy · mTLS peer :9443 │
└───────────────────────────────────┘      └───────────────────────────────┘
                                                           │
                                                           ▼
                              FluxVM local API · TC/eBPF edge · migration adapter · KVM
```

v0.6 foundations: AssignedNode-scoped watches, capacity-aware scheduling, status skip-patch.
v0.7 on `main`: eBPF operator surface, Network panel, multi-volume virtiofs, lease-aware fencing, CSI CHAP, opt-in OTel.

Full write-up: [`ARCHITECTURE.md`](ARCHITECTURE.md) · [`docs/architecture.md`](docs/architecture.md).

---

## What ships

| Area | Highlights |
|------|------------|
| **Lifecycle** | `Machine` / `MachineSet`, instance types, NUMA/pinning, Windows, PVC/CSI boot, multi-volume virtiofs, hotplug, pause/halt, sandboxes |
| **Migration** | Cold + evacuate, secure live mTLS, `NeedsRecovery`, FluxVM adapter, fencing (`AgentLivenessStale` when leases enabled) |
| **Network** | FluxVM **eBPF** edge, `MachineNetworkPolicy` / `NetworkSecurityGroup`, flows/drops/stats CLI + dashboard **Network** panel |
| **Fleet guards** | `MachineDisruptionBudget`, `MachineQuota`, opt-in admission webhook |
| **Operate** | `kaironctl` / `kubectl kairon`, optional `kairon-ui` (SSO, VNC), Prometheus, HA leases, opt-in OTel spans |
| **Snapshots** | `MachineSnapshot` / restore, CSI schedules, guest quiesce |

Full inventory with every guide → **[docs/WHAT_SHIPS.md](docs/WHAT_SHIPS.md)**

---

## Operate

```bash
# Relocate
kaironctl migrate demo --strategy cold --target-node worker-2
kaironctl migrate demo --strategy live --target-node worker-2 --mode pre-copy
kaironctl evacuate worker-1 --wait

# eBPF observability (tap + dataplaneMode: ebpf)
kaironctl network status demo
kaironctl network flows demo --limit 20
kaironctl network drop-reasons demo

# Dashboard
helm upgrade --install kairon oci://ghcr.io/zyvorai/charts/kairon \
  --version 0.6.0 -n kairon-system --set ui.enabled=true
kubectl -n kairon-system port-forward svc/kairon-ui 8082:8082
```

→ [Relocating](docs/guides/relocating-a-machine.md) · [Network fabric](docs/network-fabric.md) · [NeedsRecovery](docs/runbook-migration-failures.md) · [CLI](docs/CLI.md)

---

## Docs

| Goal | Document |
|------|----------|
| Getting started | [docs/getting-started.md](docs/getting-started.md) |
| What ships | [docs/WHAT_SHIPS.md](docs/WHAT_SHIPS.md) |
| Architecture | [ARCHITECTURE.md](ARCHITECTURE.md) · [docs/architecture.md](docs/architecture.md) |
| Status & gaps | [docs/STATUS.md](docs/STATUS.md) |
| Hardware matrix | [docs/COMPATIBILITY.md](docs/COMPATIBILITY.md) |
| Roadmap | [ROADMAP.md](ROADMAP.md) |
| OpenSSF / Scorecard | [docs/OPENSSF_BEST_PRACTICES.md](docs/OPENSSF_BEST_PRACTICES.md) |
| Security | [SECURITY.md](SECURITY.md) |
| Contributing | [CONTRIBUTING.md](CONTRIBUTING.md) |

---

## Status

**v0.6.0** is the latest release — production foundations: status skip-patch, node-scoped watches, capacity scheduling, production Helm profile, signed release artifacts, expanded CI.

**Toward v0.7** (on `main`, untagged): eBPF CLI + Network panel, multi-volume virtiofs, lease-aware fencing, CSI node CHAP, opt-in OTel. Single-host deploy smoke is green; **multi-host live migration is not yet green on the lab matrix** — see [`COMPATIBILITY.md`](docs/COMPATIBILITY.md). Cut v0.7.0 after that. Details → **[docs/STATUS.md](docs/STATUS.md)** · **[ROADMAP.md](ROADMAP.md)**.

Report vulnerabilities to **security@zyvor.dev** — [`SECURITY.md`](SECURITY.md).

---

## License

**Apache-2.0** — use, modify, and run in production at no charge ([LICENSE](LICENSE), [NOTICE](NOTICE)).

Enterprise support and Zyvor products are licensed separately → [sales@zyvor.dev](mailto:sales@zyvor.dev) · [zyvor.dev](https://zyvor.dev).
