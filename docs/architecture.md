# Architecture

Kairon separates Kubernetes orchestration from VM execution. Kubernetes is the source of truth; FluxVM owns normal VMM lifecycle. Live-transfer mechanics are behind a backend-neutral migration adapter rather than assumed FluxVM/QMP endpoints.

## Components

- `kairon-controller`: Machine placement, `MachineMigration` state and `MachineSnapshot` -> CSI `VolumeSnapshot` orchestration. Exposes Prometheus metrics and a migration concurrency quota (see Operational visibility below).
- `kairon-node`: one per VM node; reconciles assigned Machines into FluxVM, resolves DRA/VFIO, exposes the mTLS migration peer API, and optionally (`console.enabled`) a shared-token-gated VNC relay to each VM's local QEMU socket.
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
trust model before enabling it. Text console (FluxVM's own vsock-based
`GET /v1/vms/{id}/console` shell) and guest-exec (`/qga/*`) are a
different transport entirely and remain unwrapped, as does live resize
(`POST /v1/vms/{id}/resources`) -- tracked as future work.

## Operational visibility

`kairon-controller` computes Prometheus metrics (`internal/metrics`) from the same migration list it already fetches every reconcile tick -- one call site, not scattered instrumentation -- and serves them on its existing health port. `charts/kairon/alerts.yaml` ships example alert rules for a stuck `NeedsRecovery`, a high migration failure rate, a migration stuck in flight, and an unencrypted data-plane; each links to [`runbook-migration-failures.md`](runbook-migration-failures.md).

A migration concurrency quota (`migration.maxConcurrentPerNode`/`maxConcurrentCluster`, both `0` = unlimited) is enforced in the same admission path as target-eligibility checks, rejecting into the existing `Blocked` phase -- not a new state, not a new mechanism.

Every reconcile-loop failure is logged (`internal/controller`, `internal/agent`), including the *secondary* case of the follow-up status-patch itself failing after a primary reconcile error -- both are surfaced, not just the first. All three workloads set CPU/memory `resources:` requests and limits by default, and their ClusterRoles grant exactly the verbs their code paths use (no unused `update` alongside `patch`, confirmed against `internal/kube.Client`'s actual HTTP methods).
