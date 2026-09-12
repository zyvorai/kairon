<div align="center">

# Kairon

### Kubernetes-native VMs — without KubeVirt, without libvirt, without a virt-launcher Pod

**Kubernetes declares. Kairon orchestrates. FluxVM executes.**

[![CI](https://github.com/zyvorai/kairon/actions/workflows/ci.yml/badge.svg)](https://github.com/zyvorai/kairon/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/github/license/zyvorai/kairon)](LICENSE)
[![Release](https://img.shields.io/badge/version-v0.4.0-blue)](VERSION)
[![Go](https://img.shields.io/badge/Go-stdlib%20only-00ADD8?logo=go)](go.mod)
[![Helm chart](https://img.shields.io/badge/Helm-0.4.0-0F1689?logo=helm)](charts/kairon/Chart.yaml)
[![Dashboard](https://img.shields.io/badge/dashboard-kairon--ui-ff5a15)](#dashboard)

[Architecture](docs/architecture.md) · [Getting started](docs/getting-started.md) · [Network Fabric](docs/network-fabric.md) · [Tutorial](docs/tutorials/network-fabric.md) · [Migration adapter](docs/migration-adapter.md) · [Recovery runbook](docs/runbook-migration-failures.md) · [Roadmap](ROADMAP.md) · [Security](SECURITY.md) · [zyvor.dev](https://zyvor.dev)

</div>

---

## Contents

- [Why Kairon](#why-kairon)
- [What you get in v0.4](#what-you-get-in-v04)
- [Quick start](#quick-start)
- [Architecture](#architecture)
- [Relocate a Machine](#relocate-a-machine)
- [Dashboard](#dashboard)
- [Snapshots & devices](#snapshots--devices)
- [CLI](#cli)
- [Operability](#operability)
- [Develop](#develop)
- [Documentation map](#documentation-map)
- [Status](#status)

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

## Why Kairon

- **Small enough to read in an afternoon.** Runtime code is Go standard library only (see `go.mod`) — no client-go, no generated deep call stacks, no vendored operator framework. `kairon-controller` and `kairon-node` are each a handful of files you can actually audit.
- **Kubernetes stays the source of truth.** A `Machine` object is the only place desired state lives. Kairon never invents a second store, and neither does its optional dashboard (`kairon-ui`) — it's just another thin API client, the same standing as `kaironctl`.
- **Ambiguity gets a name, not a guess.** `NeedsRecovery` exists because a genuinely uncertain migration commit is a real state, not a bug to paper over — Kairon parks it and waits for an operator's attested decision rather than risking split-brain. See [Relocate a Machine](#relocate-a-machine).
- **Honest about maturity.** The [Status](#status) section below lists real, currently-open gaps, not a marketing gloss. v0.3 and v0.4 both shipped with their own boundaries stated plainly.

---

## What you get in v0.4

**Core Machine lifecycle**
- **Machine CRD** — CPU, memory, image, network, power, volumes, DRA device claims
- **PVC-backed boot disk** — `spec.volumes[0]` resolves through a Bound `PersistentVolumeClaim` to a real host directory (hostPath/local `PersistentVolume`s today) instead of requiring a hand-placed image file — see [docs/guides/machine-storage.md](docs/guides/machine-storage.md)
- **Placement** — Ready, capable-labeled nodes; least-loaded with deterministic tie-break; required (hard) `spec.placement.affinity`/`antiAffinity` co-location/separation constraints against other Machines — see [docs/guides/machine-placement.md](docs/guides/machine-placement.md)
- **`MachineDisruptionBudget`** — `kaironctl evacuate` throttles itself against a `minAvailable`/`maxUnavailable` budget instead of migrating an entire node's Machines at once — see [docs/guides/machine-disruption-budgets.md](docs/guides/machine-disruption-budgets.md)
- **`MachineQuota`** — a namespace-scoped `maxMachines`/`maxTotalCpu`/`maxTotalMemory` cap, enforced by `kairon-controller`'s scheduling loop (no admission webhook — an over-quota Machine stays `Pending` with a clear message) — see [docs/guides/machine-quotas.md](docs/guides/machine-quotas.md)
- **Network Fabric** — rich `spec.network`, `MachineNetworkPolicy`, `NetworkSecurityGroup`, Service Fabric VIP membership → FluxVM eBPF edge (see [docs/network-fabric.md](docs/network-fabric.md))
- **CSI VolumeSnapshot** — `MachineSnapshot` orchestrates standard snapshot objects; `MachineSnapshotRestore` restores one into a new `PersistentVolumeClaim` via the standard CSI `dataSource` flow — see [docs/guides/machine-snapshot-restore.md](docs/guides/machine-snapshot-restore.md)
- **CPU/memory hotplug** — grow a running Machine's `spec.resources` without a reboot, via FluxVM's real QMP `device_add`/`object-add` (not just cgroup throttling) — see [docs/guides/machine-hotplug.md](docs/guides/machine-hotplug.md)
- **DRA → VFIO** — allocated `ResourceClaim` → PCI BDF → allowlisted `vfio_devices` (fail-closed)

**Migration**
- **Cold migrate & evacuate** — stop → reassign → restart, restart-safe in the API
- **Secure live handshake** — TLS 1.3 mTLS `prepare → transfer → commit`; no user-supplied `tcp:` URIs
- **Split-brain guards** — rollback on transfer failure; `NeedsRecovery` when commit is ambiguous; adopt-only cutover
- **A real migration adapter ships** — `cmd/kairon-migration-adapter-fluxvm` implements the target/source contract against real FluxVM endpoints (not just a stub) — see the caveat below

**Operate it**
- **kaironctl** — create, start/stop, migrate, evacuate, snapshot
- **kairon-ui** — optional web dashboard for Machines, Migrations, Snapshots and operator recovery, deployable via Helm or `scripts/deploy-remote.sh --with-ui` (see [Dashboard](#dashboard))
- **Prometheus metrics + alert rules**, a per-node/cluster migration concurrency quota, and `status.dataPlaneEncrypted` visibility into whether a live transfer is actually encrypted (see [Operability](#operability))
- **Helm + raw manifests + CI** — auditable, reproducible builds

Live *memory* transfer needs a node-local [migration adapter](docs/migration-adapter.md) deployed and configured on each node. Without one, live requests block **before** the source is touched. Cold migration works today.

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

Prefer point-and-click? Jump to [Dashboard](#dashboard) once your first Machine is up.

---

## Architecture

```text
     kubectl / GitOps / kaironctl / kairon-ui
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
│ Prometheus metrics│    └────────┬────────┘
└─────────────────┘              │
              ┌──────────────────┼──────────────────┐
              ▼                  ▼                  ▼
         FluxVM API      migration adapter    KVM / VMM
         (node-local)    (optional Unix sock)
```

Kubernetes is source of truth. FluxVM owns execution. Kairon owns placement, relocation policy, and Kubernetes lifecycle semantics. `kairon-ui` (not pictured — it's a peer of `kaironctl`, not a fourth tier) talks to the same Kubernetes API everything else does.

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

If a commit's outcome is genuinely ambiguous, the migration lands in `NeedsRecovery` instead of guessing — see [`docs/runbook-migration-failures.md`](docs/runbook-migration-failures.md) for the decision tree, or resolve it straight from the [Dashboard](#dashboard).

---

## Dashboard

`kairon-ui` is an optional web dashboard (Go backend + a small React/TypeScript SPA, Apple-inspired styling) for Machines, Migrations, Snapshots, and — the one workflow worth a UI on its own — the `NeedsRecovery` operator-attested recovery form. It's a thin wrapper over the same Kubernetes API `kaironctl` uses; it has no side channel and no extra source of truth.

| Page | What it shows |
|---|---|
| **Overview** | Fleet tiles: Machine/Migration counts by phase, a prominent warning tile whenever anything is parked in `NeedsRecovery` |
| **Machines** | Table + create form (mirrors `kaironctl create`'s flags) + row actions: Start / Stop / Console / Migrate / Snapshot / Delete |
| **Migrations** | List with phase badges, a "New Migration" form (mirrors `kaironctl migrate`), an "Evacuate Node" action, per-migration RAM/downtime detail, `status.dataPlaneEncrypted` badge, and the recovery workflow described above |
| **Snapshots** | List + create form (mirrors `kaironctl snapshot`) |

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system --set ui.enabled=true
kubectl -n kairon-system port-forward svc/kairon-ui 8082:8082
```

Real per-operator username/password login, Apple-ID-style (username, then password on a second screen). With no `ui.*` auth values set, the chart seeds one default `admin` account with a random, generated-once password:

```bash
kubectl -n kairon-system get secret kairon-ui-session -o jsonpath='{.data.defaultAdminPassword}' | base64 -d; echo
```

Open `http://127.0.0.1:8082` and sign in as `admin` with that password. For real, named accounts instead, generate a bcrypt hash and set `ui.auth.users`:

```bash
kairon-ui -hash-password 'a real password'
helm upgrade --install kairon ./charts/kairon -n kairon-system \
  --set ui.enabled=true \
  --set ui.auth.users[0].username=alice \
  --set ui.auth.users[0].passwordHash='$2a$10$...'
```

Every mutating request (create/delete/start/stop/migrate/evacuate/recover) is logged with method, path, remote address, and status — now including the signed-in username for session-token logins, closing the "who did it" gap the legacy mode still has. The legacy single shared token (`ui.token`/`ui.existingSecret`, or `ui.allowUnauthenticated=true` for local development) still works unchanged and is accepted alongside `ui.auth.users` — treat it like a shared root password if you're still using it.

**VNC console** (`--set console.enabled=true`, off by default): a real graphical VNC session in the browser for QEMU-backend Machines, relayed `kairon-ui -> kairon-node -> the VM's local socket`. Read [SECURITY.md](SECURITY.md)'s "VNC console" section first — FluxVM's own VNC socket has no auth of its own, so this trades convenience for a trust chain appropriate for a trusted operator team, not a hostile-network deployment.

Bare-metal alternative, no Kubernetes-hosted deployment needed:

```bash
scripts/deploy-remote.sh sus@80.79.5.173 --with-controller --with-ui
```

Builds and installs `kairon-ui` as a systemd service alongside `kairon-node`/`kairon-controller`. A dashboard token is auto-generated and printed once at the end of the run (pass `--ui-token=...` to pin one across redeploys, or `--ui-allow-unauthenticated` for local/dev). Requires `npm` locally — the only place this script needs Node.js, and only with `--with-ui`.

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
                 [--forward hostPort:guestPort[/proto]] [--hostname NAME] [--user NAME]
                 [--ssh-key KEY] [--package PKG] [--runcmd CMD]  # last five repeatable/cloud-init; see docs/guides/machine-network.md
kaironctl start|stop|delete NAME
kaironctl migrate MACHINE --strategy auto|cold|live --target-node NODE
kaironctl evacuate NODE [--strategy cold|auto]
kaironctl snapshot MACHINE [--name NAME] [--class CLASS]
kaironctl recover MIGRATION --action ACTION --diagnosis DIAGNOSIS --reason REASON
kaironctl version
```

Point at a cluster with `KAIRON_KUBE_URL` (for example after `kubectl proxy`) or run in-cluster with the mounted service account.

---

## Operability

`kairon-controller` exposes Prometheus metrics on its existing health port (`/metrics`): migration counts by phase, phase age, transfer/downtime histograms, completion counters, and a `dataplane_encrypted` gauge. Example alert rules ship in [`charts/kairon/alerts.yaml`](charts/kairon/alerts.yaml) (optionally rendered as a `PrometheusRule` via `metrics.prometheusRule.enabled=true`), each pointing at [`docs/runbook-migration-failures.md`](docs/runbook-migration-failures.md).

`migration.maxConcurrentPerNode` / `migration.maxConcurrentCluster` (both `0` = unlimited) cap how many non-terminal migrations may touch one node or the cluster at once — a lightweight admission control, mainly useful to bound the blast radius of a bulk `kaironctl evacuate`.

Real two-host testing and a live `NeedsRecovery` drill (rehearsing operator recovery on purpose) are documented as runbooks with helper scripts, since they need hardware this repository's own CI doesn't have: [`docs/runbook-multi-host-migration-test.md`](docs/runbook-multi-host-migration-test.md), [`docs/runbook-recovery-drill.md`](docs/runbook-recovery-drill.md).

All three workloads (`kairon-controller`, `kairon-node`, `kairon-ui`) set CPU/memory `resources:` requests and limits by default (`values.yaml`'s `controller.resources`/`node.resources`/`ui.resources`), and CI runs `govulncheck` on every push. `kairon-ui` logs every mutating `/api/v1/...` request (method, path, remote address, resulting status) — see [Dashboard](#dashboard)'s note on what that trail can and can't tell you.

---

## Develop

```bash
make all        # fmt · vet · lint · test-race · cover-check · build · validate · smoke
make test-race
./scripts/must-gather.sh
```

Runtime code uses the **Go standard library only** — no client-go, no generated deep stacks. That keeps the control plane small enough to audit.

Touching the dashboard (`web/`)? `make all` doesn't build the frontend — run its own checks:

```bash
npm --prefix web run typecheck
npm --prefix web test
npm --prefix web run build
```

---

## Documentation map

| Doc | Covers |
|---|---|
| [`docs/getting-started.md`](docs/getting-started.md) | Install, run a first Machine, cold migrate, enable live migration, deploy the dashboard, Network Fabric |
| [`docs/architecture.md`](docs/architecture.md) | Components, the cold/live migration state machines, session durability, CSI/DRA mechanics, operational visibility |
| [`docs/migration-adapter.md`](docs/migration-adapter.md) | The migration adapter HTTP contract (destination/source APIs), trust boundary, the real `kairon-migration-adapter-fluxvm` implementation |
| [`docs/network-fabric.md`](docs/network-fabric.md) | `MachineNetworkPolicy`/`NetworkSecurityGroup` reference and the FluxVM eBPF edge |
| [`docs/tutorials/network-fabric.md`](docs/tutorials/network-fabric.md) · [`docs/guides/machine-network.md`](docs/guides/machine-network.md) · [`docs/guides/network-policy.md`](docs/guides/network-policy.md) | Network Fabric walkthrough and field-level guides |
| [`docs/runbook-migration-failures.md`](docs/runbook-migration-failures.md) | Diagnosing and resolving `NeedsRecovery`, alert-to-runbook cross-references |
| [`docs/runbook-multi-host-migration-test.md`](docs/runbook-multi-host-migration-test.md) | Real two-host live-migration testing (cert generation, adapter deploy, verification checklist) |
| [`docs/runbook-recovery-drill.md`](docs/runbook-recovery-drill.md) | Deliberately drilling a live `NeedsRecovery` recovery, including an honestly-labeled best-effort trigger |
| [`ROADMAP.md`](ROADMAP.md) | What shipped per version, what's still open, v0.5+ plans |
| [`RELEASE_NOTES.md`](RELEASE_NOTES.md) | Per-release changelog |
| [`SECURITY.md`](SECURITY.md) | Threat model, vulnerability reporting |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | PR checklist, coverage floor, frontend checks |

---

## Status

**v0.4.0** is open source and honest about maturity. Cold relocation, snapshots, DRA bridging, the secure live control plane, and a real FluxVM migration adapter are all real and tested — but real two-host live migration has not yet been exercised against real hardware in this repository's own CI (see [Operability](#operability)). HA fencing, PVC attach, full admission/quotas, and qualification work are tracked in [`ROADMAP.md`](ROADMAP.md).

### Production gaps

Pre-GA gaps include storage/network migration preflight, automatic fencing, DRA topology-aware placement scoring, certificate rotation, confidential-compute enforcement, and large-scale hardware qualification. Real two-host live migration and a live `NeedsRecovery` drill are documented as runbooks but not yet exercised against real hardware in this repository's own CI. PVC-backed boot disks (above) are a first cut: one boot volume per Machine, `Filesystem`-mode `PersistentVolume`s only, and only `hostPath`/`local` sources — Kairon doesn't run a CSI node plugin itself, so a network-block volume (Ceph RBD, EBS, etc.) needs to already be attached/mounted on the node by something else before Kairon can use it. `MachineSnapshotRestore` (above) restores into a new PVC only — it deliberately doesn't also create a Machine (see the guide for why) — and needs a real CSI snapshotter behind your StorageClass; Rancher's `local-path-provisioner` (a common default) doesn't implement one. CPU/memory hotplug (above) is grow-only (no CPU/DIMM unplug in FluxVM), bounded by headroom reserved automatically at creation (not yet configurable from Kairon), and every hotplugged resource is lost across a stop/start cycle — expected QEMU behavior, not a bug. Guest-agent integration is still not implemented on the Kairon side. Affinity/anti-affinity (above) is required-constraints-only — no preferred/soft affinity, no topology spread constraints, since there's no weighted scheduler scoring system to plug them into yet. `MachineDisruptionBudget` (above) is a `kaironctl`-side check only — there's still no automatic node-drain/eviction path at all, and a `MachineMigration` created directly through the API bypasses the budget entirely. `MachineQuota` (above) has no admission webhook either — it's enforced only once `kairon-controller`'s scheduling loop looks at a Machine, so an over-quota namespace can still create as many `Machine` objects as it wants, they just stay `Pending`; the migration concurrency quota (see [Operability](#operability)) remains a separate, narrower control specific to in-flight migrations.

A code-level audit also surfaced gaps not on that list: `kairon-ui` now supports real per-operator username/password login (`ui.auth.users`, bcrypt-hashed, signed session tokens, audit-attributed) alongside the legacy shared bearer token, with rate limiting/lockout on repeated failed attempts (5 failures → 5-minute lockout, per requested username) — but there's still no password-reset flow beyond regenerating a hash and redeploying, and its session-logout/default-admin-password/lockout state lives on a single replica (the chart runs exactly one). OIDC/SSO is a bigger, separate decision and isn't implemented. Also open: no `PodDisruptionBudget` or `NetworkPolicy` in the Helm chart, no image digest pinning or vulnerability scanning of published images (CI builds all three images but never pushes them — publishing is an out-of-repo process today), and no CRD-version-upgrade story beyond today's single `v1alpha1`.

The VNC console (`console.enabled`) inherits FluxVM's own unauthenticated VNC socket as-is — Kairon can't add auth/encryption FluxVM itself doesn't have — so its security rests on kairon-ui's operator auth, a single-use ticket bound to the requesting username (each session is now audit-logged), and a shared cluster-wide token to kairon-node. That hop can now optionally run over one-way TLS (`console.tls.enabled`); it's plain HTTP by default. `scripts/deploy-remote.sh --with-console` covers the bare-metal install path (token generation and env wiring only — TLS material there is still a manual step). See [SECURITY.md](SECURITY.md).

Report vulnerabilities privately to **security@zyvor.dev** — see [`SECURITY.md`](SECURITY.md).

---

## License

### Open source (Apache-2.0)

This repository is licensed under the [Apache License, Version 2.0](LICENSE).
You may use, modify, and run it for personal, lab, and commercial production
use at no charge, subject to Apache-2.0 (preserve notices / NOTICE where required).
See [NOTICE](NOTICE).

### Enterprise

Production support, SLAs, and Zyvor Enterprise products are licensed separately.
Contact [sales@zyvor.dev](mailto:sales@zyvor.dev) or see [zyvor.dev](https://zyvor.dev).
