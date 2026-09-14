# Architecture

This is the ten-minute tour: what each piece owns, how a `Machine` actually turns into a running VM, and where the trust boundaries are. For exact state-machine transitions, session-durability internals, and code-identifier-level detail, see [`docs/architecture.md`](docs/architecture.md) — that doc is the reference; this one is the map.

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

Kubernetes is the only source of truth — there is no second database anywhere in this system, not even behind the dashboard. FluxVM owns actual VMM execution; Kairon owns placement, relocation policy, and Kubernetes lifecycle semantics. The one connection this diagram can't easily draw: when `webhook.enabled`, the API server calls back out to `kairon-controller`'s admission webhook *synchronously, during* every `Machine`/`MachineMigration` write — the arrow from the API box down to `kairon-controller` in a real deployment runs both ways, not just top-to-bottom.

## What each component owns

- **`kairon-controller`** — the one cluster-wide reconciler. Owns placement (least-loaded, deterministic tie-break, required affinity/anti-affinity), the `MachineMigration` state machine (cold and live), `MachineSnapshot` → CSI `VolumeSnapshot` orchestration, `MachineQuota` accounting, Prometheus metrics, and — opt-in — the `MachineQuota`/`MachineDisruptionBudget` admission webhook. Does not talk to FluxVM directly, and does not execute anything on a node.
- **`kairon-node`** — one per capable node. Owns turning an assigned `Machine` into a real FluxVM instance, resolving DRA `ResourceClaim`s to VFIO PCI BDFs against a node-local allowlist, serving the mTLS live-migration peer API, and — opt-in — the VNC console relay and its own TLS. Does not decide placement; it only executes what `kairon-controller` already assigned to it. When a Machine's boot disk is a CSI-backed `PersistentVolume`, it also acts as its own CSI client against `kairon-csi-node`'s local Unix socket, directly — see `kairon-csi-node` below.
- **`kairon-csi-node`** (optional) — one per capable node, deployed alongside `kairon-node`. Kairon's own first-cut CSI node plugin (`csi.kairon.zyvor.dev`, iSCSI only, no Controller service): logs in to an iSCSI target, formats it if needed, and mounts it, all driven directly by `kairon-node` rather than through kubelet's Pod volume machinery (a `Machine` has no backing Pod for kubelet to trigger that against). The upstream `csi-node-driver-registrar` sidecar it ships with still registers it with kubelet, so a real Kubernetes Pod can also use it normally. Runs privileged, and is the one Kairon-built image not based on the minimal distroless base every other component uses — see SECURITY.md.
- **`kaironctl`** — a thin Kubernetes API client, nothing more. Every subcommand is a `GET`/`POST`/`PATCH` against the same CRDs anyone with `kubectl` could issue; it never bypasses `kairon-controller` or talks to a node directly.
- **`kairon-ui`** (optional) — a Go backend (`internal/uiapi`) plus a small React SPA, with exactly the same standing as `kaironctl`: another Kubernetes API client, no privileged side channel, no second source of truth. Its one genuinely separate piece of state is its own operator account list (`ui.auth.users`, a Kubernetes Secret) and session/lockout/console-ticket bookkeeping; `ui.replicaCount > 1` propagates that state across replicas via a shared Kubernetes `ConfigMap` rather than a new stateful dependency like Redis — eventually-consistent, not instant, see [`docs/guides/kairon-ui-ha.md`](docs/guides/kairon-ui-ha.md). Optional OIDC/SSO (`ui.oidc.enabled`) authenticates against an external identity provider instead of (or alongside) `ui.auth.users`, issuing the exact same kind of session token either way — see [`docs/guides/kairon-ui-oidc.md`](docs/guides/kairon-ui-oidc.md).
- **The migration adapter** — an optional, VMM-specific component (`cmd/kairon-migration-adapter-fluxvm` is the real implementation) that `kairon-node` talks to over a local Unix socket to actually move VM memory/state during a live migration. Cold migration needs none of this; a live migration request blocks before the source is even touched if no adapter is configured.
- **FluxVM** — not part of Kairon at all. It's the actual hypervisor-facing daemon (QEMU, Cloud Hypervisor, Firecracker) that every `kairon-node` calls over its local REST API. Kairon never re-implements VMM control; it only orchestrates around it.

