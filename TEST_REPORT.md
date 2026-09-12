# Unreleased: PVC-backed boot disk test report

## Result

**PASS.** `make all` green (coverage 68.1%, threshold 50%). `helm lint` and `helm template` green. New unit tests cover `resolveBootDiskPath`/`hostDirForPV` (fallback to `spec.image.path`, missing `claimName`, unbound PVC, `Block` volumeMode rejection, unsupported volume source rejection, `hostPath`/`local` resolution) plus a full-reconcile integration test proving a Machine with only `spec.volumes` set (no `spec.image.path`) reaches FluxVM's create call with the PVC-resolved image path, and that the resolved path is exempt from the `--image-root` allowlist.

## What shipped

`spec.volumes[0]` now actually does something: `kairon-node` resolves the named `PersistentVolumeClaim` (must be `Bound`) to its `PersistentVolume`, accepts only `Filesystem`-mode volumes backed by `hostPath` or `local` sources, and boots the Machine from `<that directory>/disk.img` instead of requiring `spec.image.path`. New `internal/kube.Client.GetPersistentVolumeClaim`/`GetPersistentVolume`; new RBAC (`get` on `persistentvolumeclaims`/`persistentvolumes`) on `kairon-node`'s ClusterRole in both the Helm chart and raw `deploy/rbac.yaml`. The `Machine` CRD's `spec.image` field is no longer schema-required (`deploy/crd.yaml`, `charts/kairon/crds/machines.yaml`) — a Machine can now be created with only `spec.volumes`, validated at the Go level instead (`spec.image.path or spec.volumes[0] is required`).

## Real verification (beyond source-level gates)

Deployed the updated `kairon-node` binary to the same real lab host (`212.8.248.187`, a real k3s cluster) used throughout this project's other real-deployment verification, and ran the feature against **actual dynamic provisioning**, not a hand-crafted PV:

1. Created a real `PersistentVolumeClaim` against the cluster's real default `local-path` StorageClass (Rancher's `local-path-provisioner`, already installed). Its `VolumeBindingMode: WaitForFirstConsumer` meant the PVC stayed `Pending` until a consumer was scheduled — used a disposable trigger Pod (whose own image pull failed, irrelevant -- scheduling alone was enough) to force provisioning, then deleted it.
2. **Real, unscripted discovery**: the provisioned `PersistentVolume` used `spec.local.path`, not `spec.hostPath.path` — confirming the decision to support both sources (not just `hostPath`) in `hostDirForPV` was correct, not a hypothetical.
3. Copied a real qcow2 base image to `<PV path>/disk.img`, applied the updated RBAC and CRD (schema-required `image` removed) to the live cluster, and created a `Machine` with **only `spec.volumes`, no `spec.image.path` at all**.
4. The Machine reached `status.phase: Running` with a real `runtimeID`. Queried FluxVM's own API directly for that VM and confirmed its create-request `image` field was exactly `/data/k3s/storage/pvc-<uid>_default_kairon-storage-test-pvc/disk.img` — the literal PVC-resolved path, not a coincidence — and `status: running`.
5. Cleaned up (`kubectl delete machine`, `kubectl delete pvc`) and confirmed the PV was reclaimed (`Delete` policy) shortly after.

## Coverage

```text
total (internal/...): 68.1% (threshold 50%)
```

# Unreleased: login lockout, console audit/TLS, deploy-remote.sh console support test report

## Result

**PASS.** `make all` green (coverage 68.1%, threshold 50%). `helm lint` green plus `helm template --set console.enabled=true --set console.tls.enabled=true --set console.tls.secretName=my-console-tls` confirming the new TLS Secret/volume/env wiring on both the kairon-node DaemonSet and the kairon-ui Deployment, and confirming zero console-TLS output when `console.tls.enabled` is left at its default `false`. `npm run typecheck && build` green. `bash -n scripts/deploy-remote.sh` clean.

## What shipped

