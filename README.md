<div align="center">

# Kairon

### Real VMs, on Kubernetes, without the weight of KubeVirt

**Kubernetes declares. Kairon orchestrates. FluxVM executes.**

<img src="docs/assets/social-preview.png" alt="Kairon — Real VMs on Kubernetes without KubeVirt" width="720">

[![CI](https://github.com/zyvorai/kairon/actions/workflows/ci.yml/badge.svg)](https://github.com/zyvorai/kairon/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/github/license/zyvorai/kairon)](LICENSE)
[![Release](https://img.shields.io/badge/version-v0.5.0-blue)](VERSION)
[![Go](https://img.shields.io/badge/Go-stdlib%20control%20plane-00ADD8?logo=go)](docs/DEPENDENCIES.md)
[![Helm chart](https://img.shields.io/badge/Helm-0.5.0-0F1689?logo=helm)](charts/kairon/Chart.yaml)
[![Dashboard](https://img.shields.io/badge/dashboard-kairon--ui-ff5a15)](#operate)

[Why](#why-kairon-exists) · [Architecture](ARCHITECTURE.md) · [Quick start](#quick-start) · [Docs](docs/README.md) · [What ships](docs/WHAT_SHIPS.md) · [Status](docs/STATUS.md) · [Security](SECURITY.md) · [zyvor.dev](https://zyvor.dev?utm_source=github&utm_medium=kairon)

</div>

---

## Why Kairon exists

KubeVirt makes a VM look like a Pod: a `virt-launcher` Pod wrapping libvirt wrapping QEMU, scheduled by the Kubernetes Pod scheduler, migrated by machinery bolted onto that same abstraction. It works, but every layer you add is a layer you have to trust, patch, and debug at 2am.

Kairon starts from a different premise: a VM is not a Pod, so stop pretending it is. A `Machine` is desired state in the Kubernetes API. `kairon-controller` places it. `kairon-node` turns that placement into a real [FluxVM](https://github.com/zyvorai/fluxvm) instance — QEMU, Cloud Hypervisor, Firecracker, or the FluxVM hypervisor — on real KVM: Kubernetes-native VMs without KubeVirt, without libvirt, without a `virt-launcher` Pod standing in for the VM, and without a guessed hypervisor migration endpoint smuggled through an annotation.

|  | KubeVirt-style stacks | **Kairon** |
|---|---|---|
| Execution | `virt-launcher` Pod + libvirt | Node agent → FluxVM REST, directly |
| Scheduling | Pod scheduler + virt extras | Kairon's own least-loaded placement |
| Live migrate | Built into the VMM stack | mTLS peer handshake + optional adapter |
| Runtime dependencies | Large Go/operator surface | **Stdlib-only controller & node** ([policy](docs/DEPENDENCIES.md)) |

Three things follow from that premise:

- **Small enough to read in an afternoon.** `go.mod` has no `client-go`, no generated deep call stacks, no vendored operator framework — `kairon-controller` and `kairon-node` are each a handful of files. Two deliberate exceptions: opt-in OIDC in `kairon-ui` and opt-in CSI (`kairon-csi-node`). See [SECURITY.md](SECURITY.md).
- **Kubernetes stays the only source of truth.** A `Machine` object is where desired state lives. The dashboard (`kairon-ui`) is a thin HTTP client with the same standing as `kaironctl` or `kubectl`.
- **An uncertain outcome gets a name, not a guess.** Ambiguous live-migration commits park in `NeedsRecovery` for an attested operator decision — never silent split-brain. See [Relocating](docs/guides/relocating-a-machine.md).

---

## Who reaches for this

| You are... | You need... | Kairon gives you... |
|---|---|---|
| **A platform engineer replacing KubeVirt** | Real VMs on Kubernetes without `virt-launcher` / libvirt | A `Machine` CRD, stdlib-only controller/agent, FluxVM on KVM |
| **An operator who needs real SSO** | Dashboard login tied to your IdP | `ui.oidc.enabled` — Authorization Code + PKCE ([guide](docs/guides/kairon-ui-oidc.md)) |
| **An SRE running a Machine fleet** | Drain without blowing capacity; namespace caps | `MachineDisruptionBudget` + `MachineQuota`, enforceable at admission ([guide](docs/guides/admission-webhook.md)) |
| **Whoever's on call for live migration** | No silent split-brain on ambiguous commit | `NeedsRecovery` + attested recovery ([runbook](docs/runbook-migration-failures.md)) |
| **An economic buyer sizing build-vs-adopt** | Honesty about what's real today | Apache-2.0 + [Status](docs/STATUS.md) as a punch list, not marketing |

---

## Quick start

```bash
# 1. Label the nodes FluxVM actually runs on
kubectl label node worker-1 kairon.zyvor.dev/capable=true

# 2. Install (chart + Helm SDK are embedded in kaironctl — no helm binary required)
kaironctl install

# Or via kubectl plugin (after: kubectl krew install kairon)
# kubectl kairon install

# 3. Run something
kaironctl create demo --image /var/lib/fluxvm/images/ubuntu.qcow2 --cpu 2 --memory 2Gi --backend qemu
kaironctl get machines
kaironctl status
```

FluxVM needs to already be listening on each capable node (default `127.0.0.1:7788`) with the image path visible to the host. Prefer raw manifests? `kubectl apply -f deploy/crd.yaml -f deploy/rbac.yaml -f deploy/controller.yaml -f deploy/node.yaml`. Full walkthrough: [docs/getting-started.md](docs/getting-started.md).

---

## How it fits together

```text
                  kubectl / GitOps / kaironctl / kairon-ui
                                      │
                                      ▼
                 ┌────────────────────────────────────────┐
                 │             Kubernetes API             │
                 │ Machine · MachineMigration · Snapshot  │
                 │ MachineQuota · MachineDisruptionBudget │
                 └────────────────────────────────────────┘
                                      │
                  ┌───────────────────┴────────────────────┐
                  ▼                                        ▼
┌───────────────────────────────────┐      ┌───────────────────────────────┐
│         kairon-controller         │      │          kairon-node          │
│     placement · migration FSM     │      │ FluxVM lifecycle · DRA → VFIO │
│ CSI snapshots · admission webhook │      │        mTLS peer :9443        │
└───────────────────────────────────┘      └───────────────────────────────┘
                                                           │
                                                           ▼
                                     FluxVM local API · migration adapter · KVM/VMM
```

Kubernetes is the source of truth. FluxVM owns execution. Kairon owns placement, relocation policy, and Kubernetes lifecycle semantics. Full write-up: [`ARCHITECTURE.md`](ARCHITECTURE.md) · [`docs/architecture.md`](docs/architecture.md).

---

## Capabilities

- **Lifecycle & placement** — `Machine`, `MachineSet`, instance types, NUMA/pinning, Windows guests, PVC/CSI boot, image import/catalog, DRA→VFIO, hotplug, pause/halt, sandboxes
- **Migration** — cold migrate & evacuate, secure live mTLS handshake, `NeedsRecovery`, real FluxVM migration adapter, fencing
- **Fleet guards** — `MachineDisruptionBudget`, `MachineQuota`, opt-in admission webhook, cordon-triggered evacuation
- **Operate** — `kaironctl` / `kubectl kairon`, optional `kairon-ui` (SSO, VNC, recovery workflow), Prometheus metrics, HA leases
- **Snapshots & network** — CSI snapshots + schedules, guest quiesce, Network Fabric / eBPF policies

Full inventory with every guide link → **[docs/WHAT_SHIPS.md](docs/WHAT_SHIPS.md)**

---

## Operate

**Relocate**

```bash
kaironctl migrate demo --strategy cold --target-node worker-2
kaironctl migrate demo --strategy live --target-node worker-2 --mode pre-copy
kaironctl evacuate worker-1 --wait
```

→ [Relocating a Machine](docs/guides/relocating-a-machine.md) · [migration adapter](docs/migration-adapter.md) · [NeedsRecovery runbook](docs/runbook-migration-failures.md)

**Dashboard**

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system --set ui.enabled=true
kubectl -n kairon-system port-forward svc/kairon-ui 8082:8082
kubectl -n kairon-system get secret kairon-ui-session -o jsonpath='{.data.defaultAdminPassword}' | base64 -d; echo
```

→ [Getting started — dashboard](docs/getting-started.md#deploy-the-web-dashboard) · [OIDC](docs/guides/kairon-ui-oidc.md) · [UI HA](docs/guides/kairon-ui-ha.md)

**CLI** — `kaironctl` and `kubectl kairon` share the same command tree (Cobra + embedded Helm). Dependency exceptions: [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md).

→ Full reference: **[docs/CLI.md](docs/CLI.md)** · Krew: `make krew-package`

---

## Explore the docs

| Goal | Document |
|------|----------|
| Docs index | [docs/README.md](docs/README.md) |
| Getting started | [docs/getting-started.md](docs/getting-started.md) |
| What ships today | [docs/WHAT_SHIPS.md](docs/WHAT_SHIPS.md) |
| Architecture (tour) | [ARCHITECTURE.md](ARCHITECTURE.md) |
| Architecture (deep) | [docs/architecture.md](docs/architecture.md) |
| CLI | [docs/CLI.md](docs/CLI.md) |
| Status & production gaps | [docs/STATUS.md](docs/STATUS.md) |
| Security | [SECURITY.md](SECURITY.md) |
| Roadmap / release notes | [ROADMAP.md](ROADMAP.md) · [RELEASE_NOTES.md](RELEASE_NOTES.md) |
| Contributing / develop | [CONTRIBUTING.md](CONTRIBUTING.md) |

Social preview asset: [`docs/assets/social-preview.png`](docs/assets/social-preview.png) (source: [`social-preview.svg`](docs/assets/social-preview.svg)).

---

## Status

**v0.5.0** is tagged and open source. Cold relocation, snapshots, DRA bridging, the secure live control plane, and a real FluxVM migration adapter are real and tested — real two-host live migration has not yet been exercised against real hardware in this repository's own CI.

Honest production gaps (fencing signals, PVC first-cut limits, HA eventual consistency, API-only features, and more) → **[docs/STATUS.md](docs/STATUS.md)**

Report vulnerabilities privately to **security@zyvor.dev** — see [`SECURITY.md`](SECURITY.md).

---

## License

### Open source (Apache-2.0)

This repository is licensed under the [Apache License, Version 2.0](LICENSE). You may use, modify, and run it for personal, lab, and commercial production use at no charge, subject to Apache-2.0 (preserve notices / NOTICE where required). See [NOTICE](NOTICE).

### Enterprise

Production support, SLAs, and Zyvor Enterprise products are licensed separately. Contact [sales@zyvor.dev](mailto:sales@zyvor.dev) or see [zyvor.dev](https://zyvor.dev).
