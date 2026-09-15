# Architecture

Kairon separates Kubernetes orchestration from VM execution. Kubernetes is the source of truth; FluxVM owns normal VMM lifecycle. Live-transfer mechanics are behind a backend-neutral migration adapter rather than assumed FluxVM/QMP endpoints.

## Components

- `kairon-controller`: Machine placement, `MachineMigration` state and `MachineSnapshot` -> CSI `VolumeSnapshot` orchestration. Exposes Prometheus metrics and a migration concurrency quota (see Operational visibility below).
- `kairon-node`: one per VM node; reconciles assigned Machines into FluxVM, resolves DRA/VFIO, exposes the mTLS migration peer API, and optionally (`console.enabled`) a shared-token-gated relay (`internal/consoleproxy`) kairon-ui dials for everything that needs a network path only this node has -- VNC/text console, guest exec (both channels), guest file access, logs, VM-state snapshot/restore, sandboxes/templates/HTTP-proxy, the image catalog, warm pools, and the runtime/network diagnostics -- see "Day-2 operations on a running Machine" below for the full list.
- migration peer: TLS 1.3, mandatory client certificate, target prepare/commit/abort and a local atomic session journal.
- migration adapter: optional HTTP-over-Unix-socket component that implements VMM-specific target/source migration operations.
- `kaironctl`: thin Kubernetes API client; it never bypasses the controllers.
- `kairon-ui` (optional): a web dashboard (`internal/uiapi` Go backend + `web/` React SPA) that is itself just another Kubernetes API client, same standing as `kaironctl` -- no privileged side channel, no second source of truth.

## Cold migration

```text
Pending -> Stopping -> Restarting -> Succeeded
             |             |
      verify source    assign target
         stopped       and restart
```

This state is represented in Kubernetes and is restart-safe. Kairon does not copy host-local disk content.

## Live migration

```text
Pending
  -> Starting
     source peer authenticates target over mTLS
     target adapter prepares destination
     target journals Prepared session
  -> Running
     source adapter transfers VM state to opaque endpoint
  -> Cutover
     transfer completed AND target commit succeeded
  -> Adopting
     Machine.nodeName=target
     kairon.zyvor.dev/adopt-only=true
  -> Succeeded
     target discovers incoming runtime and reports Running
```

Failure rules:

- Target unsupported: `Blocked`; source is untouched.
- Source start/transfer failure: target is aborted; no cutover.
- Source transfer succeeds but target commit fails: `NeedsRecovery`; no automatic cutover or restart.
- Target runtime missing after commit: adopt-only guard blocks duplicate creation.

No raw destination URI is accepted from users. A peer endpoint is derived from the selected target node `InternalIP`; TLS validates the configured migration server identity.

## Session durability

Destination sessions are persisted as mode `0600` JSON files using write -> fsync -> atomic rename. The default Helm `emptyDir` survives process/container restart within the Pod. For Pod replacement/node-level durability, configure `migration.stateHostPath` to a pre-created directory writable by the non-root Kairon UID.

## CSI snapshots

`Machine.spec.volumes[]` stores PVC references. `MachineSnapshot` creates one standard `snapshot.storage.k8s.io/v1` `VolumeSnapshot` per PVC and mirrors readiness into Kairon status.

## DRA / VFIO

`Machine.spec.deviceClaims[]` references same-namespace `resource.k8s.io/v1` `ResourceClaim` objects. The node agent requires an allocation, resolves a PCI BDF, normalizes it and checks a node-local allowlist. Namespace users cannot bypass the host PCI authorization boundary by editing annotations.

This path is deliberately device-type-agnostic -- `resolveVFIODevices` only
ever cares that a claim resolves to an allowlisted BDF, never what kind of
PCI device it is, and `spec.deviceClaims` is a list, resolved and passed
to FluxVM's own `vfio_devices` as a set. That means an SR-IOV NIC Virtual
Function, once created and bound to `vfio-pci` on the host (the same
operator/host-setup responsibility GPU passthrough already has -- Kairon
manages neither GPU nor VF driver binding itself), passes through via this
exact same mechanism with no new allocation code -- a Machine can combine
a GPU claim and a NIC-VF claim in one `spec.deviceClaims` list today. See
[guides/machine-sriov.md](guides/machine-sriov.md). Multus (the standard
Kubernetes SR-IOV integration path) doesn't apply here at all -- its whole
mechanism wires extra interfaces into a Pod's network namespace, and a
Machine has no Pod for it to attach to.

