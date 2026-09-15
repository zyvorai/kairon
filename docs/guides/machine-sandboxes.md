# User guide: sandboxes (`spec.sandbox`)

Run a short-lived, ephemeral workload -- e.g. an AI agent's own generated
code -- on FluxVM's own lightweight, fast-boot in-tree hypervisor track,
instead of a full QEMU/Cloud Hypervisor/Firecracker VM. Admin-only,
API-only first cut, no dashboard yet.

## What this is

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata: {name: agent-run}
spec:
  image: {path: /images/sandbox.qcow2}
  resources: {cpu: "1", memory: 512Mi}
  ttlSeconds: 900           # auto-expire after 15 minutes
  guestAgent: {console: true}   # unlocks Kairon's own file/exec/console UI (see below)
  sandbox: {}                    # opts into FluxVM's sandbox track
```

Or, booting from a pre-built template instead of an image (faster boot,
no `spec.image`/`spec.resources` needed -- FluxVM loads the template's own
pre-baked spec and ignores both entirely):

```yaml
spec:
  sandbox:
    templateName: alpine-agent
```

Under the hood: `kairon-node` calls FluxVM's real `POST /v1/sandboxes`
instead of `POST /v1/vms` -- FluxVM forces the runtime to its own
lightweight `flux-vm` backend and force-enables its own vsock guest agent
regardless of `spec.guestAgent`, whatever that says. Everything else about
a sandbox Machine -- `status.phase`, `spec.powerState`, `spec.ttlSeconds`,
deletion -- works exactly like any other Machine.

## Building a template first

A template is a pre-exported OCI image rootfs FluxVM can boot from
instantly, skipping a full image boot. Build one via the node-scoped admin
API (admin-only -- pulling and exporting an arbitrary OCI image is a real
resource cost):

```bash
curl -X POST https://<kairon-ui>/api/v1/nodes/<node>/templates \
  -H "Authorization: Bearer <token>" \
  -d '{"name": "alpine-agent", "imageRef": "docker.io/library/alpine:3.19"}'
```

List what's already built on a node with `GET
/api/v1/nodes/<node>/templates`. Templates are **per-node** state -- one
built on `worker-1` isn't visible from `worker-2`; a `spec.sandbox.templateName`
referencing a template that doesn't exist on the Machine's assigned node
fails with FluxVM's own clear error.

## Guest access

FluxVM always runs its own vsock guest agent for a sandbox, but Kairon's
own [guest file access](machine-guest-agent-files.md) and
[backend-agnostic guest exec](machine-guest-agent-files.md#related-guest-exec-over-the-same-vsock-agent-api-only)
still gate on `spec.guestAgent.console: true` client-side regardless --
set it anyway to use those against a sandbox. The QEMU-only
[guest exec](machine-guest-exec.md) (`qemu-guest-agent`) and
[interactive text console](machine-text-console.md) work the same way
too, since both ride channels a sandbox's lightweight hypervisor actually
supports (the vsock agent) or don't need QEMU specifically.

## HTTP proxy: reaching a service running inside the sandbox

**`ANY /api/v1/machines/{ns}/{name}/sandbox-http/{port}/{path...}`** relays
an arbitrary HTTP request straight through to a sandbox's own guest HTTP
server on `port` -- method, headers, body, and the guest's response
status/headers/body all pass through. Useful for a webhook receiver or dev
server the sandboxed workload starts on its own. Admin-only: this is a
materially broader capability than guest exec or file access -- whatever
the guest's own web server does with an arbitrary request is between the
guest and the caller, Kairon has no visibility into it.

Requires the Machine to actually have a guest IP (`spec.network` using
`tap`/bridge networking, not `user`/SLIRP -- the proxy dials the guest
directly, the same requirement FluxVM's own proxy route has).

```bash
curl -X POST https://<kairon-ui>/api/v1/machines/default/agent-run/sandbox-http/8080/api/webhook \
  -H "Authorization: Bearer <token>" \
  -d '{"event": "..."}'
```

## Listing sandboxes

**`GET /api/v1/nodes/{node}/sandboxes`** lists every FluxVM sandbox on a
node -- node-scoped, not Machine-scoped, since it reflects FluxVM's own
real state (including a sandbox created directly against FluxVM outside
Kairon entirely), the same posture `GET /api/v1/nodes` itself has for
node visibility. Any authenticated operator, since this is read-only.

## Real limits today (first cut)

- **No VFIO passthrough.** `spec.deviceClaims` on a sandbox Machine is
  refused outright at create time with a clear error -- FluxVM's
  lightweight sandbox hypervisor doesn't support PCI passthrough.
- **A template-based sandbox ignores `spec.image`/`spec.resources`/
  `spec.runtime.kernel` entirely.** FluxVM loads the template's own
  pre-baked spec; setting those fields alongside `templateName` has no
  effect and isn't rejected as a conflict -- know that a template wins
  before wondering why a resource change didn't apply.
- **No admin-only NUMA/CPU-set/hugepage support** -- those already require
  the `qemu` backend elsewhere in this project, and a sandbox always
  resolves to `flux-vm`.
- **No dashboard yet.** Every capability here (create, list, HTTP proxy,
  template build/list) is API-only -- `kaironctl` has no dedicated verbs
  either.
- **No snapshot listing/deletion for templates**, matching
  [VM-state snapshot/restore](machine-vm-state-snapshot.md)'s own posture
  for tags -- FluxVM owns storage, Kairon has no prune/list-by-age
  mechanism.
- **`SnapshotSandbox`** (FluxVM's own `POST /v1/sandboxes/{id}/snapshot`,
  which exports a sandbox's rootfs state to an explicit file path) isn't
  wired to a Kairon-facing route yet -- `internal/fluxvm.Client.SnapshotSandbox`
  exists but has no admin API/consoleproxy relay in front of it today.
