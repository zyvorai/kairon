---
sidebar_position: 5
title: CLI
---

# CLI (`kaironctl` / `kubectl kairon`)

Command reference formerly maintained in the root README.

```text
kaironctl get [machines|migrations|snapshots|restores|quotas|budgets|machinesets|instancetypes|migrationpolicies|snapshotschedules|networkpolicies|securitygroups] [-n NS]
kaironctl describe [RESOURCE] NAME  # RESOURCE defaults to "machine", same aliases as `get`
                 # describe snapshotschedule also previews which Machines its selector currently
                 # matches and whether the next reconcile tick would fire for them
kaironctl create NAME --image PATH [--cpu N] [--memory SIZE] [--backend qemu|…]
                 [--forward hostPort:guestPort[/proto]] [--hostname NAME] [--user NAME]
                 [--ssh-key KEY] [--package PKG] [--runcmd CMD]  # repeatable/cloud-init, see guides/machine-network.md
kaironctl create machineset NAME --image PATH [--replicas N] [--strategy RollingUpdate|Recreate]
                 [--max-unavailable N] [--label k=v] [same Machine-spec flags as `create` above]
kaironctl create instancetype NAME --cpu N --memory SIZE [--max-cpu N] [--max-memory SIZE]
                 [--hugepages] [--numa-node N] [--cpu-set SET] [--cpu-pinning]
kaironctl create migrationpolicy NAME --selector k=v [--bandwidth-mbps N] [--max-concurrent N]
kaironctl create snapshotschedule NAME --selector k=v --interval-seconds N [--volume-snapshot-class NAME]
                 [--keep-last N] [--starting-deadline-seconds N]
kaironctl create quota NAME [--max-machines N] [--max-total-cpu N] [--max-total-memory SIZE]  # at least one required
kaironctl create budget NAME --selector k=v (--min-available X | --max-unavailable X)  # exactly one required
kaironctl create networkpolicy NAME (--machine-name X | --selector k=v)  # at least one required
                 [--allow-cidr CIDR] [--deny-cidr CIDR] [--allow-port proto/port] [--allow-fqdn FQDN]
                 [--policy-group NAME] [--policy-label k=v] [--entity NAME] [--default-allow] [--audit-mode]
                 [--allow-icmp] [--max-egress-mbps N] [--max-egress-pps N] [--sample-rate N]
kaironctl create securitygroup NAME [--group-name X] [--group-label k=v] [--priority N] [--description TEXT]
                 [same policy flags as `create networkpolicy` above]
kaironctl start|stop NAME
kaironctl delete [RESOURCE] NAME  # RESOURCE defaults to "machine", same aliases as `get`
kaironctl scale machineset NAME --replicas N
kaironctl edit machineset NAME [--strategy RollingUpdate|Recreate] [--max-unavailable X]  # only patches flags you actually pass
kaironctl edit migrationpolicy NAME [--bandwidth-mbps N] [--max-concurrent N]  # only patches flags you actually pass
kaironctl edit snapshotschedule NAME [--suspend true|false] [--interval-seconds N] [--keep-last N] [--starting-deadline-seconds N]
kaironctl edit quota NAME [--max-machines N] [--max-total-cpu N] [--max-total-memory SIZE]
kaironctl edit budget NAME [--selector k=v] [--min-available X] [--max-unavailable X]
kaironctl edit networkpolicy NAME [--machine-name X] [--selector k=v] [--allow-cidr CIDR] [--deny-cidr CIDR]
                 [--allow-port proto/port] [--default-allow BOOL] [--audit-mode BOOL] [--max-egress-mbps N] [--max-egress-pps N]
kaironctl edit securitygroup NAME [--group-label k=v] [--priority N] [--description TEXT] [same policy flags as above]
kaironctl migrate MACHINE --strategy auto|cold|live --target-node NODE
kaironctl evacuate NODE [--strategy cold|auto] [--wait] [--timeout 15m] [--poll-interval 10s]
kaironctl snapshot MACHINE [--name NAME] [--class CLASS]
kaironctl restore SNAPSHOT --target-claim NAME
kaironctl recover MIGRATION --action ACTION --diagnosis DIAGNOSIS --reason REASON
kaironctl cancel-migration MIGRATION  # only while phase is Starting/Running (before the destination commits)
kaironctl fence MACHINE --reason REASON  # only once NodeUnreachable=True and you've confirmed the node is truly gone
kaironctl trigger snapshotschedule NAME  # requests an immediate run, bypassing spec.suspend/startingDeadlineSeconds
kaironctl top [machines|nodes] [--selector k=v]  # kubectl-top-style live cgroup-derived usage; "top nodes" rolls Machines up per Spec.NodeName
kaironctl version
```

`create`/`scale`/`edit` cover the common flag-friendly fields only — `spec.placement`, device claims, security, and per-volume claims on a `MachineSet`'s template (and anything else these flags don't expose) still need `kubectl apply`/YAML, the same limit `create machine` already had for those fields. `MachineNetworkPolicy`/`NetworkSecurityGroup`'s own `spec.cnp` (a free-form CiliumNetworkPolicy-shaped document) is the same story — no sensible flag shape for arbitrary nested JSON, so it stays kubectl/YAML-only, and `edit networkpolicy`/`edit securitygroup` cover a narrower flag set than `create` does (see guides/network-policy.md).

Point at a cluster with `KAIRON_KUBE_URL` (e.g. after `kubectl proxy`), or run in-cluster with the mounted service account.

**`kubectl kairon ...`** works identically once `kubectl-kairon` (built by `make build` alongside `kaironctl`) is on `$PATH` — a real kubectl plugin sharing kaironctl's exact command dispatch (`internal/kaironctl`), not a shim shelling out to a separate binary. `kubectl kairon evacuate worker-1` is exactly `kaironctl evacuate worker-1`.

---
