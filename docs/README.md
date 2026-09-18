---
sidebar_position: 1
title: Overview
---

# Kairon documentation

The root [README](https://github.com/zyvorai/kairon/blob/main/README.md) is the product landing page. **This `docs/` tree is authoritative** for install, runbooks, guides, and status detail.

## Start here

| I want to… | Read this |
|---|---|
| Install and run a first Machine | [getting-started.md](getting-started.md) |
| Ten-minute architecture tour | [../ARCHITECTURE.md](https://github.com/zyvorai/kairon/blob/main/ARCHITECTURE.md) |
| Deep architecture reference | [architecture.md](architecture.md) |
| Full feature inventory | [WHAT_SHIPS.md](WHAT_SHIPS.md) |
| CLI reference | [CLI.md](CLI.md) |
| Dependency policy | [DEPENDENCIES.md](DEPENDENCIES.md) |
| Release status & production gaps | [STATUS.md](STATUS.md) |
| Migration failures / NeedsRecovery | [runbook-migration-failures.md](runbook-migration-failures.md) |
| Security / threat model | [../SECURITY.md](https://github.com/zyvorai/kairon/blob/main/SECURITY.md) |
| Contributing | [../CONTRIBUTING.md](https://github.com/zyvorai/kairon/blob/main/CONTRIBUTING.md) |

## Documentation map

| Doc | Covers |
|---|---|
| [`ARCHITECTURE.md`](https://github.com/zyvorai/kairon/blob/main/ARCHITECTURE.md) | The ten-minute tour: components, request flow, trust boundaries/deployment topology, design rationale |
| [`getting-started.md`](getting-started.md) | Install, first Machine, cold migrate, live migration, the dashboard, Network Fabric |
| [`tutorials/machine-lifecycle.md`](tutorials/machine-lifecycle.md) | Narrated walkthrough: one Machine through create → relocate → snapshot → restore |
| [`architecture.md`](architecture.md) | Deep reference: the cold/live migration state machines, session durability, CSI/DRA mechanics, operational visibility |
| [`migration-adapter.md`](migration-adapter.md) | The migration adapter HTTP contract, trust boundary, the real `kairon-migration-adapter-fluxvm` implementation |
| [`network-fabric.md`](network-fabric.md) | `MachineNetworkPolicy`/`NetworkSecurityGroup` reference and the FluxVM eBPF edge |
| [`tutorials/network-fabric.md`](tutorials/network-fabric.md) · [`guides/machine-network.md`](guides/machine-network.md) · [`guides/network-policy.md`](guides/network-policy.md) | Network Fabric walkthrough and field-level guides |
| [`guides/machine-quotas.md`](guides/machine-quotas.md) · [`guides/machine-disruption-budgets.md`](guides/machine-disruption-budgets.md) | `MachineQuota`/`MachineDisruptionBudget` reference, including the admission webhook |
| [`runbook-migration-failures.md`](runbook-migration-failures.md) | Diagnosing and resolving `NeedsRecovery`, alert-to-runbook cross-references |
| [`guides/machine-fencing.md`](guides/machine-fencing.md) | `NodeUnreachable`/`Fenced` conditions, `kaironctl fence`'s safety model, storage/network migration preflight labels |
| [`guides/kairon-ui-ha.md`](guides/kairon-ui-ha.md) | Running `ui.replicaCount > 1`: what's shared, how, and its real limits |
| [`guides/kairon-controller-ha.md`](guides/kairon-controller-ha.md) | Running `controller.replicaCount > 1`: Lease-based leader election, RBAC, bare-metal setup |
| [`guides/observability.md`](guides/observability.md) | What each component's `/metrics` exposes, the `kairon-health` alert group, and the renamed `PrometheusRule` |
| [`guides/machine-migration-tls.md`](guides/machine-migration-tls.md) | Control-plane vs. data-plane migration TLS, and real per-node data-plane identity via `migration.dataplaneTlsSecretName` |
| [`guides/kairon-ui-oidc.md`](guides/kairon-ui-oidc.md) | OIDC/SSO setup, the Authorization Code + PKCE flow, and why it breaks Go-stdlib-only |
| [`guides/kairon-ui-console-rbac.md`](guides/kairon-ui-console-rbac.md) | Real Kubernetes RBAC (`machines/console` + `SubjectAccessReview`) for console access, opt-in alongside the annotation allowlist |
| [`guides/machine-storage.md`](guides/machine-storage.md) · [`guides/machine-storage-csi.md`](guides/machine-storage-csi.md) | PVC-backed boot disks; Kairon's own first-cut iSCSI CSI driver and why it breaks Go-stdlib-only |
| [`guides/machine-storage-thirdparty-csi.md`](guides/machine-storage-thirdparty-csi.md) | `node.thirdPartyCSIDrivers`: `kairon-node` as a generic CSI client against an allowlisted third-party driver, first cut scoped to `attachRequired: false`, no-secret drivers |
| [`guides/machine-image-import.md`](guides/machine-image-import.md) | `spec.image.source`: downloading a remote image URL into `kairon-node`'s own digest-keyed cache |
| [`guides/machine-sets.md`](guides/machine-sets.md) | `MachineSet`: replica reconciliation, `RollingUpdate`/`Recreate` rollout strategy |
| [`guides/machine-instance-types.md`](guides/machine-instance-types.md) | `MachineInstanceType`: a reusable named CPU/memory shape resolved into `spec.resources` once |
| [`guides/machine-cpu-numa.md`](guides/machine-cpu-numa.md) | `spec.resources.numaNode`/`.cpuSet`/`.hugepages`: qemu-only NUMA/hugepage passthroughs, and what they don't guarantee (no real host-core pinning) |
| [`guides/machine-cpu-pinning.md`](guides/machine-cpu-pinning.md) | `spec.resources.cpuPinning`: real exclusive host-core allocation, and the operator-asserted `pinnable-cpus` node label it depends on |
| [`guides/machine-windows-guests.md`](guides/machine-windows-guests.md) | What Windows guest support covers today (legacy-BIOS + cloudbase-init, and now UEFI Secure Boot/vTPM for Windows 11 given a node-configured OVMF vars template) |
| [`guides/migration-policies.md`](guides/migration-policies.md) | `MigrationPolicy`: selector-scoped migration bandwidth defaulting and concurrency caps |
| [`guides/machine-snapshot-quiesce.md`](guides/machine-snapshot-quiesce.md) | Real guest `fsfreeze`/`fsthaw` around `MachineSnapshot`, and the controller↔node coordination protocol behind it |
| [`guides/machine-snapshot-restore.md`](guides/machine-snapshot-restore.md) | Restoring a `MachineSnapshot` volume into a new `PersistentVolumeClaim` via the standard CSI `dataSource` flow |
| [`guides/machine-pause-resume.md`](guides/machine-pause-resume.md) · [`guides/machine-halt.md`](guides/machine-halt.md) | `spec.powerState: Paused`/`Halted`: suspending guest CPUs vs. powering off the VMM process while FluxVM keeps its record |
| [`guides/machine-hotplug.md`](guides/machine-hotplug.md) | Growing `spec.resources.cpu`/`.memory` on an already-`Running` Machine via FluxVM's real QMP hotplug |
| [`guides/machine-resource-limits.md`](guides/machine-resource-limits.md) | `spec.resources.limits`: real, kernel-enforced cgroup v2 caps, and `status.resourceUsage` live usage |
| [`guides/machine-placement.md`](guides/machine-placement.md) | Scheduler internals: affinity/anti-affinity, weighted soft scoring, `topologySpreadConstraints`, DRA topology hints |
| [`guides/machine-sriov.md`](guides/machine-sriov.md) | SR-IOV NIC passthrough by reusing the existing GPU/VFIO DRA mechanism — why Multus doesn't apply to Kairon Machines at all |
| [`guides/machine-guest-agent.md`](guides/machine-guest-agent.md) | `spec.guestAgent`: real `qemu-guest-agent`-reported `status.guestIP`, including `user`/SLIRP networking |
| [`guides/machine-guest-exec.md`](guides/machine-guest-exec.md) | Guest exec (`qemu-guest-agent`), and the smaller API-only fsfreeze-status/firewall endpoints alongside it |
| [`guides/machine-guest-agent-files.md`](guides/machine-guest-agent-files.md) | Guest file access and the backend-agnostic vsock-agent guest exec, both over `spec.guestAgent.console` |
| [`guides/machine-text-console.md`](guides/machine-text-console.md) | Interactive in-browser text console over FluxVM's own proprietary vsock guest agent |
| [`guides/machine-logs.md`](guides/machine-logs.md) | Streaming a Machine's real captured serial console output -- Kairon's `kubectl logs`/`-f` equivalent |
| [`guides/machine-vm-state-snapshot.md`](guides/machine-vm-state-snapshot.md) | Full hypervisor-level VM-state checkpoint/restore (RAM, CPU, device state) -- distinct from `MachineSnapshot`'s disk-content-only CSI snapshot |
| [`guides/machine-sandboxes.md`](guides/machine-sandboxes.md) | `spec.sandbox`: FluxVM's lightweight agent-sandbox track, templates, the HTTP proxy relay, warm pools, and the egress check |
| [`guides/machine-image-catalog.md`](guides/machine-image-catalog.md) | `spec.image.catalogName`: FluxVM's node-local, checksummed image catalog and its admin API |
| [`guides/machine-diagnostics.md`](guides/machine-diagnostics.md) | Runtime capabilities, PSI pressure, effective CPU set, and cgroup-level freeze/thaw |
| [`guides/crd-versioning.md`](guides/crd-versioning.md) | What a real CRD version bump (`v1beta1`) still requires; the conversion webhook scaffold that exists today |
| [`runbook-multi-host-migration-test.md`](runbook-multi-host-migration-test.md) · [`runbook-recovery-drill.md`](runbook-recovery-drill.md) | Real two-host live-migration testing; deliberately drilling a `NeedsRecovery` recovery |
| [`runbook-backup-restore.md`](runbook-backup-restore.md) | Backing up/restoring Kairon's CRD state (`scripts/backup-crds.sh`/`restore-crds.sh`), and what it doesn't cover (VM disk content, FluxVM host state) |
| [`runbook-velero-backup.md`](runbook-velero-backup.md) | Using generic Velero (no Kairon-specific plugin) instead — what works out of the box, and the one real gap (no disk-content snapshot without a real CSI storage backend) |
| [`runbook-vm-export.md`](runbook-vm-export.md) | Getting a Machine's disk content out of the cluster entirely, with standard Kubernetes primitives (no new Kairon-specific export tooling) |
| [`design-cluster-api-provider.md`](design-cluster-api-provider.md) | Scoped, not built: what a real Cluster API infrastructure provider would take, and the real `ownerReferences` gap that blocks it today |
| [`ROADMAP.md`](https://github.com/zyvorai/kairon/blob/main/ROADMAP.md) · [`RELEASE_NOTES.md`](https://github.com/zyvorai/kairon/blob/main/RELEASE_NOTES.md) | What shipped per version, what's next; per-release changelog |
| [`SECURITY.md`](https://github.com/zyvorai/kairon/blob/main/SECURITY.md) | Threat model, vulnerability reporting |
| [`CONTRIBUTING.md`](https://github.com/zyvorai/kairon/blob/main/CONTRIBUTING.md) | PR checklist, coverage floor, frontend checks |

---

### Also in this tree

| Doc | Covers |
|---|---|
| [`guides/relocating-a-machine.md`](guides/relocating-a-machine.md) | Cold/live migrate, evacuate, enable mTLS, cancel/delete/quiesce behavior |
| [`guides/admission-webhook.md`](guides/admission-webhook.md) | Enabling the ValidatingWebhook for quotas/budgets (README “Guarding the fleet”) |
| [`WHAT_SHIPS.md`](WHAT_SHIPS.md) | Full feature inventory (was README “What ships today”) |
| [`CLI.md`](CLI.md) | Full `kaironctl` / `kubectl kairon` command reference (Cobra, embedded Helm, Krew) |
| [`DEPENDENCIES.md`](DEPENDENCIES.md) | Stdlib-only controller/node vs named exceptions (CLI Helm, OIDC, CSI) |
| [`STATUS.md`](STATUS.md) | v0.5.0 status + production gaps |
