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

## Migration matrix

| Case | Last result | Date | Notes |
|---|---|---|---|
| Create / restart / delete smoke (≤100 VMs scaled down in CI) | not run | — | Requires `KAIRON_HW_LAB=1` |
| Cold migration | not run | — | |
| Live pre-copy under load | not run | — | |
| Source failure during transfer | not run | — | |
| Ambiguous commit → `NeedsRecovery` | not run | — | |
| Controller failover mid-migration | not run | — | |

Update this table when a matrix run completes (script prints a markdown
row summary to stdout for copy/paste).

## Component versions (fill per run)

| Piece | Version |
|---|---|
| Kairon | |
| Kubernetes | |
| FluxVM | |
| Cilium | |
| CSI / storage | |
