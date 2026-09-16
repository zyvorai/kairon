# User guide: MachineSnapshotSchedule

Periodically creates a `MachineSnapshot` for every `Machine` matching a
label selector -- Kairon's first-cut backup-automation primitive, roughly
analogous to a Kubernetes `CronJob`, but for VM snapshots and on a plain
wall-clock interval rather than real cron syntax (see "Real limits today"
below for exactly what that means).

## Example

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: MachineSnapshotSchedule
metadata:
  name: nightly
  namespace: prod
spec:
  selector: {tier: web}
  intervalSeconds: 86400
```

This creates a new `MachineSnapshot` for every `Machine` in the `prod`
namespace labeled `tier: web` roughly once every 86400 seconds (24 hours).
A brand-new schedule fires on its very first reconcile tick after creation
-- it doesn't wait a full interval before its first run. `kaironctl get
snapshotschedules` (or `kubectl get machinesnapshotschedules`) lists what's
configured, including each schedule's `status.lastRunTime`/
`lastRunSnapshotCount`.

Or, via `kaironctl`:

```console
$ kaironctl create snapshotschedule nightly --selector tier=web --interval-seconds 86400
snapshotschedule/nightly created
$ kaironctl edit snapshotschedule nightly --suspend true
snapshotschedule/nightly updated
$ kaironctl edit snapshotschedule nightly --suspend false --interval-seconds 43200
snapshotschedule/nightly updated
```

`--selector` is repeatable for a multi-label selector (`--selector tier=web
--selector env=prod`), same as `create migrationpolicy`. `edit` only patches
the flags you actually pass -- omitting `--interval-seconds` on an `edit`
call never resets it.

`spec.volumeSnapshotClassName`, if set, is passed straight through to every
`MachineSnapshot` this schedule creates, mirroring
`MachineSnapshot.spec.volumeSnapshotClassName` exactly.

## How it's enforced

Entirely by `kairon-controller`'s own reconcile loop
(`internal/controller/machinesnapshotschedule.go`) -- there's no admission
webhook, the same reasoning `MigrationPolicy`'s own guide already gives:
this CRD doesn't gate any other object's admission, it only creates new
objects on a timer, so there's nothing for a webhook to validate at
create-time that the CRD's own OpenAPI schema (`intervalSeconds: minimum
60`) doesn't already cover.

Each reconcile tick:

1. Every `MachineSnapshotSchedule` is checked against
   `status.lastRunTime`: due if it's never run before, or if at least
   `spec.intervalSeconds` have elapsed since the last run, and not
   `spec.suspend`d.
2. For each due schedule, every `Machine` in the same namespace matching
   `spec.selector` gets a new `MachineSnapshot`, named
   `<schedule-name>-<unix-timestamp>`.
3. `status.lastRunTime`/`lastRunSnapshotCount`/`lastRunError` are patched
   once, after every match has been attempted -- a schedule matching zero
   Machines still gets `lastRunTime` patched, so it doesn't re-fire every
   tick forever waiting for a Machine that may never appear. A failure
   creating one Machine's snapshot is logged and counted
   (`kairon_reconcile_item_errors_total{kind="snapshotschedule"}`) but
   never stops the rest of that schedule's matches from being attempted.

## Real limits today (first cut)

- **`intervalSeconds`, not real cron syntax.** There's no day-of-week/
  time-of-day expression support -- a schedule fires roughly every
  `intervalSeconds`, starting from whenever it was created or last ran,
  not at a fixed wall-clock time. This is a deliberate simplification
  matching this project's Go-stdlib-only bias (no new cron-parsing
  dependency) and its established pattern of shipping a simpler mechanism
  honestly labeled as such (see `MigrationPolicy`'s own plain
  `bandwidthMbps`/`maxConcurrent` scalars for the same precedent).
- **No jitter or stagger.** Several schedules sharing the same interval
  (or created around the same time) can all become due on the same
  reconcile tick and fire together.
- **No automatic snapshot pruning or retention policy.** Every run's
  `MachineSnapshot`s accumulate forever -- there's no `spec.keepLast: N`
  or similar to automatically delete older ones. Cleaning up old scheduled
  snapshots is a real, separate, un-implemented follow-up, not a hidden
  gap: use `kaironctl get snapshots`/`kaironctl delete snapshot NAME`
  (or a script around them) until one exists.
- **Namespace-scoped only** -- a `MachineSnapshotSchedule` only ever
  matches Machines in its own namespace, same as `MigrationPolicy`/
  `MachineDisruptionBudget`.
- **No dashboard page yet** -- API/`kaironctl`/`kubectl` only, matching how
  most CRDs in this project ship before getting a dashboard page.
