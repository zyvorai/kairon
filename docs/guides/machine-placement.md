# User guide: Machine placement

How `spec.placement` steers which node a Machine lands on, and what today's
real limits are.

## Fields

| Field | What it does | Hard or soft |
|---|---|---|
| `architecture` | Only consider nodes whose `kubernetes.io/arch` label matches | Hard |
| `nodeSelector` | Only consider nodes matching every given label | Hard |
| `affinity` | Require co-location with at least one other Machine matching a selector | Hard |
| `antiAffinity` | Require separation from every other Machine matching a selector | Hard |
| `preferredAffinity` | Weighted preference to co-locate with a matching Machine | Soft |
| `preferredAntiAffinity` | Weighted preference to separate from a matching Machine | Soft |
| `topologySpreadConstraints` | Weighted preference to spread matching Machines evenly across a topology label's domains; hard-caps skew too if `whenUnsatisfiable: DoNotSchedule` | Soft, or hard per-constraint |

The first four fields are always-hard filters evaluated at scheduling time
by `internal/scheduler`'s `eligible` check: a node failing one is never a
candidate at all. `preferredAffinity`/`preferredAntiAffinity` never reject
a node -- they only influence which *eligible* node wins, via a real
weighted scoring pass (`internal/scheduler.score`).
`topologySpreadConstraints` is scored the same way regardless, but each
constraint can *also* hard-filter (see below) if you ask it to. None of
these fields influence an already-scheduled Machine (`spec.nodeName` is
set once, at scheduling time, same as `spec.image`/`spec.resources`).

When every node is filtered out, the resulting error names which
constraint(s) actually did it -- aggregated per distinct reason across
every excluded node (e.g. `2 node(s): nodeSelector zone=us-east not
satisfied; 1 node(s): cpuPinning requests 8 vCPU(s), only 4 free`) --
rather than one undifferentiated "no nodes match" message, so debugging a
stuck `Machine` doesn't require guessing which of `architecture`/
`nodeSelector`/affinity/anti-affinity/`cpuPinning` capacity is the actual
blocker.

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

## Preferred (soft) affinity and topology spread

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: cache
spec:
  placement:
    preferredAffinity:
      - weight: 20
        labelSelector: {tier: web}
        topologyKey: kubernetes.io/hostname
    preferredAntiAffinity:
      - weight: 10
        labelSelector: {role: db-primary}
        topologyKey: kubernetes.io/hostname
    topologySpreadConstraints:
      - topologyKey: topology.kubernetes.io/zone
        labelSelector: {tier: web}
```

Each eligible node's score starts at `-(Machines already assigned to it)`
(the whole of the old load-balancing behavior) and then:

- `preferredAffinity`/`preferredAntiAffinity` add/subtract that term's
  `weight` when satisfied (same `{labelSelector, topologyKey}` semantics
  as the hard fields above, just non-rejecting).
- `topologySpreadConstraints` subtracts however many other Machines
  matching `labelSelector` are already in that node's `topologyKey`
  domain -- more existing matches there, lower score, favoring the
  emptier domain.
- The highest-scoring eligible node wins; a Machine with none of these
  three fields set schedules identically to before this scoring pass
  existed (least-loaded, deterministic hash tie-break on exact ties).

There's no fixed range for `weight` -- it's compared directly against the
1-point-per-Machine load penalty, so pick it relative to how much load
imbalance you want the preference to be able to outweigh (weight 20
comfortably beats several Machines' worth of imbalance; weight 1 only
matters when load is already tied).

## Hard-enforcing `topologySpreadConstraints.maxSkew`

```yaml
    topologySpreadConstraints:
      - topologyKey: topology.kubernetes.io/zone
        labelSelector: {tier: web}
        maxSkew: 1
        whenUnsatisfiable: DoNotSchedule
```

`whenUnsatisfiable: DoNotSchedule` (mirroring the Kubernetes field exactly)
turns this one constraint into a real filter, on top of the scoring it
already gets: a candidate node is dropped outright if placing this
Machine there would push that domain's skew (the matching-Machine count in
that domain versus the least-populated domain among candidates) over
`maxSkew`. Every domain still eligible after *all* `DoNotSchedule`
constraints are applied moves on to the same weighted scoring as before;
`Choose` returns an error if none are left. Omitting `whenUnsatisfiable`,
or setting it to `ScheduleAnyway` (the default, and every
`topologySpreadConstraints` entry before this field existed), never
rejects a node over skew -- `maxSkew` stays purely informational for
scoring, exactly as before.

`maxSkew`'s zero value (unset, same as writing `0` explicitly -- there's
no way to tell those apart through this field alone) is treated as a real
"domains must stay perfectly balanced," not as disabling enforcement --
the stricter of the two readings, deliberately, matching this project's
general fail-closed posture elsewhere. A real Kubernetes cluster's API
server would reject `maxSkew: 0` outright as invalid; Kairon has no
equivalent admission-time field validation to hook a rejection into, so
if you meant to write a positive number, write one.

## Scheduling priority

```yaml
apiVersion: kairon.zyvor.dev/v1alpha1
kind: Machine
metadata:
  name: urgent-vm
