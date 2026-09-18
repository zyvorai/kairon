---
sidebar_position: 2
title: Getting started
---

# Getting started

## Prerequisites

- Kubernetes cluster and Kairon-capable nodes.
- KVM and a reachable FluxVM service on each VM node.
- VM image paths available below `--image-root`.
- CSI snapshot stack for `MachineSnapshot`.
- Kubernetes DRA plus administrator-approved BDFs for VFIO.

## Install

Preferred: `kaironctl` embeds the Helm chart, so no separate `helm` binary or chart checkout is required.

```bash
kaironctl install
kubectl label node worker-1 kairon.zyvor.dev/capable=true
kubectl label node worker-2 kairon.zyvor.dev/capable=true
kaironctl status
```

Raw manifests still work:

```bash
kubectl apply -f deploy/crd.yaml
kubectl apply -f deploy/rbac.yaml
kubectl apply -f deploy/controller.yaml
kubectl apply -f deploy/node.yaml
```

## Create a Machine

```bash
kubectl apply -f examples/linux-machine.yaml
kubectl get machines -A -w
```

Or via `kaironctl`, with an SSH key and port forward set at creation (both
only take effect at creation -- see [guides/machine-network.md](guides/machine-network.md)):

```bash
kaironctl create demo --image /var/lib/fluxvm/images/ubuntu-24.04.qcow2 \
  --ssh-key "$(cat ~/.ssh/id_ed25519.pub)" --forward=2222:22
ssh -p 2222 <node-ip>
```

## Cold migrate

```bash
kaironctl migrate demo --strategy cold --target-node worker-2
```

## Enable the secure live-migration peer

Use the Helm chart and provide `kairon-migration-tls` with `ca.crt`, `tls.crt`, and `tls.key`, then enable `migration.enabled=true`. The chart credential must have `serverAuth` + `clientAuth` EKUs and DNS SAN `kairon-node` unless `migration.tlsServerName` is changed.

A real live transfer additionally requires a Kairon migration adapter on each node (`cmd/kairon-migration-adapter-fluxvm`; not installed automatically today -- see [`runbook-multi-host-migration-test.md`](runbook-multi-host-migration-test.md)). Without one configured, an explicit live request is safely blocked before source transfer.

```bash
kaironctl migrate demo --strategy live --target-node worker-2 --mode pre-copy
```

## Snapshot

```bash
kaironctl snapshot database --name database-before-upgrade --class csi-snapclass
kaironctl get snapshots
```

## Deploy the web dashboard

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system --set ui.enabled=true
kubectl -n kairon-system port-forward svc/kairon-ui 8082:8082
```

No `ui.*` auth values needed: with nothing else configured, the chart seeds a
default `admin` account with a random, generated-once password. Retrieve it
from the `helm install` output (or any time later) with:

```bash
kubectl -n kairon-system get secret kairon-ui-session -o jsonpath='{.data.defaultAdminPassword}' | base64 -d; echo
```

Open `http://127.0.0.1:8082` and sign in as `admin` with that password.

| Page | What it shows |
|---|---|
| **Overview** | Fleet tiles by phase, a prominent warning whenever anything is parked in `NeedsRecovery` |
| **Machines** | Table + create form (mirrors `kaironctl create`) + Start / Stop / Console / Migrate / Snapshot / Delete |
| **Migrations** | List with phase badges, a "New Migration" form, an "Evacuate Node" action, `status.dataPlaneEncrypted` badge, a "Cancel migration" action while `Starting`/`Running`, and the recovery workflow |
| **Snapshots** | List + create form |
| **Nodes** | Real Kubernetes `Node` list — `Ready`, `spec.unschedulable`, taints, addresses, plus per-node Machines/CPU/Memory usage rollup; read-only |
| **Account** | Change your own password; an admin account can reset another operator's |

For real, named per-operator accounts instead of the single generated admin,
set `ui.auth.users` -- generate each password's hash first:

```bash
kairon-ui -hash-password 'a real password'   # prints a bcrypt hash; pipe from a file/heredoc, don't type a real password on the command line
```

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system \
  --set ui.enabled=true \
  --set ui.auth.users[0].username=alice \
  --set ui.auth.users[0].passwordHash='$2a$10$...'
