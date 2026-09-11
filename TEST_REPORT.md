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
