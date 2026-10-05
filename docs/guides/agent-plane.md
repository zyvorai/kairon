# Agent plane

Kairon does not put a model in the reconcile loop. `internal/agentplane` compiles proposals. Admission and MCP call it. Apply stays with the operator or a later controller that only accepts the compiled object.

## Opt-in

A Machine is unchanged until it sets one of:

| Annotation | Effect |
| --- | --- |
| `kairon.zyvor.dev/agent-pool=true` | Requires `spec.tenant`, a sha256 image digest, `kairon.zyvor.dev/image-signature=cosign:sha256:<64 hex>`, `kairon.zyvor.dev/egress-allowlist`, and `dataplaneMode: ebpf` (defaulted when empty). |
| `kairon.zyvor.dev/require-tenant=true` | Requires a DNS-label `spec.tenant`. |
| `kairon.zyvor.dev/confidential` | `sev-snp` or `tdx` only. Node report verification is a separate call. |
| `kairon.zyvor.dev/gpu-count` | Whole-GPU claim. `kairon.zyvor.dev/live-migrate=true` is refused. |
| `kairon.zyvor.dev/gateway` | Name recorded for a Gateway binding built from `spec.network.forwards`. |

`KAIRON_MCP_TENANT`, when set, scopes `compile_network_policy` and `validate_agent_claim` to that tenant.

## MCP tools

Read tools, always offered:

- `compile_network_policy` — strict allowlist, `defaultAllow: false`, no bare `*`, no `0.0.0.0/0`.
- `explain_drops` — `spoof_ip`, `dns_deny`, `sni_deny`, `rate_limit`. `apply` is false.
- `explain_pending` — repeats scheduler reasons. Does not reschedule.
- `detect_edge_anomalies` — beacon, long DNS qname, SNI spread, deny burst.
- `validate_agent_claim` — TTL 30–86400, egress required, guest cannot hold `delete_machine`, `fork_machine`, `claim_machine`, `release_claim`, `set_power_state`.
- `propose_boot_repair` — virtio, VMware tools, disk names, ssh, cloud-init, virtio-win. Does not write a disk.
- `migration_claim` — green only when cold, live, live-ebpf, source-failure and controller-failover passed.

Write tool, `--allow-write` only:

- `audit_record` — hashes principal, tool, tenant, claim and diff into an id for replay.

## CLI

```
kaironctl agent compile-policy --file intent.json
kaironctl agent explain-drops --file drops.json
kaironctl agent migration-claim --file matrix.json
```

Nothing in these commands writes to the apiserver.
