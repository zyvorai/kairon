# User guide: MachineSet

A Deployment/ReplicaSet-shaped abstraction over `Machine`: `kairon-controller`
creates/deletes plain `Machine` objects to match `spec.replicas`, each built
from `spec.template` -- so a fleet of identical Machines doesn't mean
hand-creating each one and hand-tracking template drift yourself.

## Example

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: MachineSet
metadata:
  name: web
  namespace: prod
spec:
  replicas: 3
  strategy: RollingUpdate
  maxUnavailable: 1
  template:
    labels:
      app: web
    spec:
      image: {path: /var/lib/fluxvm/images/web.qcow2}
      resources: {cpu: "2", memory: "4Gi"}
      runtime: {backend: qemu}
      powerState: Running
```

`spec.template.spec` is exactly a `Machine`'s own `spec` -- anything a
hand-created `Machine` accepts (`image`, `volumes`, `deviceClaims`,
`network`, `cloudInit`, ...) works here unchanged. `spec.template.labels`
are applied to every replica in addition to (never replacing) the two
labels `kairon-controller` itself always adds:
`kairon.zyvor.dev/machineset=<name>` and
`kairon.zyvor.dev/machineset-template-hash=<hash of the current template>`.

Every replica is a completely ordinary `Machine` object once created --
`kubectl get machines`, `kaironctl evacuate`, `MachineDisruptionBudget`
selectors (match on `spec.template.labels`, e.g. `{app: web}`, to throttle
disruption across a MachineSet's own replicas the same way they'd throttle
any other Machines), and everything else that already operates on Machines
keeps working exactly as it does today. `kaironctl get machinesets` (or
`kubectl get machinesets`) lists the `MachineSet`s themselves --
`STRATEGY`/`REPLICAS`/`READY`/`UPDATED` at a glance, without having to
separately count matching Machines by hand -- `kairon-ui`'s own "Machine
sets" dashboard page (read-only, `GET /api/v1/machinesets`) shows the same
thing.

## How replicas are reconciled

Every reconcile tick, for each `MachineSet`:

1. Find its owned Machines (`kairon.zyvor.dev/machineset` label match,
   excluding any already being deleted).
2. Split them into **current** (template hash matches `spec.template`
   right now) and **outdated** (an earlier template hash -- meaning
   `spec.template` changed since they were created).
3. If the total (current + outdated) is below `spec.replicas`, create
   enough current-template replicas to fill the gap -- this always takes
   priority over any rollout concern, whether the shortfall is a fresh
   scale-up or a replica that died on its own.
4. Otherwise, advance the rollout by exactly one bounded step (see below),
   never the whole thing in one tick -- a MachineSet rollout is always
   paced by, and visible across, real reconcile intervals.

`status.replicas`/`.readyReplicas`/`.updatedReplicas` are patched every
tick from this same pass -- `readyReplicas` counts current-template
replicas whose `status.phase` is `Running`; `updatedReplicas` counts every
current-template replica regardless of phase.

## Rollout strategy

- **`RollingUpdate`** (the default): replaces outdated replicas
  `maxUnavailable`-at-a-time, computed each tick as `(ready current +
  outdated) - (replicas - maxUnavailable)` -- i.e. never letting actual
  available capacity drop further below `replicas` than `maxUnavailable`
  allows. A current-template replica only counts toward that available
  total once its `status.phase` is `Running`; one that merely exists (just
  created, not yet scheduled or booted) doesn't -- so a rollout won't tear
  down another old, healthy replica to make room for a replacement that
  hasn't actually come up yet. Replacement creation happens automatically
  on a later tick, once a deletion has actually dropped the total below
  `replicas` again (step 3 above picks it up). `maxUnavailable` is an
  integer or a percentage string (e.g. `"25%"`), same parsing
  `MachineDisruptionBudget` already uses; empty defaults to `1`.
- **`Recreate`**: terminates every outdated replica first, and creates no
  replacements until none remain -- matches a real Kubernetes
  `Deployment`'s own `Recreate` strategy (full stop, then restart) exactly.
  Simpler than `RollingUpdate`, at the cost of a window with fewer than
  `replicas` Machines running during every template change.

Changing `spec.template` never touches an already-running replica directly
-- it only changes what *new* replicas look like; existing ones are
replaced (not edited in place) by the rollout logic above.

## Real limits today (first cut)

- **Gated on `status.phase == Running`, not on deeper application health.**
  `RollingUpdate` now withholds further replacement until each already-
  created current-template replica reaches `Running` (see above) -- but
  that's Kairon's own coarse VM-power-state signal, not a guest-level
  readiness probe (nothing here waits for a guest agent heartbeat, an HTTP
  health check, or any other application-defined signal). A template with
  a boot failure severe enough to never reach `Running` correctly stalls
  the rollout in place rather than cycling every replica through it; one
  that boots fine but is application-broken is not caught here.
- **No `maxSurge`.** A rollout never temporarily exceeds `spec.replicas`
  -- only `maxUnavailable` (a dip below, never a surge above) is offered,
  since Kairon's scheduler (`internal/scheduler`) has no capacity/
  overcommit model to safely reason about a temporary surge against yet.
- **No autoscaling.** `spec.replicas` is only ever changed by you (or your
  own tooling) editing the object -- there's no `MachineHorizontalAutoscaler`
  or similar reacting to load.
- **No pause/rollback primitive.** There's no `kubectl rollout
  pause`/`undo` equivalent -- to stop a rollout partway, edit
  `spec.template` back to the previous shape yourself; the same
  incremental reconciliation resumes toward whatever `spec.template` says
  right now.
- **Replica names are randomly suffixed** (`<machineset-name>-<8 hex
  chars>`), not sequential/predictable -- don't script against a specific
  replica's name surviving a rollout.
