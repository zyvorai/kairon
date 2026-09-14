# User guide: MachineDisruptionBudget

Caps how many Machines `kaironctl evacuate` is willing to disrupt at once.

## Why this exists, and what it isn't

`node.spec.unschedulable` only blocks *new* placements -- nothing moves
already-running Machines off a node being decommissioned just because it's
cordoned, the same as real Kubernetes (`kubectl cordon` alone never evicts
anything either). The operator-invoked action that actually moves Machines
off a node is `kaironctl evacuate NODE`, which creates a `MachineMigration`
for each one. `MachineDisruptionBudget` makes `evacuate` throttle itself
against those, instead of firing everything simultaneously; `evacuate
--wait` (below) additionally keeps retrying until the node is actually
empty, the closest thing to an automatic drain Kairon has -- deliberately
still something an operator starts, not something that begins on its own
the moment a node is cordoned for whatever reason.

**Enforcement is still client-side by default, not a server-side admission
guarantee.** With the admission webhook below disabled (the default),
nothing stops a `MachineMigration` created directly through the Kubernetes
API (bypassing `kaironctl evacuate`) from ignoring it entirely. Treat it
the same way you'd treat any other `kaironctl`-enforced convention -- real
for anyone using the CLI as intended, not a hard multi-tenant guarantee --
unless you enable the webhook.

`kairon-controller` *does* now reconcile this object on a timer, but only
for `status` -- see "Status" below. That reconciliation is purely
observational; it doesn't change the enforcement story above at all.

### Admission webhook (`webhook.enabled`)

An opt-in validating admission webhook on `kairon-controller`
(`webhook.enabled`, off by default -- see the Helm chart's `webhook` values
and SECURITY.md) closes the direct-API bypass above: it rejects a
`MachineMigration` create outright if disrupting its target Machine would
violate a matching budget, reusing the exact same `LoadBudgetStates`/
`AdmitDisruption` decision `kaironctl evacuate` already makes
(`internal/controller/disruption.go`) rather than a second implementation.
It only evaluates `CREATE` -- a `MachineMigration`'s target never changes
after creation, so there's nothing for `UPDATE` to re-check. Requires an
operator-supplied TLS certificate (`webhook.tlsSecretName`,
`webhook.caBundle`); like `migration.tlsSecretName`, this chart doesn't
mint one for you.

## Example

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: MachineDisruptionBudget
metadata:
  name: web-tier
spec:
  selector: {tier: web}
  minAvailable: "2"      # or maxUnavailable: "1" -- exactly one of the two
```

`minAvailable`/`maxUnavailable` are a plain integer or a `"N%"` string,
evaluated against however many Machines currently match `selector` --
same shape and rounding (percentages round up) as a real Kubernetes
`PodDisruptionBudget`.

## Status

Every reconcile tick, `kairon-controller` computes each budget's
`status.expectedMachines`/`currentHealthy`/`desiredHealthy`/
`disruptionsAllowed` from the exact same `LoadBudgetStates` logic
`evacuate`/the admission webhook use, and patches it onto the object --
`kubectl get mdb`/`kaironctl get budgets` now report real numbers instead
of an empty `{}`. This is purely observational: `evacuate` and the
webhook still each recompute their own allowance fresh at decision time
rather than trusting this status, since it can be up to one reconcile
interval (`controller.interval`, 5s default) stale -- a real-time decision
should never be made against a cached number that old.

```
$ kaironctl get budgets
NAME       MINAVAILABLE  MAXUNAVAILABLE  EXPECTED  HEALTHY  DESIRED  ALLOWED
web-tier   2             -               3         3        2        1
```

## How `evacuate` uses it

For every Machine on the node being evacuated:

1. Find every `MachineDisruptionBudget` whose `selector` matches that
   Machine's labels.
2. For each one, count `total` (all Machines cluster-wide matching that
   selector) and `currentHealthy` (`status.phase == Running` **and** not
   already targeted by a non-terminal `MachineMigration` -- so a disruption
   already in flight is never counted as available headroom twice).
3. If `currentHealthy - desiredHealthy <= 0` for any matching budget, that
   Machine is skipped (reported as `machine ns/name skipped: ...`, not
   silently dropped) rather than migrated.
4. Queuing a Machine spends one allowance from every budget it matches, for
   the rest of that one `evacuate` pass -- recomputed from scratch on each
   invocation, nothing is persisted.

`kaironctl evacuate` exits non-zero if anything was skipped, so it's safe to
script/alert on -- unless you pass `--wait` (below), which only exits
non-zero on an actual timeout.

### `--wait`: retrying until the node is actually empty

Plain `evacuate` makes exactly one pass: anything `MachineDisruptionBudget`
blocks that tick is skipped for good, left on the node, reported, and
`evacuate` exits -- even though that Machine might become disruptable a
minute later, once an earlier migration from the same pass actually
finishes and frees up the budget's headroom again.

```
kaironctl evacuate worker-3 --wait [--timeout 15m] [--poll-interval 10s]
```

`--wait` re-runs the same pass on `--poll-interval` (default 10s) until
every Machine has actually left the node (`spec.nodeName != worker-3`) or
`--timeout` (default 15m) elapses -- the closest thing to an automatic
node-drain Kairon has, matching `kubectl drain`'s own retry model. It's
still deliberately an **operator-invoked** action, not something that
starts on its own the moment `node.spec.unschedulable` is set (by
`kaironctl` or anything else) -- the same "operator attests, Kairon then
follows through automatically" shape as `kaironctl fence`/`recover`,
chosen over a background controller that would silently start migrating
every Machine on a node the instant *anything* cordons it, for whatever
reason.

Each pass also now skips (never duplicates) a Machine that already has a
non-terminal `MachineMigration` in flight -- a real gap `--wait` needed
closed to avoid creating a second, conflicting migration for the same
Machine on its next retry, and one plain (non-`--wait`) `evacuate` shares
the fix too.

## Real limits today (v1 of this feature)

- Client-side only unless `webhook.enabled` (see above) -- a
  `MachineMigration` created some other way isn't gated at all.
- `status` is observational only, not authoritative for any real-time
  decision (see "Status" above) -- up to one reconcile interval stale, and
  reconciling it is best-effort (a patch failure is logged, not retried
  until the next tick).
- A Machine matching zero `MachineDisruptionBudget`s is never blocked --
  budgets are opt-in per selector, not a cluster-wide default.
- No background/automatic drain triggered by `node.spec.unschedulable` or
  anything else -- `evacuate --wait` (above) is the closest thing to
  automatic node-drain, and it's still an explicit operator action to
  start, by design.
- `--wait`'s timeout is wall-clock, not "N more retries" -- a
  `MachineDisruptionBudget` that can genuinely never be satisfied (e.g.
  `minAvailable` higher than the selector will ever match) just retries
  uselessly until `--timeout` regardless of how the budget is
  misconfigured; `evacuate --wait` has no way to detect that case and
  fail fast instead.
