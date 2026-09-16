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

The baseline Helm deployment uses a shared cluster migration certificate with both server/client authentication EKUs for the control-plane peer connection (`migration.tlsSecretName`) -- deliberately: peers are addressed by Kubernetes node InternalIP, not a stable per-host name, so this proves "is this a Kairon node," not "is this host X.Y.Z.W" (see `internal/migration/tls.go`). For stronger node identity there, use per-node credentials or SPIFFE-style workload identity and configure the expected server name appropriately.

The **data plane** -- the actual QEMU RAM/state stream (`migration.dataplaneTls`) -- is different: real per-connection hostname verification already exists (the source adapter passes the real destination host as the expected TLS name), so a shared identity there is a real, closeable weakness, not a deliberate tradeoff. `migration.dataplaneTlsSecretName` (opt-in, `scripts/gen-migration-mtls-certs.sh` generates both the certs and a ready-to-apply Secret in the right shape) gives every node its own distinguishable cert instead: each `kairon-node` pod's init container picks out only its own node's three files from the shared Secret (matched via the Downward API's `spec.nodeName`), and fails the pod outright rather than falling back to a shared or wrong identity if its own entry is missing. Left unset (the default), `dataplaneTls` still falls back to staging the shared control-plane cert onto the data plane too -- weaker, but exactly this project's behavior before `dataplaneTlsSecretName` existed, so no existing deployment's `helm upgrade` changes anything on its own. See `docs/guides/machine-migration-tls.md`.

## DRA / VFIO

A `ResourceClaim` must be allocated before Kairon considers it. Resolved PCI BDFs are syntax checked and must be present in the node administrator's explicit allowlist. Empty allowlists deny passthrough.

## Images

`--image-root` constrains Machine image paths. Keep VM image directories non-writable by untrusted workloads. CI builds all four container images (`controller`, `node`, `ui`, `csi-node`), pins their base images (`gcr.io/distroless/static-debian12`, `golang`, `node`) to a resolved digest rather than a floating tag, and runs both `govulncheck` against the Go dependency graph and a Trivy image-layer scan (failing on HIGH/CRITICAL) on every push. A separate tag-triggered workflow (`.github/workflows/release.yml`) publishes to `ghcr.io/zyvorai/kairon-*` -- this repository's CI is now the one place that mints those tags; it scans the exact bits it's about to push (not a separate build) and refuses to push anything the scan flags.

Every published image is also SBOM'd and signed: `anchore/sbom-action` (wrapping `syft`) generates a real SPDX SBOM from the exact pushed image (by digest, not tag -- a tag is mutable, a digest isn't), `cosign` signs that digest and attests the SBOM to it, keylessly -- no private key this project generates, stores, or can leak; the GitHub Actions job's own short-lived OIDC token (`id-token: write`) is exchanged for a Fulcio-issued certificate scoped to this exact workflow/repo/ref, and the signature is recorded in Sigstore's public Rekor transparency log. Verify either with `cosign`:

```bash
CERT_ID='^https://github.com/zyvorai/kairon/\.github/workflows/release\.yml@refs/tags/.*$'
ISSUER='https://token.actions.githubusercontent.com'

cosign verify --certificate-identity-regexp "$CERT_ID" --certificate-oidc-issuer "$ISSUER" \
  ghcr.io/zyvorai/kairon-controller:vX.Y.Z

cosign verify-attestation --type spdxjson --certificate-identity-regexp "$CERT_ID" --certificate-oidc-issuer "$ISSUER" \
  ghcr.io/zyvorai/kairon-controller:vX.Y.Z
```

Confirms two different things: `verify` proves the image byte-for-byte is what `release.yml` actually built and pushed for that tag (not tampered with, not substituted); `verify-attestation` additionally proves the attached SBOM is the real one generated from that same image, not a hand-edited or stale one. Each SBOM is also uploaded as a plain build artifact on the release workflow run, for anyone who just wants to read it without `cosign` at all. First shipped for the next tagged release after `v0.4.0` -- earlier tags were never signed or SBOM'd retroactively.

## `kairon-controller` leader election (`controller.leaderElection`)

`controller.replicaCount > 1` is coordinated by a `coordination.k8s.io/v1` Lease (`internal/leaderelection`), so only the elected leader's reconcile loop runs -- otherwise two unsynchronized replicas would race writing `spec.nodeName` and migration/snapshot/quota status. On by default (even at `replicaCount: 1`, where it's a no-op single-writer race) via a namespaced `Role`/`RoleBinding` this chart creates, scoped to `get/list/watch/create/update` on `leases` in `kairon-controller`'s own namespace only -- not cluster-wide, and not a resource any other component touches. The admission webhook and health/metrics endpoints are deliberately *not* gated by this: both are stateless per-request reads, safe to serve from every replica regardless of who holds the Lease.

