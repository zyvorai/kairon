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
- [ ] shared-storage and block-migration preflight
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

- [x] PVC -> FluxVM disk lifecycle (first cut): `spec.volumes[0]` boots from a Bound PVC's `hostPath`/`local` PersistentVolume — see `docs/guides/machine-storage.md`. Still open: network-block CSI volumes (no CSI node-plugin integration), multiple volumes per Machine.
- [x] `MachineSnapshotRestore` (first cut): restores one `MachineSnapshot` volume into a new PVC via the standard CSI `dataSource` flow — "restore" and "clone-from-snapshot" are the same operation in Kubernetes' storage model, so this covers both. Deliberately doesn't also create a Machine (point the existing PVC-backed-disk feature at the result instead) — see `docs/guides/machine-snapshot-restore.md`. Needs a real CSI snapshotter behind your StorageClass (confirmed on a real cluster: Rancher's `local-path-provisioner` doesn't have one -- "snapshotting non-CSI volumes is not supported").
- [x] CPU/memory hotplug (first cut): growing `spec.resources` on a Running Machine calls FluxVM's real QMP hotplug (`device_add`/`object-add`, shipped upstream in FluxVM's own `feat/qemu-cpu-memory-hotplug`, PR #62) — see `docs/guides/machine-hotplug.md`. Grow-only (no CPU/DIMM unplug), bounded by headroom reserved automatically at creation (not yet Kairon-configurable), lost across any Machine re-realization (stop/start, cold migration).
- [x] guest-agent (qemu-guest-agent) integration (first cut): `spec.guestAgent.enabled` resolves `status.guestIP` via FluxVM's real `guest-network-get-interfaces` (shipped upstream, [zyvorai/fluxvm#63](https://github.com/zyvorai/fluxvm/pull/63)) for network modes with no DHCP lease file (`user`/SLIRP in particular) — see `docs/guides/machine-guest-agent.md`. Graceful-shutdown wiring turned out to be unnecessary: FluxVM's `stop`/`delete` already does ACPI `system_powerdown` with a wait-then-force fallback regardless of guest agent presence.
- [x] Machine affinity/anti-affinity (first cut, required constraints only): `spec.placement.affinity`/`antiAffinity` gate scheduling against other Machines' current placement — see `docs/guides/machine-placement.md`. Still open: preferred/soft affinity and topology spread constraints (both need a weighted scoring system the scheduler doesn't have yet).
- [x] `MachineDisruptionBudget` (first cut, `kaironctl`-side only): `evacuate` throttles itself against `minAvailable`/`maxUnavailable` instead of migrating an entire node at once — see `docs/guides/machine-disruption-budgets.md`. Still open: no automatic node-drain/eviction path exists at all yet (this only gates `kaironctl evacuate`, not a `MachineMigration` created any other way).
- [x] `MachineQuota` (first cut, namespace-scoped): `maxMachines`/`maxTotalCpu`/`maxTotalMemory`, enforced in `kairon-controller`'s scheduling loop (no admission webhook) — see `docs/guides/machine-quotas.md`. `migration.maxConcurrentPerNode`/`maxConcurrentCluster` (a per-node/cluster concurrency cap on in-flight migrations specifically) remains a separate, narrower control this doesn't replace.
- DRA topology-aware scheduler scoring
- SR-IOV/GPU migration capability checks
- guest quiesce hooks for snapshots
- per-node certificate automation/rotation and SPIFFE support
- confidential VM policy (SEV-SNP/TDX)
- upgrade/scale/failure-injection test suites
