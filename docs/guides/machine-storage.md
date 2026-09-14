# User guide: Machine storage

How to boot a Machine's disk from a `PersistentVolumeClaim` instead of a
hand-placed image file on the node, and what today's real limits are.

## The two ways to give a Machine a disk

| Field | What it means | Node-placement requirement |
|---|---|---|
| `spec.image.path` (default) | A plain file path already present on the node the Machine is scheduled to | An operator/image pipeline has to put the file there first, and it must live under `--image-root` if the node enforces one |
| `spec.volumes[0]` | The Machine boots from a file named `disk.img` inside the directory a Bound `PersistentVolumeClaim` resolves to | The bound `PersistentVolume` must already be a real, attach-ready directory on that node -- see limits below |

`spec.volumes[0]` takes priority when set; `spec.image.path` is only used as
a fallback when `spec.volumes` is empty. Both are creation-time-only, same
as `spec.resources`/`spec.network.forwards` — changing them on an existing
Machine has no effect.

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: db
spec:
  volumes:
    - name: root
      claimName: db-root-pvc
  resources: {cpu: "2", memory: "4Gi"}
  runtime: {backend: qemu}
  powerState: Running
```

`kairon-node` resolves `db-root-pvc` → its `status.phase == Bound` check →
`spec.volumeName` → the matching `PersistentVolume` → that PV's real host
directory, and boots from `<directory>/disk.img`. If you're hand-provisioning
storage for a test, that means placing (or copying) your qcow2/raw image at
exactly `<PersistentVolume path>/disk.img` before the Machine reconciles.

## Real limits today (v1 of this feature)

- **One boot volume per Machine.** Only `spec.volumes[0]` is used; additional
  entries are accepted by the CRD but currently ignored by the reconciler.
- **`Filesystem`-mode `PersistentVolume`s only.** A `Block`-mode PV is
  rejected with a clear error (`volumeMode "Block" is not supported...`) —
  Kairon opens a file inside the volume's directory, it doesn't hand FluxVM
  a raw block device.
- **`hostPath`, `local`, or Kairon's own CSI-backed volume sources.**
  `hostPath`/`local` already name a real, present-today directory on a
  specific node, resolved directly, no attach/mount step. A `csi`-backed PV
  is also resolvable now, but *only* when it names Kairon's own first-cut
  network-block (iSCSI) driver (`csi.kairon.zyvor.dev`) — see
  [`docs/guides/machine-storage-csi.md`](machine-storage-csi.md) for setup,
  and note this needs `csiNode.enabled` in the Helm chart plus
  `kairon-node`'s own `--csi-socket` flag pointed at it, neither of which
  is on by default. A PV naming any *other* CSI driver is still refused —
  Kairon only ever attaches/mounts network storage through its own driver,
  never an arbitrary third-party one.
- **The PVC must already be `Bound`.** A `Pending` claim fails reconcile
  with a clear "not Bound yet" error rather than retrying silently forever —
  check `kubectl get pvc` if a Machine referencing one gets stuck.
- **The PVC-resolved path is exempt from `--image-root`.** That allowlist
  exists to fence an arbitrary string in `spec.image.path`; a PVC name has
  already gone through a stronger gate (the claim had to exist and be bound
  by the cluster's own storage machinery), so the same node-local directory
  restriction doesn't apply to it.

## What this unlocks vs. what's still missing

This closes the gap between "Machines only boot from files an operator
manually placed on a specific node" and "Machines can boot from Kubernetes
storage" — for local/directory-backed storage classes (e.g. Rancher's
`local-path-provisioner`) directly, and for real network-block storage via
Kairon's own first-cut iSCSI CSI driver — see
[`docs/guides/machine-storage-csi.md`](machine-storage-csi.md). It does
**not** yet provide: snapshot **restore** or clone-from-snapshot into a new
Machine (`MachineSnapshot` is still create-only — see
[architecture.md](../architecture.md)), CPU/memory hotplug via this path, or
support for any network-block backend other than iSCSI (Ceph RBD/EBS/etc.
would each need their own driver backend, not just config).
