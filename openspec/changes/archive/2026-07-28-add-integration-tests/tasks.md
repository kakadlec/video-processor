## 1. Non-behavioral setup

- [x] 1.1 Extract Gin router/middleware/route registration out of `main()` into `setupRouter() *gin.Engine` in `main.go`; `main()` calls it then `.Run(":8080")`. No handler logic changes.
- [x] 1.2 Manually re-verify current behavior still works after the extraction (`go run main.go`, one upload via curl) before writing any tests against it.

## 2. Test harness

- [x] 2.1 Add `main_test.go` with `TestMain` that checks `exec.LookPath("ffmpeg")` and skips the suite with a clear message if not found.
- [x] 2.2 Add a helper that generates a short synthetic test video (`ffmpeg -f lavfi -i testsrc=duration=Ns:size=320x240:rate=1`) into `t.TempDir()`, parameterized by duration.
- [x] 2.3 Add a helper that spins up `httptest.NewServer(setupRouter())` per test and returns its base URL.

## 3. Behavior tests (per `specs/video-frame-extraction/spec.md`)

- [x] 3.1 Test: valid video upload (e.g. 3s) returns `HTTP 200`, `success: true`, `frame_count` ≈ 3.
- [x] 3.2 Test: downloading the returned `zip_path` returns a zip whose entry count equals `frame_count`.
- [x] 3.3 Test: unsupported extension (e.g. `.txt`) returns `HTTP 400`, `success: false`, no zip created.
- [x] 3.4 Test: `POST /upload` with no `video` field returns `HTTP 400`.
- [x] 3.5 Test: original uploaded file under `uploads/` is removed after a successful run.
- [x] 3.6 Test: `GET /api/status` after a successful upload includes an entry matching the returned `zip_path`.
- [x] 3.7 Test: `GET /download/:filename` for a nonexistent file returns `HTTP 404`.
- [x] 3.8 Ensure every test that creates files under `uploads/`/`outputs/`/`temp/` removes them via `t.Cleanup`, so `go test ./...` leaves no residue and is safe to re-run.

## 4. Wrap-up

- [x] 4.1 Run `go test ./... -v` and confirm all tests pass against current (pre-refactor) behavior.
- [x] 4.2 Add `go test ./...` to the Commands section of `CLAUDE.md`.
