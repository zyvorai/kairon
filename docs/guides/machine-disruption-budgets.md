# User guide: MachineDisruptionBudget

Caps how many Machines `kaironctl evacuate` is willing to disrupt at once.

## Why this exists, and what it isn't

Kairon has no automatic node-drain/eviction path today -- `node.spec.unschedulable`
only blocks *new* placements, nothing ever moves already-running Machines off
a node being decommissioned on its own. The only thing that does that is
`kaironctl evacuate NODE`, which immediately creates a `MachineMigration` for
every Machine on that node at once. `MachineDisruptionBudget` makes
`evacuate` throttle itself instead of firing everything simultaneously.

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
   the rest of that one `evacuate` run -- recomputed from scratch on each
   invocation, nothing is persisted.

`kaironctl evacuate` exits non-zero if anything was skipped, so it's safe to
script/alert on.

## Real limits today (v1 of this feature)

- Client-side only unless `webhook.enabled` (see above) -- a
  `MachineMigration` created some other way isn't gated at all.
- `status` is observational only, not authoritative for any real-time
  decision (see "Status" above) -- up to one reconcile interval stale, and
  reconciling it is best-effort (a patch failure is logged, not retried
  until the next tick).
- A Machine matching zero `MachineDisruptionBudget`s is never blocked --
  budgets are opt-in per selector, not a cluster-wide default.
- Still no automatic node-drain/eviction path -- `MachineDisruptionBudget`
  only throttles `kaironctl evacuate`'s own batch, it doesn't cause
  anything to start draining on its own.
