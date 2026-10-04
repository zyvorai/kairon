---
sidebar_position: 5
title: CLI
---

# CLI (`kaironctl` / `kubectl kairon`)

`kaironctl` is a [Cobra](https://github.com/spf13/cobra)-based CLI with Cilium-style colored tables, emoji progress for install/uninstall, and hierarchical help.

**Dependency exception:** the CLI embeds the Helm chart (`go:embed`) and drives install/upgrade/uninstall through `helm.sh/helm/v3`. `kairon-controller` and `kairon-node` remain Go-stdlib-only — see [DEPENDENCIES.md](DEPENDENCIES.md).

```bash
kaironctl --help
kaironctl install --help
```

Global flags: `-n/--namespace`, `--color=auto|always|never` (also respects `NO_COLOR`).

## Install via Krew

```bash
# From a release (once assets are published):
kubectl krew install kairon

# Local smoke-test from this repo:
make krew-package
kubectl krew install --manifest=dist/krew/kairon.yaml \
  --archive=dist/krew/kairon_$(uname -s | tr A-Z a-z)_$(uname -m | sed 's/x86_64/amd64/;s/aarch64/arm64/').tar.gz

kubectl kairon version
kubectl kairon install --dry-run
```

Manifest: [`deploy/krew/kairon.yaml`](../deploy/krew/kairon.yaml). Packaging script: [`scripts/krew-package.sh`](../scripts/krew-package.sh).

## Lifecycle (embedded Helm SDK)

No separate `helm` binary or `./charts/kairon` checkout required by default — the chart is baked into the binary. Pass `--helm-cli` to shell out to Helm 3 on `$PATH`, or `--chart PATH` / `$KAIRON_CHART` for a filesystem chart.

```text
kaironctl install [--chart embedded|PATH] [--namespace kairon-system] [--set k=v] [-f values.yaml]
                 [--wait] [--timeout 5m] [--dry-run] [--helm-cli]
                 [--helm-release-name kairon] [--create-namespace]
kaironctl upgrade  [same chart/set/values/wait flags as install]
kaironctl uninstall [--namespace kairon-system] [--force] [--wait] [--timeout 5m] [--dry-run] [--helm-cli]
kaironctl status [--namespace kairon-system] [--wait] [--timeout 5m] [--interactive]
```

`uninstall` refuses while any Machine objects still exist unless `--force` is set. `--dry-run` on install renders manifests offline (no cluster needed).

## Resources & power

```text
kaironctl get [machines|migrations|snapshots|restores|quotas|budgets|machinesets|machinepools|machineclaims|instancetypes|migrationpolicies|snapshotschedules|networkpolicies|securitygroups|nodes] [--selector k=v]
kaironctl describe [RESOURCE] NAME
kaironctl create NAME --image PATH [--cpu N] [--memory SIZE] [--backend qemu|…]
                 [--forward hostPort:guestPort[/proto]] [--hostname NAME] [--user NAME]
                 [--ssh-key KEY] [--package PKG] [--runcmd CMD] [--priority N]
kaironctl create machineset|machinepool|instancetype|migrationpolicy|snapshotschedule|quota|budget|networkpolicy|securitygroup NAME …
kaironctl delete [RESOURCE] NAME
kaironctl delete RESOURCE --selector k=v [--dry-run]
kaironctl edit [machine|machineset|migrationpolicy|snapshotschedule|quota|budget|networkpolicy|securitygroup] NAME …
kaironctl scale machineset NAME --replicas N
kaironctl scale machineset --selector k=v --replicas N
kaironctl scale machinepool NAME --replicas N
kaironctl import ova SOURCE [--url URL] [--name NAME] [--no-repair] [--dry-run] [create flags]
kaironctl claim POOL [NAME] [--label k=v] [--retain] [--ttl 1h] [--allow-fqdn H] [--allow-sni N] [--allow-cidr C] [--allow-port P] [--allow-dns N] [--allow-icmp] [--wait 60s | --no-wait]
kaironctl start|stop|pause|resume|halt MACHINE
kaironctl disk attach|detach MACHINE NAME [--claim PVC]   # live, see guides/machine-hotplug.md
kaironctl disk list MACHINE
kaironctl nic add|remove MACHINE NAME [--bridge BR] [--mac MAC]
kaironctl nic list MACHINE
kaironctl top [machines|nodes] [--selector k=v]
kaironctl trigger snapshotschedule NAME
kaironctl network status MACHINE [--flows] [--drop-reasons] [--limit N]
kaironctl network policies   # same as get networkpolicies
kaironctl network flows|drop-reasons|drops|stats|effective MACHINE [--limit N]
kaironctl network identity MACHINE
kaironctl network capture MACHINE [--seconds 1-30] [--filter EXPR] [--output FILE]
kaironctl network captures MACHINE
```

### Network status

`kaironctl network status MACHINE` prints emoji lines for FluxVM dataplane
attach/mode, Cilium ExternalWorkload identity/IP (when `ciliumAttach` is set),
and any matching `MachineNetworkPolicy` with `spec.cilium.sync`. `--flows` /
`--drop-reasons` call the existing uiapi pass-through when `KAIRON_UI_URL`
(and optionally `KAIRON_UI_TOKEN`) is set.

### VM edge

- `kaironctl network drops MACHINE` — drops recorded by FluxVM's VM edge,
  with Kairon's reason names (`spoof_ip`, `spoof_mac`, `dns_deny`,
  `sni_deny`, `rate_limit`, `policy_deny`, `default_deny`, `malformed`,
  `migration`), the policy name, flow and packet count. `--limit`
  defaults to 10. Needs `KAIRON_UI_URL`.
