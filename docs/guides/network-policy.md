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
| `effectiveSynced` | At least one Machine was updated *and* a read-back of `GET /v1/vms/{id}/network/policy` against every applied Machine matches what was sent -- a real confirmation, not just "the write call returned success". A FluxVM that silently normalizes or partially rejects part of the request now shows up as `false` here instead of a false `true`. |
| `lastAppliedTime` | Last successful apply |

On delete, matched Running Machines on this node are reset to
`defaultAllow: true` (finalizer `kairon.zyvor.dev/network-policy`).

## Reconcile rules

1. Machine must be on this node (`spec.nodeName`).
2. Machine `status.phase` must be `Running` with a `runtimeID`.
3. Selector / `machineName` must match.
4. If `Machine.spec.network.dataplaneRequired` and attach is unhealthy, Machine
   reconcile errors (policy may still attempt apply independently).

## Inspecting policy objects

`kaironctl get networkpolicies` / `kaironctl get securitygroups` list every
`MachineNetworkPolicy`/`NetworkSecurityGroup` in a namespace (aliases:
`networkpolicy`/`machinenetworkpolicies`, `securitygroup`/
`networksecuritygroups`); `kaironctl describe networkpolicy NAME` /
`describe securitygroup NAME` and `kaironctl delete ...` round it out —
until now these two CRDs had no `kaironctl` support at all, unlike every
other kind. The dashboard's **Network policies** and **Security groups**
pages give the same read-only view; both stay list-only there —
`kaironctl`/`kubectl` remain how they get created or edited.

## Troubleshooting: why is traffic being allowed/blocked?

Four read-only, any-authenticated-operator API endpoints (API-only, no
dashboard yet) answer "what is actually happening," as opposed to "what
was configured":

- **`GET /api/v1/machines/{ns}/{name}/network-effective`** -- the
  Machine's fully-resolved effective policy, *after*
  `NetworkSecurityGroup`/label merging -- what's actually enforced right
  now, not just what the last `MachineNetworkPolicy` apply sent.
- **`GET .../network-drop-reasons?limit=N`** -- why the eBPF dataplane
  most recently dropped packets for this Machine. The single most direct
  answer to "why is my policy blocking traffic I expected to allow" --
  start here before re-reading your own policy YAML.
- **`GET .../network-flows?limit=N`** -- the Machine's most recent
  eBPF-observed network flows (allowed and denied).
- **`GET .../network-stats`** -- real, eBPF-dataplane-derived byte/packet
  counters for the Machine.

All four are raw JSON passthroughs of FluxVM's own response (no fixed
Kairon-side schema) -- FluxVM's own handlers return dynamic, evolving
shapes here rather than a versioned struct.

**Deliberately not wrapped**, discovered during the same FluxVM route
audit that added the four endpoints above: FluxVM's own Cilium-style
network dataplane exposes a much larger surface
(`/v1/network/cnp`, `/v1/network/identities`, `/v1/network/observe`,
`/v1/network/health`, `/v1/network/ipcache*`, `/v1/network/hubble/*`,
`/v1/network/services/*` health/stats/telemetry/conntrack-export-import-
delta-ack, per-service L7 Envoy contracts). Most of this is either
cluster-mesh-style internal node-to-node coordination machinery (conntrack
delta/ack, telemetry export, ipcache federation) FluxVM's own Fabric
integration uses internally, not something an operator calls directly, or
a substantial standalone observability product in its own right (Hubble's
full flow-observability UI) that would need its own dedicated design pass
rather than a quick wrap alongside four smaller diagnostics. Not
implementing these for now; the four endpoints above already cover the
concrete "why isn't my policy working" question this section exists to
answer.

## RBAC

`kairon-node` and `kairon-controller` ClusterRoles include
`machinenetworkpolicies` and `networksecuritygroups` (get/list/watch/patch).

## Tutorial

Step-by-step: [tutorials/network-fabric.md](../tutorials/network-fabric.md).
