# User guide: MigrationPolicy

Scopes migration bandwidth and concurrency to Machines matching a label
selector, instead of the global, cluster-wide
`migration.maxConcurrentPerNode`/`migration.maxConcurrentCluster` Helm
values -- Kairon's namespace-scoped equivalent of KubeVirt's own
`MigrationPolicy` CRD.

## Example

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: MigrationPolicy
metadata:
  name: web-tier
  namespace: prod
spec:
  selector: {tier: web}
  bandwidthMbps: 500
  maxConcurrent: 2
```

This applies to every `Machine` in the `prod` namespace labeled
`tier: web`, regardless of who creates the `MachineMigration` for it
(`kaironctl evacuate`, the dashboard, a direct `kubectl apply`, or
`controller.cordonEvacuation`) -- the same "applies automatically to
anything matching, not just one caller's own code path" shape
`MachineDisruptionBudget` already has.

## How it's enforced

Both fields are enforced by `kairon-controller`'s existing migration
reconcile loop, at the exact moment a `Pending` `MachineMigration` is
about to be admitted (the same point `MachineDisruptionBudget`/the global
concurrency caps are already checked):

- **`maxConcurrent`** caps how many non-terminal migrations of *matching*
  Machines may run at once, cluster-wide. A migration that would exceed
  it is left `Blocked` with a clear message
  (`MigrationPolicy prod/web-tier: maxConcurrent reached`) and retried
  automatically on a later tick, exactly like the global
  `maxConcurrentPerNode`/`maxConcurrentCluster` caps already behave. This
  is **independent of, and in addition to** those global caps -- both are
  checked, and either one blocking is enough to block the migration.
- **`bandwidthMbps`** becomes the `MachineMigration`'s own
  `spec.bandwidthMbps` the moment it's admitted, **but only if that
  `MachineMigration` didn't already set one explicitly** -- an explicit
  per-migration `--bandwidth-mbps` (via `kaironctl migrate`) or a
  hand-written `spec.bandwidthMbps` always wins over any policy default.
  When more than one `MigrationPolicy` in the namespace matches the same
  Machine, the first one found supplies the default -- deliberately not
  merged or most-restrictive, keeping this a simple default value, not a
  second competing enforcement layer on top of `maxConcurrent`.
- `status.activeMigrations` is patched every tick, mirroring
  `MachineDisruptionBudget`'s own purely-observational status
  reconciliation.

## Real limits today (first cut)

- **Namespace-scoped only** -- a `MigrationPolicy` only ever matches
  Machines in its own namespace, same as `MachineDisruptionBudget`.
- **`bandwidthMbps` is a default, not an enforced ceiling.** Nothing stops
  an explicit per-migration `--bandwidth-mbps` from setting a *higher*
  value than a matching policy's default -- this only fills in an unset
  value, it doesn't cap an explicit one.
- **No admission webhook.** Like `MachineQuota`'s own reconcile-loop
  enforcement, a `MachineMigration` created directly through the API is
  still fully subject to this (unlike a webhook, this can't be bypassed by
  skipping a CLI), but there's no immediate `kubectl apply`-time
  rejection either -- an over-cap migration is admitted as `Pending` and
  becomes `Blocked` on the very next reconcile tick, not instantly.
- **First-match-wins bandwidth defaulting, not merged.** See above -- if
  you need one Machine to definitely get a specific bandwidth regardless
  of policy overlap, set `spec.bandwidthMbps` on the `MachineMigration`
  directly instead of relying on policy defaulting.
