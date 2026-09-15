# User guide: real CPU pinning (`spec.resources.cpuPinning`)

Real, exclusive host-core allocation -- a Machine actually owns specific
physical CPUs, with Kairon's own scheduler tracking and enforcing
non-overlapping allocation across every Machine competing for cores on a
node. The capability [`machine-cpu-numa.md`](machine-cpu-numa.md)'s own
`cpuSet` field explicitly does **not** provide (that one is a
guest-visible topology hint only).

## Why this exists

FluxVM already supports real `cgroup v2 cpuset.cpus` pinning via its own
resize API -- the missing piece was never the low-level primitive, it was
that *allocating specific, non-overlapping host CPU numbers* across every
Machine competing for them on one node is a real capacity-allocation
problem, and Kairon's scheduler used to do pure Machine-*count*
bin-packing with no notion of CPU capacity at all. This closes that gap.

## Prerequisites: assert which CPUs are pinnable

Kairon has no way to independently discover which host CPUs are safe to
exclusively hand to a Machine -- an operator must assert this via a node
label, the same "operator asserts a fact Kairon can't otherwise know"
pattern `kairon.zyvor.dev/storage-domain`/`network-domain`/`vfio-devices`
already use:

```bash
kubectl label node worker-1 kairon.zyvor.dev/pinnable-cpus="2-15"
```

The value uses Linux's own `cpuset.cpus` list syntax (`"2-15"` or
`"2,3,4-8,20"`) -- you can often paste the output of
`cat /sys/fs/cgroup/cpuset.cpus.effective` directly, after excluding
whatever cores you want reserved for the OS, `kairon-node` itself, and any
kubelet-managed (Guaranteed-QoS) Pods already running real workloads on
that node. **This is a real, deliberate design choice**: rather than
Kairon reading kubelet's own internal, undocumented, version-dependent
`cpu_manager_state` file (a known but fragile, unsupported community
pattern with no stability guarantee), the operator is the one source of
truth for which cores are actually safe to hand out -- the same posture
already established for storage/network domain compatibility and VFIO
allowlisting. **A node with no `pinnable-cpus` label has zero pinnable
CPUs -- fail-closed**, the same posture an empty `KAIRON_VFIO_ALLOWLIST`
already has.

## Requesting real pinning

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata: {name: latency-sensitive-db}
spec:
  image: {path: /var/lib/fluxvm/images/db.qcow2}
  resources:
    cpu: "4"
    memory: 8Gi
    cpuPinning: true
  runtime: {backend: qemu}
  powerState: Running
```

At scheduling time, `kairon-controller` filters out any candidate node
without at least `spec.resources.cpu` free, currently-unclaimed cores from
its `pinnable-cpus` label, then deterministically picks the cores (a
Machine `Pending` with a clear message if no node has enough). The chosen
core numbers land in `spec.resources.allocatedCpuSet` -- **system-computed,
the same relationship `spec.nodeName` itself already has to the
scheduler: not something you set yourself.** `kairon-node`
applies it to the live FluxVM cgroup the same reconcile pass that already
applies `spec.resources.limits`
([`machine-resource-limits.md`](machine-resource-limits.md)) -- both merge
into one FluxVM call, never two competing writes.

## Real limits today (first cut)

- **QEMU only**, matching every other NUMA/cpuSet/hugepages field.
- **Not NUMA-topology-aware.** Core selection is deterministic first-fit
  over the `pinnable-cpus` label's own ascending order -- it does not
  cross-validate against `spec.resources.numaNode` even when both are set
  on the same Machine. If you need pinned cores to also fall within a
  specific NUMA node, the `pinnable-cpus` label itself must already be
  scoped to that node's own cores (an operator responsibility).
- **Creation-time allocation only.** `spec.resources.allocatedCpuSet` is
  computed once, at initial scheduling -- there's no live re-allocation if
  a Machine's `spec.resources.cpu` changes later (hotplug doesn't grow the
  pinned set) or if the node's own `pinnable-cpus` label changes after the
  fact.
- **Races across reconcile ticks are possible in principle.** There is no
  distributed lock in this codebase -- two Machines scheduled in the same
  `kairon-controller` reconcile pass are allocated from the same
  consistent snapshot (safe), but `kairon-controller` itself doesn't run
  more than one replica actively reconciling at once (leader election
  already guarantees this, see
  [`kairon-controller-ha.md`](kairon-controller-ha.md)), so this is a
  theoretical, not a practically-observed, risk today.
- **No enforcement against kubelet's own CPU Manager beyond what the
  operator excludes from the label.** If the operator's `pinnable-cpus`
  label includes a core kubelet's static CPU Manager later exclusively
  grants to a new Guaranteed-QoS Pod, nothing here detects or prevents
  that collision -- keeping the label accurate as node workloads change is
  an ongoing operator responsibility, not something Kairon verifies.
- **No live-migration compatibility checking** -- the same posture
  `machine-cpu-numa.md`'s own NUMA fields already have; a pinned Machine
  can attempt migration to a target with a completely different
  `pinnable-cpus` label, and whether that's sensible is between you and
  the target node's own topology.
- **No dashboard support** -- `spec.resources.cpuPinning` is spec-only.
