# User guide: Machine image import

How to boot a Machine from a remote qcow2/raw image URL instead of a file an
operator already placed on the node, and what today's real limits are.

## `spec.image.source` vs. `spec.image.path`

| Field | What it means | Node-placement requirement |
|---|---|---|
| `spec.image.path` (default) | A plain file path already present on the node the Machine is scheduled to | An operator/image pipeline has to put the file there first |
| `spec.image.source.httpURL` | A plain `http(s)://` URL to a qcow2/raw image | `kairon-node` downloads it itself into a node-local, digest-keyed cache -- see below |

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

## Real limits today (v1 of this feature)

- **No format conversion.** The downloaded bytes are trusted via digest, not
  inspected -- FluxVM already consumes qcow2/raw directly (see
  [`machine-storage.md`](machine-storage.md)), so there's no `qemu-img
  convert` step. VMDK/OVA or any other format needs the source file already
  converted before it's fetched.
- **`http(s)://` URLs only.** No OCI/container-registry references, no
  authenticated/private URLs (a URL with embedded credentials works the same
  as any other `http.Client` request, but there's no separate
  credentials-Secret mechanism).
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
