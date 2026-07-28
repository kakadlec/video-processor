## 1. Tests

- [x] 1.1 Add a helper that uploads a file with a valid video extension but undecodable content, to reliably trigger an `ffmpeg` failure.
- [x] 1.2 Test: `temp/` has no leftover per-request directory after a failed processing run.
- [x] 1.3 Test: `temp/` has no leftover per-request directory after a successful processing run (explicit assertion, previously only implicit).
- [x] 1.4 Test: after a failed processing run, the uploaded file still exists under `uploads/` (documents current behavior).

## 2. Wrap-up

- [x] 2.1 Run `go test ./... -v` (via Docker, since host has no `ffmpeg`) and confirm all tests pass.
