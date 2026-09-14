# User guide: MachineDisruptionBudget

Caps how many Machines `kaironctl evacuate` is willing to disrupt at once.

## Why this exists, and what it isn't

Kairon has no automatic node-drain/eviction path today -- `node.spec.unschedulable`
only blocks *new* placements, nothing ever moves already-running Machines off
a node being decommissioned on its own. The only thing that does that is
`kaironctl evacuate NODE`, which immediately creates a `MachineMigration` for
every Machine on that node at once. `MachineDisruptionBudget` makes
`evacuate` throttle itself instead of firing everything simultaneously.

**By default, this is a client-side check inside `kaironctl`, not a
server-side admission guarantee.** Nothing reconciles this object on a
timer, and with the admission webhook below disabled (the default),
nothing stops a `MachineMigration` created directly through the Kubernetes
API (bypassing `kaironctl evacuate`) from ignoring it entirely. Treat it
the same way you'd treat any other `kaironctl`-enforced convention -- real
for anyone using the CLI as intended, not a hard multi-tenant guarantee --
unless you enable the webhook.

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

- Client-side only (see above) -- a `MachineMigration` created some other
  way isn't gated at all.
- No live `status` (the CRD has a status subresource for schema consistency
  with every other Kairon CRD, but nothing writes to it -- there's no
  controller for this object).
- A Machine matching zero `MachineDisruptionBudget`s is never blocked --
  budgets are opt-in per selector, not a cluster-wide default.
