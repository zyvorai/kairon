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
`lastRunSnapshotCount`/`nextRunTime` -- see "When will it run next?" below
for exactly what `nextRunTime` does and doesn't promise.

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

## Previewing what would fire right now (`kaironctl describe`)

`describe` for every other kind in this project uniformly prints the raw
object as JSON and nothing else. `kaironctl describe snapshotschedule`
is the one deliberate exception: it prints that same JSON, then appends a
preview of exactly what the *next* reconcile tick would do with this
schedule, right now:

```console
$ kaironctl describe snapshotschedule nightly
{
  "apiVersion": "kairon.zyvor.dev/v1alpha1",
  "kind": "MachineSnapshotSchedule",
  ...
}

Matching machines (2) -- due now, the next reconcile tick will snapshot these:
  web-1
  web-2
```

or, for a schedule that isn't due yet:

```console
$ kaironctl describe snapshotschedule nightly
...
Matching machines (2) -- not due yet (next projected run: 2026-09-17T02:00:00Z):
  web-1
  web-2
```

The machine list is exactly which `Machine`s in the schedule's own
namespace currently satisfy `spec.selector` -- the same `LabelsMatch` check
`reconcileMachineSnapshotSchedules` itself uses -- and the due/not-due
verdict comes from calling the schedule's own `Spec.Due(status.lastRunTime,
time.Now())`, the identical pure function the controller calls every
reconcile tick, so this preview can never disagree with what actually
happens next. A selector matching zero Machines prints an explicit `(none
-- check spec.selector against these Machines' own labels)` hint instead of
a bare, unexplained empty list -- useful for catching a typo'd label value
before assuming the schedule is simply working correctly with nothing to
do.

This is a real, useful question to ask before loosening or tightening a
selector, or before shortening/lengthening `intervalSeconds` -- without it,
the only way to find out was to wait for the next tick and check
`status.lastRunSnapshotCount` after the fact. It's a snapshot of *this
instant*, though, not a guarantee: a Machine's own labels, or the
schedule's `selector`/`suspend`/`intervalSeconds` fields, can change
between running `describe` and the actual next reconcile tick.

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

## When will it run next? (`status.nextRunTime`)

Every schedule's `status` also carries `nextRunTime`, projecting the next
time it's expected to fire -- `kaironctl get snapshotschedules` shows it in
a `NEXTRUN` column, `kubectl get machinesnapshotschedules` has its own
`NextRun` printer column, and the dashboard's **Snapshot schedules** page
has a **Next run** column too.

`nextRunTime` is set once, every time a schedule actually fires, to
`lastRunTime + intervalSeconds` -- a simple as-of-last-fire projection, not
a live countdown recomputed on every reconcile tick. This is a deliberate
choice, not an oversight: `MachineSnapshotSchedule`'s own status is only
ever patched when a schedule is `Due` (see "How it's enforced" below) --
adding a second, independent tick that patches `nextRunTime` alone for
every schedule that *isn't* due yet would multiply this CRD's write volume
against the apiserver for a value that's already a pure function of fields
already in `status`/`spec`, for no real benefit.

One consequence of that choice: `nextRunTime` is **not** cleared or
recomputed if a schedule is suspended sometime after it last fired -- the
stored value can point at an already-passed timestamp while
`spec.suspend: true`. Both `kaironctl` and the dashboard handle this
correctly at *display* time rather than trusting the stored field blindly:
each checks the live `spec.suspend` flag first and shows `suspended`
instead of a stale, already-passed timestamp whenever it's set. A schedule
that has never yet fired shows `pending` the same way, rather than a zero/
epoch date. `kubectl get machinesnapshotschedules`' own `NextRun` printer
column is the one place this project can't apply that same live-suspend
check -- it's a bare `jsonPath: .status.nextRunTime` and shows the raw
stored value verbatim, so a `kubectl`-only workflow should also check
`spec.suspend`/the `Suspend` printer column before trusting it. A schedule
that's never fired shows a blank `NextRun` there, not `pending` (`kubectl`
has no such formatting hook either) -- a real, honestly-named limit of the
plain-CRD-printer-columns mechanism itself, not something this project's
own code can paper over.

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
4. `status.lastRunTime`/`lastRunSnapshotCount`/`lastRunError`/`nextRunTime`
   are patched once, after every match has been attempted -- a schedule
   matching zero Machines still gets `lastRunTime`/`nextRunTime` patched,
   so it doesn't re-fire every tick forever waiting for a Machine that may
   never appear. `nextRunTime` is set to this tick's `lastRunTime +
   intervalSeconds` -- see "When will it run next?" above for exactly what
   it does and doesn't promise. A failure creating one Machine's snapshot
   is logged and counted
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
- **`status.nextRunTime` is a projection, not a live countdown.** It's only
  ever recomputed when a schedule actually fires (`lastRunTime +
  intervalSeconds` at that moment) -- see "When will it run next?" above.
  `kaironctl`'s and the dashboard's own displays correctly show
  `suspended`/`pending` instead of a stale timestamp by checking the live
  `spec.suspend` flag first, but `kubectl get machinesnapshotschedules`'
  plain `NextRun` printer column can't apply that same logic -- it shows
  the raw stored value (or a blank cell if the schedule has never fired),
  even while suspended. Check `spec.suspend`/the `Suspend` column
  alongside it in a `kubectl`-only workflow.
- **`kaironctl describe snapshotschedule`'s preview is a snapshot of this
  instant, not a guarantee.** It's a plain read-then-compute at the moment
  you ran it -- a Machine's labels, or the schedule's own `selector`/
  `suspend`/`intervalSeconds`, can change before the next actual reconcile
  tick runs, and this preview does nothing to lock or reserve anything.
  There's also no equivalent preview in the dashboard or via `kubectl` --
  it's `kaironctl`-only for now.