```

The legacy single shared token (`ui.token`, or `ui.allowUnauthenticated=true`
for local development) still works unchanged for existing deployments, and
is accepted alongside `ui.auth.users` if both are set.

Repeated failed logins against one username are rate-limited (5 failures
locks that username out for 5 minutes, `429` with `Retry-After`) -- tracked
per requested username, including unknown ones, so the lockout itself
can't be used to enumerate valid accounts.

### VNC console

```bash
helm upgrade --install kairon ./charts/kairon -n kairon-system --set ui.enabled=true --set console.enabled=true
```

Adds a "Console" button per Machine in the dashboard -- a real graphical
VNC session in the browser, for QEMU-backend Machines only (the button
hides itself for ineligible Machines, or entirely when `console.enabled`
is off). Read [SECURITY.md](https://github.com/zyvorai/kairon/blob/main/SECURITY.md)'s "VNC console" section first:
FluxVM's own VNC socket has no auth of its own, so this feature's security
rests on kairon-ui's operator auth, a single-use connection ticket bound
to the requesting username (every session is audit-logged), and a shared
token between kairon-ui and every kairon-node -- appropriate for a trusted
operator team, not a hostile-network or multi-tenant deployment. That last
hop can optionally run over one-way TLS instead of plaintext HTTP -- see
`console.tls.enabled`/`console.tls.secretName` in `values.yaml`.

**Prerequisite confirmed against a real deployment**: FluxVM creates
`vnc.sock` root-owned with no `other` write bit, and `kairon-node` runs as
an unprivileged, deliberately capability-less user -- without OS-level
access to that socket, the console fails with a clear 502
(`dial vnc socket: ... permission denied`), not a silent hang. Grant
`kairon-node`'s user read/write access to FluxVM's per-VM socket files
(a POSIX ACL, or a shared group between the FluxVM and kairon-node service
users) before expecting `console.enabled` to actually work end-to-end.

Bare-metal alternative:

```bash
scripts/deploy-remote.sh sus@80.79.5.173 --with-controller --with-ui --with-console
```

Installs `kairon-ui` as a systemd service alongside `kairon-node`/`kairon-controller`; a dashboard token is auto-generated and printed once at the end of the run. Requires `npm` locally to build `web/dist` -- the only place this script needs Node.js. `--with-console` (optionally `--console-port=N`, otherwise auto-picked if the default collides) generates the shared console token and wires it into both services' systemd env files. Add `--console-tls` to also enable TLS on that hop -- with no `--console-tls-cert`/`-key`/`-ca`, a private CA and server certificate are generated locally and installed on the remote host automatically (SAN list is a best-effort guess at the host's own addresses; if kairon-ui's console button fails with a TLS error afterward, regenerate with explicit `--console-tls-cert`/`-key`/`-ca` covering the right one), or pass those three yourself to bring your own.

## Network Fabric (eBPF edge)

Apply the example Machine + policy + security group, then follow the tutorial:

```bash
kubectl apply -f examples/network-fabric-machine.yaml
kaironctl network status web
```

On a cluster that already runs Cilium as the CNI, three opt-in paths exist (all off by default):

| Path | What you set |
|---|---|
| FluxVM cilium dataplane | `/etc/fluxvm.toml` `sandbox.dataplane.mode = "cilium"` and/or `spec.network.dataplaneMode: cilium` |
| Cluster network attach | Helm `network.ciliumAttach.enabled=true` plus `spec.network.ciliumAttach: true` (`mode: tap`, `netns: true`) |
| Policy visible to Hubble | Helm `network.ciliumPolicySync.enabled=true` plus `spec.cilium.sync: true` on a `MachineNetworkPolicy` |

- Tutorial: [tutorials/network-fabric.md](tutorials/network-fabric.md)
- User guides: [guides/machine-network.md](guides/machine-network.md), [guides/network-policy.md](guides/network-policy.md)
- Reference: [network-fabric.md](network-fabric.md)
