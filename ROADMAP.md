# Roadmap

## v0.3 — secure migration control plane

- mTLS node-to-node prepare/commit/abort protocol
- atomic destination session journal and idempotency
- no user-supplied migration transport URI
- local Unix-socket migration adapter contract
- rollback before commit and `NeedsRecovery` after ambiguous commit
- adopt-only target cutover
- cold migration, CSI snapshots, DRA/VFIO guard retained

## Network Fabric — FluxVM eBPF edge (in progress)

Kairon declares VM-edge networking; FluxVM owns TAP/TC/eBPF; Fabric proxies dataplane UX.
See [`docs/network-fabric.md`](docs/network-fabric.md).

- **N1** Machine network create parity (`forwards`, `macvtapMode`, `staticNetwork`, `podUID`) + dataplane status projection
- **N2** `MachineNetworkPolicy` + `NetworkSecurityGroup` → FluxVM policy/groups/CNP
- **N3** Live-migration network quiesce / export / restore / resume
- **N4** Service Fabric VIP membership + `dataplaneRequired` fail-closed readiness

## v0.4 — FluxVM live-migration backend

- [x] implement the Kairon migration adapter in FluxVM or a companion host service (`cmd/kairon-migration-adapter-fluxvm`)
- [x] QEMU incoming destination lifecycle and QMP transfer implementation
- [x] authenticated/encrypted data-plane transport (`migration.dataplaneTls`, `status.dataPlaneEncrypted`)
- [x] cancellation and operator recovery commands (`kaironctl recover`, `docs/runbook-migration-failures.md`)
- [x] shared-storage and block-migration preflight (first cut, opt-in `kairon.zyvor.dev/storage-domain`/`network-domain` node labels — see `docs/guides/machine-fencing.md`)
- [x] migration network selection and bandwidth policy

## Operational tooling (shipped ahead of schedule)

Not originally scoped for a specific version, but small enough to land alongside the v0.4 work above:

- Prometheus metrics + example alert rules (`internal/metrics`, `charts/kairon/alerts.yaml`)
- `kairon-ui` web dashboard (`cmd/kairon-ui`, `internal/uiapi`, `web/`)
- `kairon-ui` real per-operator username/password login (bcrypt accounts, signed sessions, audit attribution, Helm-generated default admin, rate-limited/lockout on repeated failed attempts) — closes what was the dashboard's longest-standing known auth gap; see SECURITY.md
- `spec.cloudInit` and `spec.network.forwards` ergonomics (`kaironctl create --hostname/--ssh-key/--forward/...`, matching dashboard fields) — see `docs/guides/machine-network.md`
- Graphical VNC console (`console.enabled`): `kairon-ui` → `kairon-node` → the VM's local QEMU VNC socket, rendered in-browser via noVNC — FluxVM exposes no remote VNC endpoint of its own, so this is a real relay Kairon built, not a wrapper. Tickets are bound to the requesting username with a full open/close audit trail, the button hides itself when ineligible, the kairon-ui↔kairon-node hop optionally runs over one-way TLS (`console.tls.enabled`), and `scripts/deploy-remote.sh --with-console` covers the bare-metal install path — see SECURITY.md's "VNC console" section
- `internal/integration`: a CI-runnable controller+agent pipeline test
- multi-host migration test and `NeedsRecovery` drill runbooks (`docs/runbook-multi-host-migration-test.md`, `docs/runbook-recovery-drill.md`) — documented and scripted, not yet run against real hardware in this repo's own CI

## v0.5+