## Day-2 operations on a running Machine

`spec.cloudInit` (SSH keys, hostname, packages, first-boot commands) and
`spec.network.forwards` (inbound SSH/etc. via SLIRP hostfwd) are both read
only once, inside the FluxVM create call in `internal/agent.reconcileMachine`
-- editing either field, or `spec.resources`, on an already-running Machine
is a silent no-op: no error, no status signal, no drift correction. Apply
the change at creation, or delete and recreate. See
[guides/machine-network.md](guides/machine-network.md) for `cloudInit`/
`forwards` usage.

A graphical VNC console is available (`console.enabled`, off by default):
`kairon-ui` relays a browser WebSocket through `kairon-node` to the VM's
local, otherwise-unreachable QEMU VNC socket (`<workspace>/vnc.sock`,
QEMU-backend only) -- see SECURITY.md's "VNC console" section for the
trust model before enabling it. Guest-exec (`POST /v1/vms/{id}/qga/exec`,
FluxVM's real qemu-guest-agent) is now wrapped the same way -- one
synchronous request/response relay, not a long-lived connection --
requiring `spec.guestAgent.enabled` and an admin account; see SECURITY.md's
"Guest exec" section. FluxVM's own vsock-based interactive text console
(`GET /v1/vms/{id}/console`, `virtctl console`'s equivalent) is also
wrapped now, as `spec.guestAgent.console`: `kairon-ui` (xterm.js) relays a
browser WebSocket through `kairon-node`'s own client-dialed WebSocket
connection to FluxVM's console endpoint -- unlike VNC (a Unix socket
kairon-node dials directly), FluxVM itself is the upstream WebSocket
server here. This genuinely needed FluxVM's own proprietary in-guest agent
binary (`fluxvm-guest-agent`, a separate systemd service from
qemu-guest-agent) baked into the guest image -- a real, new dependency
this project hadn't asked of users before -- see
[guides/machine-text-console.md](guides/machine-text-console.md) and
SECURITY.md's "Text console" section. Live cgroup resize
(`POST /v1/vms/{id}/resources`) is now wrapped too, as `spec.resources.limits`
(`internal/agent/resourcelimits.go`) -- a real, kernel-enforced host-side cap
distinct from the QMP hotplug Kairon already wraps (`internal/agent/hotplug.go`,
which grows what the *guest* sees); see
[guides/machine-resource-limits.md](guides/machine-resource-limits.md).

A full hypervisor-level VM-state checkpoint/restore
(`POST /v1/vms/{id}/snapshot`/`start-from-snapshot`/`stop`) is now wrapped
too, admin-only and API-only (`internal/uiapi/vmsnapshot.go` ->
`internal/consoleproxy` -> `internal/fluxvm/hibernate.go`) -- RAM, CPU, and
device state via QEMU's real `savevm` or a Cloud Hypervisor snapshot,
restored back into the *same* Machine, unrelated to `MachineSnapshot`'s
disk-content-only CSI snapshot. Restoring always stops the Machine first,
since FluxVM's own `start-from-snapshot` silently ignores the requested tag
on an already-running VM; `internal/agent`'s reconcile loop self-heals a
Machine left FluxVM-stopped mid-restore back to its last-known-good disk
state, not a second automatic restore attempt. See
[guides/machine-vm-state-snapshot.md](guides/machine-vm-state-snapshot.md)
and SECURITY.md's "VM-state snapshot/restore" section.

A `spec.powerState: Halted` value now sits alongside `Running`/`Stopped`/
`Paused` -- FluxVM's own `stop`/`start` (`internal/fluxvm/hibernate.go`),
powering the VMM process off while FluxVM keeps its own record/disk
intact, so resume reuses FluxVM's own last-applied config instead of a
full Kairon-side recreate the way `Stopped` -> `Running` does. Resume
reuses the exact reconcile branch that already handled `RestoreSnapshot`'s
own crash-recovery self-heal, since both leave FluxVM reporting the
runtime Stopped-with-record-kept; `status.phase` reports `Halted`,
deliberately distinct from `Stopped`, and both scheduler capacity
accounting and `MachineQuota` treat it the same way they already treat
`Stopped`. See [guides/machine-halt.md](guides/machine-halt.md).

A second, backend-agnostic guest-exec channel is now wrapped too:
`POST /v1/vms/{id}/agent` (`internal/fluxvm.Client.AgentExec`) rides the
same bespoke vsock guest agent guest file access already uses
(`spec.guestAgent.console`), a genuinely different mechanism from the
QEMU-only `qemu-guest-agent` exec above -- the only guest-exec path that
works on Cloud Hypervisor, Firecracker, or a FluxVm-backend sandbox. See
[guides/machine-guest-agent-files.md](guides/machine-guest-agent-files.md).

FluxVM's own agent-sandbox track -- a lightweight, fast-boot in-tree
hypervisor (`BackendKind::FluxVm`) for short-lived ephemeral workloads --
is wrapped via `spec.sandbox`: `kairon-node` calls FluxVM's real
`POST /v1/sandboxes` instead of `POST /v1/vms` when set
(`internal/fluxvm.Client.CreateSandboxForMachine`), optionally booting
from a pre-built template (`spec.sandbox.templateName`, a node-scoped
admin API wrapping FluxVM's own `GET`/`POST /v1/templates`) instead of
`spec.image`. An HTTP proxy relay
(`ANY /api/v1/machines/{ns}/{name}/sandbox-http/{port}/{path}`) reaches a
service running inside the sandbox directly. See
[guides/machine-sandboxes.md](guides/machine-sandboxes.md) and
SECURITY.md's "Sandboxes" section.

FluxVM's own node-local image catalog (`spec.image.catalogName`) and
warm-VM pools (node-scoped admin API under `/api/v1/nodes/{node}/pools`)
are wrapped the same way as sandbox templates -- both are per-node FluxVM
state, not a new Kairon CRD with its own reconcile loop; warm pools in
particular were deliberately kept API-only rather than a competing
controller, since FluxVM already ships its own `MicroVMPool` CRD/node-local
operator reconciling the identical `/v1/pools` state. See
[guides/machine-image-catalog.md](guides/machine-image-catalog.md) and
[guides/machine-sandboxes.md](guides/machine-sandboxes.md)'s "Warm pools"
section.

Four runtime diagnostics round out the FluxVM route audit this session
worked through: a node's real capability manifest
(`GET /api/v1/nodes/{node}/capabilities`), a Machine's cgroup-derived PSI
pressure stats and effective host CPU set, and a cgroup-v2-freezer-backed
freeze/thaw distinct from `spec.powerState: Paused` (a host-kernel
operation that works even when a VM's own control channel is
unresponsive). Alongside these, four network-observability endpoints
(effective policy, stats, flows, drop-reasons) wrap FluxVM's own
eBPF-dataplane diagnostics -- `network-drop-reasons` is the most direct
answer to "why is my `MachineNetworkPolicy` blocking traffic I expected to
allow." See [guides/machine-diagnostics.md](guides/machine-diagnostics.md)
and [guides/network-policy.md](guides/network-policy.md)'s
"Troubleshooting" section.

## Operational visibility

`kairon-controller` computes Prometheus metrics (`internal/metrics`) from the same migration list it already fetches every reconcile tick -- one call site, not scattered instrumentation -- and serves them on its existing health port. `charts/kairon/alerts.yaml` ships example alert rules for a stuck `NeedsRecovery`, a high migration failure rate, a migration stuck in flight, and an unencrypted data-plane; each links to [`runbook-migration-failures.md`](runbook-migration-failures.md).

A migration concurrency quota (`migration.maxConcurrentPerNode`/`maxConcurrentCluster`, both `0` = unlimited) is enforced in the same admission path as target-eligibility checks, rejecting into the existing `Blocked` phase -- not a new state, not a new mechanism.

Every reconcile-loop failure is logged (`internal/controller`, `internal/agent`), including the *secondary* case of the follow-up status-patch itself failing after a primary reconcile error -- both are surfaced, not just the first. All three workloads set CPU/memory `resources:` requests and limits by default, and their ClusterRoles grant exactly the verbs their code paths use (no unused `update` alongside `patch`, confirmed against `internal/kube.Client`'s actual HTTP methods).
