# Preemption

`spec.priority` still orders pending Machines. It halts a running Machine only when both sides opt in.

On the pending Machine:

```yaml
metadata:
  annotations:
    kairon.zyvor.dev/preempt: "true"
spec:
  priority: 10
```

On a Machine that may be halted:

```yaml
metadata:
  annotations:
    kairon.zyvor.dev/preemption-policy: Halt
spec:
  priority: 0
```

Rules:

- Same namespace. A victim in another namespace is ignored.
- Victim priority must be strictly lower.
- Victim must already be scheduled and desired Running.
- `Pause` is rejected. A Paused Machine still counts in node load, so it would not free the capacity the preemptor is waiting on. Halted does, and it also drops out of MachineQuota.
- One victim per preemptor per tick. The lowest priority eligible victim wins. Ties break by name.
- A non-terminal MachineMigration or a MachineDisruptionBudget with no allowance refuses the victim.
- The controller writes `kairon.zyvor.dev/preempted-by` and restores `powerState: Running` when the preemptor is gone, Halted, Stopped, or no longer pending and unscheduled.

Placement is retried on the next tick, after the halt is visible to the scheduler.
