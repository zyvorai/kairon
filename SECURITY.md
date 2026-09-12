# Security policy

Kairon is Apache-2.0 open-source software from [Zyvor](https://zyvor.dev). It controls host-level VM execution. Treat the controller, node agents, FluxVM and any migration adapter as privileged infrastructure even when the Kairon process itself runs as a non-root container.

## Reporting a vulnerability

Please report security issues privately to **security@zyvor.dev**. Do not open a public GitHub issue for unfixed vulnerabilities.

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

`--image-root` constrains Machine image paths. Keep VM image directories non-writable by untrusted workloads. Signed-image policy is not yet implemented. CI builds all three container images and runs `govulncheck` against the Go dependency graph on every push, but does not publish images and does not scan the built image layers themselves (no Trivy/Grype/cosign/SBOM) -- whatever process publishes to `ghcr.io/zyvorai/kairon-*` today is outside this repository's CI, and nothing here proves what bits actually land there.

## `kairon-ui` (dashboard)

Auth is a single static, non-expiring bearer token, compared in constant time (`crypto/subtle.ConstantTimeCompare`) -- fail-closed by default (both the Helm chart's `{{ fail }}` guard and the binary's own startup check refuse to run without a token unless `ui.allowUnauthenticated`/`-allow-unauthenticated` is explicitly set for local development). Every mutating request is logged (method, path, remote address, status), including rejected-auth attempts.

**This token is shared by every operator.** There is no per-user identity, no session expiry, and no rotation short of a manual `helm upgrade --set ui.token=...` (or updating the Secret `ui.existingSecret` points at) followed by a redeploy. The request log above records *that* a destructive action (delete/evacuate/recover) happened, not *who* did it. Treat the token like a shared root password: distribute it only to operators you'd trust with direct `kubectl` access to Kairon's own CRDs, and rotate it if that trust set ever shrinks. Real per-operator attribution needs a different auth model (separate tokens or OIDC) and is not implemented.

The dashboard's ClusterRole grants exactly the verbs its handlers use (`create`/`delete`/`patch` on Machines, `create`/`patch` on MachineMigrations, `get`/`list`/`watch` elsewhere) -- it cannot do anything through the Kubernetes API that isn't already reachable through `kaironctl` with the same privilege level.
