# OpenSSF Best Practices badge

Kairon is registered as project
[15141](https://www.bestpractices.dev/projects/15141) on
[OpenSSF Best Practices](https://www.bestpractices.dev/). Scorecard's
`CII-Best-Practices` check reads that entry: in progress → 2, passing → 5,
silver → 7, gold → 10.

## Where the evidence lives

| Criteria area | Evidence |
|---------------|----------|
| Description, obtain, feedback | [`README.md`](../README.md), [GitHub Issues](https://github.com/zyvorai/kairon/issues), [Releases](https://github.com/zyvorai/kairon/releases) |
| Contribution process + requirements | [`CONTRIBUTING.md`](../CONTRIBUTING.md) (PR process, coding standard, test policy) |
| License | [`LICENSE`](../LICENSE) (Apache-2.0) |
| Interface docs | [`docs/CLI.md`](CLI.md), [`docs/guides/`](guides/), CRDs in [`charts/kairon`](../charts/kairon) |
| Releases / notes | SemVer tags `vX.Y.Z`, [`RELEASE_NOTES.md`](../RELEASE_NOTES.md) |
| Vulnerability reporting | [`SECURITY.md`](../SECURITY.md) (private email + GitHub advisories, 14-day ack) |
| Build / test / CI | `make all`, [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) |
| Warnings / static analysis | `go vet`, `golangci-lint` ([`.golangci.yml`](../.golangci.yml)), CodeQL, `govulncheck`, Trivy |
| Dynamic analysis | `go test -race`, native Go fuzzing ([`.github/workflows/fuzz.yml`](../.github/workflows/fuzz.yml)) |
| Signed delivery | cosign keyless signatures + SBOM attestations ([`release.yml`](../.github/workflows/release.yml)), `SHA256SUMS` |

## Maintained check

Scorecard `Maintained` stays at 0 until the repository is **older than 90
days** (created 2026-09-11 → eligible ~2026-12-10), then needs roughly
weekly commits. No code change can advance that calendar.
