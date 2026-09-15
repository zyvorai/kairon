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

## Requesting more hotplug headroom

By default FluxVM reserves roughly double `cpu`, and `memory + 2Gi` or
double `memory` (whichever is larger), as hotplug headroom at boot time --
enough for most cases, but a fixed ceiling you can't grow later (see "Real
limits" below). If you know upfront you'll want to grow a Machine well
beyond that default, request more headroom explicitly at creation:

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: db
spec:
  resources:
    cpu: "4"
    memory: "4Gi"
    maxCpu: "32"       # reserve headroom for hotplugging up to 32 vCPUs
    maxMemory: "128Gi" # reserve headroom for hotplugging up to 128Gi
  # ... unchanged otherwise
```

`maxCpu`/`maxMemory` are creation-time-only, like `cpu`/`memory`'s own
boot-time sizing -- unlike them, editing these on an already-running
Machine has no effect at all, since the headroom is baked into the VM's
boot-time `-smp`/`-m` arguments (`maxcpus=`/`maxmem=`) and there's no way
to grow that after boot. If `maxCpu` is set lower than `cpu`, FluxVM
silently raises it back up to at least `cpu` rather than erroring; if
`maxMemory` is set lower than `memory`, that's an inconsistent QEMU
argument FluxVM/QEMU itself will reject with an error, not something
Kairon validates first.

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
- **Bounded by headroom reserved at creation.** `spec.resources.maxCpu`/
  `.maxMemory` (see "Requesting more hotplug headroom" above) requests
  more than FluxVM's own default headroom, but it's still a fixed ceiling
  set once at creation, not something a running Machine can grow. Once
  that headroom is exhausted, a hotplug request fails clearly
  (`not enough hotplug headroom...`), surfaced in `status.message`, and
  retried automatically next tick (it won't succeed until the request is
  lowered back within headroom, or the Machine is stopped and recreated
  with a larger `maxCpu`/`maxMemory`).
- **Every hotplugged resource is lost across a stop/start cycle.** Neither
  hotplugged vCPUs nor hotplugged memory are part of the VM's boot-time
  `-smp`/`-m` arguments, so a cold migration, a manual stop/start, or any
  other Machine re-realization resets `status.applied*` back down to plain
  `spec.resources` -- this is expected, not a bug, and mirrors real QEMU
  behavior (see FluxVM's own hotplug docs).
- QEMU-only, since that's the only FluxVM backend with hotplug support.
