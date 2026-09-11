# Kairon v0.4.0 test report

## Result

**PASS.** `make all` (fmt/vet/lint/test-race/cover-check/build/validate/smoke) green on Go 1.27.1, `npm --prefix web run typecheck && test && build` green, `helm lint`/`helm template` green including the new `kairon-ui` fail-guard and resource assertions, and CI green on GitHub for every commit in this cycle (not just local checks).

## Commands executed

```text
make all
golangci-lint run ./...
npm --prefix web install && npm --prefix web run typecheck && npm --prefix web test && npm --prefix web run build
helm lint charts/kairon
helm template kairon charts/kairon --namespace kairon-system --set ui.enabled=true --set ui.token=...
```

## Coverage

```text
total (internal/...): 64.4% (threshold 50%)
```

## Real hardware verification (beyond source-level gates)

Unlike the v0.3.0 report below, this cycle's work was additionally verified against two real lab hosts, not just in-process fakes:

- `kairon-node` + `kairon-controller` deployed via `scripts/deploy-remote.sh` to two hosts, each running its own real single-node k3s cluster (`https://127.0.0.1:6443`), with the real CRDs/RBAC applied and real ServiceAccount tokens -- confirmed `kaironctl get machines` working end-to-end against a live Kubernetes API, not a fake one.
- `kairon-ui` deployed the same way (`--with-ui`), loaded in a real Chrome browser, and driven through Overview/Machines/Migrations pages including the `NeedsRecovery` recovery-form UI, with live `/api/v1/...` responses confirmed correct.
- Found and fixed two real bugs this way that no unit test would have caught: a `NetworkAwareDestination.Commit()`-adjacent reflect-based merge-patch fix that over-zeroed pointer fields (internal/integration test helper, not shipped code, but a real test-infra bug), and `deploy-remote.sh` redeploying to an already-running host silently keeping the *old* process alive (`systemctl enable --now` is a no-op on an active unit) while reporting success and printing a URL nothing was listening on -- fixed by switching to `restart`.

Real two-host **live migration** itself, and the `NeedsRecovery` drill, remain documented as runbooks (`docs/runbook-multi-host-migration-test.md`, `docs/runbook-recovery-drill.md`) but were not executed this cycle -- see those runbooks' own scope notes.

---

# Kairon v0.3.0 test report

## Result

**PASS** for the source-level release gate available in this build environment.

Validated on the final v0.3 source tree with Go 1.23 tooling.

## Commands executed

```text
make all
go mod tidy
go test -cover ./...
go clean -testcache
go test -race ./...
```

`make all` includes:

- `gofmt` cleanliness check
- `go vet ./...`
- `go test ./...`
- static `CGO_ENABLED=0` builds of `kairon-controller`, `kairon-node`, and `kaironctl`
- repository/CRD/raw-manifest validation
- version smoke tests for all three binaries

The local environment does not have the Helm binary. A Go-template-compatible render harness was therefore used to render `charts/kairon/templates/all.yaml` with migration disabled and enabled; both rendered outputs were parsed as Kubernetes YAML and the enabled output was checked for the migration TLS arguments, port, and volumes. GitHub Actions installs real Helm and runs `helm lint` plus both render modes.

## Tests

29 Go tests cover the current controller/runtime contract, including:

- Machine creation and FluxVM lifecycle mapping
- finalizer and adopt-only duplicate-boot protection
- image-root enforcement
- DRA allocation checks, PCI BDF normalization, and fail-closed VFIO allowlisting
- scheduling and controlled cold migration
- CSI `VolumeSnapshot` creation/readiness
- migration peer prepare idempotency, identity conflict detection, and local target-node binding
- mode-0600 atomic session persistence and path-traversal rejection
- TLS 1.3 server policy and mandatory client certificate enforcement
- target-first live migration ordering
- unsupported destination blocking before source transfer
- successful transfer -> target commit -> guarded cutover
- source-start failure -> prepared-target abort
- target-commit failure -> `NeedsRecovery`

## Coverage

```text
internal/agent       60.3%
internal/controller  57.4%
internal/fluxvm      55.3%
internal/health      42.1%
internal/kube        26.7%
internal/migration   53.9%
internal/model       45.5%
internal/scheduler   77.1%
```

Command packages are exercised through build/version smoke tests and currently report 0% statement coverage in `go test -cover` because they have no direct unit-test files.

## Important boundary

This environment does not execute KVM/QEMU, a real Kubernetes cluster, a real CSI driver, real DRA hardware, or a FluxVM live-migration backend. The current FluxVM repository does not expose a verified migration/QMP API, so v0.3 intentionally removes the guessed v0.2 FluxVM migration endpoints.

Kairon's target preparation, mTLS peer protocol, durable session state, rollback/fencing state machine, and migration-adapter contract are implemented and tested. **Actual VM memory-state live transfer requires a compatible Kairon migration adapter.** Without one, an explicit live migration is safely blocked before the source transfer begins. Cold migration remains implemented end-to-end at the controller/agent API-contract level.
