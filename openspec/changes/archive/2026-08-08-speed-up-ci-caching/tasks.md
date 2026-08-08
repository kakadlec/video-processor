## 1. Pin tool versions

- [x] 1.1 In the `sast` job, pin `gosec` install to `github.com/securego/gosec/v2/cmd/gosec@v2.28.0` (current latest release) instead of `@latest`.
- [x] 1.2 In the `vulncheck` job, pin `govulncheck` install to `golang.org/x/vuln/cmd/govulncheck@v1.6.0` (current latest release) instead of `@latest`.

## 2. Cache tool binaries

- [x] 2.1 In the `sast` job, add an `actions/cache` step before the install step, caching path `~/go/bin/gosec` with key `${{ runner.os }}-gosec-v2.28.0`.
- [x] 2.2 Guard the `sast` job's `go install gosec` step with `if: steps.<cache-id>.outputs.cache-hit != 'true'` so it's skipped on a cache hit.
- [x] 2.3 In the `vulncheck` job, add an `actions/cache` step before the install step, caching path `~/go/bin/govulncheck` with key `${{ runner.os }}-govulncheck-v1.6.0`.
- [x] 2.4 Guard the `vulncheck` job's `go install govulncheck` step with `if: steps.<cache-id>.outputs.cache-hit != 'true'` so it's skipped on a cache hit.

## 3. Verify no regression in what's checked

- [x] 3.1 Confirm `gosec ./...` and `govulncheck ./...` still run unconditionally (only the install step is conditional) and still gate the job on failure, matching current behavior.
- [x] 3.2 Confirm the `test` job's module/build cache (via `actions/setup-go`'s default `cache: true`, keyed on `go.sum`) needs no change — verified against the current workflow and repo layout.

## 4. Validate in CI

- [x] 4.1 Open the implementation PR and confirm the `sast` and `vulncheck` jobs both pass on the first run (cache miss, populates the cache).
- [x] 4.2 Push a trivial follow-up commit to the same PR branch and confirm the second run hits the cache (steps 2.2/2.4 skipped) and both jobs still pass, with visibly reduced job duration versus the first run / a recent `main` run.
