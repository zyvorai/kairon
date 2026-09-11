# User guide: Machine networking

How to configure `Machine.spec.network` and related status fields. Kairon maps
these into FluxVM create/status APIs; it does not run Multus or own BPF
programs. Boundary details: [network-fabric.md](../network-fabric.md).

## Modes

| `mode` | When to use | Key fields |
|---|---|---|
| `user` (default) | Lab / NAT without a host bridge | `forwards[]` for host→guest ports |
| `tap` | Bridged or isolated edge (Fabric/eBPF path) | `netns`, `bridge`, `tapName`, `mac` |
| `macvtap` | Direct MAC on a parent NIC | `parent`, `macvtapMode`, `mac` |

### User mode forwards

```yaml
spec:
  network:
    mode: user
    forwards:
      - hostPort: 8080
        guestPort: 80
        protocol: tcp   # tcp|udp; default tcp
```

FluxVM receives `host_port` / `guest_port` / `protocol` on the create payload.

### TAP + netns (recommended for Network Fabric)

```yaml
spec:
  network:
    mode: tap
    netns: true
    staticNetwork: true   # cloud-init static address (tap+netns only)
    tapName: tap-web      # optional; FluxVM may allocate
    mac: "52:54:00:12:34:56"
    dataplaneRequired: true
```

- `netns: true` — per-VM network namespace (isolation + known guest address).
- `staticNetwork: true` — sets FluxVM `cloud_init.static_network` so the guest
  does not depend on DHCP.
- `dataplaneRequired: true` — node agent fail-closes if eBPF attach is unhealthy.

### Macvtap

```yaml
spec:
  network:
    mode: macvtap
    parent: eth0
    macvtapMode: bridge   # bridge|vepa|private|passthru
```

### Pod identity (Secure Containers)

```yaml
spec:
  network:
    mode: tap
    netns: true
    podUID: "8f3c2e1a-…"   # CRI sandbox UID → FluxVM pod_uid
```

## Service Fabric membership

Declare VIP backends after the guest IP is known. The named service must
already exist in FluxVM; Kairon merges this Machine as a backend.

```yaml
spec:
  serviceFabric:
    services:
      - name: web-vip
        port: 8080
        weight: 1
```

## Status

| Field | Meaning |
|---|---|
| `status.guestIP` | Guest address from FluxVM |
| `status.network.guestIP` / `tapName` | Same, nested for Fabric Dataplane tab |
| `status.network.dataplane.attached` | TC/eBPF hook attached |
| `status.network.dataplane.mode` | `legacy` / `ebpf` / `cilium` |
| `status.network.dataplane.identity` | Dataplane identity |
| `status.network.dataplane.schemaVersion` | BPF schema |
| `status.network.dataplane.policyFingerprint` | Committed policy fingerprint |
| `status.network.dataplane.policySynced` | Maps match durable policy |

## Node readiness

Set `KAIRON_DATAPLANE_REQUIRED=true` on `kairon-node` so the agent stays
NotReady until FluxVM `/readyz` succeeds (FluxVM itself fail-closes when
`sandbox.dataplane.required` is enabled).

## Live migration and network state

On live migrate, the source agent quiesces and exports network state; the
target restores after prepare and resumes after commit. Snapshots travel on
the mTLS peer session — not in CRD status. Requires a working migration
adapter for memory transfer; see [migration-adapter.md](../migration-adapter.md).

## Examples

- Minimal TAP: [`examples/linux-machine.yaml`](../../examples/linux-machine.yaml)
- Full fabric stack: [`examples/network-fabric-machine.yaml`](../../examples/network-fabric-machine.yaml)
- Hands-on: [tutorials/network-fabric.md](../tutorials/network-fabric.md)
