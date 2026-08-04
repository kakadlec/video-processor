## Why

`TestMain` in `main_test.go` calls `os.Exit(0)` when `exec.LookPath("ffmpeg")` fails — the entire integration suite is silently skipped and `go test ./...` exits with code 0. This is a false green: the process reports success, but zero tests ran. CI installs `ffmpeg` before running tests, so the false green never surfaces there; but any developer or environment without `ffmpeg` installed sees a passing `go test` run that exercised nothing.

The fix is a one-line change: replace `os.Exit(0)` with `os.Exit(1)` and update the message to English, making "ffmpeg not found" an explicit failure rather than a silent skip. This is quality maintenance — not a new feature, not an architectural phase, and not a behavior change for any environment where `ffmpeg` is present.

## What Changes

- `main_test.go`: `TestMain` exits with code 1 (not 0) when `exec.LookPath("ffmpeg")` fails, with a clear English message. Tests in environments where `ffmpeg` is present are unchanged.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `development-workflow`: new requirement that a missing hard test prerequisite causes `go test ./...` to exit non-zero, not silently skip with exit code 0.

## Impact

- One-line change in `main_test.go`: `os.Exit(0)` → `os.Exit(1)` in the ffmpeg-absent branch of `TestMain`; Portuguese skip message replaced with an English error message.
- CI is unaffected: `ffmpeg` is already installed before `go test` runs in every CI job.
- Developers without `ffmpeg` will now see a clear failure instead of a silent pass — the correct behavior.
- The Docker fallback documented in `CLAUDE.md` (`docker build -t video-processor . && docker run --rm video-processor go test ./... -v`) remains the escape hatch for non-Linux environments, and its documentation does not change.
