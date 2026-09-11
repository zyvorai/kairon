# Test report

Generated and verified in the ChatGPT build environment on 2026-09-11.

## Passed locally

- `make all` — PASS
  - `gofmt`
  - `go vet ./...`
  - `go test ./...`
  - static `CGO_ENABLED=0` builds of `kairon-controller`, `kairon-node`, and `kaironctl`
  - repository + Kubernetes YAML validation
- `go test -race -timeout 60s ./internal/...` — PASS
- `git diff --cached --check` — PASS

## Reconcile tests

- scheduler selects a Ready Kairon-capable least-loaded node
- controller patches `spec.nodeName` through a mock Kubernetes API
- node agent performs Machine -> FluxVM lookup/create -> Machine status reconciliation
- image-root policy rejects host paths outside the configured image directory
- FluxVM DELETE is idempotent when the runtime is already absent

## Coverage snapshot

Coverage is intentionally focused on core control-loop code. See package-level output below.

```text
ok  	github.com/zyvorai/kairon/internal/agent	0.049s	coverage: 50.0% of statements
ok  	github.com/zyvorai/kairon/internal/controller	(cached)	coverage: 46.9% of statements
ok  	github.com/zyvorai/kairon/internal/fluxvm	0.040s	coverage: 54.9% of statements
ok  	github.com/zyvorai/kairon/internal/health	(cached)	coverage: 42.1% of statements
ok  	github.com/zyvorai/kairon/internal/kube	(cached)	coverage: 38.6% of statements
ok  	github.com/zyvorai/kairon/internal/model	(cached)	coverage: 54.1% of statements
ok  	github.com/zyvorai/kairon/internal/scheduler	(cached)	coverage: 77.1% of statements
```

## Not executed in this sandbox

- real Kubernetes control-plane conformance
- real KVM/FluxVM VM boot or live-migration tests
- Docker image build (Docker is unavailable here)
- Helm render/install test (Helm is unavailable here)

Those are wired into the repository structure/CI roadmap, but they should not be represented as locally verified.
