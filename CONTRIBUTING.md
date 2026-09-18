# Contributing

Kairon is Apache License 2.0 open source from [Zyvor](https://zyvor.dev).

1. Fork the repository and create a focused branch.
2. Run `make all` before opening a pull request. This now also runs `golangci-lint` (install it locally: https://golangci-lint.run/welcome/install/), the race detector, and a coverage floor (`internal/...` must stay at or above `COVERAGE_THRESHOLD`, currently 50%).
3. Add tests for scheduler, API, reconciliation or parsing changes.
4. Touching `web/` (the `kairon-ui` dashboard)? Run `npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build` -- the same three steps CI's `web` job runs. `make all` doesn't build the frontend, so this is a separate step.
5. Keep the control plane dependency-light and never add a libvirt/KubeVirt runtime dependency.
6. Document API behavior changes in `docs/` and update examples.
7. By contributing, you agree that your contributions are licensed under the Apache License, Version 2.0.

Commits should be small and reviewable. New features should prefer declarative API fields and idempotent reconciliation over imperative one-shot operations.

Questions: https://zyvor.dev · security: security@zyvor.dev

## Local development

```bash
make all        # fmt · vet · lint · test-race · cover-check · build · validate · smoke
make test-race
./scripts/must-gather.sh
```

CI also runs `python3 scripts/check_rbac_coverage.py` (needs `helm` on `PATH`; not part of `make all` for that reason) — it cross-checks every `internal/kube.Client` call `kairon-controller`/`kairon-node`/`kairon-ui` actually make against what their own rendered ClusterRole/Role really grants, the same way a real, previously-silent 403 (`kairon-ui`'s dashboard routes, then `kairon-controller`'s `MachineSnapshotSchedule` retention pruning) was found by hand twice before this existed. `internal/kube`'s own test doubles never enforce RBAC, so this is the only check in this repo that would have caught either one.

Runtime code uses the **Go standard library only** — no `client-go`, no generated deep stacks. That's a deliberate constraint, not an oversight: it keeps the control plane small enough to actually audit, and it's why the admission webhook hand-rolls the small, stable `AdmissionReview` JSON shape (`internal/admission`) instead of pulling in `k8s.io/api`. Two deliberate exceptions: `kairon-ui`'s opt-in OIDC/SSO (`golang.org/x/oauth2`, `github.com/coreos/go-oidc/v3`) and Kairon's own opt-in CSI node plugin, `kairon-csi-node` (`google.golang.org/grpc`, `github.com/container-storage-interface/spec` — the CSI protocol has no stdlib-only wire format at all). `kairon-node` also links `google.golang.org/grpc` to dial `kairon-csi-node` as a client, exercised only when a Machine uses a CSI-backed volume; `kairon-controller`/`kaironctl` pull in none of this either way. See [SECURITY.md](SECURITY.md).

Touching the dashboard (`web/`)? `make all` doesn't build the frontend — run its own checks:

```bash
npm --prefix web run typecheck
npm --prefix web test
npm --prefix web run build
```
