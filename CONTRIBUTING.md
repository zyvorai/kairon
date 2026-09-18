# Contributing

Kairon is Apache License 2.0 open source from [Zyvor](https://zyvor.dev).

1. Fork the repository and create a focused branch.
2. Run `make all` before opening a pull request. That is `gofmt` check, `go vet`, `golangci-lint` (install it locally: https://golangci-lint.run/welcome/install/), race tests, a coverage floor (`internal/...` at or above `COVERAGE_THRESHOLD`, currently 50%), build/smoke, `scripts/validate.py`, RBAC coverage, and `helm lint`. The last two need `helm` and PyYAML.
3. Add tests for scheduler, API, reconciliation or parsing changes.
4. Touching `web/` (the `kairon-ui` dashboard)? Run `npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build` -- the same three steps CI's `web` job runs. `make all` doesn't build the frontend, so this is a separate step.
5. Keep the control plane dependency-light and never add a libvirt/KubeVirt runtime dependency. `kairon-controller` and `kairon-node` stay stdlib-only. Named exceptions are in [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md). Do not add the Cilium Go SDK; Cilium objects go through raw REST in `internal/kube`.
6. Document API behavior changes in `docs/` and update examples.
7. By contributing, you agree that your contributions are licensed under the Apache License, Version 2.0.

Commits should be small and reviewable. New features should prefer declarative API fields and idempotent reconciliation over imperative one-shot operations.

Questions: https://zyvor.dev · security: security@zyvor.dev

## Local development

```bash
make all        # fmt-check · vet · lint · test-race · cover-check · build · validate · rbac-coverage · helm-check · smoke
make test-race
./scripts/must-gather.sh
```

CI (`.github/workflows/ci.yml`) runs the same Go checks, plus Helm render of the default chart, migration, kairon-ui, and Cilium RBAC flags, `kaironctl network` help smoke, the website and dashboard builds, and a Trivy scan of every container image. `python3 scripts/check_rbac_coverage.py` needs `helm` on `PATH` — it cross-checks every `internal/kube.Client` call `kairon-controller`/`kairon-node`/`kairon-ui` actually make against what their own rendered ClusterRole/Role really grants.

Runtime code for the controller and node uses the **Go standard library only** — no `client-go`, no generated deep stacks. That's a deliberate constraint, not an oversight: it keeps the control plane small enough to actually audit, and it's why the admission webhook hand-rolls the small, stable `AdmissionReview` JSON shape (`internal/admission`) instead of pulling in `k8s.io/api`. Named exceptions live in [docs/DEPENDENCIES.md](docs/DEPENDENCIES.md): `kaironctl` (Cobra + Helm SDK), `kairon-ui` OIDC, and the CSI plugins. Cilium integration does not add a fourth — it uses raw REST.

Touching the dashboard (`web/`)? `make all` doesn't build the frontend — run its own checks:

```bash
npm --prefix web run typecheck
npm --prefix web test
npm --prefix web run build
```
