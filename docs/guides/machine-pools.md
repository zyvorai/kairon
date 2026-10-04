# MachinePool and MachineClaim

A `MachinePool` keeps `spec.replicas` Machines booted and unclaimed. A
`MachineClaim` takes one of them over. The bind is a label change on an
already-Running Machine, so it completes in one kairon-controller reconcile
tick instead of a cold boot. The pool then boots a replacement.

Use it for agent sandboxes, CI runners, or anything that needs a VM now and
can tolerate it having been booted a few minutes earlier.

## Example

```bash
kubectl apply -f examples/machinepool.yaml      # pool "agents" + claim "job-42"
kaironctl get machinepools
kaironctl claim agents --label team=ml --ttl 1h
kaironctl get machineclaims
```

`kaironctl claim` creates the claim, waits for it to bind (`--wait`, default
60s) and prints the Machine name and bind time:

```text
machineclaim/agents-claim-3fa91c bound to machine/agents-8c21d0e4 in 412ms
```

## How it works

Pool members carry these labels:

| Label | Value |
| --- | --- |
| `kairon.zyvor.dev/machinepool` | pool name |
| `kairon.zyvor.dev/machinepool-template-hash` | hash of `spec.template` |
| `kairon.zyvor.dev/pool-state` | `warm` or `claimed` |
| `kairon.zyvor.dev/machineclaim` | claim name, once claimed |

Each tick, kairon-controller:

1. Binds pending claims, oldest first, to the oldest Running `warm` member of
   their pool. The label patch carries the Machine's `resourceVersion`, so a
   member changed since the listing is skipped (409) instead of being bound
   twice. `spec.labels` from the claim are added to the Machine.
2. Brings each pool to `spec.replicas` warm members. Claimed members don't
   count, so a claim triggers a replacement in the same tick. Warm members
   built from an older template are deleted and recreated at once (nobody is
   using them). Surplus members are trimmed, booting ones first.

A claim with no Running warm member stays `Pending` with a message like
`no Running warm machine in pool "agents" (2 warming)` and binds as soon as one
is ready.

## Claim lifecycle

| Field | Effect |
| --- | --- |
| `spec.poolName` | Pool in the claim's namespace. Immutable. |
| `spec.labels` | Added to the Machine at bind. |
| `spec.reclaimPolicy` | `Delete` (default): deleting the claim deletes the Machine. `Retain`: the pool and claim labels are removed and the Machine is kept as a plain Machine. |
| `spec.ttlSeconds` | Deletes the claim this long after it binds, which releases the Machine per `reclaimPolicy`. |
| `status.phase` | `Pending`, `Bound`, or `Lost` (the Machine was deleted out from under the claim). |
| `status.bindMillis` | Claim creation to bind, in milliseconds. |

Deleting a pool deletes its warm members only. Claimed Machines belong to
their claims and are left running.

## Scaling

`MachinePool` and `MachineSet` both have the `scale` subresource, so these
work:

```bash
kubectl scale machinepool agents --replicas 10
kubectl scale machineset web --replicas 5
kaironctl scale machinepool agents --replicas 10
```

A `HorizontalPodAutoscaler` (or KEDA) can target either kind through the
same subresource.

## AI agents

`kaironctl mcp serve --allow-write` exposes `claim_machine`, `release_claim`
and `delete_machine`; `list_machine_pools` is read-only. See
[hermes-mcp.md](hermes-mcp.md).

## Limits

- Readiness is `status.phase == Running`, not a guest-agent heartbeat.
- Members boot through the normal Machine path (scheduler, quota, webhook),
  so a pool counts against `MachineQuota` for every warm member.
- Claims don't yet carry their own egress allowlist; apply a
  `MachineNetworkPolicy` that selects the claim's labels.
- FluxVM's node-local warm pools (`/v1/pools/{name}/claim`) are a separate,
  lower-level mechanism; a MachinePool works with any backend.