This closes out the five-item gap list from the previous VNC console report, minus two items deliberately scoped out as bigger, separate architecture decisions (self-service password reset — would require `kairon-ui` to gain Kubernetes Secret-write RBAC it doesn't have; OIDC/SSO — already a documented exclusion):

- Login rate limiting/lockout: 5 failed attempts against one username lock it out for 5 minutes (`429` with `Retry-After`), tracked per raw requested username (including unknown ones) so the lockout itself can't be used to enumerate valid accounts. A success clears the counter.
- Console tickets are now bound to the authenticated username at issuance (`issueConsoleTicket`/`consumeConsoleTicket`), and every console session logs a `uiapi console opened`/`uiapi console closed` audit line (username, namespace, name, remote address, duration) — previously only ticket *issuance* was logged, with no record of who actually used a console or for how long.
- The dashboard's "Console" button now hides itself when `console.enabled` is off or the Machine isn't `Running` on a `qemu`/unset/`auto` backend, via a new `GET /api/v1/config` endpoint, instead of only failing with an error after the click.
- One-way TLS on the kairon-ui↔kairon-node console relay hop (`console.tls.enabled`/`console.tls.secretName`, a pre-created Secret with `tls.crt`/`tls.key`/`ca.crt`) — opt-in, mirroring `migration.dataplaneTls`; not mutual TLS, since the existing shared bearer token already authenticates kairon-ui to kairon-node.
- `scripts/deploy-remote.sh --with-console` (`--console-port` to override the auto-picked port): generates and wires `KAIRON_NODE_CONSOLE_TOKEN`/`KAIRON_NODE_CONSOLE_PORT` through both systemd env files, passes `--console-addr` to `kairon-node`, validates `--with-console` requires `--with-ui`, and reports console listener health in `run_status()`/the post-deploy summary.

## A real bug found and fixed along the way

`withAudit` only set up the request-context username holder when `s.Log != nil` — conflating "should this request be logged" with "should downstream handlers see the authenticated username." This meant `handleConsoleTicket` would silently bind tickets to an empty username whenever logging was disabled. Found via `TestHandleConsoleTicketBindsToTheAuthenticatedUsername` failing with `got ""` instead of the expected username; fixed by always attaching the holder in `withAudit` and only skipping the logging work (not holder setup) for GET requests or a nil `Log`.

## Real verification (beyond source-level gates)

- Two new TLS-interop tests (`TestHandleConsoleFullRelayOverTLS`, `TestHandleConsoleTLSRejectsUntrustedCert`) use `httptest.NewUnstartedServer().StartTLS()` for a real, interoperable self-signed certificate — confirming genuine TLS negotiation succeeds with the right CA and fails (`tls: bad certificate`) against an untrusted one, not just that a `tls.Config` field got set.
- **Deployed to the same real lab host used for the original VNC console verification** and driven against a freshly created real QEMU VM end to end: issued a real console ticket through the deployed `kairon-ui`, opened the WebSocket relay through the deployed `kairon-node`, and received the genuine `RFB 003.008\n` protocol banner from the VM's actual QEMU VNC socket — proof the ticket-binding, relay, and (TLS-disabled, this host's configuration) plaintext hop all work together against real infrastructure, not just in-process fakes. The `uiapi console opened`/`uiapi console closed` audit lines were confirmed written for that session.
- **Real login rate-limiter verified in production**: 5 wrong-password attempts against a real account returned `429` with `Retry-After: 5m0s`, and a 6th attempt with the *correct* password still returned `429` — confirming the lockout blocks correct credentials too, not just wrong ones, for the full window.
- **A real port-collision auto-recovery, observed live**: on this shared host, `--console-port`'s default `8090` was already in use (an unrelated Docker container) and the node's own health port also collided (a restart-race self-collision); `deploy-remote.sh`'s `resolve_port()` correctly fell back to a free port (`31315`) and the script's own post-deploy check correctly flagged that the pre-existing env files hadn't picked up the new token/port automatically, requiring (and receiving) a manual `KAIRON_NODE_CONSOLE_PORT` reconciliation — exactly the operator workflow the script's warning describes.
- A real `permission denied` dialing a freshly created VM's `vnc.sock` was hit again on this pass (same root-owned-socket/unprivileged-`kairon`-user prerequisite documented in the previous report) and resolved the same documented way (`setfacl -m u:kairon:rw <path>/vnc.sock`), reconfirming the documented prerequisite still holds rather than having been silently fixed elsewhere.