- [x] PVC -> FluxVM disk lifecycle (first cut): `spec.volumes[0]` boots from a Bound PVC's `hostPath`/`local` PersistentVolume — see `docs/guides/machine-storage.md`. Still open: multiple volumes per Machine.
- [x] CSI node-plugin integration for network-block volumes (first cut, one backend): `csiNode.enabled` deploys Kairon's own iSCSI-only CSI node plugin (`internal/csinode`, no Controller service, no dynamic provisioning -- PVs are static). The second deliberate exception to Go-stdlib-only (`google.golang.org/grpc`, `github.com/container-storage-interface/spec`) -- the CSI protocol itself has no stdlib-only wire format. `kairon-node` dials it directly as its own CSI client rather than through kubelet's Pod volume machinery, since a Machine has no backing Pod. No Ceph/EBS/other backends, no CHAP on Kairon's own consumption path, no volume expansion. Runs privileged, on a non-distroless base image. See `docs/guides/machine-storage-csi.md`.
- [x] `MachineSnapshotRestore` (first cut): restores one `MachineSnapshot` volume into a new PVC via the standard CSI `dataSource` flow — "restore" and "clone-from-snapshot" are the same operation in Kubernetes' storage model, so this covers both. Deliberately doesn't also create a Machine (point the existing PVC-backed-disk feature at the result instead) — see `docs/guides/machine-snapshot-restore.md`. Needs a real CSI snapshotter behind your StorageClass (confirmed on a real cluster: Rancher's `local-path-provisioner` doesn't have one -- "snapshotting non-CSI volumes is not supported").
- [x] CPU/memory hotplug (first cut): growing `spec.resources` on a Running Machine calls FluxVM's real QMP hotplug (`device_add`/`object-add`, shipped upstream in FluxVM's own `feat/qemu-cpu-memory-hotplug`, PR #62) — see `docs/guides/machine-hotplug.md`. Grow-only (no CPU/DIMM unplug), bounded by headroom reserved automatically at creation (not yet Kairon-configurable), lost across any Machine re-realization (stop/start, cold migration).
- [x] guest-agent (qemu-guest-agent) integration: `spec.guestAgent.enabled` resolves `status.guestIP` via FluxVM's real `guest-network-get-interfaces` (shipped upstream, [zyvorai/fluxvm#63](https://github.com/zyvorai/fluxvm/pull/63)) for network modes with no DHCP lease file (`user`/SLIRP in particular) — see `docs/guides/machine-guest-agent.md`. Graceful-shutdown wiring turned out to be unnecessary: FluxVM's `stop`/`delete` already does ACPI `system_powerdown` with a wait-then-force fallback regardless of guest agent presence. Multi-NIC/IPv6 (`status.guestIPs`) and periodic re-resolution (every 5 minutes once first resolved, instead of once-ever) closed the two gaps this used to list as still open.
- [x] Machine affinity/anti-affinity: `spec.placement.affinity`/`antiAffinity` (required/hard) gate scheduling against other Machines' current placement. Since v0.5, `internal/scheduler` also does real weighted scoring on top of the existing least-loaded/hash-tiebreak baseline: `preferredAffinity`/`preferredAntiAffinity` (soft, weighted), `topologySpreadConstraints` (soft, minimizes matching-Machine count per topology domain -- `maxSkew` accepted but not yet hard-enforced), and a best-effort DRA topology-awareness hint (reads back an already-allocated `ResourceClaim`'s device pool via `ResourceSlice.spec.nodeName`, no role in allocation itself). A Machine using none of the new fields schedules identically to before this scoring pass existed. See `docs/guides/machine-placement.md`.
- [x] `MachineDisruptionBudget`: `evacuate` throttles itself against `minAvailable`/`maxUnavailable` instead of migrating an entire node at once — see `docs/guides/machine-disruption-budgets.md`. Since v0.5, an opt-in validating admission webhook (`webhook.enabled`) also rejects a `MachineMigration` created directly through the API if it would violate a budget — closing the "only gates `kaironctl evacuate`" gap this line used to describe. Still no automatic node-drain/eviction path.
- [x] `MachineQuota` (namespace-scoped): `maxMachines`/`maxTotalCpu`/`maxTotalMemory`, enforced in `kairon-controller`'s scheduling loop — see `docs/guides/machine-quotas.md`. Since v0.5, the same opt-in webhook above also rejects a `Machine` create that would immediately exceed a quota (CREATE only, matching the reconcile loop's own scope), instead of only leaving it `Pending`. `migration.maxConcurrentPerNode`/`maxConcurrentCluster` (a per-node/cluster concurrency cap on in-flight migrations specifically) remains a separate, narrower control this doesn't replace.
- [x] `kairon-ui` in-place password management: self-service change + admin reset (`POST /api/v1/auth/password`, `POST /api/v1/users/{username}/password`), replacing "regenerate a bcrypt hash and redeploy" for named `ui.auth.users` accounts.
- [x] `scripts/deploy-remote.sh --console-tls`: automates (or accepts bring-your-own) TLS material for the kairon-ui↔kairon-node console relay hop, previously a manual step.
- [x] Helm chart hardening: `PodDisruptionBudget` (on by default) and opt-in `NetworkPolicy` for `kairon-controller`/`kairon-ui`.
- [x] Supply chain: Dockerfile base images pinned to a digest, Trivy image scanning in CI, and a tag-triggered `release.yml` that publishes scanned, digest-pinned images to `ghcr.io/zyvorai/kairon-*` — this repo's CI is now the source of those tags.
- SR-IOV/GPU migration capability checks
- guest quiesce hooks for snapshots
- [x] TLS certificate hot-reload (`internal/tlsreload`): migration mTLS, the VNC console relay, and the admission webhook all now pick up a renewed leaf certificate/key (e.g. from cert-manager) within 30s, no restart. Only the leaf cert/key -- a CA bundle still loads once at startup. Automated issuance (ACME/cert-manager integration Kairon itself drives) and SPIFFE-style workload identity remain open.
- [x] node fencing (first cut): `kairon-controller` sets a `NodeUnreachable` condition on any Machine whose node stops being `Ready`, every reconcile tick -- detection only, never automatic rescheduling (risks running the same VM twice). `kaironctl fence --reason ...` is the explicit operator-attested action that clears a Machine for rescheduling once a human has confirmed out-of-band the node is truly gone -- see `docs/guides/machine-fencing.md`.
- confidential VM policy (SEV-SNP/TDX)
- [x] OIDC/SSO for `kairon-ui` (first cut): `ui.oidc.enabled`, Authorization Code + PKCE against an external identity provider, alongside `ui.auth.users`. The one deliberate exception to Kairon's Go-stdlib-only design (`golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3`) -- real JWT/JWK verification isn't something to hand-roll. No group-to-admin claim mapping, no SP-initiated single-logout, no refresh-token renewal. See `docs/guides/kairon-ui-oidc.md`.
- [x] multi-replica `kairon-ui` (first cut): `ui.replicaCount > 1` propagates session revocation, login lockout, console tickets, and password changes across replicas via a shared, Kubernetes-native `ConfigMap` (deliberately not Redis) -- eventually-consistent (~15s), not instant; login-lockout's failure count is per-replica, not one cluster-wide atomic counter; concurrent password changes to two different accounts on two different replicas can still race. See `docs/guides/kairon-ui-ha.md`.
- CRD version-upgrade story beyond today's single `v1alpha1` (no conversion webhook exists)
- upgrade/scale/failure-injection test suites
