# User guide: CPU/memory hotplug

How growing `spec.resources` on a running Machine works, and what today's
real limits are.

## How it works

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: db
spec:
  resources: {cpu: "4", memory: "4Gi"}   # was "2" / "2Gi"
  # ... unchanged otherwise
```

Just edit `spec.resources` on an already-`Running` Machine and increase
`cpu`/`memory` — `kairon-node` picks up the difference on its next reconcile
tick and calls FluxVM's real QMP hotplug (`device_add`/`object-add`), not a
reboot. `status.appliedVCPUs`/`appliedMemoryMiB` report what's actually been
realized so far.

Unlike every other `spec.resources` field, this one **is** live-reconciled
-- `spec.image`, `spec.network`, `spec.cloudInit` etc. remain creation-time-only.

## Why Kairon tracks `status.appliedVCPUs`/`appliedMemoryMiB` itself

FluxVM has no endpoint to query a VM's *current* live vCPU/memory count --
hotplugged CPUs and DIMMs are pure QMP-runtime state, never written back
into FluxVM's own stored VM record. So `kairon-node` is the only source of
truth for how much of `spec.resources` has actually been realized: it
computes the delta between `spec.resources` and `status.applied*`, sends
only that delta to FluxVM's hotplug endpoints, and advances `status.applied*`
by the amount FluxVM reports actually landed.

## Real limits today (v1 of this feature)

- **Grow-only.** FluxVM has no CPU/DIMM unplug. Lowering `spec.resources`
  below what's already hotplugged is logged and ignored, not applied and
  not an error -- `status.applied*` simply stays at its current (higher)
  value. To actually shrink a Machine, stop and recreate it.
- **Bounded by headroom reserved at creation.** FluxVM reserves hotplug
  headroom automatically (roughly double `vcpus`, and `memory_mib + 2Gi` or
  double, whichever is larger) -- Kairon doesn't yet expose a way to
  request more headroom than that default at creation time. Once that
  headroom is exhausted, a hotplug request fails clearly
  (`not enough hotplug headroom...`), surfaced in `status.message`, and
  retried automatically next tick (it won't succeed until the request is
  lowered back within headroom, or the Machine is recreated with a larger
  explicit headroom -- not yet configurable from Kairon).
- **Every hotplugged resource is lost across a stop/start cycle.** Neither
  hotplugged vCPUs nor hotplugged memory are part of the VM's boot-time
  `-smp`/`-m` arguments, so a cold migration, a manual stop/start, or any
  other Machine re-realization resets `status.applied*` back down to plain
  `spec.resources` -- this is expected, not a bug, and mirrors real QEMU
  behavior (see FluxVM's own hotplug docs).
- QEMU-only, since that's the only FluxVM backend with hotplug support.