spec:
  priority: 10
```

Every field above steers *which node* a Machine lands on. `spec.priority`
answers a different question: *when several Machines are all still
unscheduled at once and can't all fit, which gets tried first?*

Every reconcile tick, `kairon-controller` collects every Machine that isn't
scheduled yet (no `spec.nodeName`), sorts that list by `spec.priority`
descending, then runs each one through placement (this page's own
eligibility/scoring pass) and, if a node is found, `MachineQuota` admission
-- in that order. A higher-`priority` Machine is attempted, and can claim
scarce node capacity or the last unit of `MachineQuota` headroom, before a
lower-priority one — even one that was created earlier, or that the API
happened to list first.

There's no fixed range for `priority`, same as `preferredAffinity`'s own
`weight` — pick any integer, positive or negative; the default is `0`. Ties
(the overwhelming common case: every Machine that doesn't set this field)
keep whatever order they'd have had anyway — the sort used is stable, so a
fleet that never sets `priority` schedules in exactly the order it always
did.

```
kaironctl create urgent-vm --image /base.qcow2 --priority 10
kaironctl create machineset web --image /base.qcow2 --replicas 5 --priority 5
kaironctl edit machine urgent-vm --priority 20
```

`kaironctl create`/`create machineset` both take `--priority N` (a
MachineSet's replicas all inherit its template's priority); `kaironctl edit
machine NAME --priority N` changes it on an existing Machine without a
delete/recreate — the one Machine-spec field this project's `edit` verb
supports patching after creation, since it only ever affects a *future*
tick's admission order.

**This is not preemption.** A high-`priority` Machine created after a
lower-`priority` one is already running never evicts, migrates, or
otherwise disturbs it — `priority` only ever orders Machines that are
*already* competing to be scheduled in the same tick, not Machines that
already won a previous tick. It also never changes *which* eligible node a
Machine lands on — that's still this page's own load/affinity/topology-spread
scoring, untouched. If you need a busy fleet to actively make room for a
new high-priority Machine by evicting a running lower-priority one, that's
real preemption — a materially riskier mechanism (choosing what to kill,
draining it cleanly, handling the case where nothing suitable exists to
evict) this project doesn't implement yet; this is a smaller, safer first
cut that solves the much more common "a burst of new Machines exceeds
capacity, who goes first" problem without it.

## Real limits today

- **DRA topology-awareness is a best-effort hint, not an allocation
  decision.** `kairon-controller` has no role in DRA device allocation
  itself -- for a Machine with `spec.deviceClaims`, it only reads back an
  already-allocated `ResourceClaim`'s device pool and, if that pool maps to
  a specific node via a `ResourceSlice`, adds a small fixed score bonus
  toward that node. An unallocated claim, or a pool with no single owning
  node (a network-attached device pool), contributes no hint at all --
  scheduling falls back to the other signals.
- **Evaluated against the reconcile-time Machine list, not a live watch.**
  A `Machine` scheduled in the same reconcile tick as the one it's meant to
  be co-located/separated from may not see that Machine's `spec.nodeName`
  yet (only set once its own scheduling completes) and fail with the same
  "no eligible nodes" message -- resolved automatically on the next
  reconcile tick (every `controller.interval`, 5s by default) once the
  other Machine has a node. This is a real, observed timing window, not a
  hypothetical -- it applies to `DoNotSchedule` topology-spread domain
  counts too, not just affinity/anti-affinity.
- **`DoNotSchedule` domains are only counted among candidate nodes, not
  every node in the cluster** -- a domain with matching Machines but no
  currently-eligible node in it (filtered out by `architecture`/
  `nodeSelector`/affinity/anti-affinity first) isn't part of the skew
  calculation at all, the same "candidate nodes matching the pod's own
  node affinity" scope real Kubernetes topology spread uses.
