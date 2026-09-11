<div align="center">

# Kairon

### Kubernetes-native virtual machines without KubeVirt or libvirt

**Kubernetes declares. Kairon orchestrates. FluxVM executes.**

[Apache License 2.0](LICENSE) · [zyvor.dev](https://zyvor.dev) · [Security](SECURITY.md) · [Contributing](CONTRIBUTING.md)

</div>

Kairon is an Apache-2.0 Kubernetes control plane for virtual machines executed by [Zyvor FluxVM](https://github.com/zyvorai/fluxvm). It does not create a `virt-launcher` Pod for every VM and it does not use libvirt. Kubernetes remains the desired-state API, `kairon-controller` handles placement/relocation/storage orchestration, and `kairon-node` drives the node-local FluxVM runtime.

## v0.3.0

v0.3 replaces the provisional raw-QEMU migration contract from v0.2 with a secure, backend-neutral migration protocol:

- Zero-touch **target preparation handshake** between Kairon node agents; users specify a target node, never `tcp:host:port`.
- TLS 1.3 peer transport with mandatory client certificates.
- Atomic, crash-safe destination session journal with idempotent prepare/commit/abort semantics.
- Target-first migration ordering: Kairon never touches the source runtime until the destination reports that it is prepared.
- Rollback on source-transfer failure: the prepared target is aborted and the source remains assigned.
- `NeedsRecovery` fencing when source transfer completes but target commit fails; Kairon refuses an automatic cutover that could create split brain.
- A small HTTP-over-Unix-socket **migration adapter API** so FluxVM/QEMU/other VMM-specific transfer support can be added without hard-coding guessed FluxVM endpoints into Kairon.
- v0.2 cold migration, adopt-only target cutover protection, CSI `VolumeSnapshot`, and DRA -> allowlisted VFIO continue to work.

The previously guessed FluxVM `/migration/start|status|cancel` calls have been removed. The current public FluxVM tree does not expose a verified live-migration/QMP API, so Kairon v0.3 does not pretend that it does.

## Architecture

```text
kubectl / GitOps / kaironctl
             |
             v
+--------------------------------------------------+
| Kubernetes API                                   |
| Machine | MachineMigration | MachineSnapshot     |
| ResourceClaim | VolumeSnapshot                   |
+-------------------------+------------------------+
                          |
              +-----------+-----------+
              |                       |
              v                       v
 +----------------------+   +----------------------+
 | kairon-controller    |   | kairon-node          |
 | placement            |   | FluxVM lifecycle     |
 | migration state      |   | DRA -> VFIO guard    |
 | CSI snapshots        |   | mTLS migration peer  |
 +----------------------+   +----------+-----------+
                                      |
                    +-----------------+------------------+
                    |                                    |
                    v                                    v
             node-local FluxVM                 optional migration
             VM lifecycle API                  adapter Unix socket
                    |                                    |
                    +----------------+-------------------+
                                     v
                                  KVM/VMM
```

## Install

Label every virtualization node explicitly:

```bash
kubectl label node worker-1 kairon.zyvor.dev/capable=true
kubectl label node worker-2 kairon.zyvor.dev/capable=true
```

Raw manifests install the normal VM lifecycle, cold migration, CSI snapshot and DRA/VFIO paths:

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

For CSI snapshots, install the CSI snapshot CRDs/controller and snapshot support for your CSI driver. For DRA/VFIO, configure only administrator-approved PCI BDFs through `KAIRON_VFIO_ALLOWLIST` or `node.vfioAllowlist`. An empty allowlist is fail-closed.

## VM lifecycle

```bash
kaironctl create demo \
  --image /var/lib/fluxvm/images/ubuntu.qcow2 \
  --cpu 2 --memory 2Gi --backend qemu

kaironctl get machines
kaironctl stop demo
kaironctl start demo
```

## Cold migration / evacuation

```bash
kaironctl migrate demo --strategy cold --target-node worker-2
kaironctl evacuate worker-1
```

Cold migration stops the source, verifies it is stopped, changes node ownership, restarts on the target, then verifies `Running`. It does not copy node-local image data; shared storage or identically provisioned images are required.

`strategy: auto` intentionally selects the proven cold path in v0.3. Use `--strategy live` only when the secure peer layer and a compatible migration adapter are configured.

## Secure live-migration handshake

A live request now contains no transport endpoint:

```bash
kaironctl migrate demo \
  --strategy live \
  --target-node worker-2 \
  --mode pre-copy \
  --bandwidth-mbps 800 \
  --max-downtime-ms 200 \
  --multifd-channels 4
```

The flow is:

```text
controller selects target
        |
        v
source kairon-node --mTLS--> target kairon-node:9443
        |                       |
        |                  prepare session
        |                  persist journal
        |                       |
        |<--- opaque endpoint --+
        |
        +--> local migration adapter starts source transfer
                    |
              transfer succeeds
                    |
        +--mTLS--> target commit
                    |
             guarded cutover
                    |
             adopt-only target
```

The opaque transfer endpoint never appears in `MachineMigration.spec` or status. Target preparation is idempotent: retrying the same session identity returns the original result, while reusing a session ID for a different VM returns a conflict.

### Enable peer mTLS

Create a secret with `ca.crt`, `tls.crt`, and `tls.key`. The chart uses the same node credential for both directions, so `tls.crt` must contain both `serverAuth` and `clientAuth` EKUs plus the DNS SAN configured by `migration.tlsServerName` (default `kairon-node`). The same CA must trust every node credential.

```bash
kubectl -n kairon-system create secret generic kairon-migration-tls \
  --from-file=ca.crt \
  --from-file=tls.crt \
  --from-file=tls.key

helm upgrade --install kairon ./charts/kairon \
  -n kairon-system \
  --set migration.enabled=true
```

The Helm pod uses `fsGroup: 65532` and mounts the TLS secret group-readable so the distroless non-root node process can read it. For stronger per-node identity, deploy per-node credentials with your secret/SPIFFE mechanism and set `--migration-server-name`/certificate SANs accordingly. v0.3's built-in chart uses one cluster migration credential as the simple baseline.

### Migration adapter boundary

Kairon deliberately keeps VMM-specific live transfer out of the Kubernetes API. The optional node-local adapter is a root-owned Unix-socket HTTP service configured with `--migration-adapter-socket`. Its contract is documented in [`docs/migration-adapter.md`](docs/migration-adapter.md).

**Current FluxVM boundary:** the FluxVM repository inspected for this release has no verified public live-migration/QMP API. With mTLS enabled but no adapter installed, an explicit live migration becomes `Blocked` during target preparation **before the source transfer starts**. Cold migration remains fully functional. This is intentional fail-closed behavior.

## Split-brain protection

Kairon has two independent guards:

1. If transfer fails before target commit, the target session is aborted and the source assignment is retained.
2. If transfer reports success but target commit fails, `MachineMigration.status.phase` becomes `NeedsRecovery`. Kairon does not automatically reassign, restart, or adopt a target until an operator resolves the ambiguous state.

After a successful target commit, the controller changes assignment with `kairon.zyvor.dev/adopt-only=true`. The target node must discover the incoming runtime by its stable Kairon name. If it is missing, the target is `Blocked`; it is never replaced by a newly created duplicate VM.

## CSI snapshots

Declare PVC-backed Machine volumes:

```yaml
spec:
  volumes:
    - name: data
      claimName: database-data
```

Then create a snapshot:

```bash
kaironctl snapshot database --name database-before-upgrade --class csi-snapclass
kubectl get machinesnapshots,volumesnapshots
```

Kairon creates standard `snapshot.storage.k8s.io/v1` `VolumeSnapshot` objects and mirrors readiness into `MachineSnapshot.status`.

## ResourceClaim / VFIO

A Machine may reference same-namespace Kubernetes DRA claims:

```yaml
spec:
  deviceClaims:
    - name: gpu-claim
```

The node agent requires a real `resource.k8s.io/v1` `ResourceClaim` allocation, resolves a PCI BDF, validates its syntax, and checks it against the node administrator's allowlist before passing it to FluxVM as `vfio_devices`. Missing allocation, invalid mapping and unauthorized BDFs all fail closed.

## CLI

```text
kaironctl get [machines|migrations|snapshots] [-n NAMESPACE]
kaironctl describe NAME [-n NAMESPACE]
kaironctl create NAME --image PATH [flags]
kaironctl start NAME [-n NAMESPACE]
kaironctl stop NAME [-n NAMESPACE]
kaironctl delete NAME [-n NAMESPACE]
kaironctl migrate MACHINE [--strategy auto|live|cold] [--target-node NODE]
kaironctl evacuate NODE [--strategy cold|auto]
kaironctl snapshot MACHINE [--name NAME] [--class CSI_CLASS]
kaironctl version
```

## Development

```bash
make all
make test-race
```

The repository uses only the Go standard library in the runtime code. `make all` checks formatting, `go vet`, unit/integration contract tests, static builds, manifest validation, and binary version smoke tests.

Supportability bundle:

```bash
./scripts/must-gather.sh
```

## Production gaps

Kairon v0.3 is pre-GA. A real hypervisor migration adapter still needs to be implemented in FluxVM (or as a separate privileged node component) before real memory-state live migration can run. Other gaps include storage/network migration preflight, automatic fencing integration, PVC-to-FluxVM disk attachment, DRA topology-aware placement, admission/quotas, certificate automation and rotation, confidential-compute enforcement, upgrade compatibility tests, and large-scale real-hardware qualification. See [`ROADMAP.md`](ROADMAP.md) and [`SECURITY.md`](SECURITY.md).

## License

Copyright 2026 Zyvor ([zyvor.dev](https://zyvor.dev)).

Licensed under the [Apache License, Version 2.0](LICENSE). See [NOTICE](NOTICE).
