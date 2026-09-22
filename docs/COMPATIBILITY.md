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

**Lab enablement (current blocker for the multi-host matrix):** the
`matrix` job runs only on `runs-on: [self-hosted, kairon-lab]`. Bring that
runner online and set repository secrets/vars `KAIRON_HW_LAB=1`,
`KAIRON_KUBE_URL`, `KAIRON_KUBE_TOKEN`, `KAIRON_KUBE_CA` (plus optional
`KAIRON_HW_EBPF_MACHINE` for the live-eBPF case). Until then,
push-triggered runs succeed on the `validate` job only and leave every
**migration** row below as `not run`.

## Single-host deploy smoke (manual)

Bare-metal systemd path (`scripts/deploy-remote.sh`), not a substitute for
the multi-host matrix above.

| Case | Last result | Date | Notes |
|---|---|---|---|
| Deploy node+controller+ui (`v0.6.0-24-g0020758`) | pass | 2026-09-22 | Host `80.79.5.173` / `nldw4-4-16-36` (k3s). Health: node `:8081`, controller `:8083` (host `:8080` occupied by `krytond`), ui `:8082` |
| CRD server-side apply | pass | 2026-09-22 | `deploy/crd.yaml` |
| Machine create / Running | pass | 2026-09-22 | `v07-smoke` then deleted |
| Machine stop → start | pass | 2026-09-22 | `deploy-smoke` |
| UI overview + machines API | pass | 2026-09-22 | http://80.79.5.173:8082 |
| Network `effective` (console relay) | pass | 2026-09-22 | Needs matching `KAIRON_NODE_CONSOLE_TOKEN` in both env files; redeploy leaves existing env untouched |
| Network flows/stats/drops | n/a | 2026-09-22 | `deploy-smoke` uses `network.mode: user` — FluxVM eBPF flow/drop/stats need tap+`dataplaneMode: ebpf` |

## Migration matrix

| Case | Last result | Date | Notes |
|---|---|---|---|
| Create / restart / delete smoke (≤100 VMs scaled down in CI) | not run | — | Multi-host matrix; single-host create/stop/start smoke is green above |
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
