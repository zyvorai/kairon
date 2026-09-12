# Security policy

Kairon is Apache-2.0 open-source software from [Zyvor](https://zyvor.dev). It controls host-level VM execution. Treat the controller, node agents, FluxVM and any migration adapter as privileged infrastructure even when the Kairon process itself runs as a non-root container.

## Reporting a vulnerability

Please report security issues privately to **security@zyvor.dev**. Do not open a public GitHub issue for unfixed vulnerabilities.

## v0.3 migration security

- Users cannot submit a raw QEMU/TCP/exec migration destination. The target is a Kubernetes node selected by Kairon.
- Node-to-node migration control uses TLS 1.3 and `RequireAndVerifyClientCert`.
- Peer API responses use `Cache-Control: no-store`.
- The destination session ID is deterministic but contains no secret. Transfer endpoints remain inside the mTLS peer exchange and are not copied into CRD status.
- Destination session files are mode `0600` and written using fsync + atomic rename.
- Reusing a session ID with different immutable VM/source/target identity returns HTTP 409.
- A target that has no adapter returns `Unsupported`; the source VM is not touched.
- Source transfer failure aborts the prepared target.
- Commit ambiguity produces `NeedsRecovery`; Kairon does not automatically restart/cut over and risk split brain.
- The post-commit adopt-only guard refuses to create a fresh target runtime if the expected incoming VM cannot be found.

The baseline Helm deployment uses a shared cluster migration certificate with both server/client authentication EKUs for operational simplicity. For stronger node identity and independent key rotation, use per-node credentials or SPIFFE-style workload identity and configure the expected server name appropriately.

## DRA / VFIO

A `ResourceClaim` must be allocated before Kairon considers it. Resolved PCI BDFs are syntax checked and must be present in the node administrator's explicit allowlist. Empty allowlists deny passthrough.

## Images

`--image-root` constrains Machine image paths. Keep VM image directories non-writable by untrusted workloads. Signed-image policy is not yet implemented. CI builds all three container images and runs `govulncheck` against the Go dependency graph on every push, but does not publish images and does not scan the built image layers themselves (no Trivy/Grype/cosign/SBOM) -- whatever process publishes to `ghcr.io/zyvorai/kairon-*` today is outside this repository's CI, and nothing here proves what bits actually land there.

## `kairon-ui` (dashboard)

`kairon-ui` supports two auth modes, and accepts either whenever both are configured:

- **Real per-operator username/password login** (`ui.auth.users`, or a pre-created Secret via `ui.auth.existingSecret`): each account is a username plus a bcrypt hash of its password (`golang.org/x/crypto/bcrypt`; generate one with `kairon-ui -hash-password 'the password'` -- passwords are never stored or logged in plaintext). A successful login issues a signed, 12-hour session token (`base64url(payload).base64url(HMAC-SHA256(payload, sessionSecret))`, verified with `hmac.Equal`); `POST /api/v1/auth/logout` revokes it early via a small in-memory revocation list (effective on the replica that served the logout -- `kairon-ui` runs a single replica by default). A wrong password and an unknown username return the identical response, in the same time (a dummy bcrypt compare runs either way), to avoid leaking valid usernames. Every mutating request is now attributed to the authenticated username in the audit log, not just logged as an anonymous event -- the gap this section used to describe as unsolved.
- **Legacy shared bearer token** (`ui.token`/`ui.existingSecret`, compared in constant time via `crypto/subtle.ConstantTimeCompare`): kept working unchanged for existing deployments. **This token is still shared by every operator** -- no per-user identity, no expiry, no rotation short of a manual `helm upgrade --set ui.token=...` followed by a redeploy. Treat it like a shared root password and prefer migrating to `ui.auth.users` when you can.

Fail-closed by default: the binary's startup check refuses to run unless at least one of a token, a configured user, or `-allow-unauthenticated` (local development only) is present. The Helm chart goes further -- if an install sets none of `ui.token`, `ui.existingSecret`, `ui.auth.users`, `ui.auth.existingSecret`, or `ui.allowUnauthenticated`, it now **seeds one default `admin` account with a random password it generates once and persists across upgrades** (see `helm install`/`upgrade` NOTES for the retrieval command), rather than either failing the install or coming up unauthenticated. Setting any of those values explicitly opts out of the generated default entirely.

The dashboard's ClusterRole grants exactly the verbs its handlers use (`create`/`delete`/`patch` on Machines, `create`/`patch` on MachineMigrations, `get`/`list`/`watch` elsewhere) -- it cannot do anything through the Kubernetes API that isn't already reachable through `kaironctl` with the same privilege level.

Known limitations of the username/password mode, by design in this first pass: no rate limiting or account lockout on repeated failed logins; no password-reset flow (an operator regenerates a hash and redeploys, the same operational model as rotating `ui.token`); session revocation and the generated default-admin password only live on one replica (the chart runs exactly one); OIDC/SSO is a bigger, separate decision and is not implemented.

## VNC console (`console.enabled`)

Graphical VNC access to a Machine's QEMU display is a relay, not a new capability FluxVM itself exposes remotely: `browser (noVNC) -> kairon-ui -> kairon-node -> <workspace>/vnc.sock`. Understand the trust chain before enabling this on a shared cluster:

- **FluxVM's own VNC socket has no authentication or encryption of its own** -- it's a bare Unix-domain RFB server (`-vga std -vnc unix:<workspace>/vnc.sock`, no `password-secret`, no TLS). Anyone who can reach that socket file gets a full graphical session, full stop. This is a FluxVM-side property Kairon cannot change; the entire security of the feature rests on the layers around it.
- **kairon-ui's browser-facing WebSocket is gated by a single-use, ~30-second ticket**, not a bearer header (a native browser `WebSocket` can't set one). The ticket is minted only after normal operator auth succeeds (`POST .../console/ticket`, behind the same `withAuth` as everything else) and is consumed on first use -- it's useless if intercepted a moment later.
- **kairon-ui's hop to kairon-node is a shared bearer token** (`KAIRON_NODE_CONSOLE_TOKEN`, one value cluster-wide, generated by Helm the same way `kairon-ui-session` is), sent in plaintext HTTP over the cluster network -- there is no TLS on this hop in this first pass, mirroring `migration.dataplaneTls` being opt-in rather than mandatory on day one. Treat this token like the migration mTLS material: a cluster-internal secret, not one to expose outside the cluster network.
- **No fine-grained per-Machine authorization**: any operator who can reach kairon-ui and see a Machine can open its console, the same all-or-nothing model as every other dashboard action.
- **QEMU-only**: Cloud Hypervisor and Firecracker Machines have no display device at all (confirmed in FluxVM's own source) and are refused with a clear error, not a silent no-op.
- **kairon-node's OS user needs real filesystem access to `vnc.sock`**: FluxVM creates it root-owned, mode `0755` (no `other` write bit) -- confirmed against a real deployment, where `kairon-node`'s deliberately unprivileged `kairon` service user got `permission denied` dialing it, a clear 502 rather than a silent hang, but a real prerequisite operators must satisfy (e.g. a POSIX ACL granting the node's user `rw` on FluxVM's per-VM socket files, or running FluxVM and kairon-node under a shared group) before `console.enabled` actually works, not just renders.

Off by default (`console.enabled: false`); enabling it is a deliberate trade of convenience for the trust-chain above, appropriate for a trusted operator team, not a multi-tenant or hostile-network environment.
