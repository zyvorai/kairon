<div align="center">

# Kairon

### Real VMs, on Kubernetes, without the weight of KubeVirt

**Kubernetes declares. Kairon orchestrates. FluxVM executes.**

<img src="docs/assets/social-preview.png" alt="Kairon — Kubernetes-native VMs without KubeVirt" width="720">

[![CI](https://github.com/zyvorai/kairon/actions/workflows/ci.yml/badge.svg)](https://github.com/zyvorai/kairon/actions/workflows/ci.yml)
[![License: Apache-2.0](https://img.shields.io/github/license/zyvorai/kairon)](LICENSE)
[![Release](https://img.shields.io/badge/version-v0.4.0-blue)](VERSION)
[![Go](https://img.shields.io/badge/Go-stdlib%20only-00ADD8?logo=go)](go.mod)
[![Helm chart](https://img.shields.io/badge/Helm-0.4.0-0F1689?logo=helm)](charts/kairon/Chart.yaml)
[![Dashboard](https://img.shields.io/badge/dashboard-kairon--ui-ff5a15)](#the-dashboard)

[Why](#why-kairon-exists) · [Architecture](ARCHITECTURE.md) · [Quick start](#quick-start) · [Dashboard](#the-dashboard) · [Guarding the fleet](#guarding-the-fleet) · [Getting started](docs/getting-started.md) · [Security](SECURITY.md) · [Roadmap](ROADMAP.md) · [zyvor.dev](https://zyvor.dev?utm_source=github&utm_medium=kairon)

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
| Runtime dependencies | Large Go/operator surface | **Go standard library only** |

Three things follow from that premise, and they shape everything below:

- **Small enough to read in an afternoon.** `go.mod` has no `client-go`, no generated deep call stacks, no vendored operator framework — `kairon-controller` and `kairon-node` are each a handful of files. You can actually audit what's running your VMs, not just trust that someone else did. Two deliberate exceptions: `kairon-ui`'s opt-in OIDC/SSO (`ui.oidc.enabled`, off by default — real JWT/JWK verification against an external identity provider isn't something to hand-roll, pulling in `golang.org/x/oauth2`/`github.com/coreos/go-oidc/v3`) and Kairon's own opt-in CSI node plugin, `kairon-csi-node` (`csiNode.enabled`, off by default — the CSI protocol itself is a gRPC/protobuf contract with no stdlib-only way to speak it at all, pulling in `google.golang.org/grpc`/`github.com/container-storage-interface/spec`; `kairon-node` also links `google.golang.org/grpc` to dial it as a client, exercised only when a Machine actually uses a CSI-backed volume). See [SECURITY.md](SECURITY.md). `kairon-controller` and `kaironctl` stay Go-stdlib-only, unconditionally.
- **Kubernetes stays the only source of truth.** A `Machine` object is where desired state lives, full stop. The dashboard (`kairon-ui`) isn't a second database with its own opinions — it's a thin HTTP client with exactly the same standing as `kaironctl` or `kubectl`.
- **An uncertain outcome gets a name, not a guess.** When a live migration's commit result is genuinely ambiguous, Kairon doesn't flip a coin between "assume it worked" and "assume it didn't" — either one risks running the same VM twice. It parks the migration in `NeedsRecovery` and waits for an operator's attested decision. See [Relocating a Machine](#relocating-a-machine).

---

## Who reaches for this

| You are... | You need... | Kairon gives you... |
|---|---|---|
| **A platform engineer replacing KubeVirt** | Real VMs on Kubernetes without adopting `virt-launcher` Pods, libvirt, or a large operator surface | A `Machine` CRD, a stdlib-only controller/agent pair, FluxVM doing the actual KVM work — see the table above |
| **An operator who needs real SSO** | `kairon-ui` login tied to your existing identity provider, not a second set of passwords to manage | `ui.oidc.enabled`: Authorization Code + PKCE against any OIDC provider, alongside `ui.auth.users` — the control plane itself stays untouched and Go-stdlib-only either way, see [SECURITY.md](SECURITY.md) |
| **An SRE running a Machine fleet** | Draining a node without taking out more capacity than you can afford; capping what a noisy namespace can consume | `MachineDisruptionBudget` throttles disruption, `MachineQuota` caps `maxMachines`/CPU/memory per namespace — both are now enforceable at admission time, not just convention, see [Guarding the fleet](#guarding-the-fleet) |
| **Whoever's on call for live migration** | A migration commit that can't silently resolve into split-brain | `NeedsRecovery` names the ambiguous case explicitly and parks it for an attested human decision — see [Relocating a Machine](#relocating-a-machine) |
| **An economic buyer sizing up build-vs-adopt** | Honesty about what's real today versus what's still open | Apache-2.0, actively developed, and [Status](#status) below is a punch list, not a marketing page |

---

## Quick start

```bash
# 1. Label the nodes FluxVM actually runs on
kubectl label node worker-1 kairon.zyvor.dev/capable=true

# 2. Install
helm upgrade --install kairon ./charts/kairon --namespace kairon-system --create-namespace

# 3. Run something
kaironctl create demo --image /var/lib/fluxvm/images/ubuntu.qcow2 --cpu 2 --memory 2Gi --backend qemu
kaironctl get machines
```

FluxVM needs to already be listening on each capable node (default `127.0.0.1:7788`) with the image path visible to the host. Prefer raw manifests over Helm? `kubectl apply -f deploy/crd.yaml -f deploy/rbac.yaml -f deploy/controller.yaml -f deploy/node.yaml` does the same thing. Prefer a UI? Jump to [the dashboard](#the-dashboard) once your first Machine is up.

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
                                     FluxVM local API · migration adapter (Unix socket) · KVM/VMM
```

Kubernetes is the source of truth. FluxVM owns execution. Kairon owns placement, relocation policy, and Kubernetes lifecycle semantics — nothing more, nothing hidden behind an abstraction. `kairon-ui` isn't pictured as a fourth tier because it isn't one: it talks to the same Kubernetes API everything else does. Full write-up: [`ARCHITECTURE.md`](ARCHITECTURE.md) (the ten-minute tour) or [`docs/architecture.md`](docs/architecture.md) (the deep reference).

---

## What ships today

**Machine lifecycle & placement**
- **Machine CRD** — CPU, memory, image, network, power state, volumes, DRA device claims
- **`MachineSet`** — a Deployment/ReplicaSet-shaped fleet of identical Machines from one template, with `RollingUpdate`/`Recreate` rollout — [guide](docs/guides/machine-sets.md)
- **`MachineInstanceType`** — a reusable named CPU/memory shape (`spec.instanceTypeName`), resolved into `spec.resources` once instead of inlining it every time — [guide](docs/guides/machine-instance-types.md)
- **NUMA topology, CPU set, hugepages** (`spec.resources.numaNode`/`.cpuSet`/`.hugepages`, qemu-only) — direct passthroughs to FluxVM's own existing support, refused with a clear error on a non-qemu backend rather than silently ignored — [guide](docs/guides/machine-cpu-numa.md)
- **Real CPU pinning** (`spec.resources.cpuPinning`, qemu-only) — exclusive host-core allocation, a real scheduler capacity model against operator-asserted `kairon.zyvor.dev/pinnable-cpus` node labels — atomically patched alongside node assignment, applied via the same cgroup `cpuset.cpus` write `spec.resources.limits` already uses — [guide](docs/guides/machine-cpu-pinning.md)
- **Windows guests** — legacy-BIOS Windows Server/10 with a pre-built, virtio-driver + cloudbase-init image works today (`spec.cloudInit` unchanged); `spec.security.secureBoot`/`.tpm` (Windows 11) are now refused with a clear error instead of silently doing nothing — no FluxVM backend implements UEFI/vTPM yet — [guide](docs/guides/machine-windows-guests.md)
- **`MigrationPolicy`** — scopes migration bandwidth/concurrency to Machines matching a selector, independent of the global `migration.maxConcurrentPerNode`/`maxConcurrentCluster` caps — [guide](docs/guides/migration-policies.md)
- **PVC-backed boot disk** — `spec.volumes[0]` resolves through a Bound `PersistentVolumeClaim` to a real host directory (`hostPath`/`local` `PersistentVolume`s directly, or a network-block volume via Kairon's own first-cut CSI driver) instead of a hand-placed image file — [guide](docs/guides/machine-storage.md) · [CSI guide](docs/guides/machine-storage-csi.md)
- **Third-party CSI client** (`node.thirdPartyCSIDrivers`, first cut) — `kairon-node` acts as its own generic CSI client against an operator-allowlisted third-party driver's socket (validated conceptually against Ceph-CSI/RBD), for `attachRequired: false`, no-secret drivers only — [guide](docs/guides/machine-storage-thirdparty-csi.md)
- **Image import** (`spec.image.source`, opt-in via `node.imageCacheDir`) — `kairon-node` downloads a remote qcow2/raw image URL into its own digest-keyed cache instead of requiring a hand-placed file, shared across every Machine naming the same digest — [guide](docs/guides/machine-image-import.md)
- **Image catalog** (`spec.image.catalogName`, admin-only for mutations, API-only) — reference a named, checksummed (optionally signed) FluxVM image-catalog alias instead of a raw path; node-scoped register/rename/clone/export/read-only API — [guide](docs/guides/machine-image-catalog.md)
- **Egress check** (admin-only, API-only) — ask a node whether a sandbox's outbound request to a host would be allowed under its static egress allowlist, with any matched credential-vault secret reported only as a boolean — [guide](docs/guides/machine-sandboxes.md#checking-egress-policy)
- **Warm pools** (admin-only, API-only) — create a FluxVM pool of pre-booted, `Paused` VMs for instant claiming instead of a cold create; node-scoped create/list/get/delete/claim API — [guide](docs/guides/machine-sandboxes.md#warm-pools)
- **Runtime diagnostics** (freeze/thaw admin-only, rest any-operator, API-only) — a node's real FluxVM capability manifest, a Machine's cgroup-derived PSI pressure stats and effective host CPU set, and a kernel-level cgroup freeze/thaw distinct from `spec.powerState: Paused` — [guide](docs/guides/machine-diagnostics.md)
- **Network observability** (any-operator, API-only) — a Machine's fully-resolved effective network policy, real drop-reason/flow/stats data straight from FluxVM's eBPF dataplane — the direct answer to "why is my `MachineNetworkPolicy` blocking traffic" — [guide](docs/guides/network-policy.md) ("Troubleshooting" section)
- **Placement** — least-loaded scheduling across Ready, capable-labeled nodes, deterministic tie-break, required (hard) `spec.placement.affinity`/`antiAffinity`, weighted soft scoring (`preferredAffinity`/`preferredAntiAffinity`, `topologySpreadConstraints`), a best-effort DRA topology-awareness hint, and `topologySpreadConstraints.maxSkew` hard enforcement via `whenUnsatisfiable: DoNotSchedule` — [guide](docs/guides/machine-placement.md)
- **DRA → VFIO** — an allocated `ResourceClaim`'s PCI BDF against the node's `vfio_devices` administrator allowlist, fail-closed (an empty allowlist denies all passthrough); device-type-agnostic, so this also covers **SR-IOV NIC passthrough** once a VF is bound to `vfio-pci` on the host — the same mechanism GPU passthrough already uses, not a Multus integration (Machines have no Pod for Multus to attach to) — [guide](docs/guides/machine-sriov.md)
- **CPU/memory hotplug** — grow a running Machine's `spec.resources` via FluxVM's real QMP `device_add`/`object-add`, no reboot — [guide](docs/guides/machine-hotplug.md)
- **Pause and resume** (`spec.powerState: Paused`) — suspend a running Machine's guest CPUs via FluxVM's real QMP `stop`/`cont` (backend-agnostic), RAM and device state fully resident, Kairon's `virtctl pause`/`unpause` equivalent — `kaironctl pause`/`resume`, or the dashboard — [guide](docs/guides/machine-pause-resume.md)
- **Halt** (`spec.powerState: Halted`) — power a Machine's VMM process off while FluxVM keeps its own VM record/disk intact, freeing node capacity like `Stopped` but resuming via FluxVM's own kept config instead of a full Kairon-side recreate — `kaironctl halt`, or the dashboard — [guide](docs/guides/machine-halt.md)
- **Live host resource limits** (`spec.resources.limits`) — a real, kernel-enforced cgroup v2 cap (CPU quota %, memory ceiling, I/O weight, PID count) on a running Machine's own VMM process, freely raisable/lowerable at any time, backend-agnostic — [guide](docs/guides/machine-resource-limits.md)
- **Live resource usage** (`status.resourceUsage`) — real, cgroup-derived CPU/memory/disk-I/O usage refreshed every reconcile tick, shown inline in the dashboard next to each Machine — [guide](docs/guides/machine-resource-limits.md)
- **Guest agent** (`spec.guestAgent`) — real `qemu-guest-agent`-reported `status.guestIP`, including for `user`/SLIRP networking, which has no DHCP lease to parse at all — [guide](docs/guides/machine-guest-agent.md)
- **Guest exec** (`spec.guestAgent.enabled`, admin-only) — run a one-shot command inside the guest via `kairon-ui`'s dashboard, no SSH/console needed — real `qemu-guest-agent` guest-exec, synchronous, exit code + stdout/stderr — [guide](docs/guides/machine-guest-exec.md)
- **Text console** (`spec.guestAgent.console`) — an interactive shell in-browser (xterm.js), Kairon's `virtctl console` equivalent, working on every backend — requires FluxVM's own proprietary `fluxvm-guest-agent` baked into the guest image — [guide](docs/guides/machine-text-console.md)
- **Guest file access** (`spec.guestAgent.console`, admin-only) — read or write a file inside a `Running` guest at runtime, no SSH/shared volume needed — same `fluxvm-guest-agent` channel as the text console — [guide](docs/guides/machine-guest-agent-files.md)
- **Backend-agnostic guest exec** (`spec.guestAgent.console`, admin-only, API-only) — run a command over FluxVM's own vsock agent instead of `qemu-guest-agent` — the only guest-exec path that works on Cloud Hypervisor/Firecracker/FluxVm-backend sandboxes, not just QEMU — [guide](docs/guides/machine-guest-agent-files.md)
- **Sandboxes** (`spec.sandbox`, admin-only, API-only) — run a Machine on FluxVM's own lightweight, fast-boot in-tree hypervisor track for short-lived ephemeral workloads, optionally from a pre-built template; includes an HTTP proxy relay straight into the guest's own web server — [guide](docs/guides/machine-sandboxes.md)
- **Machine logs** — `kubectl logs`/`-f` equivalent: the VM's real captured serial console output, with a live-tailing **Follow** toggle, same any-operator authorization as the VNC console — [guide](docs/guides/machine-logs.md)
- **VM-state snapshot/restore** (admin-only, API-only) — a full hypervisor-level checkpoint of a running Machine's RAM/CPU/device state via FluxVM's real `savevm`/Cloud Hypervisor snapshot, restored back into the same Machine in place; distinct from `MachineSnapshot`'s disk-content-only CSI snapshot — [guide](docs/guides/machine-vm-state-snapshot.md)

**Migration**
- **Cold migrate & evacuate** — stop → reassign → restart, restart-safe in the API
- **Secure live handshake** — TLS 1.3 mTLS `prepare → transfer → commit`, never a user-supplied `tcp:` URI
- **Split-brain guards** — rollback on transfer failure, `NeedsRecovery` on ambiguous commit, adopt-only cutover
- **A real migration adapter ships** — `cmd/kairon-migration-adapter-fluxvm` against real FluxVM endpoints, not just a test double
- **Node fencing is detection + operator-attested action, never a guess** — `kairon-controller` flags a Machine's node `NodeUnreachable` every reconcile tick but never reschedules it itself (that risks running the same VM twice if the node isn't actually dead); `kaironctl fence --reason ...` is the explicit, attested step that clears it for rescheduling — same shape as `NeedsRecovery`. Opt-in `kairon.zyvor.dev/storage-domain`/`network-domain` node labels also let migration preflight block a target when it's *confirmed* to not share storage/network with the source — [guide](docs/guides/machine-fencing.md)

**Guarding the fleet**
- **`MachineDisruptionBudget`** — `kaironctl evacuate` throttles itself against `minAvailable`/`maxUnavailable` instead of taking a whole node's Machines at once; `kairon-controller` now reconciles real `status` every tick too (`kaironctl get budgets`/`kubectl get mdb`), purely observational — [guide](docs/guides/machine-disruption-budgets.md)
- **Cordon-triggered automatic evacuation** (`controller.cordonEvacuation.enabled`, opt-in) — `kairon-controller` itself migrates every Machine off a Node the moment it's cordoned, budget-throttled exactly like `kaironctl evacuate`, mirroring KubeVirt's `LiveMigrateIfPossible` — [guide](docs/guides/machine-disruption-budgets.md#automatic-cordon-triggered-evacuation)
- **`MachineQuota`** — a namespace-scoped `maxMachines`/`maxTotalCpu`/`maxTotalMemory` cap — [guide](docs/guides/machine-quotas.md)
- **Admission webhook** (`webhook.enabled`, opt-in) — both of the above can now be enforced *at admission*, not just in the reconcile loop or `kaironctl`: a `Machine` create that would blow a quota, or a `MachineMigration` create that would violate a budget, gets rejected outright instead of just parked `Pending` or silently allowed. See [Guarding the fleet](#guarding-the-fleet) below.
- **Network Fabric** — `spec.network`, `MachineNetworkPolicy`, `NetworkSecurityGroup`, Service Fabric VIP membership → FluxVM eBPF edge — [reference](docs/network-fabric.md)

**Snapshots**
- **CSI VolumeSnapshot** — `MachineSnapshot` orchestrates a standard snapshot object; `MachineSnapshotRestore` restores one into a *new* `PersistentVolumeClaim` via the standard CSI `dataSource` flow (deliberately doesn't also create a Machine — see the guide for why) — [guide](docs/guides/machine-snapshot-restore.md)
- **Guest quiesce for snapshots** (`spec.guestAgent.enabled`) — a real `guest-fsfreeze`/`-thaw` around `MachineSnapshot`'s VolumeSnapshot creation for an application-consistent snapshot, coordinated between `kairon-controller` and `kairon-node` since only the node has a network path to the Machine's FluxVM instance — [guide](docs/guides/machine-snapshot-quiesce.md)

**Operate it**
- **`kaironctl`** — create, start/stop, migrate, evacuate, snapshot, recover
- **`kairon-ui`** — an optional web dashboard, real per-operator login with in-place password change/reset (not "regenerate a hash and redeploy"), optional OIDC/SSO, a `NeedsRecovery` recovery workflow, an optional VNC console, and more than one replica once you need it — see [The dashboard](#the-dashboard)
- **`kairon-controller` HA** — `controller.replicaCount` can go above 1: a `coordination.k8s.io/v1` Lease (`internal/leaderelection`, on by default) coordinates replicas so only the elected leader reconciles, while the admission webhook and health/metrics endpoints keep serving from every replica regardless — see [guide](docs/guides/kairon-controller-ha.md)
- **Prometheus metrics + alert rules**, a per-node/cluster migration concurrency quota, `status.dataPlaneEncrypted` visibility into whether a live transfer is actually encrypted
- **`PodDisruptionBudget`** on by default, opt-in `NetworkPolicy`, digest-pinned + Trivy-scanned + cosign-signed + SBOM'd container images, Helm + raw manifests + CI

Live *memory* transfer needs a node-local [migration adapter](docs/migration-adapter.md) deployed and configured on each node — without one, a live request blocks before the source is touched. Cold migration needs nothing extra and works today.

---

## Relocating a Machine

**Cold** — the proven path, needs shared or identically provisioned images:

```bash
kaironctl migrate demo --strategy cold --target-node worker-2
kaironctl evacuate worker-1          # batch cold/auto migrations off a node, budget-aware
kaironctl evacuate worker-1 --wait   # ...and keep retrying until the node is actually empty, like kubectl drain
```

**Live** — a secure peer handshake; you pick a *node*, never a raw destination:

```bash
kaironctl migrate demo --strategy live --target-node worker-2 \
  --mode pre-copy --bandwidth-mbps 800 --max-downtime-ms 200
```

```text
controller → source node ──mTLS──▶ target :9443
                      prepare + journal
             ◀── opaque transfer endpoint ──
source adapter transfers ──▶ target commit
                      adopt-only cutover
```

Enable it:

```bash
kubectl -n kairon-system create secret generic kairon-migration-tls \
  --from-file=ca.crt --from-file=tls.crt --from-file=tls.key
helm upgrade --install kairon ./charts/kairon -n kairon-system --set migration.enabled=true
```

Certificates need `serverAuth` + `clientAuth` and the chart's migration server name (default `kairon-node`) — see [`docs/migration-adapter.md`](docs/migration-adapter.md) for the full contract and fail-closed behavior.

If a commit's outcome is genuinely ambiguous, the migration lands in `NeedsRecovery` instead of a guess. Work through it via [`docs/runbook-migration-failures.md`](docs/runbook-migration-failures.md)'s decision tree, or resolve it straight from [the dashboard](#the-dashboard).

---

## Guarding the fleet

`MachineQuota` and `MachineDisruptionBudget` have always existed as reconcile-loop/CLI-level checks — a Machine over quota just stayed `Pending`; a `MachineMigration` created directly through the API skipped `kaironctl evacuate`'s budget check entirely. Both gaps are now closeable with a real `ValidatingWebhookConfiguration`:

```bash
# You bring the certificate -- this chart doesn't mint one for you,
# same posture as migration.tlsSecretName/console.tls.secretName.
kubectl -n kairon-system create secret generic kairon-webhook-tls \
  --from-file=tls.crt --from-file=tls.key

helm upgrade --install kairon ./charts/kairon -n kairon-system \
  --set webhook.enabled=true \
  --set webhook.tlsSecretName=kairon-webhook-tls \
  --set webhook.caBundle="$(base64 -w0 ca.crt)"
```

It reuses the exact decision functions the reconcile loop and `kaironctl evacuate` already had (`internal/controller/quota.go`, `internal/controller/disruption.go`) — not a second implementation to drift out of sync. `MachineDisruptionBudget` and Machine `CREATE` quota checks only ever evaluate `CREATE`, matching each check's pre-existing scope in the reconcile loop, so the webhook can't become *stricter* than what it backstops there. `MachineQuota` is the one exception: the webhook also evaluates `UPDATE`, denying a hotplug resize (`spec.resources` growing on an already-scheduled Machine) that would push its namespace over quota — closing a real gap CREATE alone leaves open, since `kairon-node`'s hotplug has no cluster-wide quota visibility of its own to backstop with. `webhook.failurePolicy` defaults to `Fail` — an outage blocks every `Machine`/`MachineMigration` write cluster-wide rather than silently letting the old bypass back in; flip it to `Ignore` if you'd rather trade that guarantee for availability.

---

## The dashboard

`kairon-ui` is an optional web dashboard (Go backend + a small React/TypeScript SPA) for Machines, Migrations, Snapshots, and — the one workflow that earns a UI on its own — the `NeedsRecovery` operator-attested recovery form. It has no side channel and no second source of truth; it's a thin wrapper over the same Kubernetes API `kaironctl` uses.

| Page | What it shows |
|---|---|
| **Overview** | Fleet tiles by phase, a prominent warning whenever anything is parked in `NeedsRecovery` |
| **Machines** | Table + create form (mirrors `kaironctl create`) + Start / Stop / Console / Migrate / Snapshot / Delete |
| **Migrations** | List with phase badges, a "New Migration" form, an "Evacuate Node" action, `status.dataPlaneEncrypted` badge, and the recovery workflow above |
| **Snapshots** | List + create form |
| **Account** | Change your own password; an admin account can reset another operator's |

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system --set ui.enabled=true
kubectl -n kairon-system port-forward svc/kairon-ui 8082:8082
```

With no `ui.*` auth values set, the chart seeds one default `admin` account with a random, generated-once password:

```bash
kubectl -n kairon-system get secret kairon-ui-session -o jsonpath='{.data.defaultAdminPassword}' | base64 -d; echo
```

For real, named, per-operator accounts instead:

```bash
kairon-ui -hash-password 'a real password'
helm upgrade --install kairon ./charts/kairon -n kairon-system \
  --set ui.enabled=true \
  --set ui.auth.users[0].username=alice \
  --set ui.auth.users[0].passwordHash='$2a$10$...' \
  --set ui.auth.users[0].admin=true
```

A named account can change its own password from the dashboard (`POST /api/v1/auth/password`) — no more hand-hashing and redeploying. An `admin: true` account can reset anyone else's, which also immediately signs that operator's active sessions out. (The zero-config seeded `admin` account can sign in like anyone else but can't self-service its password yet — graduate to a named account for that.) Every mutating request is logged with method, path, remote address, resulting status, and — for a session-token login — the signed-in username. The legacy shared token (`ui.token`/`ui.existingSecret`, or `ui.allowUnauthenticated=true` for local dev) still works unchanged alongside `ui.auth.users`; treat it like a shared root password if you're still on it.

**OIDC/SSO** (`ui.oidc.enabled`, off by default): "Sign in with SSO" against an external identity provider, alongside `ui.auth.users` — good for tying kairon-ui into your existing SSO rather than managing a second set of passwords. Deliberately the one place Kairon breaks its own Go-stdlib-only guarantee, since real JWT/JWK verification isn't something to hand-roll; see [SECURITY.md](SECURITY.md). An OIDC-authenticated session is a normal operator, never an admin, regardless of provider-side groups/roles — see [`docs/guides/kairon-ui-oidc.md`](docs/guides/kairon-ui-oidc.md).

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system \
  --set ui.enabled=true \
  --set ui.oidc.enabled=true \
  --set ui.oidc.issuerURL=https://idp.example.com \
  --set ui.oidc.clientID=kairon-ui \
  --set ui.oidc.clientSecret=... \
  --set ui.oidc.redirectURL=https://kairon.example.com/api/v1/auth/oidc/callback
```

`ui.replicaCount` (`1` by default) can now go above 1 — session revocation, login lockout, console tickets, and password changes all propagate across replicas via a Kubernetes-native `ConfigMap` this chart creates alongside `kairon-ui`, deliberately not Redis. Propagation is eventually-consistent (~15s), not instant, and login-lockout's failure count is evaluated per-replica, not as one cluster-wide counter — see [`docs/guides/kairon-ui-ha.md`](docs/guides/kairon-ui-ha.md) for what that actually means before you rely on it.

**VNC console** (`--set console.enabled=true`, off by default): a real graphical VNC session in the browser for QEMU-backend Machines, relayed `kairon-ui → kairon-node → the VM's local socket`. Read [SECURITY.md](SECURITY.md)'s "VNC console" section first — FluxVM's own VNC socket has no auth of its own, so this trades convenience for a trust chain suited to a trusted operator team, not a hostile network. That relay hop can now run over TLS (`console.tls.enabled`).

No Kubernetes cluster handy? Bare-metal works too:

```bash
scripts/deploy-remote.sh sus@80.79.5.173 --with-controller --with-ui --with-console --console-tls
```

Installs everything as systemd services; a dashboard token is auto-generated and printed once. `--console-tls` now automates TLS for the console relay hop too — with no cert flags given, it generates a private CA and server certificate locally and installs them on the remote host; pass `--console-tls-cert`/`-key`/`-ca` to bring your own instead. Requires `npm` locally only for `--with-ui` — the one place this script needs Node.js.

---

## Snapshots & devices

```yaml
spec:
  volumes:
    - {name: data, claimName: database-data}
```
```bash
kaironctl snapshot database --name before-upgrade --class csi-snapclass
```

```yaml
spec:
  deviceClaims:
    - name: gpu-claim   # administrator allowlist on the node -- empty allowlist denies all
```

More worked examples: [`examples/`](examples/).

---

## CLI

```text
kaironctl get [machines|migrations|snapshots|quotas|restores] [-n NS]
kaironctl describe NAME
kaironctl create NAME --image PATH [--cpu N] [--memory SIZE] [--backend qemu|…]
                 [--forward hostPort:guestPort[/proto]] [--hostname NAME] [--user NAME]
                 [--ssh-key KEY] [--package PKG] [--runcmd CMD]  # repeatable/cloud-init, see docs/guides/machine-network.md
kaironctl start|stop|delete NAME
kaironctl migrate MACHINE --strategy auto|cold|live --target-node NODE
kaironctl evacuate NODE [--strategy cold|auto] [--wait] [--timeout 15m] [--poll-interval 10s]
kaironctl snapshot MACHINE [--name NAME] [--class CLASS]
kaironctl restore SNAPSHOT --target-claim NAME
kaironctl recover MIGRATION --action ACTION --diagnosis DIAGNOSIS --reason REASON
kaironctl fence MACHINE --reason REASON  # only once NodeUnreachable=True and you've confirmed the node is truly gone
kaironctl version
```

Point at a cluster with `KAIRON_KUBE_URL` (e.g. after `kubectl proxy`), or run in-cluster with the mounted service account.

**`kubectl kairon ...`** works identically once `kubectl-kairon` (built by `make build` alongside `kaironctl`) is on `$PATH` — a real kubectl plugin sharing kaironctl's exact command dispatch (`internal/kaironctl`), not a shim shelling out to a separate binary. `kubectl kairon evacuate worker-1` is exactly `kaironctl evacuate worker-1`.

---

## Operability

All three workloads expose Prometheus metrics on their existing health/listen port (`/metrics`) — previously this was `kairon-controller`-only and migration-lifecycle-only, the one real observability blind spot a production-readiness review of this project turned up: no signal at all if the controller itself was unhealthy or wedged, or if the apiserver was unreachable. Now: `kairon-controller` additionally exposes reconcile-loop duration/error metrics and, if `webhook.enabled`, admission-webhook decision counts; `kairon-node` exposes the same reconcile-loop metrics on its own `/metrics`; `kairon-ui` exposes its own HTTP request duration/status metrics. Every component with a `internal/kube.Client` (all three) also exposes `kairon_apiserver_request_duration_seconds`, by HTTP method and outcome — see `internal/metrics`'s package doc for exactly which metrics each component's Recorder registers. `kairon-controller` still additionally carries the original migration-specific set: counts by phase, phase age, transfer/downtime histograms, completion counters, a `dataplane_encrypted` gauge. Example alert rules ship in [`charts/kairon/alerts.yaml`](charts/kairon/alerts.yaml) (optionally a real `PrometheusRule` via `metrics.prometheusRule.enabled=true`, renamed `kairon-alerts` from `kairon-migrations` to match) — migration alerts point at [`docs/runbook-migration-failures.md`](docs/runbook-migration-failures.md), the three new cross-component health alerts (`kairon-health` group) at each component's own logs.

`migration.maxConcurrentPerNode`/`migration.maxConcurrentCluster` (both `0` = unlimited) bound how many non-terminal migrations may touch one node or the cluster at once — a lightweight admission control, mainly useful to cap the blast radius of a bulk `kaironctl evacuate`.

All three workloads set CPU/memory `resources:` by default, `kairon-controller`/`kairon-ui` each get a `PodDisruptionBudget` (on by default — voluntary-eviction protection only, not HA), and an opt-in `NetworkPolicy` (`*.networkPolicy.enabled`) restricts their ingress once you tell it which other namespace needs to reach in (`*.networkPolicy.allowIngressFrom` — get this wrong and Prometheus scraping or your ingress controller breaks silently, so it's opt-in rather than default-on). CI runs `govulncheck` on every push, pins every `Dockerfile` base image to a digest, and scans every built image with Trivy; a tag-triggered workflow publishes scanned, digest-pinned images to `ghcr.io/zyvorai/kairon-*`, each one cosign-signed (keyless, via the workflow's own GitHub OIDC identity) and SBOM-attested (`anchore/sbom-action`/`syft`, SPDX) by digest — see [SECURITY.md](SECURITY.md)'s "Images" section for the exact `cosign verify` commands.

Real two-host live-migration testing and a live `NeedsRecovery` drill are documented as runbooks with helper scripts, since they need hardware this repository's own CI doesn't have: [`docs/runbook-multi-host-migration-test.md`](docs/runbook-multi-host-migration-test.md), [`docs/runbook-recovery-drill.md`](docs/runbook-recovery-drill.md).

---

## Develop

```bash
make all        # fmt · vet · lint · test-race · cover-check · build · validate · smoke
make test-race
./scripts/must-gather.sh
```

Runtime code uses the **Go standard library only** — no `client-go`, no generated deep stacks. That's a deliberate constraint, not an oversight: it keeps the control plane small enough to actually audit, and it's why the admission webhook above hand-rolls the small, stable `AdmissionReview` JSON shape (`internal/admission`) instead of pulling in `k8s.io/api`. Two deliberate exceptions: `kairon-ui`'s opt-in OIDC/SSO (`golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3`) and Kairon's own opt-in CSI node plugin, `kairon-csi-node` (`google.golang.org/grpc`, `github.com/container-storage-interface/spec` — the CSI protocol has no stdlib-only wire format at all). `kairon-node` also links `google.golang.org/grpc` to dial `kairon-csi-node` as a client, exercised only when a Machine uses a CSI-backed volume; `kairon-controller`/`kaironctl` pull in none of this either way. See [SECURITY.md](SECURITY.md).

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
| [`ARCHITECTURE.md`](ARCHITECTURE.md) | The ten-minute tour: components, request flow, trust boundaries/deployment topology, design rationale |
| [`docs/getting-started.md`](docs/getting-started.md) | Install, first Machine, cold migrate, live migration, the dashboard, Network Fabric |
| [`docs/tutorials/machine-lifecycle.md`](docs/tutorials/machine-lifecycle.md) | Narrated walkthrough: one Machine through create → relocate → snapshot → restore |
| [`docs/architecture.md`](docs/architecture.md) | Deep reference: the cold/live migration state machines, session durability, CSI/DRA mechanics, operational visibility |
| [`docs/migration-adapter.md`](docs/migration-adapter.md) | The migration adapter HTTP contract, trust boundary, the real `kairon-migration-adapter-fluxvm` implementation |
| [`docs/network-fabric.md`](docs/network-fabric.md) | `MachineNetworkPolicy`/`NetworkSecurityGroup` reference and the FluxVM eBPF edge |
| [`docs/tutorials/network-fabric.md`](docs/tutorials/network-fabric.md) · [`docs/guides/machine-network.md`](docs/guides/machine-network.md) · [`docs/guides/network-policy.md`](docs/guides/network-policy.md) | Network Fabric walkthrough and field-level guides |
| [`docs/guides/machine-quotas.md`](docs/guides/machine-quotas.md) · [`docs/guides/machine-disruption-budgets.md`](docs/guides/machine-disruption-budgets.md) | `MachineQuota`/`MachineDisruptionBudget` reference, including the admission webhook |
| [`docs/runbook-migration-failures.md`](docs/runbook-migration-failures.md) | Diagnosing and resolving `NeedsRecovery`, alert-to-runbook cross-references |
| [`docs/guides/machine-fencing.md`](docs/guides/machine-fencing.md) | `NodeUnreachable`/`Fenced` conditions, `kaironctl fence`'s safety model, storage/network migration preflight labels |
| [`docs/guides/kairon-ui-ha.md`](docs/guides/kairon-ui-ha.md) | Running `ui.replicaCount > 1`: what's shared, how, and its real limits |
| [`docs/guides/kairon-controller-ha.md`](docs/guides/kairon-controller-ha.md) | Running `controller.replicaCount > 1`: Lease-based leader election, RBAC, bare-metal setup |
| [`docs/guides/observability.md`](docs/guides/observability.md) | What each component's `/metrics` exposes, the `kairon-health` alert group, and the renamed `PrometheusRule` |
| [`docs/guides/machine-migration-tls.md`](docs/guides/machine-migration-tls.md) | Control-plane vs. data-plane migration TLS, and real per-node data-plane identity via `migration.dataplaneTlsSecretName` |
| [`docs/guides/kairon-ui-oidc.md`](docs/guides/kairon-ui-oidc.md) | OIDC/SSO setup, the Authorization Code + PKCE flow, and why it breaks Go-stdlib-only |
| [`docs/guides/kairon-ui-console-rbac.md`](docs/guides/kairon-ui-console-rbac.md) | Real Kubernetes RBAC (`machines/console` + `SubjectAccessReview`) for console access, opt-in alongside the annotation allowlist |
| [`docs/guides/machine-storage.md`](docs/guides/machine-storage.md) · [`docs/guides/machine-storage-csi.md`](docs/guides/machine-storage-csi.md) | PVC-backed boot disks; Kairon's own first-cut iSCSI CSI driver and why it breaks Go-stdlib-only |
| [`docs/guides/machine-storage-thirdparty-csi.md`](docs/guides/machine-storage-thirdparty-csi.md) | `node.thirdPartyCSIDrivers`: `kairon-node` as a generic CSI client against an allowlisted third-party driver, first cut scoped to `attachRequired: false`, no-secret drivers |
| [`docs/guides/machine-image-import.md`](docs/guides/machine-image-import.md) | `spec.image.source`: downloading a remote image URL into `kairon-node`'s own digest-keyed cache |
| [`docs/guides/machine-sets.md`](docs/guides/machine-sets.md) | `MachineSet`: replica reconciliation, `RollingUpdate`/`Recreate` rollout strategy |
| [`docs/guides/machine-instance-types.md`](docs/guides/machine-instance-types.md) | `MachineInstanceType`: a reusable named CPU/memory shape resolved into `spec.resources` once |
| [`docs/guides/machine-cpu-numa.md`](docs/guides/machine-cpu-numa.md) | `spec.resources.numaNode`/`.cpuSet`/`.hugepages`: qemu-only NUMA/hugepage passthroughs, and what they don't guarantee (no real host-core pinning) |
| [`docs/guides/machine-cpu-pinning.md`](docs/guides/machine-cpu-pinning.md) | `spec.resources.cpuPinning`: real exclusive host-core allocation, and the operator-asserted `pinnable-cpus` node label it depends on |
| [`docs/guides/machine-windows-guests.md`](docs/guides/machine-windows-guests.md) | What Windows guest support actually covers today (legacy-BIOS + cloudbase-init) and what's blocked on FluxVM (UEFI/Secure Boot/vTPM) |
| [`docs/guides/migration-policies.md`](docs/guides/migration-policies.md) | `MigrationPolicy`: selector-scoped migration bandwidth defaulting and concurrency caps |
| [`docs/guides/machine-snapshot-quiesce.md`](docs/guides/machine-snapshot-quiesce.md) | Real guest `fsfreeze`/`fsthaw` around `MachineSnapshot`, and the controller↔node coordination protocol behind it |
| [`docs/guides/machine-snapshot-restore.md`](docs/guides/machine-snapshot-restore.md) | Restoring a `MachineSnapshot` volume into a new `PersistentVolumeClaim` via the standard CSI `dataSource` flow |
| [`docs/guides/machine-pause-resume.md`](docs/guides/machine-pause-resume.md) · [`docs/guides/machine-halt.md`](docs/guides/machine-halt.md) | `spec.powerState: Paused`/`Halted`: suspending guest CPUs vs. powering off the VMM process while FluxVM keeps its record |
| [`docs/guides/machine-hotplug.md`](docs/guides/machine-hotplug.md) | Growing `spec.resources.cpu`/`.memory` on an already-`Running` Machine via FluxVM's real QMP hotplug |
| [`docs/guides/machine-resource-limits.md`](docs/guides/machine-resource-limits.md) | `spec.resources.limits`: real, kernel-enforced cgroup v2 caps, and `status.resourceUsage` live usage |
| [`docs/guides/machine-placement.md`](docs/guides/machine-placement.md) | Scheduler internals: affinity/anti-affinity, weighted soft scoring, `topologySpreadConstraints`, DRA topology hints |
| [`docs/guides/machine-sriov.md`](docs/guides/machine-sriov.md) | SR-IOV NIC passthrough by reusing the existing GPU/VFIO DRA mechanism — why Multus doesn't apply to Kairon Machines at all |
| [`docs/guides/machine-guest-agent.md`](docs/guides/machine-guest-agent.md) | `spec.guestAgent`: real `qemu-guest-agent`-reported `status.guestIP`, including `user`/SLIRP networking |
| [`docs/guides/machine-guest-exec.md`](docs/guides/machine-guest-exec.md) | Guest exec (`qemu-guest-agent`), and the smaller API-only fsfreeze-status/firewall endpoints alongside it |
| [`docs/guides/machine-guest-agent-files.md`](docs/guides/machine-guest-agent-files.md) | Guest file access and the backend-agnostic vsock-agent guest exec, both over `spec.guestAgent.console` |
| [`docs/guides/machine-text-console.md`](docs/guides/machine-text-console.md) | Interactive in-browser text console over FluxVM's own proprietary vsock guest agent |
| [`docs/guides/machine-logs.md`](docs/guides/machine-logs.md) | Streaming a Machine's real captured serial console output -- Kairon's `kubectl logs`/`-f` equivalent |
| [`docs/guides/machine-vm-state-snapshot.md`](docs/guides/machine-vm-state-snapshot.md) | Full hypervisor-level VM-state checkpoint/restore (RAM, CPU, device state) -- distinct from `MachineSnapshot`'s disk-content-only CSI snapshot |
| [`docs/guides/machine-sandboxes.md`](docs/guides/machine-sandboxes.md) | `spec.sandbox`: FluxVM's lightweight agent-sandbox track, templates, the HTTP proxy relay, warm pools, and the egress check |
| [`docs/guides/machine-image-catalog.md`](docs/guides/machine-image-catalog.md) | `spec.image.catalogName`: FluxVM's node-local, checksummed image catalog and its admin API |
| [`docs/guides/machine-diagnostics.md`](docs/guides/machine-diagnostics.md) | Runtime capabilities, PSI pressure, effective CPU set, and cgroup-level freeze/thaw |
| [`docs/guides/crd-versioning.md`](docs/guides/crd-versioning.md) | What a real CRD version bump (`v1beta1`) still requires; the conversion webhook scaffold that exists today |
| [`docs/runbook-multi-host-migration-test.md`](docs/runbook-multi-host-migration-test.md) · [`docs/runbook-recovery-drill.md`](docs/runbook-recovery-drill.md) | Real two-host live-migration testing; deliberately drilling a `NeedsRecovery` recovery |
| [`docs/runbook-backup-restore.md`](docs/runbook-backup-restore.md) | Backing up/restoring Kairon's CRD state (`scripts/backup-crds.sh`/`restore-crds.sh`), and what it doesn't cover (VM disk content, FluxVM host state) |
| [`docs/runbook-velero-backup.md`](docs/runbook-velero-backup.md) | Using generic Velero (no Kairon-specific plugin) instead — what works out of the box, and the one real gap (no disk-content snapshot without a real CSI storage backend) |
| [`docs/runbook-vm-export.md`](docs/runbook-vm-export.md) | Getting a Machine's disk content out of the cluster entirely, with standard Kubernetes primitives (no new Kairon-specific export tooling) |
| [`docs/design-cluster-api-provider.md`](docs/design-cluster-api-provider.md) | Scoped, not built: what a real Cluster API infrastructure provider would take, and the real `ownerReferences` gap that blocks it today |
| [`ROADMAP.md`](ROADMAP.md) · [`RELEASE_NOTES.md`](RELEASE_NOTES.md) | What shipped per version, what's next; per-release changelog |
| [`SECURITY.md`](SECURITY.md) | Threat model, vulnerability reporting |
| [`CONTRIBUTING.md`](CONTRIBUTING.md) | PR checklist, coverage floor, frontend checks |

---

## Status

**v0.4.0** is tagged and open source; the sections above also describe real, tested work merged since then (admission webhooks, dashboard password management, console TLS automation, Helm/CI hardening, `MachineSet`/instance types/Windows/NUMA parity, pause/resume/halt, VM-state snapshot/restore, and a full second wave of FluxVM route wrapping -- both guest-exec channels, guest file access, sandboxes/templates/warm pools/image catalog, and runtime/network diagnostics -- see [`RELEASE_NOTES.md`](RELEASE_NOTES.md)'s Unreleased section) ahead of the next tag. Cold relocation, snapshots, DRA bridging, the secure live control plane, and a real FluxVM migration adapter are all real and tested — real two-host live migration has not yet been exercised against real hardware in this repository's own CI (see [Operability](#operability)).

### Production gaps

Still genuinely open, and why:

- **Fencing detection and migration preflight are single-signal, not exhaustive** — fencing only looks at the Kubernetes Node's own `Ready` condition (no independent liveness probe of kairon-node itself), and preflight can only catch a *confirmed* storage/network mismatch when both nodes are labeled with `kairon.zyvor.dev/storage-domain`/`network-domain` — it can't prove compatibility when the labels are unset. Real multi-host fencing/preflight behavior hasn't been exercised against real hardware in this repo's own CI.
- **`topologySpreadConstraints.maxSkew` hard enforcement is opt-in per constraint** (`whenUnsatisfiable: DoNotSchedule`) — omitted or `ScheduleAnyway` (the default) stays scoring-only, as before. A `DoNotSchedule` domain is only counted among currently-eligible candidate nodes, not every node cluster-wide, and — like affinity/anti-affinity — is evaluated against the reconcile-time Machine list, not a live watch, so a same-tick scheduling race can transiently see stale domain counts (resolved on the next tick). See [`docs/guides/machine-placement.md`](docs/guides/machine-placement.md).
- **DRA topology-awareness is a best-effort scoring hint, not an allocation decision** — `kairon-controller` has no role in DRA device allocation itself; see [`docs/guides/machine-placement.md`](docs/guides/machine-placement.md).
- **Confidential-compute enforcement (SEV-SNP/TDX), large-scale hardware qualification** — hardware-dependent, not exercisable in CI.
- **PVC-backed boot disks are a first cut**: one boot volume per Machine, `Filesystem`-mode `PersistentVolume`s only. `hostPath`/`local` sources resolve directly; a network-block volume now works too, through Kairon's own first-cut CSI driver (`csiNode.enabled`, iSCSI only) — dynamic provisioning, CHAP, volume expansion, and volume snapshots are all supported (`csiController.enabled`/`.snapshotter.enabled`), but there's still no CHAP on Kairon's own node-side consumption path and no other backends (Ceph/EBS/etc.) — see [`docs/guides/machine-storage-csi.md`](docs/guides/machine-storage-csi.md). A PV naming any other CSI driver is still refused outright.
- **`MachineSnapshotRestore` restores into a new PVC only**, deliberately not also a Machine (see its guide for why), and needs a real CSI snapshotter behind your StorageClass — Rancher's `local-path-provisioner`, a common default, doesn't have one.
- **CPU/memory hotplug is grow-only** (FluxVM has no CPU/DIMM unplug), bounded by headroom reserved at creation (`spec.resources.maxCpu`/`.maxMemory`, itself fixed once set — not something a running Machine can grow), and lost across any stop/start — expected QEMU behavior, not a bug.
- **`kairon-ui` multi-replica state propagation is eventually-consistent, not instant**: `ui.replicaCount > 1` is now supported (session revocation, login lockout, console tickets, and password changes propagate via a shared `ConfigMap`, deliberately not Redis), but cross-replica visibility lands within ~15s, login-lockout's failure count is per-replica not cluster-wide-atomic, and concurrent password changes to two different accounts on two different replicas can still race. See [`docs/guides/kairon-ui-ha.md`](docs/guides/kairon-ui-ha.md).
- **`kairon-controller` leader-election takeover is coarse, not instant**: `controller.replicaCount > 1` is now safe (a `coordination.k8s.io/v1` Lease, on by default, ensures only one replica ever reconciles), but a crashed or partitioned leader costs up to ~15s before another replica takes over and reconciliation resumes. See [`docs/guides/kairon-controller-ha.md`](docs/guides/kairon-controller-ha.md).
- **No distributed tracing, and `kairon-ui`'s own request metrics have no per-route breakdown** (method/status-class only, to keep label cardinality bounded against dynamic `{namespace}/{name}` paths). Every component now exposes reconcile-loop and apiserver-call health metrics (previously `kairon-controller`-only and migration-only), but a reconcile error is only attributed to "this tick failed," not which Machine/migration inside it. See [`docs/guides/observability.md`](docs/guides/observability.md).
- **Rate limiting covers `kairon-ui` only, keyed by remote address alone.** `kairon-ui` now rate-limits every route per client address (`-rate-limit-rps`/`-rate-limit-burst`, on by default), on top of the existing per-username login lockout — but behind a `Service`/`Ingress`/load balancer with no `X-Forwarded-For` trust configured, every client can collapse into one address. The admission webhook and inter-component RPCs (migration control-plane mTLS, the console relay) deliberately have no rate limiting added here: the webhook backstops real Kubernetes writes under `failurePolicy: Fail`, so throttling it risks rejecting a legitimate bulk `kubectl apply`; the RPCs are already restricted to mTLS/bearer-token-authenticated peers, not open to arbitrary traffic. See [SECURITY.md](SECURITY.md).
- **Live-migration data-plane identity is opt-in, and still two places to configure at once.** `migration.dataplaneTlsSecretName` (opt-in) gives every node a real, distinguishable cert for the QEMU RAM/state stream instead of the shared control-plane cert every node could otherwise present there — `scripts/gen-migration-mtls-certs.sh` generates both the certs and a ready-to-apply Secret. Left unset (the default), the data plane still falls back to the shared cert, same as before this existed. Either way, actually turning data-plane encryption on also requires each node's `kairon-migration-adapter-fluxvm` systemd unit to pass `-migration-data-tls=true` independently — outside Helm's control, easy to half-upgrade a fleet. The control-plane cert (`migration.tlsSecretName`) stays deliberately shared regardless — it proves cluster membership, not host identity, by design. See [`docs/guides/machine-migration-tls.md`](docs/guides/machine-migration-tls.md).
- **Backup/restore covers Kairon's Kubernetes-level state only** (`scripts/backup-crds.sh`/`restore-crds.sh`, see [`docs/runbook-backup-restore.md`](docs/runbook-backup-restore.md)) — the eight `kairon.zyvor.dev` CRDs and, opt-in, the chart-managed Secrets. It does not back up VM disk content (your CSI driver's/storage backend's own job) or FluxVM's own per-host runtime state; whether a restore gets you working VMs back, not just Kubernetes objects, depends on whether those survived independently. Not yet drilled against a real full cluster-loss scenario in this repo's own CI.
- **OIDC/SSO has no group-to-admin mapping**: an OIDC-authenticated session is a normal, non-admin operator identity — never eligible to reset another account's password — regardless of what groups/roles the identity provider reports. Admin capability stays exclusively a `ui.auth.users[].admin: true` property. See [`docs/guides/kairon-ui-oidc.md`](docs/guides/kairon-ui-oidc.md).
- **No CRD actually runs more than `v1alpha1` yet, but the conversion webhook scaffold now exists**: `kairon-controller`'s webhook server exposes a tested `POST /convert/machinequotas` (`internal/conversion`), proven end to end against a real worked `MachineQuota` field-rename example — on the same TLS listener/certificate/`Service` as the existing admission webhook, no new trust boundary. Cutting a real `v1beta1` still needs a CRD manifest change and a real converter for whatever's actually changing, plus solving one still-open gap: `charts/kairon/crds/*.yaml` lives in Helm's special, never-templated `crds/` directory, so wiring a live `caBundle` into a CRD's `spec.conversion` needs a deliberate choice (move that CRD into `templates/`, or a separate patch step) not yet made. See [`docs/guides/crd-versioning.md`](docs/guides/crd-versioning.md).
- **The admission webhook's `MachineDisruptionBudget` and Machine-`CREATE`-`MachineQuota` checks only ever evaluate `CREATE`**, matching the reconcile-loop/`kaironctl` checks they backstop exactly. `MachineQuota` also now evaluates `UPDATE` (a hotplug resize past quota) — but only when `webhook.enabled` is true; with it off (the default), a resize past quota still isn't caught anywhere, since `kairon-node`'s hotplug reconciliation has no cluster-wide quota visibility of its own.
- **The VNC console inherits FluxVM's own unauthenticated VNC socket as-is** — Kairon can't add auth/encryption FluxVM itself doesn't have. Its real security rests on kairon-ui's operator auth, a single-use per-session ticket now bound to the one Machine it was issued for, an opt-in per-Machine allowlist (`kairon.zyvor.dev/console-allowed-users`, unset means unchanged all-operator access), and the shared cluster-wide token to kairon-node (optionally now over TLS). See [SECURITY.md](SECURITY.md).
- **Every feature added since VM-state snapshot/restore (Halted, both guest-exec channels, sandboxes/templates/HTTP-proxy/warm pools, the image catalog, egress check, runtime and network diagnostics) is API-only** — no dashboard button yet, matching how earlier features like `qga/fsfreeze-status`/firewall shipped kubectl/API-only first before getting a UI. `kaironctl` also has no dedicated verbs for most of these.
- **A warm-pool claim is real FluxVM state, not automatically a Kairon-managed `Machine`** — claiming hands back FluxVM's own VM record directly; there is no automatic step that creates a corresponding `Machine` object, so a claimed VM sits outside admission-time guards (`MachineQuota`, the webhook) until an operator explicitly creates one themselves. See [`docs/guides/machine-sandboxes.md`](docs/guides/machine-sandboxes.md)'s "Warm pools" section.
- **Real CPU pinning (`spec.resources.cpuPinning`) depends entirely on an operator keeping the `kairon.zyvor.dev/pinnable-cpus` node label accurate.** Kairon deliberately doesn't read kubelet's own internal CPU Manager state file to auto-discover which cores are safe (a known, fragile, version-dependent, unsupported community pattern) — if the label includes a core kubelet's static CPU Manager later exclusively grants to a real Guaranteed-QoS Pod, nothing here detects or prevents that collision. See [`docs/guides/machine-cpu-pinning.md`](docs/guides/machine-cpu-pinning.md).
- **The third-party CSI client (`node.thirdPartyCSIDrivers`) is a first cut, not a general "any CSI driver" integration.** Only `attachRequired: false` drivers that don't require secret-based node auth work at all (Ceph-CSI/RBD is the only conceptually-validated reference case — no live third-party driver was available to test end to end in this repo's own environment); cloud block-storage drivers requiring a controller-side attach step (EBS-CSI, PD-CSI, Azure Disk CSI) don't work here. See [`docs/guides/machine-storage-thirdparty-csi.md`](docs/guides/machine-storage-thirdparty-csi.md).

Report vulnerabilities privately to **security@zyvor.dev** — see [`SECURITY.md`](SECURITY.md).

---

## License

### Open source (Apache-2.0)

This repository is licensed under the [Apache License, Version 2.0](LICENSE). You may use, modify, and run it for personal, lab, and commercial production use at no charge, subject to Apache-2.0 (preserve notices / NOTICE where required). See [NOTICE](NOTICE).

### Enterprise

Production support, SLAs, and Zyvor Enterprise products are licensed separately. Contact [sales@zyvor.dev](mailto:sales@zyvor.dev) or see [zyvor.dev](https://zyvor.dev).
