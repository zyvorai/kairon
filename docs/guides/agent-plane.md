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

## Claim step

`StepClaim` decides the next tick for an existing `MachineClaim`. It does not bind the Machine itself.

| Action | When |
| --- | --- |
| `bind` | Pending, and a Running warm member matches tenant and hypervisor. |
| `wait` | Pending, nothing eligible. |
| `hold` | Bound, TTL still open. |
| `expire` | Bound, TTL elapsed. `snapshot` is set when `kairon.zyvor.dev/snapshot-on-release=true`. Reclaim is Delete or Retain. |

Empty claim egress is refused. A compiled policy name is returned when egress is set.

`kaironctl agent step-claim --file claim.json` and MCP `step_agent_claim` return the decision. MCP `anomaly_events` returns Warning events and does not emit them.

## Dashboard

Read-only routes on kairon-ui, no namespace param, no apiserver write:

- `POST /api/v1/agent/compile-policy`
- `POST /api/v1/agent/explain-drops`
- `POST /api/v1/agent/anomalies`
- `POST /api/v1/agent/cpu-label`

CPU label projection uses `kairon.zyvor.dev/pinnable-cpus` and only reports `changed` when the projected list differs. Confidential status is sealed only when the node report kind matches.

## Apply path

The controller now uses this package on the claim path:

- A warm Machine is bound only if `EligibleWarm` matches tenant and hypervisor.
- Release creates `MachineSnapshot/<claim>-release` when `kairon.zyvor.dev/snapshot-on-release=true`, then deletes or retains the Machine. A 409 on create is treated as already done.

Status without a CRD change is annotations from `StatusAnnotations`: confidential sealed/reason, gateway name, gpu count.

MCP tools that still do not write: `project_cpu_label`, `project_confidential`, `bind_gateway`, `replay_audit`.

CLI: `kaironctl agent cpu-label --effective 0-7 --reserved 0-1` and `kaironctl agent gateway --name agents --guest-port 22 --host-port 2201`.

Dashboard adds `POST /api/v1/agent/claims/step`, `/confidential`, `/gateway`.

## Still outside this repo

A green live-migration claim still needs the Zyvor lab matrix. Cosign verification and a real SEV-SNP/TDX report are node facts. This code checks shape and refuses a mismatch; it does not talk to the AMD or Intel firmware.
