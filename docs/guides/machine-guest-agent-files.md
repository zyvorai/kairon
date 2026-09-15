# User guide: guest file access (read/write a file inside a running guest)

A real, runtime, bidirectional file copy into or out of a `Running`
guest -- no SSH key, no shared volume, no reboot. Complements
[guest exec](machine-guest-exec.md) (run a command) and
[`spec.cloudInit.writeFiles`](machine-network.md) (drop files in at
*boot* time, one-way only): this reads or writes a file at any point
while the Machine is already running.

## What this needs

Unlike guest exec (standard `qemu-guest-agent`, `spec.guestAgent.enabled`),
guest file access rides FluxVM's own proprietary vsock guest agent --
the exact same `fluxvm-guest-agent` binary/systemd-service dependency the
[interactive text console](machine-text-console.md) needs. If you've
already enabled `spec.guestAgent.console` for the text console, file
access works with no further guest-image changes.

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: db
spec:
  guestAgent:
    console: true   # required -- FluxVM's proprietary vsock agent, not qemu-guest-agent
  # ... unchanged otherwise
```

## Who can use it

- The console relay must be configured (`console.enabled`) -- the same
  deployment-level gate the text console and guest exec both share.
- The calling operator must be a `ui.auth.users[].admin` account --
  stricter than the text console's any-authenticated-operator default,
  the same posture guest exec already has. See
  [SECURITY.md](../../SECURITY.md)'s "Guest file access" section for why.
- The target Machine must be `Running` with `spec.guestAgent.console: true`.

## Using it

Click **Files** on an eligible Machine in the dashboard. Choose **Read
file** (enter a path, get its content back) or **Write file** (enter a
path and content, optionally an octal mode like `644`). The dashboard's
own panel handles text content only -- it encodes/decodes as UTF-8
client-side, so a genuinely binary file only round-trips correctly if you
work with its base64 payload directly against the API
(`POST .../agent-file/put` / `.../agent-file/get`) rather than through the
text-only dashboard panel.

## Real limits today (first cut)

- **No path restriction.** An admin who can use this can read or write
  any path the guest agent process itself can reach inside the guest --
  there is no allowlist/denylist of sensitive files. See SECURITY.md.
- **~3MB effective file-size ceiling on reads.** Responses are capped by
  `internal/fluxvm.Client`'s own 4MiB HTTP response limit; since content
  travels base64-encoded (~4/3 expansion), a file larger than roughly 3MB
  fails to decode with a clear error rather than returning truncated
  content. Writes have no such cap enforced on Kairon's side.
- **Text-only in the dashboard UI**, as above -- the API itself is
  binary-safe (it's just base64 either way), only the dashboard's own
  panel assumes UTF-8 text.
- **One-shot, not a mount.** There's no live, ongoing sync between guest
  and host -- each read or write is a single, independent round trip.
