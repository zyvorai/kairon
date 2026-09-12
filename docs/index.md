---
hero:
  eyebrow: KUBERNETES-NATIVE VMS
  title: Kairon
  lead: >-
    Kubernetes-native VMs — without KubeVirt, without libvirt, without a
    virt-launcher Pod. Kubernetes declares. Kairon orchestrates. FluxVM
    executes.
  swatches:
    - {label: "v0.4.0"}
    - {label: "Go stdlib only"}
    - {label: "Apache-2.0"}
  highlights:
    - {value: "v0.4.0", label: "Current release — real FluxVM migration adapter, PVC-backed boot disks, hotplug", footnote: "1"}
    - {value: "0", label: "Non-stdlib runtime dependencies in kairon-controller / kairon-node", footnote: "2"}
    - {value: "9", label: "Machine lifecycle guides covering placement, quotas, storage, and more", footnote: "3"}
    - {value: "6", label: "States in the live-migration state machine, from Pending to Succeeded", footnote: "4"}
    - {value: "3", label: "Operator runbooks for recovery, multi-host testing, and drills", footnote: "5"}
  hub_bands:
    - {icon: "▶", title: "Getting started", description: "Install, run a first Machine, cold migrate, enable live migration, deploy the dashboard, and set up Network Fabric.", href: "getting-started.md"}
    - {icon: "◎", title: "Architecture", description: "Kubernetes stays the source of truth; FluxVM owns VM execution; Kairon owns placement, relocation policy, and lifecycle semantics.", href: "architecture.md"}
    - {icon: "◈", title: "Network Fabric", description: "MachineNetworkPolicy / NetworkSecurityGroup reference and the FluxVM eBPF edge.", href: "network-fabric.md"}
    - {icon: "⇄", title: "Migration adapter", description: "The migration adapter HTTP contract and the real kairon-migration-adapter-fluxvm implementation.", href: "migration-adapter.md"}
    - {icon: "⚑", title: "Migration failures runbook", description: "Diagnosing and resolving NeedsRecovery, with alert-to-runbook cross-references.", href: "runbook-migration-failures.md"}
footnotes:
  - {marker: "1", text: "v0.4.0 ships a real FluxVM migration adapter (cmd/kairon-migration-adapter-fluxvm), PVC-backed boot disks, CPU/memory hotplug, and a guest agent — with open gaps disclosed in the README's Status section.", href: "https://github.com/zyvorai/kairon#status", href_label: "See the README's Status section."}
  - {marker: "2", text: "Runtime code in kairon-controller and kairon-node is Go standard library only — no client-go, no generated deep call stacks, no vendored operator framework.", href: "https://github.com/zyvorai/kairon#why-kairon", href_label: "See the README's Why Kairon section."}
  - {marker: "3", text: "docs/guides/ covers disruption budgets, guest agent, hotplug, network, placement, quotas, snapshot restore, storage, and network policy.", href: "https://github.com/zyvorai/kairon/tree/main/docs/guides", href_label: "See docs/guides/."}
  - {marker: "4", text: "Pending → Starting → Running → Cutover → Adopting → Succeeded, per the Live migration state diagram.", href: "architecture.md", href_label: "See Architecture — Live migration."}
  - {marker: "5", text: "runbook-migration-failures.md, runbook-multi-host-migration-test.md, and runbook-recovery-drill.md.", href: "runbook-migration-failures.md", href_label: "See the migration failures runbook."}
---

Kairon is Zyvor's Apache-2.0 control plane for running virtual machines on
Kubernetes through [FluxVM](https://github.com/zyvorai/fluxvm). A `Machine`
is desired state in the API. A small cluster controller places it. A
node-local agent turns that into FluxVM — QEMU, Cloud Hypervisor,
Firecracker, or the FluxVM hypervisor — on real KVM.

No per-VM wrapper Pod. No libvirt. No guessed hypervisor migration
endpoints in the Kubernetes API.

## What you get in v0.4

<div class="icon-badge-list" markdown="1">

- 🖥️ Machine CRD — CPU, memory, image, network, power, volumes, DRA device claims
- 💾 PVC-backed boot disk
- 📍 Least-loaded placement with required affinity/anti-affinity
- 🛡️ MachineDisruptionBudget
- 📊 MachineQuota
- 🌐 Network Fabric (FluxVM eBPF edge)
- 📸 CSI VolumeSnapshot / MachineSnapshotRestore
- 🔌 CPU/memory hotplug (no reboot)

</div>

Source code, releases, and issue tracking live in the
[zyvorai/kairon](https://github.com/zyvorai/kairon) GitHub repository.