## Coverage

```text
total (internal/...): 68.1% (threshold 50%)
```

# Unreleased: VNC console + login redesign test report

## Result

**PASS.** `make all` green (coverage 67.4%, up from 66.1%, with the new `internal/consoleproxy` package and `internal/uiapi/console.go` tests). `helm lint` green plus `helm template --set console.enabled=true` confirming the new Secret/env/port wiring, and confirming zero console-related output when `console.enabled` is left at its default `false`. `npm run typecheck && test && build` green with the new `@novnc/novnc` dependency.

## What shipped

- Graphical VNC console (`console.enabled`, off by default): `browser (noVNC) -> kairon-ui -> a new kairon-node listener (internal/consoleproxy) -> the VM's local QEMU VNC socket`. New Go dependency `github.com/coder/websocket` (stdlib has no WebSocket support); new frontend dependency `@novnc/novnc`. Gated by kairon-ui's existing operator auth, a single-use ~30s connection ticket (a browser WebSocket can't carry an Authorization header), and a shared bearer token between kairon-ui and kairon-node (Helm-generated, same `lookup`+`randAlphaNum` pattern as `kairon-ui-session`).
- Login screen visual redesign: a two-column split layout (brand/tagline/animated-gradient-orb panel + card), a per-step CSS transition, a first-letter avatar on the password step, a loading spinner, and a proper error banner. No backend changes.

## Real verification (beyond source-level gates)

