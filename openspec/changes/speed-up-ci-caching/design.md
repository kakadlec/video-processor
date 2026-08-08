## Context

`.github/workflows/ci.yml` has three parallel jobs (`test`, `sast`, `vulncheck`). `actions/setup-go@v7` already caches the Go module/build cache by default (keyed off `go.sum`), which helps `test`, but it does not cache compiled tool binaries under `GOPATH/bin` — so `sast` and `vulncheck` rebuild `gosec`/`govulncheck` from source on every single run via `go install <tool>@latest`. gosec has a much larger dependency tree than govulncheck, so this is almost certainly the dominant cost in the `sast` job specifically.

## Goals / Non-Goals

**Goals:**
- Cut the wall-clock cost of the `sast` and `vulncheck` jobs by skipping `go install` when the pinned tool version is already cached.
- Pin `gosec` and `govulncheck` to specific released versions instead of `@latest`, for reproducibility and as the cache-key prerequisite.
- Leave what gosec/govulncheck actually scan, their arguments, and their gating behavior (fail-the-build-on-any-finding) completely unchanged.

**Non-Goals:**
- Changing the `test` job's caching — it already gets module/build caching for free from `actions/setup-go`'s default `cache: true`; verified against `go.sum` at the repo root, no gap found.
- Adopting a policy for how/when to bump the pinned tool versions going forward (e.g. Dependabot/Renovate for GitHub Actions or a scheduled bump) — worth a follow-up, out of scope here.
- Revisiting the project's existing decision (recorded in `openspec/changes/archive/2026-07-28-add-ci-testing-and-sast/design.md`) to avoid third-party GitHub Actions for security tooling.

## Decisions

**Cache the `go install`ed binary via `actions/cache`, keep installing via `go install` from the tool's own official module path.**
Alternatives considered:
- *Switch `sast` to the official `securego/gosec` Docker container action* (`securego/gosec@v2.28.0`, a Docker action published from the gosec repo itself, pulling a prebuilt `ghcr.io/securego/gosec` image instead of compiling from source). This would likely be faster than even a binary-cache hit, and is officially maintained by the gosec project. Rejected for this change: the previous SAST design doc explicitly chose `go install` over any marketplace/Docker Action specifically to minimize the trust surface for security tooling ("one fewer intermediary to trust"), and that reasoning still holds — `go install ...@<pinned-version>` still resolves straight from `github.com/securego/gosec`'s own module path via the Go module proxy/checksum database, with no additional Action or container registry in the chain. Revisiting that trust trade-off is a separate decision from "make CI faster," so it's left alone here. `actions/cache` (a first-party GitHub Action already implicitly trusted via `actions/checkout`/`actions/setup-go` in this same workflow) achieves comparable speedup without expanding that surface.
- *`golang/govulncheck-action`* (official, maintained by the Go team). Inspected its `action.yml`: it still runs `go install golang.org/x/vuln/cmd/govulncheck@latest` internally on every invocation — its `cache: true` input only wires up `actions/setup-go`'s module/build cache, not a cached tool binary. So adopting it would not actually solve the problem this change targets (rebuilding the tool binary every run), while also giving up the explicit version pin. Not adopted.
- *`golangci-lint-action`-style bundled tool caching*: no equivalent maintained action exists for gosec/govulncheck that caches the built binary; `actions/cache` applied directly to `GOPATH/bin` is the standard, well-documented pattern for this.

**Cache key: `${{ runner.os }}-<tool>-v<pinned-version>`, cache path `~/go/bin/<tool>`.**
`~/go/bin` is where `go install` puts binaries by default on the `ubuntu-latest` runner (unset `GOBIN`, default `GOPATH=~/go`) — the current workflow already relies on this path being on `PATH` (post-`actions/setup-go`) since `gosec`/`govulncheck` run unqualified today. Keying strictly on the pinned version means a version bump automatically invalidates the cache and forces a fresh install; no separate cache-busting mechanism needed.

**Skip `go install` on cache hit via `if: steps.<cache-id>.outputs.cache-hit != 'true'`.**
Standard `actions/cache` conditional-step pattern. `actions/cache` restoring a hit does not, by itself, prevent the subsequent `run: go install ...` step from executing — the `if:` guard is what makes it a no-op.

## Risks / Trade-offs

- [First run after this change (or after any future version bump) still pays the full `go install` cost once, to populate the new cache entry] → Expected and acceptable; every run after that on the same pinned version is a cache hit.
- [Pinned versions go stale unless someone bumps them] → Same maintenance burden as any other pinned dependency in this repo (already true for `go.mod` dependencies); a version bump is a one-line change to the pinned version string, verified by the workflow's own `sast`/`vulncheck` job results.
- [`actions/cache` has per-repository storage limits and eviction under pressure] → Two small tool-binary caches are negligible relative to typical Actions cache quotas; not a practical concern for this repo's scale.

## Open Questions

- Whether to automate future version bumps (Dependabot/Renovate for `go install` version pins embedded in workflow YAML isn't natively supported the way it is for `uses:` Action refs) — deferred; manual bumps are fine at this repo's current pace of change.
