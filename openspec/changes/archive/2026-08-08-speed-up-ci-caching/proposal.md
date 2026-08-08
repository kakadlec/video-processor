## Why

CI is slow, and the `SAST (gosec)` job is the worst offender. Every run of the `sast` and `vulncheck` jobs does `go install <tool>@latest` from scratch — no version pin, no cache of the resulting binary — so each run re-downloads and recompiles gosec (a comparatively large dependency tree) and govulncheck from source, even though the tool versions rarely change between runs.

## What Changes

- Pin `gosec` and `govulncheck` to specific released versions (`v2.28.0` and `v1.6.0` respectively) instead of `@latest`, in `.github/workflows/ci.yml`. This is a prerequisite for a stable cache key and is good practice on its own (reproducible builds, consistent with how Dependabot findings are resolved per `CLAUDE.md`).
- Add an `actions/cache` step to the `sast` and `vulncheck` jobs, keyed on `runner.os` + tool name + pinned version, caching the `go install`ed binary (`~/go/bin/gosec`, `~/go/bin/govulncheck`). Skip the `go install` step when the cache already has the binary.
- No change to what either tool actually checks, its arguments, or its pass/fail behavior — `gosec ./...` and `govulncheck ./...` still run and still gate the build exactly as before.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `development-workflow`: the SAST and vulnerability-scan gates now install their tools at pinned, cached versions rather than always fetching `@latest`.

## Impact

- `.github/workflows/ci.yml`: `sast` and `vulncheck` jobs gain a cache step and a version pin each; `test` job is unchanged (its module/build cache via `actions/setup-go`'s default caching was verified to already be working — keyed off `go.sum`, present at repo root — so no change is needed there).
- No application code, dependency, or test changes.
- CI run time for `sast` (and to a lesser extent `vulncheck`) should drop materially on cache hits (i.e. every run after the first one that creates each cache entry); first run after this change pays the normal `go install` cost once to populate the cache.
