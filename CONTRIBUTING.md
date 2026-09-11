# Contributing

1. Fork the repository and create a focused branch.
2. Run `make all` before opening a pull request.
3. Add tests for scheduler, API, reconciliation or parsing changes.
4. Keep the control plane dependency-light and never add a libvirt/KubeVirt runtime dependency.
5. Document API behavior changes in `docs/` and update examples.

Commits should be small and reviewable. New features should prefer declarative API fields and idempotent reconciliation over imperative one-shot operations.
