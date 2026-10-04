# User guide: AI agents over MCP (Hermes Agent)

`kaironctl mcp serve` is a [Model Context Protocol](https://modelcontextprotocol.io)
server on stdin/stdout. An MCP client such as
[Hermes Agent](https://github.com/NousResearch/hermes-agent) starts it as a
subprocess and can then list and inspect Machines, read VM-edge network data
and, if you allow it, change power state, take snapshots and run packet
captures.

FluxVM has a matching server, `fluxctl mcp serve`, for the VMs on one host
(see FluxVM's `docs/mcp.md`). Use both to give an agent the cluster view and
the host view. For end-to-end setup of both, scoped credentials, other MCP
clients and example workflows, see [AI agent integration](../ai-agents.md).
This page is the `kaironctl mcp serve` reference.

## Tools

Read tools are always offered:

| Tool | What it returns | Backend |
| --- | --- | --- |
| `list_machines` | Machines with phase, power state, node, guest IP, CPU and memory; all namespaces unless `namespace` is set | Kubernetes API |
| `get_machine` | One Machine's metadata, spec and status | Kubernetes API |
| `list_network_policies` | MachineNetworkPolicies | Kubernetes API |
| `machine_network` | `kind` = `network-effective`, `network-stats`, `network-flows`, `network-drops`, `network-drop-reasons` or `network-capture` (capture sessions); `limit` for flows and drops | kairon-ui |
| `machine_edge_identity` | The stable VM-edge identity | computed locally |
| `machine_volumes` | Each `spec.volumes` entry: source (`pvc`, `atlas-pvc`, `atlas-rbd`), claim, size, Atlas phase, backend id, error | Kubernetes API |
| `get_machine_snapshot` | A MachineSnapshot's phase and per-volume snapshots (CSI VolumeSnapshot or Atlas snapshot id) | Kubernetes API |
| `list_machine_pools` | MachinePools with warm size, ready and claimed counts | Kubernetes API |

Write tools are offered only with `--allow-write`:

| Tool | Effect |
| --- | --- |
| `set_power_state` | Sets `spec.powerState` to `Running`, `Stopped`, `Paused` or `Halted`. |
| `create_snapshot` | Creates a MachineSnapshot (name generated unless `snapshotName` is set). |
| `snapshot_volume` | Snapshots one named volume (MachineSnapshot with `spec.volumeNames`); Atlas volumes use Atlas snapshots. |
| `network_capture` | Runs a 1-30 s tcpdump capture on the Machine's VM edge. With `output`, waits and writes the pcap to that path on the machine running kaironctl; otherwise returns the token. |
| `claim_machine` | Creates a MachineClaim against `pool` and waits up to `waitSeconds` (default 30) for it to bind; returns the Machine name and bind time. Optional `labels`, `retain`, `ttlSeconds`. |
| `release_claim` | Deletes a MachineClaim; its Machine is deleted too unless the claim was made with `retain`. |
| `machine_disk` | Adds (`attach`, with `claim`) or removes (`detach`) a `spec.disks` entry; kairon-node hot-attaches or unplugs the PVC disk. |
| `machine_nic` | Adds (`add`, with `bridge`) or removes (`remove`) a `spec.network.extraInterfaces` entry; kairon-node hot-adds or unplugs the NIC. |
| `delete_machine` | Deletes a Machine. Refuses MachineSet replicas (the set would recreate them). |

Migrate, exec, and edge or policy changes are not exposed.

Every tool takes `namespace` (default: `--namespace`, else `default`) and
`name` where it acts on one Machine. Unknown arguments are rejected, so a
mistyped field comes back as an error instead of being ignored.

## Configure Hermes

Add the server to `~/.hermes/config.yaml` (or run `hermes mcp add`):

```yaml
mcp_servers:
  kairon:
    command: kaironctl
    args: ["mcp", "serve"]            # add "--allow-write" for power, snapshot, capture
    env:
      KAIRON_KUBE_URL: "https://127.0.0.1:6443"
      KAIRON_KUBE_TOKEN: "..."
      KAIRON_KUBE_INSECURE: "true"    # lab only; prefer KAIRON_KUBE_CA
      KAIRON_UI_URL: "http://127.0.0.1:22000"
      KAIRON_UI_TOKEN: "..."
    timeout: 120
```

Then `/reload-mcp` in a running Hermes session. The tools appear as
`mcp_kairon_list_machines` and so on. A copy of this config is in
[`.hermes/config.example.yaml`](../../.hermes/config.example.yaml).

To keep an agent read-only even if someone adds `--allow-write`, filter the
tools on the Hermes side as well:

```yaml
    tools:
      exclude: [set_power_state, create_snapshot, snapshot_volume, network_capture, claim_machine, release_claim, delete_machine]
```

## Credentials

| Variable | Needed for |
| --- | --- |
| `KAIRON_KUBE_URL`, `KAIRON_KUBE_TOKEN`, `KAIRON_KUBE_CA` / `KAIRON_KUBE_INSECURE` | Machine, policy, power and snapshot tools. In a Pod, the service account is used instead. |
| `KAIRON_UI_URL`, `KAIRON_UI_TOKEN` | `machine_network` and `network_capture`. kairon-ui needs diagnostics enabled (`KAIRON_NODE_CONSOLE_TOKEN` on kairon-ui and kairon-node). |

The agent can do whatever these credentials allow. Give it a Kubernetes
token bound to a Role that only has the verbs you want (for example `get`
and `list` on `machines`, plus `patch` only if power changes are allowed).
Tokens are read from the environment and never appear in tool results.

## Limits

- Tool output is capped at 64 KB; longer results are cut with a note to
  narrow the request (for example with `limit`).
- Each call times out after 30 s; a capture after its length plus 45 s.
- Only the tools capability is implemented (no MCP resources or prompts).

## Testing without Hermes

The server speaks newline-delimited JSON-RPC, so a pipe is enough:

```bash
printf '%s\n' \
  '{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18"}}' \
  '{"jsonrpc":"2.0","id":2,"method":"tools/list"}' \
  '{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"list_machines","arguments":{}}}' \
  | kaironctl mcp serve
```

Logs go to stderr; stdout carries only protocol messages.

## Troubleshooting

| Symptom | Fix |
| --- | --- |
| `kubernetes endpoint not configured` | Set `KAIRON_KUBE_URL` and `KAIRON_KUBE_TOKEN` in the server's `env`. |
| `set KAIRON_UI_URL to reach uiapi …` | Set `KAIRON_UI_URL` (and `KAIRON_UI_TOKEN`). |
| `HTTP 501: diagnostics are not enabled` | Set `KAIRON_NODE_CONSOLE_TOKEN` on kairon-node and kairon-ui. |
| A write tool says to start with `--allow-write` | Add `--allow-write` to `args`, then `/reload-mcp`. |
| Hermes shows no `mcp_kairon_*` tools | Check that `kaironctl` is on Hermes' `PATH` (or use an absolute `command`), and run the pipe test above. |
