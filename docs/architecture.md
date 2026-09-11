# Architecture

Kairon separates Kubernetes orchestration from VM execution. Kubernetes is the source of truth; FluxVM owns VMM process lifecycle.

## Components

- `kairon-controller`: schedules Machines, drives MachineMigration state, and reconciles MachineSnapshot objects into CSI VolumeSnapshots.
- `kairon-node`: one per virtualization node; reconciles assigned Machines into the node-local FluxVM endpoint, drives source-side FluxVM live migration, and resolves authorized DRA claims into VFIO BDFs.
- `kaironctl`: thin client over the Kubernetes API. It does not bypass the controllers.

## Cold migration

```text
Pending
  -> Stopping
     Machine.powerState=Stopped
  -> Restarting
     wait source status=Stopped
     Machine.nodeName=target
     Machine.powerState=Running
  -> Succeeded
     wait target status=Running
```

The state is represented in Kubernetes and is restart-safe. v0.2 does not copy host-local storage.

## Live migration

```text
Pending
  -> Starting
     validate target and tcp:host:port destination
  -> Running
     source kairon-node -> FluxVM migration/start + migration/status
  -> Cutover
     FluxVM reports completed
  -> Adopting
     controller moves Machine.nodeName to target
     controller sets kairon.zyvor.dev/adopt-only=true
     target node agent must discover the existing migrated runtime
  -> Succeeded
     controller removes adopt-only after target reports Running
```

The target agent cannot create a VM while the adopt-only annotation is set. A missing incoming runtime therefore becomes a visible blocked Machine rather than an accidental second boot.

v0.2 intentionally does not claim zero-touch live migration. The FluxVM contract used here has a verified source-side migration API; Kairon does not have a verified runtime endpoint for provisioning/authenticating the target incoming QEMU listener. The explicit `spec.destination` must therefore point at a target prepared outside Kairon.

## CSI snapshots

`Machine.spec.volumes[]` records PVC names for Kubernetes storage orchestration. A MachineSnapshot creates one `snapshot.storage.k8s.io/v1` VolumeSnapshot per declared PVC and projects each `readyToUse` state into MachineSnapshot status.

This is snapshot orchestration, not VM disk attachment. Arbitrary PVC -> FluxVM block-device attachment remains future work.

## DRA / VFIO

`Machine.spec.deviceClaims[]` references same-namespace `resource.k8s.io/v1` ResourceClaims. The node agent requires an allocation, extracts a PCI BDF from the claim annotation or a BDF-shaped allocation result, normalizes it, and checks it against the node's explicit allowlist. Only approved BDFs are passed to FluxVM `vfio_devices`.

The node-local allowlist is a privilege boundary: namespace users cannot request arbitrary host PCI functions merely by editing a claim annotation.
