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
