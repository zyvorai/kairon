# Compatibility matrix

Short, dated evidence for Kairon’s differentiating features on real
hardware. Historical narrative belongs in runbooks; this file is the
pass/fail surface operators and releases should cite.

**Do not claim production live migration in README until the live row
below is green from an automated nightly run.**

## Lab profile (Zyvor)

| Component | Expectation |
|---|---|
| Nodes | ≥3 KVM hosts with FluxVM |
| Storage | Shared filesystem or Ceph RBD at identical paths |
| Network | L2/L3 reachability for guest + migration data plane |
| CNI | Cilium (when network-policy / identity tests run) |
| Runner | Self-hosted GitHub Actions runner with lab access |

Automation entry points:

- Runbook: [`docs/runbook-multi-host-migration-test.md`](runbook-multi-host-migration-test.md)
- Matrix driver: [`scripts/hardware-migration-matrix.sh`](../scripts/hardware-migration-matrix.sh)
- Workflow: [`.github/workflows/hardware-migration.yml`](../.github/workflows/hardware-migration.yml) (`workflow_dispatch` + nightly)

**Lab enablement (current blocker):** the `matrix` job runs only on
`runs-on: [self-hosted, kairon-lab]`. Bring that runner online and set
repository secrets/vars `KAIRON_HW_LAB=1`, `KAIRON_KUBE_URL`,
`KAIRON_KUBE_TOKEN`, `KAIRON_KUBE_CA` (plus optional
`KAIRON_HW_EBPF_MACHINE` for the live-eBPF case). Until then, push-triggered
runs succeed on the `validate` job only and leave every row below as
`not run`.

## Migration matrix

| Case | Last result | Date | Notes |
|---|---|---|---|
| Create / restart / delete smoke (≤100 VMs scaled down in CI) | not run | — | Requires `KAIRON_HW_LAB=1` |
| Cold migration | not run | — | |
| Live pre-copy under load | not run | — | |
| Live + eBPF dataplane | not run | — | Machine with `spec.network.dataplaneMode=ebpf`; set `KAIRON_HW_EBPF_MACHINE` |
| Source failure during transfer | not run | — | |
| Ambiguous commit → `NeedsRecovery` | not run | — | |
| Controller failover mid-migration | not run | — | |

Update this table when a matrix run completes (script prints a markdown
row summary to stdout for copy/paste). For the eBPF live case, the lab
Machine must set `dataplaneMode: ebpf` and `dataplaneRequired: true` so
migration network quiesce/export/restore exercises FluxVM's TC/eBPF path
(see [`network-fabric.md`](network-fabric.md)).

## Component versions (fill per run)

| Piece | Version |
|---|---|
| Kairon | |
| Kubernetes | |
| FluxVM | |
| Cilium | |
| CSI / storage | |
