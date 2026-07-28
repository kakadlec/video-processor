## Why

`main.go` has zero test coverage and is about to go through a full refactor. Without tests, there's no way to tell whether the refactor preserved behavior or silently broke something (frame extraction, zip contents, error responses). We need a safety net first, and since the current code is one large, untestable-in-isolation file (HTTP handling, `ffmpeg` invocation, and filesystem writes all mixed together), unit tests aren't realistic yet — black-box integration tests against the real running server are the pragmatic first step.

## What Changes

- Extract the Gin router/route setup out of `main()` into a `setupRouter() *gin.Engine` function, so tests can exercise the real handlers in-process without starting a blocking server on `:8080`. This is a mechanical extraction only — no behavior change.
- Add `main_test.go` with integration tests that drive the app through `net/http/httptest`, covering the current observable behavior of the upload → extract → zip → download → status pipeline.
- Tests generate their own tiny synthetic video via `ffmpeg`'s `testsrc` filter at run time (no binary video fixture committed to the repo) and skip with a clear message if `ffmpeg` isn't on `PATH`.
- Document the `go test ./...` command in `CLAUDE.md`.

## Capabilities

### New Capabilities
- `video-frame-extraction`: first-time spec for the existing upload → ffmpeg frame extraction (1fps) → zip → download/status behavior. No specs exist yet for this project; this change captures current behavior as the baseline before refactor work changes it.

### Modified Capabilities
(none — this change only adds tests and a non-behavioral extraction, no existing spec to modify)

## Impact

- `main.go`: route registration moves into a new `setupRouter()` function; `main()` calls it. No handler logic changes.
- New file `main_test.go`.
- `CLAUDE.md`: add `go test ./...` to the commands list.
- Runtime dependency: tests require `ffmpeg` on `PATH` (same requirement the app already has to run at all).
