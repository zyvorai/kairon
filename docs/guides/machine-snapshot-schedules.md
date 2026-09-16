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
  keepLast: 7
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
$ kaironctl edit snapshotschedule nightly --keep-last 7
snapshotschedule/nightly updated
```

Or from the dashboard: the **Snapshot schedules** page lists every schedule
in the `default` namespace with its interval, retention, and last-run
status, and a **Suspend**/**Resume** button toggles `spec.suspend` without
needing `kaironctl`/`kubectl` for that one action.

`--selector` is repeatable for a multi-label selector (`--selector tier=web
--selector env=prod`), same as `create migrationpolicy`. `edit` only patches
the flags you actually pass -- omitting `--interval-seconds` on an `edit`
call never resets it.

`spec.volumeSnapshotClassName`, if set, is passed straight through to every
`MachineSnapshot` this schedule creates, mirroring
`MachineSnapshot.spec.volumeSnapshotClassName` exactly.

## Retention (`spec.keepLast`)

Set, `spec.keepLast: N` bounds how many of THIS schedule's own
`MachineSnapshot`s are kept **per Machine**: once a Machine has more than
`N` ready-to-use snapshots this exact schedule created, the oldest (by
`metadata.creationTimestamp`) are deleted right after each due run, once
the run's own new snapshot has been created. Unset (or `0`, the default)
never prunes anything -- snapshots accumulate forever, exactly this
project's behavior before `keepLast` existed.

Two things `keepLast` deliberately never touches, so it's safe to turn on
against an existing schedule with pre-existing snapshots:

- **A `MachineSnapshot` this schedule didn't create** -- one made by hand,
  by a script, or by a *different* `MachineSnapshotSchedule` is never a
  pruning candidate. Every snapshot a schedule creates is stamped with the
  `kairon.zyvor.dev/snapshot-schedule: <schedule-name>` label at creation
  time; pruning only ever counts and deletes snapshots carrying that exact
  label with that exact schedule's own name.
- **A snapshot that isn't `status.readyToUse` yet** -- one still
  `Freezing`/`Thawing`/`Pending` is never counted toward the limit and
  never itself a deletion candidate. This means a `keepLast: 1` schedule
  never has a moment with zero completed backups: the old ready snapshot
  only gets deleted once a newer one has actually finished, never while a
  replacement is still in progress.

Retention is per-Machine, not per-schedule-in-total: a schedule matching 5
Machines with `keepLast: 3` keeps up to 3 snapshots *for each* of those 5
Machines, not 3 total across all of them.

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
   `<schedule-name>-<unix-timestamp>` and labeled
   `kairon.zyvor.dev/snapshot-schedule: <schedule-name>`.
3. If `spec.keepLast` is set, right after each successful create the
   schedule's own ready-to-use `MachineSnapshot`s for that same Machine
   (matched by the label above) beyond `keepLast` are deleted, oldest
   first -- see "Retention" above for exactly what does and doesn't count.
4. `status.lastRunTime`/`lastRunSnapshotCount`/`lastRunError` are patched
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
- **`spec.keepLast` retention is per-Machine and count-only, not
  time-based.** There's no "keep one per day for 7 days, one per week for
  4 weeks"-style tiered retention (the kind a dedicated backup tool would
  offer) -- just a flat "keep the N most recent ready ones per Machine."
  Leaving `keepLast` unset (the default) still accumulates snapshots
  forever, exactly as before this field existed.
- **Namespace-scoped only** -- a `MachineSnapshotSchedule` only ever
  matches Machines in its own namespace, same as `MigrationPolicy`/
  `MachineDisruptionBudget`.
- **Dashboard is list + suspend/resume only.** The **Snapshot schedules**
  page shows every schedule and its last-run status, and can toggle
  `spec.suspend` with a click -- but editing `selector`/`intervalSeconds`/
  `keepLast`/`volumeSnapshotClassName`, or creating/deleting a schedule,
  still needs `kaironctl`/`kubectl`.
