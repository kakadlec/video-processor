## Why

CI's `SAST (gosec)` job has been red since it was introduced, blocking every PR (per `openspec/specs/development-workflow/spec.md`, 9 pre-existing findings were accepted as known debt at the time). Separately, Dependabot has 26 open alerts (several critical) against transitive dependencies pulled in by an outdated `gin` version. Both need to be resolved so the SAST gate can pass cleanly and the dependency tree is free of known vulnerabilities.

## What Changes

- Fix all 9 `gosec` findings in `main.go`:
  - **G204** (subprocess launched with variable, `ffmpeg` invocation): sanitize the uploaded filename so `videoPath` cannot contain path-traversal segments, then suppress with a justified `#nosec G204` comment since invoking `ffmpeg` on a server-controlled path is the app's core purpose.
  - **G304** (potential file inclusion via variable) x3 (`os.Create` for the saved upload, `os.Create` for the zip, `os.Open` in `addFileToZip`): the zip/glob paths are already fully server-derived (timestamp + internal temp dir), so suppress with justified `#nosec G304` comments; the upload path additionally gets the filename sanitized (shared fix with G204) to remove the actual traversal risk, not just silence the linter.
  - **G301** (directory permissions) x2 (`createDirs`, `processVideo`): change `os.MkdirAll` mode from `0755` to `0750`.
  - **G104** (unhandled errors) x3 (`os.MkdirAll` x2, `os.Remove`): log the error instead of discarding it; these are best-effort cleanup/setup calls where failure shouldn't abort the request, but must be observable.
- Resolve all 26 open Dependabot alerts (`golang.org/x/crypto`, `golang.org/x/net`, `google.golang.org/protobuf`), all transitive via `github.com/gin-gonic/gin v1.9.1`:
  - Upgrade `gin` to the latest `v1.12.x` and run `go mod tidy` / `go get -u` on the flagged transitive modules so `go.sum` only contains patched versions.
  - Re-run `govulncheck`/`gosec`/`go test` to confirm no regressions from the dependency bump.

## Capabilities

### New Capabilities
(none — this is remediation of existing behavior, not new user-facing capability)

### Modified Capabilities
- `development-workflow`: the "SAST Gate" requirement's known-findings caveat is removed — the codebase now has zero outstanding `gosec` findings, and a new requirement covers keeping dependencies free of known vulnerabilities.

## Impact

- `main.go`: filename sanitization, directory permission constants, error handling on cleanup calls, `#nosec` annotations with justifications.
- `go.mod` / `go.sum`: `gin` upgraded to latest v1.12.x; `golang.org/x/crypto`, `golang.org/x/net`, `google.golang.org/protobuf` bumped to patched versions via transitive resolution.
- No API/behavior changes visible to clients of `/upload`, `/download/:filename`, `/api/status`.
