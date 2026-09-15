# Network Fabric (Kairon ↔ FluxVM ↔ Fabric)

Kairon declares Kubernetes desired state for VM-edge networking. **FluxVM** owns
TAP/netns, TC/eBPF attach, maps, CNP compile, and Service Fabric VIPs.
**Fabric** owns UX/SDN labels and proxies FluxVM dataplane APIs. Kairon does
**not** own BPF programs, Multus NADs (primary path), or PacketWolf.

```mermaid
flowchart TB
  subgraph consumers [Consumers]
    Fabric[zyvor_fabric]
    Kubectl[kubectl_GitOps]
  end
  subgraph kairon [Kairon_API]
    Machine[Machine]
    NetPolicy[MachineNetworkPolicy]
    Groups[NetworkSecurityGroup]
  end
  subgraph node [Node]
    Agent[kairon_node]
    FluxVM[FluxVM_REST]
    TC[TC_eBPF_maps]
  end
  Fabric --> Machine
  Fabric --> NetPolicy
  Kubectl --> Machine
  Machine --> Agent
  NetPolicy --> Agent
  Agent -->|"POST /v1/vms create network"| FluxVM
  Agent -->|"POST .../network/policy"| FluxVM
  Agent -->|"migration quiesce/export/restore"| FluxVM
  FluxVM --> TC
```

## Field → FluxVM → Fabric mapping

| Kairon | FluxVM | Fabric proxy |
|---|---|---|
| `Machine.spec.network.{mode,netns,bridge,parent,mac,tapName,macvtapMode,forwards,staticNetwork,podUID}` | `POST /v1/vms` `network` + `cloud_init.static_network` + `pod_uid` | VM create / edit |
| `Machine.spec.network.dataplaneRequired` | fail-closed on `GET …/network/status` when attach unhealthy | Dataplane health |
| `Machine.spec.serviceFabric.services[]` | merge backend into `POST /v1/network/services` | Service Fabric membership |
| `Machine.status.network.{guestIP,tapName,dataplane.*}` | `guest_ip` / `GET …/network/status` | Dataplane tab |
| `MachineNetworkPolicy` | `POST /v1/vms/{id}/network/policy` (+ optional `POST /v1/network/cnp`) | label→policy |
| `NetworkSecurityGroup` | `POST /v1/network/groups` | security groups |
| live migrate network | `…/network/migration/{quiesce,export,restore,resume}` | cross-node CT/policy continuity |
| observability (pass-through) | `…/network/{stats,flows,drop-reasons}` | `/api/dataplane/hubble/flows` |

## Agent behavior

1. **Create** — map rich `spec.network` into FluxVM create payload (Fabric create parity).
2. **Status** — project guest IP + dataplane attach fields Fabric already expects.
3. **Policy** — when Machine is Running on this node and selected by `machineName` or labels, upsert FluxVM policy; on CR delete, reset to `default_allow: true`.
4. **Groups** — upsert node-local FluxVM security groups from `NetworkSecurityGroup`. Deletion fails closed: the finalizer only clears once `DELETE /v1/network/groups/{name}` actually succeeds (idempotently tolerating "already gone"), so a real delete failure -- FluxVM unreachable, a transient error -- leaves the object (and the finalizer) in place for a retry on the next tick, rather than the Kubernetes object silently vanishing while its FluxVM-side security group state leaks behind, untracked.
5. **Migration** — source quiesce+export before transfer; target restore after prepare; resume after commit (mTLS peer carries opaque snapshot, not CRD status).
6. **Service Fabric** — after guest IP is known, register Machine as backend of named VIPs.

## Examples

See [`examples/network-fabric-machine.yaml`](../examples/network-fabric-machine.yaml).

## Docs

- Tutorial: [tutorials/network-fabric.md](tutorials/network-fabric.md)
- User guide — Machine networking: [guides/machine-network.md](guides/machine-network.md)
- User guide — policies & groups: [guides/network-policy.md](guides/network-policy.md)

## Non-goals

- Replacing Fabric host nftables SDN (`/api/network-policies`)
- Owning BPF programs inside Kairon
- Multus NAD as the primary attach path
- PacketWolf control plane in Kairon
