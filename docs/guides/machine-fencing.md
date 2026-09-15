# User guide: node fencing and migration preflight

How Kairon reacts when a Machine's node goes unreachable, and how to stop a
migration from landing on a node that doesn't actually share storage or
network with the source.

## Why this exists, and what it isn't

If a node running a Machine goes unreachable, the obvious "fix" is to
reschedule the Machine somewhere else automatically. Kairon deliberately
doesn't do this. If the node is merely partitioned -- still running the real
FluxVM instance, just unreachable from the Kubernetes API -- rescheduling
onto another node starts a *second* copy of the same VM: split-brain, the
exact failure `NeedsRecovery` already exists to prevent for ambiguous
migration commits (see
[`docs/runbook-migration-failures.md`](../runbook-migration-failures.md)).
Kairon has no independent way to confirm a node is truly gone versus just
unreachable, so it refuses to guess here either.

Instead, fencing in Kairon is **detection, then an operator-attested
action** -- the same shape as `NeedsRecovery`:

1. `kairon-controller` detects and names the condition automatically.
2. A human who has confirmed out-of-band that the node is truly gone (power
   fence via IPMI/iDRAC, confirmed hardware failure, deliberate
   decommission) tells Kairon so with `kaironctl fence`.
3. Only then does the Machine become eligible for normal scheduling again.

## Detection: the `NodeUnreachable` condition

Every reconcile tick, `kairon-controller` checks whether each Machine's
`spec.nodeName` still names a `Ready`, present Kubernetes Node
(`internal/controller/fencing.go`'s `detectUnreachableNodes`) and keeps a
`NodeUnreachable` condition on `status.conditions` in sync with that:

```yaml
status:
  conditions:
  - type: NodeUnreachable
    status: "True"
    reason: NodeNotReadyOrMissing
    message: 'node "worker-3" is not Ready or no longer exists in the
      cluster; this Machine will NOT be automatically rescheduled -- see
      "kaironctl fence" only once you''ve confirmed out-of-band that the
      node is truly gone, not just unreachable'
```

It flips back to `status: "False"` on its own once the node is `Ready`
again -- no operator action needed to clear a transient blip. A status patch
is only issued when the condition actually needs to change, not every tick,
so this doesn't add write load in the steady state.

This is a single signal: whether the Node object itself reports `Ready`.
There's no independent liveness probe of `kairon-node` or FluxVM on that
host -- if the Kubernetes Node is `Ready` but `kairon-node` has wedged, this
condition won't catch it (and neither would rescheduling help, since the
underlying FluxVM instance would still be untouched either way).

## Operator-attested action: `kaironctl fence`

```bash
kaironctl fence MACHINE --reason "confirmed powered off via iDRAC at 14:02"
```

Refuses to run at all unless the Machine's `NodeUnreachable` condition is
currently `True` -- there's nothing to fence if kairon-controller doesn't
currently see the node as unreachable. `--reason` is required: it's your own
attestation of the out-of-band evidence, recorded verbatim into a new
`Fenced` condition for the audit trail. Kairon cannot verify this reason
itself; the command trusts the operator running it, exactly like
`kaironctl recover`'s `--diagnosis`/`--reason` do for a `NeedsRecovery`
migration.

On success it clears `spec.nodeName` and every status field tied to the old
runtime (`status.phase`, `status.nodeName`, `status.runtimeID`,
`status.guestIP(s)`, `status.network`, applied CPU/memory) so the next
reconcile tick treats this exactly like a brand-new, unscheduled Machine --
not an adoption of a runtime that may not exist anymore.

**If the old node comes back with the FluxVM instance still actually
running after you've fenced and rescheduled**, you now have two runtimes
alive for one Machine. Fencing is only safe once you've genuinely confirmed
the node is gone -- a reboot, a network partition that clears itself, or a
misdiagnosed outage are not valid reasons to fence.

## Migration preflight: storage/network domain labels

Separately, `migrationTarget` checks a chosen live/cold-migration
destination against the source node before committing to it
(`internal/controller/preflight.go`'s `migrationPreflight`). This closes
part of what
[`docs/runbook-multi-host-migration-test.md`](../runbook-multi-host-migration-test.md)
already documents as a prerequisite:  both hosts need "a shared filesystem
mount at an identical path" and real L2/L3 network reachability for a
migration to actually work. Kairon has no other source of truth for node
storage/network topology -- it neither copies disk content nor guest network
state between nodes itself in either cold or live migration -- so this is
opt-in, via two labels you set yourself:

```bash
kubectl label node worker-1 worker-2 kairon.zyvor.dev/storage-domain=rack-a
kubectl label node worker-1 worker-2 kairon.zyvor.dev/network-domain=vlan-10
```

Two nodes sharing the same value for one of these labels are asserted, by
you, to be compatible for that dimension. If the chosen target's value
differs from the source's, the migration is blocked outright with an
explanatory error rather than proceeding:

```
migration preflight failed: storage domain mismatch: source node "worker-1"
has kairon.zyvor.dev/storage-domain="rack-a", target node "worker-3" has
kairon.zyvor.dev/storage-domain="rack-b" -- these nodes are not asserted to
share storage/network, see docs/guides/machine-placement.md
```

**This can only catch a *confirmed* mismatch.** If either node doesn't have
the label set (the default, until you opt in), preflight has no way to
confirm compatibility either way and lets the migration through -- exactly
as it always has. Setting the labels makes the check strictly more useful,
never a new blocker for clusters that don't use them. It's also checked
against the one chosen migration target only, not searched across every
candidate node -- if the winning candidate fails preflight, the migration is
blocked rather than silently retried against a different node.

## Migration preflight: VFIO device claims

A Machine with `spec.deviceClaims` set (GPU/SR-IOV passthrough --
[`docs/guides/machine-placement.md`](machine-placement.md)) gets a separate,
stricter check (`deviceClaimsPreflight`, same file):

- **Live migration of a `deviceClaims` Machine is always blocked**, no
  label can change that -- VFIO passthrough is boot-time-only, and QEMU's
  live-migration RAM transfer fundamentally cannot carry a passthrough PCI
  device's in-flight state across hosts. FluxVM has no hot-unplug/hot-plug
  API for this today (it does for CPU/memory, not VFIO devices) -- a real
  upstream dependency, tracked in `ROADMAP.md`, not something this preflight
  can work around.
- **Cold migration is allowed once the target asserts an equivalent
  device**, the same opt-in-label pattern as storage/network domains above:

```bash
kubectl label node worker-2 kairon.zyvor.dev/vfio-devices=0000:65:00.0
```

  Cold migration has none of live migration's state-transfer problem --
  the guest is stopped and a fresh runtime created at the target exactly
  like initial creation -- so it only needs the target to actually have a
  compatible device, which is exactly what this label lets you assert
  (Kairon has no other way to know a node's `KAIRON_VFIO_ALLOWLIST`
  without it). Cold migration of a `deviceClaims` Machine to an
  un-labeled target is blocked, closing what used to be a silent gap: the
  device was simply left behind with no explicit error.

## Real limits today (first cut)

- `NodeUnreachable` detection is Node-`Ready`-only, not an independent
  liveness check of `kairon-node`/FluxVM.
- `kaironctl fence` trusts the operator's `--reason`; Kairon has no way to
  verify a node is actually gone.
- Migration preflight only checks the two domain labels above -- it doesn't
  and can't verify actual filesystem/network reachability between nodes.
- Neither behavior has been exercised against a real multi-host failure
  scenario in this repository's own CI (see the README's Production gaps).
