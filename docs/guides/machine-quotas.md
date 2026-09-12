# User guide: MachineQuota

Caps how many Machines, and how much total CPU/memory, a namespace may have
scheduled at once -- Kairon's namespace-scoped equivalent of a Kubernetes
`ResourceQuota`.

## Example

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: MachineQuota
metadata:
  name: team-payments
  namespace: payments
spec:
  maxMachines: 20
  maxTotalCpu: "40"
  maxTotalMemory: "160Gi"
```

Each field is optional (unset means no cap on that dimension); set at least
one for the quota to do anything. Multiple `MachineQuota` objects in the
same namespace all apply independently -- same as multiple Kubernetes
`ResourceQuota` objects in one namespace, every one of them must be
satisfied.

## How it's enforced

Unlike a real Kubernetes `ResourceQuota` (enforced by the API server at
admission time), there's no admission webhook here -- `Machine` objects can
always be created via the API. `kairon-controller`'s existing scheduling
loop checks quota instead, immediately before it would otherwise assign a
Machine to a node:

1. Every reconcile tick, tally how much each `MachineQuota`'s namespace is
   already using: count and sum `spec.resources.cpu`/`.memory` across every
   Machine in that namespace that's already scheduled (`spec.nodeName` set)
   and not desired-`Stopped` -- **not** yet-unscheduled Machines, since
   those aren't actually consuming anything yet.
2. For each not-yet-scheduled Machine, first find whether it has an
   eligible node at all (placement/affinity/anti-affinity, unrelated to
   quota). Only once a node is otherwise eligible does quota get checked;
   checking it earlier would reserve capacity for a Machine that couldn't
   have been scheduled anyway, starving something else in the same tick
   that could.
3. If admitting this Machine would push any applicable quota's Machine
   count, total CPU, or total memory over its cap, the Machine is left
   `Pending` with a clear message (`MachineQuota ns/name: maxMachines N
   reached`, etc.) instead of being scheduled. It's retried automatically
   on the next reconcile tick -- there's no special unblocking step needed
   once capacity frees up (a Machine is deleted, a quota is raised).
4. `status.usedMachines`/`usedTotalCpuCores`/`usedTotalMemoryMiB` are
   patched onto every `MachineQuota` every tick, mirroring what a real
   `ResourceQuota`'s `status.used` reports -- `kaironctl get quotas` shows
   this alongside the configured caps.

Already-scheduled Machines are never evicted retroactively if a quota is
lowered below what's already running -- quota only blocks *new* scheduling.

## Real limits today (v1 of this feature)

- Scoped by Kubernetes namespace, not `Machine.spec.tenant` (that field
  exists in the CRD but isn't read anywhere -- namespace is the only real
  multi-tenancy boundary Kairon uses today).
- No admission webhook, so nothing stops a namespace from having far more
  `Machine` objects created than its quota allows -- it just means most of
  them stay `Pending` forever instead of being rejected up front.
- CPU/memory quantities use the same fractional-CPU-rounds-up parsing as
  `Machine.spec.resources` (`internal/model.ParseVCPUs`/`ParseMemoryMiB`) --
  a `maxTotalCpu: "4.5"` cap behaves like `5`.