- **A real RFB protocol test, not just byte-echoing**: the Go-level integration test (`internal/uiapi/console_test.go`'s `TestHandleConsoleFullRelay`) proves bytes survive the full double-hop relay, but to verify actual noVNC/browser compatibility, a minimal-but-genuine RFB 3.8 server was hand-written (ProtocolVersion handshake, Security(None), ServerInit, and a real `FramebufferUpdate` response with a generated pixel pattern) and wired up behind a real kairon-node + kairon-ui + fake-Kubernetes-API stack. Opening the Console button in a real Chrome browser against this stack rendered the actual generated gradient pattern in the noVNC canvas with a "Connected" status -- proof the whole chain (ticket issuance, WS-to-WS relay, RFB framing) is genuinely compatible with a real VNC client, not just internally consistent.
- Live-browser-verified the login redesign at the same time: the two-column layout, the animated orb, the avatar (first letter of the typed username), and the step transition all render correctly against a freshly built `web/dist`.
- `helm template` diffed with and without `console.enabled` to confirm the feature is entirely absent (zero rendered lines matching "console", case-insensitive) when left at its default off state.
- **Deployed to a real lab host and driven against a real QEMU VM** (not a fake RFB server): real `kairon-node`/`kairon-ui` binaries, a real Machine created via `kubectl apply`, its console opened from a real deployed dashboard in Chrome -- rendered the VM's actual boot console (`Ubuntu 24.04.4 LTS ubuntu tty1`, a live `login:` prompt) and accepted real keystrokes end-to-end. Two real bugs found and fixed this way, neither reachable from in-process tests:
  1. **A console-listener bind failure took down the entire node agent**, not just the optional console feature -- `configureConsole`'s error path called the same `cancel()` used to fail the whole process closed, so a mundane port collision (a Docker container already using the default `:8090` on this shared host) silently stopped all Machine reconciliation, not just VNC. Fixed by switching to an explicit `net.Listen` (checked synchronously, logged and skipped on failure) instead of letting `http.Server.ListenAndServe()`'s failure cascade into `cancel()`.
  2. **FluxVM's `vnc.sock` is root-owned with no `other` write bit**, and `kairon-node` runs as a deliberately unprivileged, capability-less user -- the dial failed with a real, correctly-surfaced `502 permission denied` rather than a silent hang, but revealed an undocumented real-world prerequisite (the node's OS user needs `rw` on FluxVM's per-VM sockets) now written up in SECURITY.md and getting-started.md rather than left for the next operator to rediscover.

## Coverage

```text
total (internal/...): 67.4% (threshold 50%)
```

# Unreleased: VM day-2 ops + kairon-ui login test report

## Result

**PASS.** `make all` (fmt/vet/lint/test-race/cover-check/build/validate/smoke) green on Go 1.27.1 -- coverage rose to 66.1% (from 64.4%) with the new `internal/uiapi/auth.go` and `cmd/kairon-ui` tests. `npm --prefix web run typecheck && test && build` green. `helm lint` green plus three `helm template` scenarios (no `ui.*` values / `ui.auth.users` configured / legacy `ui.token`) each asserting the correct Secret/env wiring. CI green on GitHub for every commit in this cycle, including a real CI failure this cycle caught and fixed (see below).

## What shipped

- `spec.cloudInit` (`hostname`/`user`/`sshAuthorizedKeys`/`packages`/`runCmd`) and `spec.network.forwards` ergonomics via `kaironctl create` flags and the dashboard create form -- both mechanisms already worked end-to-end in FluxVM, they just had no way to set them short of hand-writing a Machine manifest.
- `kairon-ui` real per-operator username/password login: bcrypt accounts (`ui.auth.users`), a two-step "Apple ID style" sign-in screen (with a brand header, a one-line project tagline, and a "Connecting to `<host>`" indicator), signed 12-hour session tokens, working logout, per-operator audit attribution, and a Helm-generated default `admin` account (random password, generated once, persisted across upgrades) when nothing else is configured -- no hardcoded credentials anywhere. The legacy shared `ui.token` keeps working unchanged.

## Real verification (beyond source-level gates)

- **Live browser testing, three times over** (once per iteration of the login feature: initial implementation, the CI-driven behavior fix, and the visual redesign) against `kairon-ui` binaries built fresh from the exact commit being verified, not a stale build: real two-step login with a configured user, the Helm-simulated default-admin path, the legacy raw-token fallback, a deliberately wrong password (confirmed identical error text/timing path to an unknown username), sign-out actually invalidating the session (confirmed via a follow-up 401), and session persistence across a page reload.
- **A real CI failure, caught and fixed in this cycle**: pushing the login feature broke `ci.yml`'s existing "Helm render -- kairon-ui" step, which asserted `helm template` must *fail* without `ui.token` -- exactly the old behavior this feature intentionally replaced with safe default-admin seeding. Fixed by replacing that assertion with three scenario checks (default seeding / configured users / legacy token), verified locally against the real `helm template` output before re-pushing, then confirmed green on GitHub.
- `go build`/`bcrypt.CompareHashAndPassword` round-tripped through the real `kairon-ui -hash-password` CLI mode, not just unit-tested in isolation.

## Coverage

```text
total (internal/...): 66.1% (threshold 50%)
```

# Kairon v0.4.0 test report

## Result

**PASS.** `make all` (fmt/vet/lint/test-race/cover-check/build/validate/smoke) green on Go 1.27.1, `npm --prefix web run typecheck && test && build` green, `helm lint`/`helm template` green including the new `kairon-ui` fail-guard and resource assertions, and CI green on GitHub for every commit in this cycle (not just local checks).

## Commands executed

```text
make all
golangci-lint run ./...
npm --prefix web install && npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build
helm lint charts/kairon
helm template kairon charts/kairon --namespace kairon-system --set ui.enabled=true --set ui.token=...
```

## Coverage

```text
total (internal/...): 64.4% (threshold 50%)
```

## Real hardware verification (beyond source-level gates)

Unlike the v0.3.0 report below, this cycle's work was additionally verified against two real lab hosts, not just in-process fakes:

- `kairon-node` + `kairon-controller` deployed via `scripts/deploy-remote.sh` to two hosts, each running its own real single-node k3s cluster (`https://127.0.0.1:6443`), with the real CRDs/RBAC applied and real ServiceAccount tokens -- confirmed `kaironctl get machines` working end-to-end against a live Kubernetes API, not a fake one.
- `kairon-ui` deployed the same way (`--with-ui`), loaded in a real Chrome browser, and driven through Overview/Machines/Migrations pages including the `NeedsRecovery` recovery-form UI, with live `/api/v1/...` responses confirmed correct.
- Found and fixed two real bugs this way that no unit test would have caught: a `NetworkAwareDestination.Commit()`-adjacent reflect-based merge-patch fix that over-zeroed pointer fields (internal/integration test helper, not shipped code, but a real test-infra bug), and `deploy-remote.sh` redeploying to an already-running host silently keeping the *old* process alive (`systemctl enable --now` is a no-op on an active unit) while reporting success and printing a URL nothing was listening on -- fixed by switching to `restart`.

Real two-host **live migration** itself, and the `NeedsRecovery` drill, remain documented as runbooks (`docs/runbook-multi-host-migration-test.md`, `docs/runbook-recovery-drill.md`) but were not executed this cycle -- see those runbooks' own scope notes.

---

# Kairon v0.3.0 test report

## Result

**PASS** for the source-level release gate available in this build environment.

Validated on the final v0.3 source tree with Go 1.23 tooling.

## Commands executed

```text
make all
go mod tidy
go test -cover ./...
go clean -testcache
go test -race ./...
```

`make all` includes:

- `gofmt` cleanliness check
- `go vet ./...`
- `go test ./...`
- static `CGO_ENABLED=0` builds of `kairon-controller`, `kairon-node`, and `kaironctl`
- repository/CRD/raw-manifest validation
- version smoke tests for all three binaries

The local environment does not have the Helm binary. A Go-template-compatible render harness was therefore used to render `charts/kairon/templates/all.yaml` with migration disabled and enabled; both rendered outputs were parsed as Kubernetes YAML and the enabled output was checked for the migration TLS arguments, port, and volumes. GitHub Actions installs real Helm and runs `helm lint` plus both render modes.

## Tests

29 Go tests cover the current controller/runtime contract, including:

- Machine creation and FluxVM lifecycle mapping
- finalizer and adopt-only duplicate-boot protection
- image-root enforcement
- DRA allocation checks, PCI BDF normalization, and fail-closed VFIO allowlisting
- scheduling and controlled cold migration
- CSI `VolumeSnapshot` creation/readiness
- migration peer prepare idempotency, identity conflict detection, and local target-node binding
- mode-0600 atomic session persistence and path-traversal rejection
- TLS 1.3 server policy and mandatory client certificate enforcement
- target-first live migration ordering
- unsupported destination blocking before source transfer
- successful transfer -> target commit -> guarded cutover
- source-start failure -> prepared-target abort
- target-commit failure -> `NeedsRecovery`

## Coverage

```text
internal/agent       60.3%
internal/controller  57.4%
internal/fluxvm      55.3%
internal/health      42.1%
internal/kube        26.7%
internal/migration   53.9%
internal/model       45.5%
internal/scheduler   77.1%
```

Command packages are exercised through build/version smoke tests and currently report 0% statement coverage in `go test -cover` because they have no direct unit-test files.

## Important boundary

This environment does not execute KVM/QEMU, a real Kubernetes cluster, a real CSI driver, real DRA hardware, or a FluxVM live-migration backend. The current FluxVM repository does not expose a verified migration/QMP API, so v0.3 intentionally removes the guessed v0.2 FluxVM migration endpoints.

Kairon's target preparation, mTLS peer protocol, durable session state, rollback/fencing state machine, and migration-adapter contract are implemented and tested. **Actual VM memory-state live transfer requires a compatible Kairon migration adapter.** Without one, an explicit live migration is safely blocked before the source transfer begins. Cold migration remains implemented end-to-end at the controller/agent API-contract level.
