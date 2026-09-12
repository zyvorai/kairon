# User guide: restoring a MachineSnapshot

How to get a usable disk back out of a `MachineSnapshot`, and why this is a
two-step flow rather than one "restore my VM" button.

## Why there's no in-place restore

A Kubernetes `PersistentVolumeClaim`'s `spec.dataSource` can't be changed
after the PVC is bound -- there is no such thing as "restore this existing
PVC back to an earlier snapshot" in the Kubernetes/CSI storage model.
Restoring **always** means creating a **new** PVC whose `dataSource` points
at the snapshot. That also means "restore" and "clone-from-snapshot" are
the same operation here -- there's no meaningful difference to model
separately.

`Machine.spec.volumes` is creation-time-only (same as `spec.image`/
`spec.resources`) -- editing it on an already-realized Machine has no
effect. So restoring into an existing running Machine isn't possible either;
what you actually get is a fresh PVC you point a **new** `Machine` at.

## The two steps

**1. Restore the snapshot into a new PVC:**

```bash
kaironctl restore my-snapshot --target-claim db-restored-pvc
```

or as a CRD:

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: MachineSnapshotRestore
metadata:
  name: db-restore-1
spec:
  snapshotName: my-snapshot
  targetClaimName: db-restored-pvc
  # volumeName: root       # required only if the snapshot covers more than one volume
  # storageClassName: ""   # empty uses the cluster default
  # storageSize: ""        # empty defaults to the VolumeSnapshot's own reported restoreSize
```

`kairon-controller` creates a real `PersistentVolumeClaim` named
`db-restored-pvc` with `spec.dataSource` pointing at the snapshot's
underlying `VolumeSnapshot`. Watch it with `kaironctl get restores` --
`status.phase` goes `Pending` → `Succeeded` once the PVC is `Bound`.
Under a `WaitForFirstConsumer` `StorageClass` (the common case, e.g.
Rancher's `local-path-provisioner`), the PVC stays `Pending` until step 2
gives it a consumer -- that's normal, not a failure.

**2. Point a new Machine at it** (this is just the existing PVC-backed boot
disk feature -- see [docs/guides/machine-storage.md](machine-storage.md)):

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: db-restored
spec:
  volumes:
    - name: root
      claimName: db-restored-pvc
  resources: {cpu: "2", memory: "4Gi"}
  runtime: {backend: qemu}
  powerState: Running
```

## Why this doesn't also create the Machine for you

The alternative -- `MachineSnapshotRestore` also constructing and creating a
full `Machine` object -- would mean either requiring the original Machine to
still exist (so its spec can be copied), which defeats the main real-world
use case (the original Machine is gone, that's *why* you're restoring), or
duplicating a whole Machine-spec templating scheme inside this CRD. Keeping
storage restoration and VM creation as two separate, already-solved steps
is simpler and works in the disaster-recovery case that actually matters.

## Real limits today (v1 of this feature)

- One volume restored per `MachineSnapshotRestore` -- for a Machine with
  multiple snapshotted volumes, create one restore object per volume you
  need back.
- No admission-time validation beyond what the CRD schema enforces --
  `kairon-controller` fails a restore clearly (`status.phase: Failed`) if
  the referenced `MachineSnapshot` isn't `Succeeded`/ready, but there's no
  earlier warning at `kubectl apply` time.