## From `kubectl apply` to a running VM

1. A `Machine` object lands in the Kubernetes API (via `kubectl`, `kaironctl create`, `kairon-ui`, or GitOps — all identical from here on).
2. If `webhook.enabled`, the API server calls `kairon-controller`'s admission webhook first: a `Machine` create that would immediately exceed a `MachineQuota` is rejected outright, before it's ever written — and so is an update that hotplug-resizes an already-scheduled Machine's `spec.resources` past quota, the one check here with no reconcile-loop equivalent backstopping it (`kairon-node`'s hotplug has no cluster-wide quota visibility of its own).
3. `kairon-controller`'s reconcile loop (on a fixed interval, not a watch — see the Go-stdlib-only note below) lists unscheduled Machines, filters nodes by required affinity/anti-affinity/architecture/selector, then scores the survivors (load balance, `preferredAffinity`/`preferredAntiAffinity`, `topologySpreadConstraints`, a best-effort DRA topology hint) and patches `spec.nodeName` to the winner — see [`docs/guides/machine-placement.md`](docs/guides/machine-placement.md).
4. The `kairon-node` running on that node notices the assignment on its own next tick and reconciles it: resolves the boot disk (`spec.volumes[0]`'s PVC, or `spec.image.path`), resolves any DRA device claims to VFIO BDFs, and calls FluxVM's local REST API to actually create and start the VM.
5. `kairon-node` patches `status.phase` back onto the `Machine` as it progresses — `kaironctl get machines` / the dashboard's Machines table are both just reading that same status field, not a separate poll of FluxVM.

Relocating an existing Machine (`kaironctl migrate`) and disrupting several at once (`kaironctl evacuate`, budget-checked against any matching `MachineDisruptionBudget`) both follow the same "write intent to the API, let the right component notice and act" shape — see [`docs/architecture.md`](docs/architecture.md)'s Cold migration/Live migration sections for the exact state diagrams.

## Trust boundaries and deployment topology

Two deployment shapes exist, and they change what "the network" means for the diagram above:

- **In-cluster (Helm)** — `kairon-controller`, `kairon-node` (as a DaemonSet), and optionally `kairon-ui` all run as Pods in one Kubernetes cluster, talking to the in-cluster API server over the normal service-account token path. This is the default, and what `charts/kairon/` assumes.
- **Bare-metal (`scripts/deploy-remote.sh`)** — the same binaries run as systemd services on a host outside Kubernetes entirely, pointed at a cluster via `KAIRON_KUBE_URL`. Useful for a node FluxVM already runs on that isn't itself a good place to run a full kubelet, or for trying Kairon out without standing up a cluster at all.

Three optional TLS hops exist, each independently opt-in, each following the same "you supply the certificate, this project doesn't mint one for you" convention (`migration.tlsSecretName`, `console.tls.secretName`, `webhook.tlsSecretName`/`webhook.caBundle`):

| Hop | Default | What it protects |
|---|---|---|
| `kairon-node` ↔ `kairon-node` (live migration peer, `:9443`) | mTLS **required** whenever `migration.enabled` | The actual VM memory/state transfer between two nodes |
| `kairon-ui` → `kairon-node` (VNC console relay) | plaintext HTTP, opt-in TLS (`console.tls.enabled`) | A shared bearer token plus, optionally, one-way TLS on the relay hop |
| Kubernetes API server → `kairon-controller` (admission webhook) | webhook itself is off by default; `failurePolicy: Fail` once on | Whether `Machine`/`MachineMigration` writes can bypass quota/budget checks |

All three hop's leaf certificates hot-reload (`internal/tlsreload`, a stdlib-only polling watcher — no fsnotify): whatever renews the cert file on disk (cert-manager or anything else) takes effect within 30s, no restart. Only the leaf cert/key reload this way; a CA bundle used to build `ClientCAs`/`RootCAs` still loads once at startup.

## Why it's built this way

Three constraints shape almost every design choice above — see the top-level [README](README.md#why-kairon-exists) for the full argument, summarized here for context:

- **Go standard library only, with two deliberate exceptions.** `go.mod` has no `client-go`, no controller-runtime, no generated deepcopy. `kairon-controller` is a hand-rolled interval poll loop over a raw REST client (`internal/kube`), not a watch-based `Manager`/`Reconciler` — which is also why the admission webhook hand-rolls the small `AdmissionReview` JSON shape (`internal/admission`) instead of importing `k8s.io/api` for it. The tradeoff is explicit: no client-side caching or watch-based low-latency reconciliation, in exchange for a codebase small enough to actually read start to finish. `kairon-ui`'s opt-in OIDC/SSO (`ui.oidc.enabled`) is one place this guarantee is deliberately broken (`golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3`) — real JWT/JWK verification against an external identity provider isn't something to hand-roll. The other is Kairon's own opt-in CSI node plugin, `kairon-csi-node` (`csiNode.enabled`) — the CSI protocol is a gRPC/protobuf wire contract with no stdlib-only way to speak it at all (`google.golang.org/grpc`, `github.com/container-storage-interface/spec`); `kairon-node` also links `grpc` to dial it as a client (`internal/agent/csi.go`), exercised only when a Machine actually uses a CSI-backed volume. `kairon-controller`/`kaironctl` pull in none of this either way.
- **Kubernetes is the only source of truth.** Every component above — including the dashboard — is a client of the same API, never a second store. There is no Kairon-side database to get out of sync with reality.
- **An ambiguous outcome gets a name, not a guess.** `NeedsRecovery` exists because a live migration whose commit result is genuinely uncertain has exactly two wrong automatic answers (assume success, assume failure) and one right one: stop, and ask an operator to attest what actually happened. Node fencing follows the same shape: `kairon-controller` names a Machine's node `NodeUnreachable` on its own, but only an operator-run `kaironctl fence`, attesting the node is truly gone (not just unreachable), makes it eligible for rescheduling — automatic rescheduling here risks the same split-brain double-run `NeedsRecovery` prevents for migrations.

## Going deeper

- [`docs/architecture.md`](docs/architecture.md) — exact FSM diagrams for cold/live migration, session durability, CSI/DRA mechanics, operational visibility
- [`docs/migration-adapter.md`](docs/migration-adapter.md) — the migration adapter's HTTP contract and trust boundary
- [`docs/network-fabric.md`](docs/network-fabric.md) — `MachineNetworkPolicy`/`NetworkSecurityGroup` and the FluxVM eBPF edge
- [`docs/tutorials/machine-lifecycle.md`](docs/tutorials/machine-lifecycle.md) — a hands-on walkthrough of the create → relocate → snapshot → restore flow described above
- [`docs/guides/machine-fencing.md`](docs/guides/machine-fencing.md) — `NodeUnreachable` detection, `kaironctl fence`'s safety model, storage/network migration preflight labels
- [`docs/guides/kairon-ui-ha.md`](docs/guides/kairon-ui-ha.md) — running `ui.replicaCount > 1`, what's shared and how
- [`docs/guides/kairon-ui-oidc.md`](docs/guides/kairon-ui-oidc.md) — OIDC/SSO setup and the Go-stdlib-only exception it is
- [`docs/guides/machine-storage.md`](docs/guides/machine-storage.md) · [`docs/guides/machine-storage-csi.md`](docs/guides/machine-storage-csi.md) — PVC-backed boot disks, and Kairon's own first-cut iSCSI CSI driver
- [SECURITY.md](SECURITY.md) — the full threat model behind every TLS hop and trust boundary mentioned here