Every failure mode here (a `Lease` read/write error, ambiguity about who currently holds it) resolves to "not leader" rather than reconciling anyway -- the same fail-closed posture the webhook's TLS cert/key pairing already has. `cmd/kairon-controller`'s own `-leader-elect` flag defaults to `false` (the opposite of the Helm chart's default) specifically so an existing bare-metal/systemd deployment's binary upgrade never silently starts refusing to boot over a newly-required namespace/RBAC it doesn't yet have -- see [`docs/guides/kairon-controller-ha.md`](docs/guides/kairon-controller-ha.md).

## MachineQuota / MachineDisruptionBudget admission

Both are enforced twice now: in-process (`kairon-controller`'s reconcile loop for quota, `kaironctl evacuate` for disruption budgets -- unchanged from before) and, when `webhook.enabled` is set, by a real validating admission webhook on `kairon-controller` that rejects a `Machine` create that would immediately exceed a `MachineQuota`, or a `MachineMigration` create that would violate a `MachineDisruptionBudget` -- closing the direct-API-write bypass both previously had. It reuses the exact same decision functions the reconcile loop and `kaironctl` already used (`internal/controller/quota.go`, `internal/controller/disruption.go`), not a second implementation to keep in sync.

Off by default: like `migration.tlsSecretName`/`console.tls.secretName`, this chart does not mint the webhook's serving certificate or `webhook.caBundle` for you -- both are required to enable it. `webhook.failurePolicy` defaults to `Fail`: a webhook outage blocks all `Machine`/`MachineMigration` writes cluster-wide rather than silently letting the bypass back in; set it to `Ignore` to trade that guarantee for availability. `MachineDisruptionBudget` enforcement, and `MachineQuota`'s Machine-`CREATE` check, only ever evaluate `CREATE`, matching the reconcile loop's own semantics exactly (`internal/controller/disruption.go`, `quota.go`) -- deliberately never stricter than the enforcement they backstop. `MachineQuota` also evaluates `UPDATE` now: `validateMachineResize` (`internal/controller/webhook.go`) denies growing an already-scheduled Machine's `spec.resources` (a hotplug resize) past its namespace's quota. This one *is* new enforcement, not just closing a bypass -- `kairon-node`'s hotplug reconciliation (`internal/agent/hotplug.go`) is per-node with no cluster-wide `MachineQuota` visibility of its own, so with `webhook.enabled` false (the default) a resize past quota still isn't caught anywhere at all.

## CRD conversion webhook (scaffold, not yet live)

The same `kairon-controller` webhook server also exposes `POST /convert/machinequotas`, implementing the `apiextensions.k8s.io/v1` `ConversionReview` contract (`internal/conversion`) -- on the identical TLS listener, certificate, and `Service` as the admission webhook above, so it introduces no new trust boundary or attack surface beyond what `webhook.enabled` already opens up. No CRD registers a second API version yet, so the real API server has no reason to ever call this route today; it exists and is tested (a real worked `MachineQuota` field-rename conversion, verified through the real HTTP envelope, not just the converter function in isolation) so that cutting a real `v1beta1` later is "wire an existing, proven route into a CRD's `spec.conversion`," not "build conversion webhook machinery from scratch." See [`docs/guides/crd-versioning.md`](docs/guides/crd-versioning.md).

## `kairon-ui` (dashboard)

`kairon-ui` supports two auth modes, and accepts either whenever both are configured:

- **Real per-operator username/password login** (`ui.auth.users`, or a pre-created Secret via `ui.auth.existingSecret`): each account is a username plus a bcrypt hash of its password (`golang.org/x/crypto/bcrypt`; generate one with `kairon-ui -hash-password 'the password'` -- passwords are never stored or logged in plaintext). A successful login issues a signed, 12-hour session token (`base64url(payload).base64url(HMAC-SHA256(payload, sessionSecret))`, verified with `hmac.Equal`); `POST /api/v1/auth/logout` revokes it early via a small in-memory revocation list, applied immediately on the replica that served the logout and, once `ui.replicaCount > 1`, mirrored to every other replica within ~15s via a shared `ConfigMap` -- see `docs/guides/kairon-ui-ha.md`. A wrong password and an unknown username return the identical response, in the same time (a dummy bcrypt compare runs either way), to avoid leaking valid usernames. Every mutating request is now attributed to the authenticated username in the audit log, not just logged as an anonymous event -- the gap this section used to describe as unsolved.
- **Legacy shared bearer token** (`ui.token`/`ui.existingSecret`, compared in constant time via `crypto/subtle.ConstantTimeCompare`): kept working unchanged for existing deployments. **This token is still shared by every operator** -- no per-user identity, no expiry, no rotation short of a manual `helm upgrade --set ui.token=...` followed by a redeploy. Treat it like a shared root password and prefer migrating to `ui.auth.users` when you can.

Fail-closed by default: the binary's startup check refuses to run unless at least one of a token, a configured user, or `-allow-unauthenticated` (local development only) is present. The Helm chart goes further -- if an install sets none of `ui.token`, `ui.existingSecret`, `ui.auth.users`, `ui.auth.existingSecret`, or `ui.allowUnauthenticated`, it now **seeds one default `admin` account with a random password it generates once and persists across upgrades** (see `helm install`/`upgrade` NOTES for the retrieval command), rather than either failing the install or coming up unauthenticated. Setting any of those values explicitly opts out of the generated default entirely.

The dashboard's ClusterRole grants exactly the verbs its handlers use (`create`/`delete`/`patch` on Machines, `create`/`patch` on MachineMigrations, `get`/`list`/`watch` elsewhere) -- it cannot do anything through the Kubernetes API that isn't already reachable through `kaironctl` with the same privilege level.

`GET /metrics`, when `Server.Metrics` is configured, is unauthenticated by design -- same convention as `/healthz`/`/readyz`, since a Prometheus scrape config never carries the bearer token either. Unlike `kairon-controller`/`kairon-node`'s own `/metrics` (served on a separate health-check port, typically not the same one exposed for browser access), kairon-ui serves it on the *same* listen port as the dashboard itself -- so if that port is reachable from wherever operators reach the dashboard (an Ingress, a LoadBalancer), `/metrics` is reachable there too. The data it exposes is aggregate request counts/latencies by method and status class, not per-request detail (no paths, no usernames, no request bodies) -- low sensitivity, but still real information disclosure to anyone who can reach the dashboard's URL. Restrict at the network layer (`ui.networkPolicy`, an Ingress rule scoping `/metrics` to your monitoring namespace) if that's a concern for your deployment.

Repeated failed logins against one username are rate-limited: 5 failures lock that username out for 5 minutes (`Retry-After` header on the `429`), tracked per raw requested username -- including unknown ones -- so the lockout behavior itself never leaks which usernames are real. A successful login clears the counter.

Separately, every route (`-rate-limit-rps`/`-rate-limit-burst`, default 20 req/s sustained with bursts to 40, on by default) is now rate-limited per remote address, via a small hand-rolled token-bucket limiter (`internal/ratelimit` -- stdlib-only, so this didn't need a third exception to this project's Go-stdlib-only design guarantee). This is a different, broader protection than the login lockout above: it bounds request *volume* from one client address against anything -- including an already-authenticated client hammering an unrelated route, or unauthenticated traffic against `/api/v1/auth/login`/`/api/v1/auth/config` itself -- not just repeated failed passwords against one username. Applied outermost, ahead of routing, so it protects `/healthz`/`/readyz`/`/metrics` too. `-rate-limit-rps=0` disables it.

Keyed by `r.RemoteAddr` alone by default: behind a Kubernetes `Service`/`Ingress`/load balancer with no forwarded-header trust configured, every client can appear as the same address, collapsing everyone's buckets into one. `-trusted-proxy-header`/`-trusted-proxy-cidrs` (Helm: `ui.rateLimit.trustedProxyHeader`/`.trustedProxyCidrs`, both empty by default -- unchanged behavior) close this: when both are set, `kairon-ui` reads the real client address from the named header (e.g. `X-Forwarded-For`, taking its leftmost entry -- the first hop's own claim about the original client) instead of the TCP peer, but *only* when that TCP peer itself falls inside `trustedProxyCidrs`. That check is load-bearing, not optional ceremony: without it, any client talking directly to `kairon-ui` -- bypassing the load balancer, or reaching a route the load balancer doesn't otherwise gate -- could set the header itself to an arbitrary value, either evading its own rate limit entirely (a fresh, never-throttled key every request) or deliberately colliding with another real client's bucket to throttle them out. Startup refuses (fails closed) if only one of the two flags is set, or if a `-trusted-proxy-cidrs` entry doesn't parse as a CIDR.

Password changes no longer require a redeploy: a signed-in operator can change their own password (`POST /api/v1/auth/password`, re-proving the current one first), and an admin account (`ui.auth.users[].admin: true`, or the legacy shared token, which is already root-equivalent for every other route) can reset another operator's password (`POST /api/v1/users/{username}/password`) -- which also immediately invalidates that operator's outstanding sessions, since there's no server-side list of every issued token to individually revoke otherwise. Both persist the change back into the `kairon-ui-users` Secret via a Kubernetes API write kairon-ui now has (scoped by a namespaced `Role` to `get`/`patch` on exactly that Secret, nothing broader), and the chart's `kairon-ui-users` template preserves whatever is already in that Secret across a `helm upgrade` rather than resetting it from `ui.auth.users` every time. This is only wired up when the chart itself owns that Secret (not `ui.auth.existingSecret`, which may be managed by something else) and only for named `ui.auth.users` accounts -- the Helm-seeded random-password default `admin` account still can't change its own password in place; graduate to a named account for that.

Known limitations of the username/password mode: with `ui.replicaCount > 1`, session revocation and login lockout propagate across replicas within ~15s (not instantly), via a shared Kubernetes `ConfigMap` rather than an external store like Redis -- see `docs/guides/kairon-ui-ha.md` for the full model and its real limits.

**OIDC/SSO** (`ui.oidc.enabled`, off by default): an Authorization Code + PKCE flow against an external identity provider, alongside -- not instead of -- the two modes above. This is the one place in Kairon that breaks the project's Go-stdlib-only design guarantee (`golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3`): real JWT/JWK signature verification isn't something this codebase hand-rolls. `GET /api/v1/auth/oidc/login` redirects to the provider with a random PKCE code verifier and nonce, both signed into the OAuth2 `state` parameter (HMAC-derived from `sessionSecret`, domain-separated from real session tokens -- see `internal/uiapi/oidc.go`) rather than kept in any server-side store. The callback verifies the ID token's signature, issuer, audience, expiry, and nonce before issuing a normal kairon-ui session token -- from there on, an OIDC-authenticated session is indistinguishable from a password one to every other route. Admin capability defaults to the same as before OIDC existed -- exclusively a `ui.auth.users[].admin: true` local account -- but `ui.oidc.adminGroups` (opt-in, empty by default) can now also grant it: an OIDC session whose ID token carries one of the configured groups (`ui.oidc.groupsClaim`, recorded on the session at login time) is treated as admin, re-checked against the *current* `ui.oidc.adminGroups` config on every request via `Server.isAdminIdentity` -- so a config change takes effect on that session's very next request, without a re-login, the same freshness the static `ui.auth.users[].admin` path already had. This does let an OIDC-admin session call `POST /api/v1/users/{username}/password` to reset another operator's password, same as any other admin identity -- an OIDC identity still has no password of its own in Kairon to reset. See `docs/guides/kairon-ui-oidc.md`.

## VNC console (`console.enabled`)

Graphical VNC access to a Machine's QEMU display is a relay, not a new capability FluxVM itself exposes remotely: `browser (noVNC) -> kairon-ui -> kairon-node -> <workspace>/vnc.sock`. Understand the trust chain before enabling this on a shared cluster:

- **FluxVM's own VNC socket has no authentication or encryption of its own** -- it's a bare Unix-domain RFB server (`-vga std -vnc unix:<workspace>/vnc.sock`, no `password-secret`, no TLS). Anyone who can reach that socket file gets a full graphical session, full stop. This is a FluxVM-side property Kairon cannot change; the entire security of the feature rests on the layers around it.
- **kairon-ui's browser-facing WebSocket is gated by a single-use, ~30-second ticket**, not a bearer header (a native browser `WebSocket` can't set one). The ticket is minted only after normal operator auth succeeds (`POST .../console/ticket`, behind the same `withAuth` as everything else) and is consumed on first use -- it's useless if intercepted a moment later. `issueConsoleTicket` fails the request outright (`500`, no ticket issued) if its own `crypto/rand` read ever fails, rather than risk minting a partially- or entirely-zero-value (and therefore guessable) ticket -- `crypto/rand.Read` only guarantees a full, unpredictable fill when it returns no error.
- **kairon-ui's hop to kairon-node is a shared bearer token** (`KAIRON_NODE_CONSOLE_TOKEN`, one value cluster-wide, generated by Helm the same way `kairon-ui-session` is), sent in plaintext HTTP over the cluster network by default. **One-way TLS is now available** (`console.tls.enabled`, with `console.tls.secretName` pointing at a pre-created Secret holding `tls.crt`/`tls.key` for kairon-node's server certificate and `ca.crt` for kairon-ui to verify it against) -- opt-in, mirroring `migration.dataplaneTls`, and not mutual TLS, since the shared token above already authenticates kairon-ui to kairon-node. Treat the token like the migration mTLS material regardless of whether TLS is enabled: a cluster-internal secret, not one to expose outside the cluster network.
- **Every console ticket is bound to the username that requested it** at issuance time (not just logged as an anonymous event), and each console session logs a `uiapi console opened`/`uiapi console closed` audit line (username, namespace, name, remote address, duration) -- closing the "no session/audit trail for console usage" gap from the first pass. A session opened via the legacy shared `ui.token` still logs with an empty username, the same limitation that token has everywhere else.
- **The dashboard hides the Console button proactively** when `console.enabled` is off or the Machine isn't `Running` on a `qemu` (or unset/`auto`) backend, via `GET /api/v1/config`, rather than only failing after the click.
- **Per-Machine authorization is opt-in** via the `kairon.zyvor.dev/console-allowed-users` annotation, a comma-separated list of kairon-ui operator usernames. Unset (the default) is unchanged from before this existed: every authenticated operator may open the console. Set, `handleConsoleTicket` (`internal/uiapi/console.go`) denies ticket issuance with a `403` to anyone not on the list, except an admin identity (a `ui.auth.users[].admin` account, or an OIDC session in `ui.oidc.adminGroups` -- see the OIDC section above), which always bypasses it for break-glass access. A caller with no real per-operator identity -- the legacy shared `ui.token`, or fully unauthenticated dev mode -- is denied outright whenever a Machine sets this annotation, since there's no identity to check against an allowlist: fail closed rather than silently allow. The ticket itself is also now bound to the one Machine it was issued for (not just the requesting operator), so a ticket minted for a Machine the caller can access can't be replayed against a different Machine's console within its 30-second TTL.
- **Real Kubernetes RBAC is also available, opt-in** (`ui.auth.rbacConsoleCheck`) -- layered alongside the annotation allowlist above, a `SubjectAccessReview` against the `machines/console` subresource must also allow it. Only meaningful for an operator identity `kube-apiserver`'s own RBAC recognizes (an OIDC claim mapped into `kube-apiserver`'s own OIDC config); a local `ui.auth.users[]` account has no real Kubernetes `User` to check and is denied outright when this is on. See [`docs/guides/kairon-ui-console-rbac.md`](docs/guides/kairon-ui-console-rbac.md).
- **QEMU-only**: Cloud Hypervisor and Firecracker Machines have no display device at all (confirmed in FluxVM's own source) and are refused with a clear error, not a silent no-op.
- **kairon-node's OS user needs real filesystem access to `vnc.sock`**: FluxVM creates it root-owned, mode `0755` (no `other` write bit) -- confirmed against a real deployment, where `kairon-node`'s deliberately unprivileged `kairon` service user got `permission denied` dialing it, a clear 502 rather than a silent hang, but a real prerequisite operators must satisfy (e.g. a POSIX ACL granting the node's user `rw` on FluxVM's per-VM socket files, or running FluxVM and kairon-node under a shared group) before `console.enabled` actually works, not just renders.

Off by default (`console.enabled: false`); enabling it is a deliberate trade of convenience for the trust-chain above, appropriate for a trusted operator team, not a multi-tenant or hostile-network environment. The bare-metal install path (`scripts/deploy-remote.sh --with-ui --with-console`) generates and wires the same token end to end; `--console-tls` now automates TLS for that hop too -- by default it generates a private CA + server certificate locally and installs it on the remote host (SAN list is a best-effort guess at the host's own addresses, since kairon-ui actually dials whatever Kubernetes reports as the Machine's Node's InternalIP, which the script can't always know in advance -- a TLS verification error names the mismatch, fix it with an explicit `--console-tls-cert`/`-key`/`-ca`), or accepts your own cert/key/CA via those same flags.

## Guest exec (`spec.guestAgent.enabled`)

Running a command inside a guest -- `browser -> kairon-ui -> kairon-node -> FluxVM's real qemu-guest-agent guest-exec` -- rides the same relay infrastructure as the VNC console above (`internal/consoleproxy`, the same `KAIRON_NODE_CONSOLE_TOKEN`/`console.tls` trust chain), but is a meaningfully bigger capability -- arbitrary code execution in the guest, not just viewing its screen -- so it's gated more strictly:

- **Admin-only, with no finer-grained permission yet**: `handleExec` (`internal/uiapi/exec.go`) requires the caller pass `Server.isAdminIdentity` outright, denying every non-admin identity regardless of the `kairon.zyvor.dev/console-allowed-users` annotation (which, for console access, defaults to allowing any authenticated operator). This is a deliberate, documented limit for a first cut, not a place to loosen without adding a real per-operator exec permission first.
- **An OIDC-only identity can pass this check, but only via `ui.oidc.adminGroups`** (opt-in, empty by default) -- see the OIDC section above. Without it configured, the prior behavior is unchanged: an OIDC-authenticated operator needs a *local* `ui.auth.users[]` admin record too, not just a successful OIDC login.
- **`ui.auth.rbacConsoleCheck`, when enabled, still applies on top** of the admin check (both `consoleAuthorized`'s per-Machine annotation allowlist and, if configured, its `SubjectAccessReview` against `machines/console` must also pass) -- an admin account is never exempt from a real Kubernetes RBAC denial.
- **Requires `spec.guestAgent.enabled`** (the same flag used for guest-IP resolution and `MachineSnapshot` quiesce) -- guest-exec is real qemu-guest-agent, QEMU-only in practice since that's the only backend with a functioning QGA channel today.
- **No command-line auditing**: `uiapi exec requested` logs who ran something, on what Machine, and when -- deliberately not *what* was run, since a command's own arguments can carry secrets (e.g. a token passed on the command line). If you need a full command transcript for compliance, that's a real, open gap, not something this logs today.
- **A stalled command has no way to attach to it after the fact**: FluxVM's own guest-exec does its own guest-exec-status polling internally and returns exactly once, on completion or its own timeout (60s default, `timeoutSeconds` overridable) -- there is no session to reconnect to, no partial output streamed as it happens.
- **Same VNC-socket-permission caveat as the console above doesn't apply here** -- guest-exec goes over vsock/virtio-serial via FluxVM's own process, not a filesystem socket kairon-node dials directly, so there's no equivalent local-file-permission prerequisite.

Off by default in practice (no admin account exists until an operator creates one, and `spec.guestAgent.enabled` is itself opt-in); once both are true, treat exec the same way you'd treat `kubectl exec` into a privileged pod -- convenient for a trusted admin team, not something to expose to less-trusted operators without a finer permission model this project doesn't have yet.

## Text console (`spec.guestAgent.console`)

An interactive shell inside the guest -- `browser (xterm.js) -> kairon-ui -> kairon-node -> FluxVM's own WebSocket-upgraded console endpoint` -- rides the exact same relay infrastructure and authorization model as the VNC console above (same ticket flow, same `consoleAuthorized` per-Machine allowlist, same `KAIRON_NODE_CONSOLE_TOKEN`/`console.tls` trust chain), just a different upstream on kairon-node's side and a different in-guest channel:

- **A completely different guest-side dependency from every other guest-agent feature in this project.** VNC needs nothing in the guest at all; guest-exec/`MachineSnapshot` quiesce/guest-IP resolution need the standard, widely-packaged `qemu-guest-agent`. Text console needs FluxVM's own **proprietary** `fluxvm-guest-agent` binary and systemd service baked into the guest image -- a real, new operational requirement this project has never asked of an operator before enabling this. See `docs/guides/machine-text-console.md` for what that actually involves.
- **Not gated by the exec's admin-only requirement above** -- text console reuses `consoleAuthorized`'s existing VNC-console model (any authenticated operator by default, restrictable via `kairon.zyvor.dev/console-allowed-users`), the same reasoning applies here as for VNC: an interactive shell is roughly the same trust level as a graphical display into the same guest.
- **Works on every backend**, unlike VNC (QEMU-only) -- FluxVM's own vsock agent isn't tied to a display device.
- **The vsock channel's own authentication token is entirely FluxVM's problem, not Kairon's**: FluxVM auto-generates a random per-VM token and burns it into the guest's own disk before boot whenever `spec.guestAgent.console` is set -- Kairon never generates, stores, or transmits this secret itself, and it never appears in a Kairon-controlled log or API response.
- **A ticket now carries a `kind`** (`"vnc"` or `"text"`, `?kind=text` when requesting one) -- the same single-use, 30-second, per-Machine-bound ticket mechanism as VNC, just naming which of the two relay routes it authorizes.

Off by default (`spec.guestAgent.console: false`); the console relay itself (`console.enabled`) must also be configured, the same deployment-level gate VNC and guest-exec both already share.

## Guest file access (`spec.guestAgent.console`)

Reading or writing a file inside a running guest -- `browser -> kairon-ui -> kairon-node -> FluxVM's own bespoke vsock guest agent` -- rides the same `fluxvm-guest-agent` channel the text console above uses (`spec.guestAgent.console`, not `spec.guestAgent.enabled`), relayed through the same `internal/consoleproxy` infrastructure as a plain JSON request/response, the same shape guest exec already has:

- **Admin-only, same posture as guest exec, stricter than text console.** Arbitrary guest file read/write is at least as sensitive as arbitrary command execution -- `handleAgentPutFile`/`handleAgentGetFile` (`internal/uiapi/agentfile.go`) require an admin identity outright (a `ui.auth.users[].admin` account, or an OIDC session in `ui.oidc.adminGroups` -- see the OIDC section above), unlike the text console's any-authenticated-operator-by-default model.
- **Requires `spec.guestAgent.console`**, not `spec.guestAgent.enabled` -- this is FluxVM's proprietary vsock agent, the same dependency the text console needs (`fluxvm-guest-agent` baked into the guest image), not standard `qemu-guest-agent`.
- **No path restriction of any kind.** An admin who can reach this can read or write any path the guest agent process itself can access inside the guest -- there is no allowlist/denylist of sensitive paths (`/etc/shadow`, guest-side credentials, etc.). Treat this exactly like `kubectl exec`/`cp` into a privileged pod: convenient for a trusted admin team, not something to expose more broadly without a finer permission model this project doesn't have yet.
- **Response size is capped at kairon-ui's own 4MiB HTTP-response limit** (`internal/fluxvm.Client`'s general response cap) -- a file whose base64 encoding exceeds that (roughly a 3MB file) fails to decode with a clear error rather than returning silently-truncated content.
- **No content-type or binary-safety guarantee on the dashboard's own UI** -- the browser encodes/decodes content as UTF-8 text; a real binary file only round-trips correctly if the operator handles the base64 payload directly against the API rather than through the dashboard's text-only panel. See `docs/guides/machine-guest-agent-files.md`.
- **A sibling command-exec route rides this exact same channel and posture**: `POST /api/v1/machines/{ns}/{name}/agent-exec` (`internal/uiapi/agentexec.go`) runs a shell command over the vsock agent -- same admin-only gate, same `spec.guestAgent.console` requirement, same `internal/consoleproxy` relay shape. Genuinely distinct from "Guest exec" above despite the similar name: this one is backend-agnostic (no QEMU/qemu-guest-agent dependency at all), so it's the only guest-exec path available for a Cloud Hypervisor/Firecracker/FluxVm-sandbox-backed Machine. Same audit-line posture as guest exec (who/when logged, never the command itself, since arguments can carry secrets).

## VM-state snapshot/restore

Checkpointing and restoring a Machine's full hypervisor state -- `browser/API
client -> kairon-ui -> kairon-node -> FluxVM's own POST /v1/vms/{id}/snapshot`,
`/start-from-snapshot`, and `/stop` -- relayed through the same
`internal/consoleproxy` infrastructure as guest exec and guest file access,
the same plain JSON request/response shape. Distinct from `MachineSnapshot`
(CSI's disk-content-only snapshot, `internal/csinode`) -- this captures RAM,
CPU, and device state via FluxVM's real QEMU `savevm`/Cloud Hypervisor
snapshot, restoring back into the *same* Machine rather than a new PVC.

- **Admin-only, same posture as guest exec and guest file access.**
  `handleVMSnapshot`/`handleVMRestoreSnapshot`
  (`internal/uiapi/vmsnapshot.go`) require a `ui.auth.users[].admin`
  account outright; the same "no group-to-admin claim mapping" OIDC
  limitation applies.
- **No guest-agent dependency at all** -- unlike every other guest-agent
  feature in this list, this operates entirely at the QEMU/Cloud
  Hypervisor level against FluxVM itself, so it needs neither
  `spec.guestAgent.enabled` nor `spec.guestAgent.console`.
- **Restoring is genuinely destructive to the Machine's current running
  state**: `RestoreSnapshot` always stops the Machine first (FluxVM's own
  `start-from-snapshot` silently ignores the requested tag on an
  already-running VM), then starts it back up from the checkpoint --
  whatever the Machine was doing at the moment of the call is interrupted,
  not preserved. If the restore's own start step fails after the stop
  already succeeded, `internal/agent`'s own reconcile loop notices the
  runtime is FluxVM-stopped while `spec.powerState` still wants it running
  and starts it back up from its last-known-good disk state as a safety
  net -- not a second automatic restore attempt, and not the snapshot
  itself.
- **No snapshot enumeration or deletion, and no path/size restriction on
  tags** -- FluxVM itself owns tag storage; Kairon has no endpoint to list
  or prune what's been saved on a Machine. See
  `docs/guides/machine-vm-state-snapshot.md`.

## Sandboxes (`spec.sandbox`)

FluxVM's own agent-sandbox track -- a lightweight, fast-boot in-tree
hypervisor for short-lived ephemeral workloads -- adds three new trust
boundaries beyond the VM lifecycle every other Machine already goes
through:

- **Template building is admin-only and a real resource cost.**
  `POST /api/v1/nodes/{node}/templates` (`internal/uiapi/sandboxes.go`)
  pulls and exports an arbitrary caller-named OCI image on the target
  node (via FluxVM's own `skopeo`/`umoci`-backed `build_oci_template`) --
  same admin-only gate FluxVM's own `build_template` handler already
  enforces, kept here too rather than relying on FluxVM alone.
- **The HTTP proxy relay (`.../sandbox-http/{port}/{path...}`) is the
  broadest capability in this list.** It passes an arbitrary HTTP
  method/path/body/headers straight through to whatever the guest's own
  web server does with them -- materially more than guest exec (one
  command, synchronous, logged who/when) or file access (one path at a
  time). Admin-only, and deliberately *not* gated on
  `spec.guestAgent.console`, since it never touches the vsock agent at
  all -- it dials the guest's own network IP directly, so it needs
  `spec.network` to actually give the guest a real IP (`tap`/bridge, not
  `user`/SLIRP). Whatever the guest's own web application does with a
  proxied request (auth, input validation, rate limiting) is entirely the
  guest's own responsibility -- Kairon relays bytes, it doesn't inspect
  them.
- **`spec.deviceClaims` is refused outright for a sandbox Machine** at
  creation time (`internal/fluxvm.Client.CreateSandboxForMachine`) --
  FluxVM's lightweight sandbox hypervisor has no VFIO passthrough support,
  so silently ignoring a device claim here would be worse than a clear
  upfront error.

See `docs/guides/machine-sandboxes.md`.

## Image catalog (`spec.image.catalogName`)

FluxVM's own node-local image catalog -- a named, checksummed (and
optionally signed) alias `spec.image.catalogName` or any `CreateVmRequest.image`
field can reference instead of a raw path:

- **Every mutating operation is admin-only**: registering, removing,
  renaming, cloning, exporting, or toggling read-only on a catalog entry
  (`internal/uiapi/catalog.go`) -- the same posture sandbox template
  building already has, and for the same reason: registering an entry
  triggers a real image download and checksum pass on the target node, a
  real resource cost an operator shouldn't trigger freely. Listing
  (`GET`) is any-authenticated-operator, read-only visibility.
- **No path restriction on `source` (register) or `path` (export)** --
  an admin who can reach this can point FluxVM at any local path or
  `http(s)://` URL the FluxVM process itself can reach/write, the same
  trust level as `kubectl exec`/`cp` into a privileged pod.
- **Kairon never generates or verifies signatures itself** -- FluxVM's
  own catalog signing (`fluxvm catalog sign`, run out of band on the
  node) is the only way an entry gets one; Kairon only ever relays
  whatever FluxVM already reports (`signatureValid`), the same "not
  Kairon's problem" posture the text console's vsock-agent token already
  has.
- **`spec.image.catalogName` bypasses `--image-root` path fencing
  entirely** -- deliberately, since it was never a filesystem path
  `validateImagePath` could meaningfully fence in the first place (the
  same exemption `spec.image.source`-resolved paths already have, for
  the analogous reason).

See `docs/guides/machine-image-catalog.md`.

## Egress check (`POST /api/v1/nodes/{node}/egress-check`)

A stateless diagnostic against a node's static sandbox egress config
(`internal/uiapi/egress.go`) -- FluxVM's own `POST /v1/egress/check`,
checking whether an outbound request to a given host would be allowed,
with no auth requirement at all on FluxVM's own side. Kairon adds two
layers on top:

- **Admin-only.** Even though the check itself is read-only, confirming
  which hosts are allowlisted (or that a credential is configured for one
  at all) is real information about a node's sandbox egress policy.
- **The credential itself is never returned.** FluxVM's own response
  carries `inject_authorization` -- the literal secret value it would
  inject into a matching request, not a boolean -- when a credential-vault
  entry matches the checked host. `handleEgressCheck` deliberately
  discards that value entirely and reports only a boolean
  (`wouldInjectCredential`) instead; returning the raw value to any admin
  who can guess or enumerate a configured host would defeat the point of
  having a vault at all. Covered by a dedicated regression test
  (`TestHandleEgressCheckRedactsCredential`) that fails loudly if the
  secret ever leaks into the response body again.

There is no way to change the underlying allowlist/credential vault
through this or any Kairon API -- it's static, node-local FluxVM config,
not a runtime-mutable resource.

## Warm pools (`POST/GET/DELETE /api/v1/nodes/{node}/pools/...`)

FluxVM's own node-local warm-VM pools -- `size` VMs pre-booted and kept
`Paused`, ready for an instant claim -- add one trust boundary beyond
what a sandbox itself already has:

- **Creating or deleting a pool is admin-only** (`internal/uiapi/pool.go`,
  reusing `requireCatalogAdmin`'s exact gate) -- creating one commits real,
  ongoing node resources (`size` resident VMs, whether ever claimed or
  not); deleting one destroys every member VM it currently holds, claimed
  or not, not just the pool's own bookkeeping. Listing is any
  authenticated operator (read-only visibility).
- **Claiming is admin-only too**, even though it just resumes an
  already-authorized, already-built VM -- the claimed VM's `name`/
  `ttlSeconds` overrides are attacker-influenceable inputs the same way
  any other Machine-creation-adjacent field is.
- **A claimed VM is not automatically a Kairon-managed `Machine`** --
  Kairon hands back FluxVM's own raw VM record; there is no automatic
  admission/ownership step, so a claimed VM sits outside every other
  admission-time guard (`MachineQuota`, the webhook, RBAC subresource
  checks) until an operator explicitly creates a corresponding `Machine`
  themselves. Not a bypass of anything -- FluxVM's own claim isn't gated
  by Kairon's admission path in the first place -- but worth knowing
  before assuming pool claims are already subject to the same policy
  Machine creation is.
- **If FluxVM's own `fluxvm-microvm` operator (`MicroVMPool` CRD) is also
  deployed against the same nodes**, it reconciles against the identical
  `/v1/pools` state this API wraps, with no coordination between the two.
  Don't manage the same pool name from both.

See `docs/guides/machine-sandboxes.md`'s "Warm pools" section.

## Runtime diagnostics (capabilities, pressure, cpuset, freeze/thaw)

Four small FluxVM diagnostics (`internal/uiapi/diagnostics.go`):

- **Runtime capabilities and pressure/cpuset are any-authenticated-operator,
  read-only visibility** -- a node's static capability manifest and a
  Machine's own PSI/cpuset readings carry no secrets and reveal nothing
  an operator with normal dashboard access couldn't already infer.
- **Freeze/thaw are admin-only**, unlike the read-only three above --
  halting a Machine's entire cgroup at the kernel level (not just its
  guest CPUs, the way `spec.powerState: Paused` does) is a materially
  more disruptive operation, reusing the same admin-gate posture guest
  exec and VM-state snapshot/restore already have for comparably
  impactful actions.
- **No interaction guard against freezing a Machine mid-migration or
  mid-hotplug** -- the same "between you and FluxVM's own semantics"
  posture already documented for `Paused`.

See `docs/guides/machine-diagnostics.md`.

## Network observability (`.../network-effective`, `.../network-stats`, `.../network-flows`, `.../network-drop-reasons`)

Four read-only network troubleshooting endpoints (`internal/uiapi/network_observability.go`):

- **Any authenticated operator**, same posture as the pressure/cpuset
  diagnostics above -- flow/drop-reason/stats data for a Machine an
  operator can already view is not more sensitive than the Machine's own
  status.
- **Raw JSON passthrough, not a Kairon-defined schema** -- FluxVM's own
  handlers return a dynamic `serde_json::Value` here, so there is no
  fixed Rust struct this project could mirror even if it wanted a typed
  response; Kairon relays bytes, it doesn't validate or reshape them.
- **Deliberately narrow scope.** FluxVM's own network dataplane exposes a
  much larger Cilium-style surface (CNP/identity/ipcache management,
  Hubble flow observability, per-service health/stats/telemetry/conntrack
  state transfer, L7 Envoy contracts) discovered during the same route
  audit that added these four -- not wrapped here, since most of it is
  either internal node-to-node coordination machinery, not an
  operator-facing capability at all, or substantial enough (Hubble) to
  need its own dedicated design pass. See
  `docs/guides/network-policy.md`'s "Troubleshooting" section for the
  full reasoning.

## CSI node plugin (`csiNode.enabled`)

Kairon's own first-cut CSI driver (`csi.kairon.zyvor.dev`, iSCSI only -- see [`docs/guides/machine-storage-csi.md`](docs/guides/machine-storage-csi.md)) is a real, larger trust boundary than every other Kairon component, inherent to what it does, not a design oversight:

- **`kairon-csi-node` runs `privileged: true`**, not just an added capability list -- logging in to an iSCSI target, creating a filesystem, and mounting it all genuinely need this. It's also the one Kairon-built container image *not* based on the minimal distroless base every other component uses: it needs a real `iscsiadm`/`blkid`/`mkfs.*` environment around it, which means a real package manager, shell, and OS userland inside the image, a meaningfully larger attack surface than `kairon-controller`/`kairon-node`/`kairon-ui`'s single static binary each.
- **`hostNetwork: true`**, the same reason `kairon-node` itself runs that way -- direct L3 reachability to whatever host:port an iSCSI target's `portal` names, taken straight from a `PersistentVolume`'s `spec.csi.volumeAttributes`. Anyone who can create a `PersistentVolume` in the cluster can point this driver at an arbitrary network destination; treat "who can create PVs naming `csi.kairon.zyvor.dev`" as equivalent to "who can make this node's iSCSI initiator connect anywhere," the same posture as `kaironctl create --image` already requires trusting whoever can create a `Machine`.
- **No Kubernetes API access at all** -- `kairon-csi-node` is a pure local gRPC server (`internal/csinode`) and never calls `kube.Client`; its ServiceAccount has `automountServiceAccountToken: false`, one less credential sitting in an already-privileged pod.
- **No CHAP support on `kairon-node`'s own consumption path** (see the CSI guide's "Real limits") -- deliberately, to avoid granting `kairon-node` `get` RBAC on arbitrary Secrets a Machine author's PV could name.
- **iSCSI login/session state is per-node, in the container's own writable root filesystem** (`/etc/iscsi`, `/var/lib/iscsi`) -- not persisted across a pod restart beyond what `iscsiadm`'s own idempotent `--op=new`/`--login` calls reconstruct on the next reconcile.

Off by default (`csiNode.enabled: false`); enabling it is a deliberate trade -- real network-block storage for Machines -- against a meaningfully larger blast radius than any other optional Kairon component. Appropriate once you've decided to trust whoever can create `PersistentVolume`/`Machine` objects with node-level `iscsiadm` access, the same way `hostNetwork`/privileged DaemonSets always require that trust.

## Third-party CSI client (`node.thirdPartyCSIDrivers`)

`kairon-node` can act as its own CSI client against a driver it doesn't ship -- see [`docs/guides/machine-storage-thirdparty-csi.md`](docs/guides/machine-storage-thirdparty-csi.md) for the feature itself. The trust model is deliberately the same shape as VFIO passthrough's allowlist above, applied to a new domain:

- **Explicit operator allowlist, not automatic discovery.** `node.thirdPartyCSIDrivers` maps a driver name to a socket path; a driver name not present is refused outright, fail-closed the same way an unlisted VFIO BDF is refused. `kairon-node` never dials a socket it wasn't explicitly told about, and never attempts kubelet's own `plugins_registry/` discovery mechanism.
- **No new Secret RBAC granted.** `kairon-node` never resolves a `nodeStageSecretRef`/`nodePublishSecretRef` for a third-party driver, for the identical reason it never does for its own iSCSI driver: doing so would mean granting `get` RBAC on Secrets named by whatever a Machine's `PersistentVolume` happens to reference, letting any Machine/PV author read an unrelated, sensitive Secret. This is the deliberate reason first-cut compatibility is narrow (`attachRequired: false`, no-secret drivers only) rather than broad.
- **Read-only host mount, broader than the sockets actually used.** Enabling this mounts the node's entire `/var/lib/kubelet/plugins` directory read-only into the `kairon-node` container, rather than templating a mount per allowlisted driver. `kairon-node` itself only ever connects to the specific socket path configured per driver name -- nothing in this codebase lists or reads anything else under that mount -- but the mount's own blast radius (what else lives under that host directory) is broader than strictly necessary; a future cut could narrow this to per-driver mounts.
- **Staging/publish paths are Kairon-synthesized**, not kubelet's Pod-UID-keyed convention -- a deviation a third-party driver's own internal assumptions could theoretically depend on; see the guide's "Real limits" for why this is named as a compatibility risk rather than a security one (no privilege implication, just a correctness one).
- **Not validated against a live third-party driver.** Request-building and the allowlist-fail-closed behavior have real unit test coverage against a fake gRPC CSI server, but no live Ceph-CSI/RBD (or other) deployment exists in this project's own test environment. Treat this as unvalidated against your specific driver until you've tested it yourself.

Off by default (`node.thirdPartyCSIDrivers: {}`); enabling it for a given driver is a deliberate, per-driver trust decision -- the same category of tradeoff as `csiNode.enabled` above, scoped to whichever driver sockets you explicitly name.
