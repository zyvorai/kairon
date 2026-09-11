# User guide: MachineNetworkPolicy and NetworkSecurityGroup

Declarative VM-edge policy for Machines. The node agent applies objects only
for Machines scheduled on its node. Enforcement runs in FluxVM (TC/eBPF), not
in Kairon.

## Concepts

| CRD | FluxVM route | Purpose |
|---|---|---|
| `NetworkSecurityGroup` | `POST /v1/network/groups` | Named reusable group (+ labels/priority) |
| `MachineNetworkPolicy` | `POST /v1/vms/{id}/network/policy` | Per-VM (or label-selected) edge policy |
| `spec.cnp` on policy (optional) | `POST /v1/network/cnp` | CNP-shaped document compiled by FluxVM |

This does **not** replace Fabric’s host nftables SDN (`/api/network-policies`).

## NetworkSecurityGroup

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: NetworkSecurityGroup
metadata:
  name: frontend
  namespace: default
spec:
  groupName: frontend          # defaults to metadata.name
  labels:
    - tier=frontend            # key=value strings FluxVM matches
  priority: 100                # lower wins on rate/deny ties
  description: HTTPS egress
  policy:
    defaultAllow: false
    allowCidrs: ["10.0.0.0/8"]
    allowPorts: ["tcp/443", "udp/53"]
    allowIcmp: true
```

Status:

- `phase: Applied` — upserted on this node’s FluxVM
- `identity` — dataplane group identity
- `appliedOn` — node name that last wrote the group

On delete, the agent removes the FluxVM group (finalizer
`kairon.zyvor.dev/network-group`).

## MachineNetworkPolicy

Select Machines by name **or** labels (same namespace):

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: MachineNetworkPolicy
metadata:
  name: web-edge
  namespace: default
spec:
  # machineName: web          # optional; wins over selector
  selector:
    app: web
  policy:
    defaultAllow: false
    allowCidrs:
      - 10.0.0.0/8
      - 2001:db8:20::/48
    denyCidrs: []
    allowPorts: ["tcp/443", "udp/53"]
    groups: ["frontend"]
    labels: ["tier=frontend"]
    allowFqdns: []
    maxEgressMbps: 250
    maxEgressPps: 100000
    auditMode: false
    allowIcmp: true
    sampleRate: 0
```

### Policy fields (FluxVM `VmNetworkPolicy`)

| Field | Effect |
|---|---|
| `defaultAllow` | Action when allowlists are empty |
| `allowCidrs` / `denyCidrs` | Destination CIDRs (IPv6 needs eBPF mode) |
| `allowPorts` | `tcp/443`, `udp/53`, … — AND with CIDRs if both set |
| `maxEgressMbps` / `maxEgressPps` | Fixed-window egress ceilings (eBPF) |
| `groups` / `labels` | Security-group membership |
| `allowFqdns` | FQDNs resolved at apply time |
| `auditMode` | Log-and-allow instead of drop |
| `allowIcmp` | Permit ICMP/ICMPv6 through L4 checks |
| `sampleRate` | Allow-event sampling (0 = off) |

### Optional CNP body

```yaml
spec:
  cnp:
    metadata: { name: web-cnp }
    spec:
      # FluxVM CiliumNetworkPolicy-shaped document
```

Posted to `/v1/network/cnp` before the VM policy upsert.

### Status

| Field | Meaning |
|---|---|
| `phase` | `Applied` / `Error` |
| `observedMachines` | Running local Machines that received the policy |
| `effectiveSynced` | At least one Machine was updated this pass |
| `lastAppliedTime` | Last successful apply |

On delete, matched Running Machines on this node are reset to
`defaultAllow: true` (finalizer `kairon.zyvor.dev/network-policy`).

## Reconcile rules

1. Machine must be on this node (`spec.nodeName`).
2. Machine `status.phase` must be `Running` with a `runtimeID`.
3. Selector / `machineName` must match.
4. If `Machine.spec.network.dataplaneRequired` and attach is unhealthy, Machine
   reconcile errors (policy may still attempt apply independently).

## RBAC

`kairon-node` and `kairon-controller` ClusterRoles include
`machinenetworkpolicies` and `networksecuritygroups` (get/list/watch/patch).

## Tutorial

Step-by-step: [tutorials/network-fabric.md](../tutorials/network-fabric.md).
