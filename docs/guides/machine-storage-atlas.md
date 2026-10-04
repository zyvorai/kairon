# User guide: Atlas-provisioned Machine storage

[Atlas](https://github.com/zyvorai/atlas) is Zyvor's storage control plane:
one REST API in front of Ceph RBD, CephFS, NFS, and any CSI StorageClass,
with policy intents, snapshots, backups, and job tracking. With Atlas
enabled, a Machine asks for a disk by size and policy instead of naming a
PersistentVolumeClaim someone created by hand.

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata: {name: web, namespace: default}
spec:
  volumes:
    - name: root
      atlas:
        size: 20Gi
        policy: gold            # Atlas policy intent (optional)
        storageClass: ceph-rbd  # optional; Atlas picks one from policy otherwise
    - name: data
      guestPath: /srv/data
      atlas:
        size: 100Gi
        retain: true            # keep the volume after the Machine is deleted
```

## What the controller does

1. Adds the `kairon.zyvor.dev/atlas-volumes` finalizer.
2. Calls Atlas `POST /volumes` with name `kairon-<namespace>-<machine>-<volume>`
   (hashed down to 63 characters when longer), tenant `--atlas-tenant`, and an
   owner of `kairon/machine/<namespace>/<name>`. Creates are idempotent, so a
   controller restart mid-create doesn't leak volumes.
3. Polls the returned job on later reconcile ticks. It never blocks a tick
   waiting on Atlas.
4. When the job succeeds, reads the volume back, records the PVC name, and
   writes it into `spec.volumes[].claimName`. From there kairon-node uses its
   existing PVC path ([machine-storage.md](machine-storage.md)).
5. Schedules the Machine only once every Atlas volume is Ready. Until then the
   Machine stays `Pending` with condition `Scheduled=False`,
   reason `VolumesNotReady`.

Per-volume state (Atlas volume id, job id, backend native id, phase) lives in
the `kairon.zyvor.dev/atlas-volumes` annotation:

```sh
kubectl get machine web -o jsonpath='{.metadata.annotations.kairon\.zyvor\.dev/atlas-volumes}' | jq
```

A volume whose Atlas job fails, or whose create Atlas rejects (bad policy,
unknown backend, size mismatch on an existing name), goes to phase `Failed`
and is not retried. Delete and recreate the Machine after fixing the spec.

## Deletion

When a Machine is deleted, kairon-node first tears down the VM and drops its
`runtime-cleanup` finalizer. Only then does the controller delete the Atlas
volumes (except `retain: true` ones) and release its own finalizer, so a disk
is never deleted under a running guest.

## Raw RBD mode

`atlas.mode: rbd` creates a raw Ceph RBD image (`POST /rbd-images`) instead of
a PVC, for FluxVM to open in place over librbd with no CSI attach in the path.
Ceph credentials stay in the node's FluxVM config; the Machine carries only
the `rbd:<pool>/<image>` id. Node-side support for booting from an in-place
RBD image is tracked on the roadmap; until it lands, use the default `pvc` mode.

## Enabling

```yaml
# values.yaml
atlas:
  enabled: true
  url: http://atlas.atlas-system.svc:5110
  tenantID: kairon
  defaultPolicy: standard
  tokenSecret:
    name: atlas-kairon-token   # Secret with key "token" (Atlas HS256 JWT)
```

Or run kairon-controller with `--atlas-url`, `--atlas-tenant`,
`--atlas-default-policy`, and `--atlas-token-file`, or set `KAIRON_ATLAS_TOKEN`.

The Atlas Go client (`github.com/zyvorai/atlas/clients/go`) is Go
standard library only, so the controller keeps its stdlib-only dependency
boundary ([DEPENDENCIES.md](../DEPENDENCIES.md)). The client is
contract-tested against a live Atlas gateway in Atlas's own CI.
