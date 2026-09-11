# Kairon v0.2.0 test report

Generated from the release candidate in this repository.

## Release gates

- `make all` — PASS
  - gofmt check
  - `go vet ./...`
  - `go test ./...`
  - static builds for `kairon-controller`, `kairon-node`, and `kaironctl`
  - Kubernetes/repository manifest validation
  - version smoke tests for all three binaries
- `go test -race ./...` — PASS
- `go test -cover ./...` — PASS

## Package coverage

- `internal/agent`: 63.6%
- `internal/controller`: 55.7%
- `internal/fluxvm`: 54.0%
- `internal/health`: 42.1%
- `internal/kube`: 26.7%
- `internal/model`: 45.5%
- `internal/scheduler`: 77.1%

## v0.2 behavior directly exercised

- adopt-only target guard refuses duplicate VM creation
- allocated DRA ResourceClaim -> normalized/allowlisted PCI BDF -> FluxVM `vfio_devices`
- missing VFIO allowlist fails closed
- migration URI accepts only valid `tcp:host:port`
- source node calls FluxVM migration start contract and projects completion
- controlled cold migration stops the source before reassignment and restarts only after Stopped
- controller live cutover sets target assignment plus adopt-only guard
- MachineSnapshot creates a standard CSI VolumeSnapshot and projects ready state
- existing VM lifecycle, image-root traversal rejection, scheduling, FluxVM mapping, health, quantity parsing

## Environment boundary

The tests use deterministic HTTP test servers for Kubernetes and FluxVM API contracts. They do not execute KVM/QEMU, a real CSI driver, or physical VFIO hardware in this build environment. End-to-end live migration still requires a compatible incoming QEMU target prepared before the source migration request, as documented in README.md.
