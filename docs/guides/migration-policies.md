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
`MachineDisruptionBudget` already has. `kaironctl get migrationpolicies`
(or `kubectl get migrationpolicies`) lists what's configured, including
each policy's current `status.activeMigrations` count -- as does
`kairon-ui`'s own read-only "Migration policies" dashboard page
(`GET /api/v1/migration-policies`).

Or, via `kaironctl`:

```console
$ kaironctl create migrationpolicy web-tier --selector tier=web --bandwidth-mbps 500 --max-concurrent 2
migrationpolicy/web-tier created
$ kaironctl edit migrationpolicy web-tier --max-concurrent 4
migrationpolicy/web-tier updated
```

`--selector` is repeatable for a multi-label selector (`--selector tier=web
--selector env=prod`). `edit` only patches the flags you actually pass --
omitting `--bandwidth-mbps` on an `edit` call never clears an
already-configured value back to 0.

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

## Previewing what a migration would get right now (`kaironctl describe`)

`describe` for every other kind in this project uniformly prints the raw
object as JSON and nothing else. `kaironctl describe migrationpolicy` is one
of only four deliberate exceptions (the others are
[`kaironctl describe snapshotschedule`](machine-snapshot-schedules.md#previewing-what-would-fire-right-now-kaironctl-describe),
[`kaironctl describe quota`](machine-quotas.md#previewing-usage-right-now-kaironctl-describe),
and [`kaironctl describe
budget`](machine-disruption-budgets.md#previewing-who-counts-right-now-kaironctl-describe)):
it prints that same JSON, then appends exactly which `Machine`s in the
policy's own namespace currently satisfy `spec.selector`, and for each one,
what creating a `MachineMigration` for it *right now* would actually get
from this policy:

```console
$ kaironctl describe migrationpolicy web-tier
{
  "apiVersion": "kairon.zyvor.dev/v1alpha1",
  "kind": "MigrationPolicy",
  ...
}

Matching machines (2) -- status.activeMigrations 1/2; a migration created for each of these right now, in this order, would get:
  web-1                    admitted                                 500 Mbps (this policy)
  web-2                    BLOCKED (MigrationPolicy prod/web-tier: maxConcurrent reached)  500 Mbps (this policy)
```

Both columns come from calling `kairon-controller`'s own exported, pure
`controller.AdmitMigrationPolicy`/`controller.BandwidthMbpsFromPolicies` --
the identical functions the migration reconcile loop itself calls for every
`MachineMigration` it admits -- so this preview can never disagree with what
the controller will actually do:

- **Admission** walks matching Machines in a stable, name-sorted order and
  spends this policy's own `maxConcurrent` cap as it goes, exactly like a
  real sequential batch of migrations created in that order would -- so
  once the cap is spent, every later match in the list correctly reports
  `BLOCKED`, with the same blocker message the controller itself would
  record.
- **Bandwidth** reports what `bandwidthMbps` a new migration for that
  Machine would actually inherit. When more than one `MigrationPolicy` in
  the namespace matches the same Machine, `spec.bandwidthMbps`
  first-match-wins semantics (see above) mean an *earlier* overlapping
  policy can win over the one you're describing -- the preview says so
  explicitly (`from "other-policy", an earlier-matching MigrationPolicy,
  not this one`) rather than silently reporting the wrong policy's number
  as this policy's own.

A selector matching zero Machines prints the same explicit `(none -- check
spec.selector against these Machines' own labels)` hint
`describe snapshotschedule` uses. Like that preview, this is a snapshot of
*this instant*, not a guarantee -- a Machine's labels, this policy's own
`spec.selector`/`maxConcurrent`/`bandwidthMbps`, another overlapping
policy's fields, or `status.activeMigrations` itself can all change between
running `describe` and whenever a migration is actually created.

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
- **The `describe` preview is `kaironctl`/`kubectl`-only, no dashboard
  equivalent yet.** `kairon-ui`'s "Migration policies" page still shows
  only the raw object and `status.activeMigrations`, not the per-Machine
  admission/bandwidth preview above.