- `kaironctl network identity MACHINE` — the Machine's stable edge
  identity (FNV-1a of namespace and name). Computed locally; no cluster
  access needed.
- `kaironctl network capture MACHINE --seconds 15 [--filter EXPR] [--output FILE]`
  — starts a tcpdump capture (1-30 seconds) on the Machine's VM edge in
  FluxVM. With `--output`, waits for it to finish and writes the pcap.
  Needs `KAIRON_UI_URL`; without it the session is only printed.
- `kaironctl network captures MACHINE` — capture sessions with their
  state (`running`, `done`, `failed`, `interrupted`) and packet counts.

See [ebpf-edge.md](ebpf-edge.md).

## Migrate / recover / fence / snapshot

```text
kaironctl migrate MACHINE --strategy auto|cold|live --target-node NODE
kaironctl evacuate NODE [--strategy cold|auto] [--wait] [--timeout 15m] [--poll-interval 10s]
kaironctl recover MIGRATION --action ACTION --diagnosis DIAGNOSIS --reason REASON
kaironctl cancel-migration MIGRATION
kaironctl fence MACHINE --reason REASON
kaironctl snapshot MACHINE [--name NAME] [--class CLASS] [--volume NAME]...
kaironctl restore SNAPSHOT --target-claim NAME
kaironctl volumes MACHINE [-n NS] [-o table|json]
kaironctl backup create MACHINE [--name NAME] [--quiesce auto|required|never] [--atlas] [--atlas-bucket ID] [--keep N]
kaironctl backup list
kaironctl backup restore BACKUP [--machine NAME] [--storage-class NAME]   # Machine must be halted
kaironctl backup delete NAME
```

`get`, `describe` and `delete` also take `backup` and `backuprestore`. See
[guides/machine-backup.md](guides/machine-backup.md).

## Images

```bash
kaironctl image upload FILE [--name NAME] [--format qcow2|raw|ova|vmdk|vhd|vhdx] [--replace]
kaironctl image list
kaironctl image delete NAME
```

`upload` streams a disk image to kairon-ui's image store
(`ui.imageStore.enabled`) and prints the `spec.image` block to boot it.
Needs `KAIRON_UI_URL` and an admin `KAIRON_UI_TOKEN` session token. See
[guides/machine-image-import.md](guides/machine-image-import.md#uploading-images).

## AI agents (MCP)

```bash
kaironctl mcp serve                 # read tools only
kaironctl mcp serve --allow-write   # also power, snapshot, capture
```

`kaironctl mcp serve` is a Model Context Protocol server on stdin/stdout for
[Hermes Agent](https://github.com/NousResearch/hermes-agent) and other MCP
clients. Read tools: `list_machines`, `get_machine`, `list_network_policies`,
`machine_network`, `machine_edge_identity`, `machine_volumes`,
`get_machine_snapshot`, `list_machine_pools`, `list_backups`. Write tools, only with `--allow-write`:
`set_power_state`, `create_snapshot`, `snapshot_volume`, `network_capture`,
`claim_machine`, `release_claim`, `delete_machine`, `machine_disk`, `machine_nic`,
`machine_backup`. It
uses `KAIRON_KUBE_*` for Machines and `KAIRON_UI_URL`/`KAIRON_UI_TOKEN` for
network data. See [ai-agents.md](ai-agents.md) for setup with Hermes and
other clients, and [guides/hermes-mcp.md](guides/hermes-mcp.md) for the reference.

## Meta

```text
kaironctl version
kaironctl completion bash|zsh|fish|powershell
```

`create`/`scale`/`edit` cover the common flag-friendly fields only — richer fields still need `kubectl apply`/YAML.

Point at a cluster with `KAIRON_KUBE_URL` (e.g. after `kubectl proxy`), a standard kubeconfig (Helm SDK path), or run in-cluster with the mounted service account.

**`kubectl kairon ...`** works identically once `kubectl-kairon` is on `$PATH` (Krew or `make build`).

---
