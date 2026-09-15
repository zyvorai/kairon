# Runbook: exporting a Machine's disk content out of the cluster

How to get a Machine's boot disk *bytes* off the cluster entirely (a
different site, an object-storage bucket, a laptop) -- for cross-cluster
clone, off-site backup, or migrating a Machine's data to a completely
different platform.

## Why this is standard Kubernetes tooling, not new Kairon machinery

Kairon has no Pod/Job/Service creation of its own anywhere -- `kairon-node`
talks to FluxVM directly and never runs a Machine inside a Pod (see
[`ARCHITECTURE.md`](../ARCHITECTURE.md)), the same reason there's no
"export server" this project could invent without taking on a real, new
category of Kubernetes API surface (Pod scheduling, Service exposure, its
own RBAC) purely to duplicate what a one-off Pod already does perfectly
well. **Getting disk content out of a `PersistentVolume` is a solved,
standard Kubernetes problem** -- a short-lived Pod that mounts the same
PVC read-only and streams it wherever you want. This runbook shows the
pattern, not a new tool.

## Step 1: get a consistent copy first (don't export a live boot disk directly)

A Machine's boot disk is a real qcow2/raw file FluxVM has open while the
Machine is `Running` -- exporting it while live only ever gets you a
crash-consistent copy (the same risk as `cp`-ing any live database file).
Two ways to get something consistent instead:

- **Stop the Machine first** (`kaironctl stop MACHINE`), then export its
  existing `PersistentVolumeClaim` directly -- always available, no extra
  step, but takes the Machine offline for the duration.
- **Take a `MachineSnapshot` first** (see
  [`machine-snapshot-restore.md`](guides/machine-snapshot-restore.md)) and
  export the *snapshot*, restored into a new PVC
  (`kaironctl restore SNAPSHOT --target-claim export-pvc`) -- the Machine
  never stops, but this needs a real CSI snapshotter behind your
  `StorageClass` (the same requirement `MachineSnapshot` itself already
  has -- Rancher's `local-path-provisioner` doesn't have one).

## Step 2: export the PVC's content with a short-lived Pod

```yaml
apiVersion: v1
kind: Pod
metadata:
  name: export-db-disk
spec:
  restartPolicy: Never
  # Only needed for a hostPath/local PV -- pins the Pod to the same node
  # the PV actually lives on. Not needed for a real network-block/cloud
  # CSI volume, which any node can mount.
  nodeSelector:
    kubernetes.io/hostname: worker-1
  containers:
    - name: export
      image: alpine:3
      command: ["sh", "-c", "gzip -c /data/disk.img > /data/disk.img.gz && echo done"]
      volumeMounts:
        - {name: disk, mountPath: /data, readOnly: false}
  volumes:
    - name: disk
      persistentVolumeClaim:
        claimName: db-root-pvc
        readOnly: false
```

Then get the compressed file out however fits your environment:

```bash
kubectl wait --for=condition=Ready pod/export-db-disk --timeout=5m || kubectl logs pod/export-db-disk
kubectl cp export-db-disk:/data/disk.img.gz ./disk.img.gz
kubectl delete pod export-db-disk
```

For a large disk, push directly to object storage from inside the Pod
instead of `kubectl cp` (swap the container image/command for one with
`aws s3 cp`/`rclone`/etc. and the right credentials mounted) -- avoids
buffering the whole file through your own machine.

To go the other direction (import an exported image into a new cluster),
place the file where `spec.image.path`/`spec.image.source` can reach it
and boot a new Machine from it normally -- see
[`machine-storage.md`](guides/machine-storage.md)/
[`machine-image-import.md`](guides/machine-image-import.md).

## Real limits today

- **No Kairon-specific tooling.** This is plain Kubernetes primitives
  (`Pod`, `PersistentVolumeClaim`, `kubectl cp`) -- no `kaironctl export`
  command, no ticket/auth mechanism, no progress tracking. If your
  workflow needs those, script this pattern yourself.
- **No built-in encryption or compression choice.** The example above
  gzips; swap in whatever your own security/bandwidth requirements need
  (the export Pod's command is yours to change).
- **hostPath/local PVs need node pinning** (`nodeSelector`) since the
  data only exists on that one node's local disk -- a real network-block/
  cloud CSI volume doesn't have this restriction.
- **Not a snapshot in itself.** Exporting a `Stopped` Machine's PVC
  directly is safe and consistent on its own; exporting a `Running`
  Machine's PVC without stopping it or snapshotting first only ever gets
  you a crash-consistent copy, the same risk as `cp`-ing any live disk
  image.
