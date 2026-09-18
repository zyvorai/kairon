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
kaironctl get [machines|migrations|snapshots|restores|quotas|budgets|machinesets|instancetypes|migrationpolicies|snapshotschedules|networkpolicies|securitygroups|nodes] [--selector k=v]
kaironctl describe [RESOURCE] NAME
kaironctl create NAME --image PATH [--cpu N] [--memory SIZE] [--backend qemu|…]
                 [--forward hostPort:guestPort[/proto]] [--hostname NAME] [--user NAME]
                 [--ssh-key KEY] [--package PKG] [--runcmd CMD] [--priority N]
kaironctl create machineset|instancetype|migrationpolicy|snapshotschedule|quota|budget|networkpolicy|securitygroup NAME …
kaironctl delete [RESOURCE] NAME
kaironctl delete RESOURCE --selector k=v [--dry-run]
kaironctl edit [machine|machineset|migrationpolicy|snapshotschedule|quota|budget|networkpolicy|securitygroup] NAME …
kaironctl scale machineset NAME --replicas N
kaironctl scale machineset --selector k=v --replicas N
kaironctl start|stop|pause|resume|halt MACHINE
kaironctl top [machines|nodes] [--selector k=v]
kaironctl trigger snapshotschedule NAME
kaironctl network status MACHINE [--flows] [--drop-reasons] [--limit N]
kaironctl network policies   # same as get networkpolicies
```

### Network status

`kaironctl network status MACHINE` prints emoji lines for FluxVM dataplane
attach/mode, Cilium ExternalWorkload identity/IP (when `ciliumAttach` is set),
and any matching `MachineNetworkPolicy` with `spec.cilium.sync`. `--flows` /
`--drop-reasons` call the existing uiapi pass-through when `KAIRON_UI_URL`
(and optionally `KAIRON_UI_TOKEN`) is set.

```
## Migrate / recover / fence / snapshot

```text
kaironctl migrate MACHINE --strategy auto|cold|live --target-node NODE
kaironctl evacuate NODE [--strategy cold|auto] [--wait] [--timeout 15m] [--poll-interval 10s]
kaironctl recover MIGRATION --action ACTION --diagnosis DIAGNOSIS --reason REASON
kaironctl cancel-migration MIGRATION
kaironctl fence MACHINE --reason REASON
kaironctl snapshot MACHINE [--name NAME] [--class CLASS]
kaironctl restore SNAPSHOT --target-claim NAME
```

## Meta

```text
kaironctl version
kaironctl completion bash|zsh|fish|powershell
```

`create`/`scale`/`edit` cover the common flag-friendly fields only — richer fields still need `kubectl apply`/YAML.

Point at a cluster with `KAIRON_KUBE_URL` (e.g. after `kubectl proxy`), a standard kubeconfig (Helm SDK path), or run in-cluster with the mounted service account.

**`kubectl kairon ...`** works identically once `kubectl-kairon` is on `$PATH` (Krew or `make build`).

---
