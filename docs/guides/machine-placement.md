# User guide: Machine placement

How `spec.placement` steers which node a Machine lands on, and what today's
real limits are.

## Fields

| Field | What it does |
|---|---|
| `architecture` | Only consider nodes whose `kubernetes.io/arch` label matches |
| `nodeSelector` | Only consider nodes matching every given label |
| `affinity` | Require co-location with at least one other Machine matching a selector |
| `antiAffinity` | Require separation from every other Machine matching a selector |

All four are filters evaluated at scheduling time by `internal/scheduler`
(`kairon-controller`'s least-loaded, deterministic-tie-break placement) --
none of them influence an already-scheduled Machine (`spec.nodeName` is set
once, at scheduling time, same as `spec.image`/`spec.resources`).

## Affinity and anti-affinity

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: cache
spec:
  placement:
    affinity:
      - labelSelector: {tier: web}
        topologyKey: kubernetes.io/hostname
    antiAffinity:
      - labelSelector: {role: db-primary}
        topologyKey: kubernetes.io/hostname
```

Each term is `{labelSelector, topologyKey}` — deliberately shaped like
Kubernetes Pod affinity terms, so it's immediately familiar:

- **`affinity`**: the candidate node is eligible only if at least one other
  Machine matching `labelSelector` is currently scheduled to a node sharing
  the candidate's value for the `topologyKey` label.
- **`antiAffinity`**: the candidate node is eligible only if **no** other
  Machine matching `labelSelector` shares the candidate's `topologyKey`
  value.
- `topologyKey` names any node label -- `kubernetes.io/hostname` for
  same/different-node, or a rack/zone/region label your nodes carry, for
  broader placement domains.
- A Machine never matches its own affinity/anti-affinity terms against
  itself (relevant when re-evaluating an already-scheduled Machine, e.g. as
  a live-migration target search).

## Real limits today (v1 of this feature)

- **Required (hard) constraints only.** There is no `preferred`/soft
  affinity, and no weighted scoring -- a term that can't be satisfied makes
  every node ineligible (the Machine goes `Pending` with a clear
  `no Ready Kairon-capable nodes match placement constraints` message), it
  doesn't just get deprioritized. Kairon's scheduler is "least-loaded with
  a deterministic tie-break," not a weighted scorer, so a soft-preference
  system doesn't exist to plug into yet.
- **No topology spread constraints.** Spreading a fleet evenly across N
  zones (Kubernetes' `topologySpreadConstraints`) needs the same kind of
  scoring/counting system preferred affinity would -- not implemented.
- **Evaluated against the reconcile-time Machine list, not a live watch.**
  A `Machine` scheduled in the same reconcile tick as the one it's meant to
  be co-located/separated from may not see that Machine's `spec.nodeName`
  yet (only set once its own scheduling completes) and fail with the same
  "no eligible nodes" message -- resolved automatically on the next
  reconcile tick (every `controller.interval`, 5s by default) once the
  other Machine has a node. This is a real, observed timing window, not a
  hypothetical.
