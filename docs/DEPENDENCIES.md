---
sidebar_position: 6
title: Dependency policy
---

# Dependency policy

Kairon’s control plane is intentionally tiny. Dependencies are allowed only where the alternative is hand-rolling cryptography, gRPC/CSI, or a full Helm engine — and every exception is named here.

## Stdlib-only (hard rule)

| Binary | Rule |
|--------|------|
| `kairon-controller` | Go standard library only |
| `kairon-node` | Go standard library only |

These two are the trust boundary for host-level VM execution. They must stay readable and free of `client-go` / operator frameworks. CI and review should reject new third-party imports in `cmd/kairon-controller`, `cmd/kairon-node`, and the packages only they use for reconcile (`internal/controller`, `internal/agent`, …) unless an ADR explicitly expands this table.

Cilium integration (ExternalWorkload attach, CNP sync) uses **raw REST** against `apis/cilium.io/...` via `internal/kube` — the same pattern as other non-Kairon APIs. Do **not** add the Cilium Go SDK to controller/node.

The README “Go-stdlib only” badge refers to **this** control-plane surface, not every binary in the repo.

## Named exceptions

| Component | Extra deps | Why |
|-----------|------------|-----|
| `kaironctl` / `kubectl-kairon` | `github.com/spf13/cobra`, `helm.sh/helm/v3` (+ transitive `client-go` for Helm only) | Hierarchical CLI help; install/upgrade/uninstall of the embedded chart without requiring a separate `helm` binary or chart checkout |
| `kairon-ui` (OIDC) | `golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3` | Real JWT/JWK verification — not something we hand-roll. Opt-in via `ui.oidc.enabled` |
| `kairon-csi-*` | `github.com/container-storage-interface/spec`, gRPC | CSI is a gRPC contract; opt-in via `csiNode.enabled` |

Optional UI features (websocket console, Prometheus metrics client) may pull small libraries already listed in `go.mod`; they do not relax the controller/node rule.

## What “embedded Helm” means for the CLI

`kaironctl install` defaults to the chart baked into the binary (`charts` package via `go:embed`) and drives install/upgrade/uninstall through the Helm v3 Go SDK. Operators can still pass `--chart ./charts/kairon` or `--helm-cli` to shell out to a system Helm 3 binary. Dry-run renders manifests offline (no cluster required).

Helm’s SDK imports `github.com/containerd/containerd` and `oras.land/oras-go` for OCI chart pulls, plus a slice of `golang.org/x/crypto` that the UI’s bcrypt path does not use. `govulncheck` in CI covers every other package. It does not fail the build on those Helm-only call graphs: several of the advisories have no upstream fix, and none of them are linked into `kairon-controller`, `kairon-node`, `kairon-ui`, or the CSI images.

## Review checklist

- New import in controller/node path? **Nack** unless this doc is updated first.
- New CLI-only dependency? OK if confined to `internal/kaironctl` / `cmd/kaironctl` / `cmd/kubectl-kairon` and listed above.
- Do not “fix” Aether, KubeVirt operator frameworks, or unrestricted `client-go` into the control plane to make the CLI easier.
