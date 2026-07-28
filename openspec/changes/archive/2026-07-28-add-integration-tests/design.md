## Context

`main.go` mixes HTTP routing, `ffmpeg` invocation, and filesystem I/O in a handful of top-level functions with no seams for mocking. A full unit-test-first refactor isn't realistic without first pinning down current behavior, so this design covers only the test harness needed to do that safely.

## Goals / Non-Goals

**Goals:**
- Exercise the real HTTP handlers (via Gin), the real `ffmpeg` binary, and the real filesystem — no mocking of any of these, since the point is to characterize actual behavior before refactor.
- Keep the harness self-contained: no committed binary video fixture, no new external test dependencies beyond `ffmpeg` (already a hard runtime requirement of the app).
- Make the minimum non-behavioral code change needed to make handlers testable in-process (route setup extraction).

**Non-Goals:**
- Unit-testing individual functions (`processVideo`, `createZipFile`, etc.) in isolation — that requires the refactor this test suite exists to protect, and comes later.
- Security testing (path traversal, zip-slip, etc.) — worth a follow-up change, out of scope here.
- CI wiring — no CI exists yet in this repo; adding one is a separate concern.
- Load/concurrency testing.

## Decisions

**In-process `httptest.NewServer` over spawning the real binary.**
Alternative considered: shell out to `go run main.go` as a subprocess per test (closer to what we did manually with Docker/curl). Rejected: slower, harder to synchronize startup, and harder to get clean per-test state. Extracting `setupRouter() *gin.Engine` and wrapping it with `httptest.NewServer` gives the same real-handler/real-ffmpeg/real-filesystem coverage with normal Go test ergonomics (`go test ./...`, table tests, `t.Cleanup`).

**Extract `setupRouter()` from `main()`.**
This is the one non-test code change. `main()` currently builds the engine and immediately calls the blocking `r.Run(":8080")` in the same function — there's no way to get a handle to the engine without starting a real listener on a fixed port (which would collide across parallel test runs and with a real dev server). The extraction is mechanical: move router/middleware/route registration into `setupRouter() *gin.Engine`, have `main()` call it and then `.Run(":8080")`. No handler logic changes.

**Generate the test video with `ffmpeg -f lavfi -i testsrc` at test run time, not a committed fixture.**
Alternative considered: commit a small `.mp4` to the repo. Rejected: binary fixtures are opaque in diffs/review and this repo has no LFS setup. `ffmpeg`'s built-in `testsrc` source needs no input file and is deterministic enough (fixed duration → fixed frame count) for assertions. Since `ffmpeg` is already a hard runtime dependency of the app itself, requiring it for tests adds no new environmental constraint.

**Skip (don't fail) the suite when `ffmpeg` is missing from `PATH`.**
A `TestMain` checks `exec.LookPath("ffmpeg")` once and calls `m.Run()` only if found; otherwise it prints a clear skip reason and exits 0. This matches how the app itself behaves (it doesn't work without ffmpeg either) without turning "ffmpeg not installed" into a confusing red test failure for a Go newcomer.

**Tests run against the real `uploads/` / `outputs/` / `temp/` directories.**
`main.go` hardcodes these as relative paths; there's no injection point to point tests at a scratch directory without changing production code beyond what this change's scope allows (see Non-Goals). Each test cleans up the specific files it creates (`t.Cleanup(func() { os.Remove(...) })`) so repeated `go test` runs don't accumulate files or leak state between tests. This constraint (no injectable storage root) is worth revisiting in the later refactor.

## Risks / Trade-offs

- [Tests depend on real `ffmpeg` behavior/version] → Acceptable: the app has the same dependency; a version-specific `ffmpeg` quirk would be a real bug worth catching, not test flakiness to hide.
- [Tests share the same `outputs/`/`uploads/` dirs as a manually-running dev server] → Mitigation: don't run `go test` while a dev server is actively processing uploads; each test uses timestamp-suffixed filenames (already how the app names outputs) so collisions are unlikely.
- [No CI means these tests only run when someone remembers to] → Accepted for this change; flagged as a natural follow-up, not blocking.
- [`setupRouter()` extraction, while mechanical, still touches `main.go`] → Mitigation: no logic inside handlers changes, only where route registration lives; first test run against current behavior serves as verification that nothing shifted.

## Open Questions

- None blocking. Whether/how to wire CI is deferred to a later change.
