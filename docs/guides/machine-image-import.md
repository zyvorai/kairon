# User guide: Machine image import

How to boot a Machine from a remote qcow2/raw image URL instead of a file an
operator already placed on the node, and what today's real limits are.

## `spec.image.source` vs. `spec.image.path`

| Field | What it means | Node-placement requirement |
|---|---|---|
| `spec.image.path` (default) | A plain file path already present on the node the Machine is scheduled to | An operator/image pipeline has to put the file there first |
| `spec.image.source.httpURL` | A plain `http(s)://` URL to a qcow2/raw image | `kairon-node` downloads it itself into a node-local, digest-keyed cache -- see below |
| `spec.image.source.oci` | A containerDisk registry reference (`quay.io/containerdisks/fedora:40`) | `kairon-node` pulls it by manifest digest into the same cache -- see [OCI containerDisks](#oci-containerdisks) |
| `spec.image.source.format` | `qcow2`/`raw` (default, booted as downloaded) or `ova`, `vmdk`, `vhd`, `vhdx` | Converted through FluxVM's image import before boot -- see [Converting and repairing](#converting-and-repairing) |
| `spec.image.source.repair` | Offline virtio repair for VMs from VMware or another hypervisor | Same |

`spec.image.source` and `spec.volumes` are mutually exclusive with each
other in practice (a Machine only has one boot disk); when `spec.volumes` is
set it still takes priority, exactly as it already does over
`spec.image.path` -- see [`machine-storage.md`](machine-storage.md).

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: web-1
spec:
  image:
    source:
      httpURL: https://cloud-images.example.com/noble-server-cloudimg-amd64.img
    digest: sha256:8f434346648f6b96df89dda901c5176b10a6d83961dd3c1ac88b59b2dc327aa
  resources: {cpu: "2", memory: "4Gi"}
  runtime: {backend: qemu}
  powerState: Running
```

`spec.image.digest` is **required** whenever `spec.image.source` is set --
it's the cache key: without it, two Machines pointing at the same (mutable)
URL would have no way to know whether they mean the same bytes. Format is
`sha256:<64 hex characters>`, the same convention Kairon already uses for
`spec.image.digest` on a plain `spec.image.path` Machine.

## Setup

Set `node.imageCacheDir` in the Helm chart (empty/off by default):

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system \
  --set node.imageCacheDir=/var/lib/fluxvm/images/cache
```

This mounts a writable `hostPath` volume into `kairon-node`'s container at
exactly that path (unlike `--image-root`, which only ever validates a path
*string* -- `kairon-node` itself never reads or writes an operator-placed
image file today). The downloaded bytes land at this same absolute path on
the host, which is what makes them visible to FluxVM (a separate process on
the same host) once `spec.image.path` is set to the cache path.

## How it works

On the first reconcile of a `spec.image.source` Machine, `kairon-node`:

1. Streams the URL into a temp file under `<imageCacheDir>/sha256/`, hashing
   as it downloads.
2. Verifies the result against `spec.image.digest` -- a mismatch deletes the
   temp file and the Machine's reconcile fails with a clear error, retried
   next tick.
3. Atomically renames the verified file to `<imageCacheDir>/sha256/<hex digest>`.
4. Sets `spec.image.path` to that cache path in memory for the rest of this
   reconcile -- `resolveBootDiskPath`/FluxVM's own `CreateWithVFIO` see an
   ordinary path, exactly as if an operator had placed it there.

A second Machine (or a later reconcile tick of the same Machine) naming the
same digest skips straight to step 4 -- no re-download. This is the whole
answer to "50 Machines booting the same golden image": the cache path is
content-addressed and immutable once written, so it's shared for free.

## Converting and repairing

With `format: ova` (or `vmdk`, `vhd`, `vhdx`) or `repair: true`, kairon-node
downloads and verifies the file as above, then calls FluxVM's
`POST /v1/images/import` with the cached path. FluxVM converts it to raw and,
with `repair`, fixes the guest offline: VMware tools disabled, virtio modules
added to the initramfs and the initramfs rebuilt, `/dev/sdX` moved to
`/dev/vdX` in fstab and grub, persistent NIC rules dropped and a DHCP fallback
added. The resulting disk path is recorded in
`<imageCacheDir>/imported/kairon-<digest prefix>[-repaired].json`, so the
import runs once per digest per node.

FluxVM reads the file from the same path kairon-node downloaded it to, so
`imageCacheDir` must be the same host directory for both. The chart mounts
`node.imageCacheDir` from the host at the same path, so this holds whenever
it is set. `kaironctl import ova` builds such a Machine from an
OVA; see [migrate-from-vmware.md](migrate-from-vmware.md).

```yaml
spec:
  image:
    source:
      httpURL: https://files.example.com/web01.ova
      format: ova
      repair: true
    digest: sha256:...
```

## OCI containerDisks

`spec.image.source.oci` boots the same containerDisk images KubeVirt uses:
a container image whose layers hold the disk under `/disk/` (for example
`quay.io/containerdisks/fedora`). No container runtime is involved;
kairon-node speaks the registry v2 API directly.

```yaml
spec:
  image:
    source:
      oci: quay.io/containerdisks/fedora:40
    digest: sha256:<manifest or index digest>
```

- `spec.image.digest` is the **manifest (or multi-arch index) digest**,
  for example from `crane digest quay.io/containerdisks/fedora:40` or
  `skopeo inspect`. The pull is always by that digest; the tag is a label.
  A reference that carries its own `@sha256:` must match it.
- For an index, the `linux/<node arch>` manifest is used. Layers are
  searched top-down for the first regular file under `disk/`. Every
  manifest and layer is checked against its digest while streaming, and
  nothing reaches the cache unless all checks pass.
- The disk lands at `<imageCacheDir>/oci/sha256/<manifest digest>` and is
  shared by every Machine on the node naming that digest. `format` and
  `repair` work as for `httpURL`.
- HTTPS only, anonymous pulls only (including the anonymous bearer-token
  flow Docker Hub, quay.io and ghcr.io use for public images). gzip and
  uncompressed layers; zstd layers are rejected.

## Real limits today (v1 of this feature)

- **Only the boot disk of a multi-disk OVA is attached.** The other
  converted disks stay on the node (logged by kairon-node).
- **No private sources.** No credentials-Secret mechanism for either
  `httpURL` or `oci` (a URL with embedded credentials works the same as any
  other `http.Client` request; private registries are refused).
- **No cache eviction.** `<imageCacheDir>` only ever grows; freeing disk
  space is an operator concern (`du`/manual cleanup), not something Kairon
  automates.
- **Single-node cache, not cluster-wide.** Two Machines scheduled to
  *different* nodes each download their own copy into their own node's
  `imageCacheDir` -- there's no cluster-wide image registry/replication.
- **Validated at admission time only when `webhook.enabled`.** With the
  admission webhook off (the default), a malformed `spec.image.source` (no
  digest, non-`http(s)` URL) still surfaces -- just later, as a stuck
  Machine reconcile error instead of an immediate `kubectl apply` rejection.
