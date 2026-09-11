# Security

Kairon controls host-level VM execution. Treat the controller, node agents, FluxVM and any migration adapter as privileged infrastructure even when the Kairon process itself runs as a non-root container.

## v0.3 migration security

- Users cannot submit a raw QEMU/TCP/exec migration destination. The target is a Kubernetes node selected by Kairon.
- Node-to-node migration control uses TLS 1.3 and `RequireAndVerifyClientCert`.
- Peer API responses use `Cache-Control: no-store`.
- The destination session ID is deterministic but contains no secret. Transfer endpoints remain inside the mTLS peer exchange and are not copied into CRD status.
- Destination session files are mode `0600` and written using fsync + atomic rename.
- Reusing a session ID with different immutable VM/source/target identity returns HTTP 409.
- A target that has no adapter returns `Unsupported`; the source VM is not touched.
- Source transfer failure aborts the prepared target.
- Commit ambiguity produces `NeedsRecovery`; Kairon does not automatically restart/cut over and risk split brain.
- The post-commit adopt-only guard refuses to create a fresh target runtime if the expected incoming VM cannot be found.

The baseline Helm deployment uses a shared cluster migration certificate with both server/client authentication EKUs for operational simplicity. For stronger node identity and independent key rotation, use per-node credentials or SPIFFE-style workload identity and configure the expected server name appropriately.

## DRA / VFIO

A `ResourceClaim` must be allocated before Kairon considers it. Resolved PCI BDFs are syntax checked and must be present in the node administrator's explicit allowlist. Empty allowlists deny passthrough.

## Images

`--image-root` constrains Machine image paths. Keep VM image directories non-writable by untrusted workloads. Signed-image policy is not yet implemented.

## Reporting

Report security issues privately to the Zyvor project maintainers rather than opening a public exploit issue.
