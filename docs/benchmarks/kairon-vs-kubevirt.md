# Kairon vs KubeVirt: benchmark

Same host, same disk image, same guest shape, same readiness probe, run
back to back. The raw JSON for every run sits next to this page.

## Results

Run on 2026-10-04: one Intel Xeon E-2336 node (12 threads, 31 GiB),
k3s, Kairon `main` at `cac5dac` with FluxVM's QEMU backend, KubeVirt v1.9.0.
Raw results: [`kairon-2026-10-04.json`](kairon-2026-10-04.json),
[`kubevirt-2026-10-04.json`](kubevirt-2026-10-04.json).

| | kairon main-cac5dac | kubevirt v1.9.0 |
|---|---|---|
| Control plane RSS (idle) | 63 MiB | 905 MiB |
| Control plane CPU (idle) | 3.5 m | 30.2 m |
| N=1: create to Running, p50 | 10.3 s | 23.4 s |
| N=1: create to SSH ready, p50 | 23.7 s | 67.6 s |
| N=1: create to SSH ready, max | 23.7 s | 67.6 s |
| N=1: host RSS per VM | 576 MiB | - |
| N=1: VMs ready | 1/1 | 1/1 |
| N=5: create to Running, p50 | 3.9 s | 78.6 s |
| N=5: create to SSH ready, p50 | 24.8 s | 184.7 s |
| N=5: create to SSH ready, max | 26.0 s | 209.2 s |
| N=5: host RSS per VM | 660 MiB | 661 MiB |
| N=5: VMs ready | 5/5 | 5/5 |
| N=10: create to Running, p50 | 18.1 s | 431.7 s |
| N=10: create to SSH ready, p50 | 39.9 s | - |
| N=10: create to SSH ready, max | 114.6 s | - |
| N=10: host RSS per VM | 592 MiB | - |
| N=10: VMs ready | 10/10 | 0/10 |

- **Control plane:** idle, Kairon's three processes use about 1/14 of
  KubeVirt's memory and about 1/9 of its CPU.
- **Time to a usable VM:** Kairon gets a guest to SSH 2.9x faster for one VM
  and 7.4x faster (p50) for five created at once.
- **Per-VM memory is the same** at N=5: both run the same QEMU with the same
  guest, and KubeVirt's extra per-VM processes are small next to guest RAM.
  Kairon's advantage is the control plane and the start path, not the VM.
- **N=10:** all ten Kairon VMs reached SSH, the slowest in 115 s. KubeVirt
  got 4 of 10 to Running and none to SSH within the 600 s limit, while
  k3s's containerd timed out creating and killing `virt-launcher` pods
  (`DeadlineExceeded` events). This host is shared, so read N=10 as "the
  pod-per-VM path degrades first under host pressure", not as KubeVirt's
  density ceiling. Per-VM memory is left out where not every VM came up.
- KubeVirt's N=1 per-VM memory is left out: VMs from the previous run were
  still shutting down when the baseline was taken.

## What is measured

All numbers come from [`scripts/bench/bench.py`](../../scripts/bench/bench.py),
which drives both platforms through one code path; only the manifests, the
status fields it reads, and the process names it counts differ.

| Metric | How |
|---|---|
| Create to Running | `kubectl apply` of N objects at once, until each reports Running (Machine `status.phase`, VMI `status.phase`) |
| Create to SSH ready | Until the guest's sshd sends its `SSH-` banner, probed from the node. No guest agent involved. A forwarded port can accept TCP before the guest listens, so the banner, not the connect, is the signal |
| Host RSS per VM | After all N are ready and a 20 s settle: RSS growth of every process the VM runtime adds (QEMU; plus `virt-launcher`, `virt-launcher-monitor`, `virtqemud`, `virtlogd` for KubeVirt) plus growth of the node agent (`kairon-node`/FluxVM, `virt-handler`), divided by N |
| Control plane (idle) | RSS and CPU over 60 s with no VMs: `kairon-controller`, `kairon-node`, FluxVM vs `virt-api`, `virt-controller`, `virt-handler`, `virt-operator`, `virt-exportproxy` |
| Host load | 1-minute load average before and after each density, recorded in the JSON |

Guest: Ubuntu 24.04 cloud image (`noble-server-cloudimg-amd64.img`), 1 vCPU,
512 MiB, identical cloud-init seed (hostname, one user with an SSH key).

- **Kairon:** `spec.image.path` on the node, QEMU backend, user-mode
  networking with a host port forward to guest port 22.
- **KubeVirt:** the same file wrapped as a containerDisk
  ([`containerdisk-import.sh`](../../scripts/bench/containerdisk-import.sh))
  and imported into containerd beforehand, so neither side downloads
  anything; masquerade pod networking, probed at the pod IP.

## Reproduce

```bash
# On the node, with KubeVirt installed (scripts/bench/kubevirt-install.sh pins v1.9.0)
BENCH_IMAGE=/path/noble.img scripts/bench/bench-kubevirt.sh --sizes 1,5,10

# With kairon-controller, kairon-node and FluxVM running
BENCH_IMAGE=/var/lib/fluxvm/images/noble.qcow2 scripts/bench/bench-kairon.sh --sizes 1,5,10

scripts/bench/report.py docs/benchmarks/kairon-*.json docs/benchmarks/kubevirt-*.json
```

`--migrate-target NODE` adds a live migration at N=1 and records Kairon's
`MachineMigration.status.downtimeMs`; KubeVirt reports migration start and
end but not guest downtime, so there is no like-for-like number for it.

## Caveats

- One node. Density beyond what one host's memory allows, and
  live-migration downtime, need a second node and are not in this run.
- Both control planes ran on the node under test; Kairon's components ran
  as plain processes, KubeVirt's as its operator-managed pods.
- KubeVirt's numbers include its normal per-VM pod start (scheduling,
  init containers, `virt-launcher`). That is the cost being compared, not
  noise to subtract.
- Guest boot work (cloud-init, sshd host keys) is identical on both sides
  and dominates "SSH ready" at N=1.
- The node is a shared lab host. Each density step records its 1-minute
  load before and after in the JSON; KubeVirt's N=5 step drove it from 7.7
  to 24.6, Kairon's from 8.6 to 10.2.
