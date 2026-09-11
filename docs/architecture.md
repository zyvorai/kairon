# Architecture

Kairon separates orchestration from execution.

## Cluster controller

`kairon-controller` watches/list-polls `Machine` resources and Kubernetes Nodes. An unscheduled running Machine is assigned to the least-loaded Ready node matching:

- `kairon.zyvor.dev/capable=true`
- `spec.placement.architecture`
- `spec.placement.nodeSelector`

The assignment is persisted in `spec.nodeName`; this makes placement visible, auditable and deterministic.

## Node agent

`kairon-node` runs as a host-networked DaemonSet. It only reconciles Machines whose `spec.nodeName` is its own node. The agent talks to node-local FluxVM on `127.0.0.1:7788`.

For each Machine it:

1. installs the runtime-cleanup finalizer;
2. finds an existing FluxVM VM by `status.runtimeID` or deterministic runtime name;
3. creates the VM if desired state is Running and no runtime exists;
4. deletes the runtime if desired state is Stopped;
5. mirrors runtime ID, phase and guest IP into Machine status;
6. deletes the runtime before releasing the finalizer during Machine deletion.

## Why no wrapper Pod

A full VM consumes host KVM, storage and networking resources independently of a normal container process. Kairon models that directly. Kubernetes remains the desired-state API and authorization system, while the FluxVM process on the node is the execution layer.

This avoids coupling VM lifecycle to a per-VM launcher Pod. The tradeoff is that Kairon must explicitly implement scheduling accounting, node-failure fencing, migration and DRA integration; those are roadmap items rather than hidden behind a Pod abstraction.

## Control loop model

All operations are idempotent. The Kubernetes Machine object is source of truth. Runtime names use `kairon-<namespace>-<name>`, allowing recovery when status was lost but the VM still exists.

## Failure semantics in v0.1

- Controller restart: safe; placement is stored in `spec.nodeName`.
- Node-agent restart: safe; runtime lookup is name/idempotency based.
- FluxVM restart: agent retries reconciliation.
- Kubernetes API outage: existing VMs continue running.
- Node loss: **not automatically failed over in v0.1**. Automated fencing and migration are required before HA production use.
