## 1. Fix TestMain exit code

- [ ] 1.1 In `main_test.go`, change `os.Exit(0)` to `os.Exit(1)` in the `exec.LookPath("ffmpeg")` error branch of `TestMain`, and replace the Portuguese skip message with a clear English error message, e.g.:
  `"FATAL: ffmpeg not found in PATH — integration tests require ffmpeg; see CLAUDE.md for the Docker fallback."`

## 2. Verify

- [ ] 2.1 Run `go test ./... -v` in an environment with `ffmpeg` on PATH and confirm all tests still pass.
- [ ] 2.2 Confirm that running `go test ./...` without `ffmpeg` available exits with code 1 (verify via `echo $?` or equivalent after temporarily hiding the binary).
