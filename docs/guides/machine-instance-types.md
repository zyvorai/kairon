# User guide: MachineInstanceType

A reusable, named CPU/memory shape a Machine references instead of
inlining `spec.resources` itself -- Kairon's equivalent of an EC2 instance
type or KubeVirt's `VirtualMachineInstancetype`.

## Example

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: MachineInstanceType
metadata:
  name: standard-2x4
  namespace: prod
spec:
  resources:
    cpu: "2"
    memory: 4Gi
```

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: web-1
  namespace: prod
spec:
  instanceTypeName: standard-2x4
  image: {path: /var/lib/fluxvm/images/ubuntu-24.04.qcow2}
  runtime: {backend: qemu}
  powerState: Running
```

`spec.resources` is omitted entirely -- `kairon-controller` resolves
`instanceTypeName` and fills it in for you, from a `MachineInstanceType` in
the *same namespace*.

## How resolution works

Resolution happens **once**, the first reconcile tick after a Machine sets
`spec.instanceTypeName` while `spec.resources` is still completely empty:

1. `kairon-controller` looks up the named `MachineInstanceType` in the
   Machine's own namespace.
2. If found, `spec.resources` is patched onto the Machine with that
   instance type's `spec.resources` verbatim -- from then on, the Machine
   behaves exactly like one that always had `spec.resources` set directly
   (hotplug, quota, the admission webhook, `kubectl get machines`'
   CPU/Memory columns -- nothing downstream needs to know an instance type
   was ever involved).
3. If not found (a typo, or the `MachineInstanceType` hasn't been created
   yet), `spec.resources` stays empty and resolution is retried every
   tick -- once a matching `MachineInstanceType` shows up, it resolves
   normally.

**Explicit `spec.resources` always wins.** Setting both
`spec.instanceTypeName` and `spec.resources` on the same Machine leaves
`spec.resources` exactly as you wrote it -- resolution only ever fills in
resources that are still empty, never overwrites an explicit value.

**This is creation-time-only**, the same as `spec.image`/`spec.network.forwards`
(see [`machine-storage.md`](machine-storage.md)): editing
`spec.instanceTypeName` on an already-resolved Machine has no further
effect, and editing a `MachineInstanceType` itself never retroactively
resizes any Machine that already resolved against it. To change an
existing Machine's resources, edit `spec.resources` directly (a hotplug
resize -- see [`machine-hotplug.md`](machine-hotplug.md)) the same way
you always could.

## Real limits today (first cut)

- **Resource shape only** -- no OS-preference bundling (preferred backend,
  guest OS hints, boot firmware defaults) the way KubeVirt's separate
  `VirtualMachinePreference` CRD offers. A `MachineInstanceType` is
  exactly `spec.resources`, nothing else, for this first cut.
- **Namespace-scoped, not cluster-wide.** A `MachineInstanceType` must
  live in the same namespace as every Machine that references it -- no
  cluster-scoped catalog of shared instance types yet.
- **No admission-time validation of the reference.** With
  `webhook.enabled`, a `Machine` naming a nonexistent `instanceTypeName`
  is still accepted at `CREATE` -- it just stays unresolved (and
  eventually surfaces a clear "cpu quantity is empty" error at FluxVM
  creation time) rather than being rejected up front.
- **No `kairon-ui` picker yet.** The dashboard's Machine-create form
  doesn't yet offer a dropdown of available instance types -- set
  `spec.instanceTypeName` via `kaironctl`/`kubectl`/the API directly.
