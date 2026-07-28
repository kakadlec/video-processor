## 1. gosec: path traversal / subprocess (G204, G304)

- [x] 1.1 In `handleVideoUpload`, sanitize `header.Filename` with `filepath.Base` before building `videoPath`, so the saved upload path can't escape `uploads/`.
- [x] 1.2 Add `#nosec G304` with a justification comment on the `os.Create(videoPath)` call in `handleVideoUpload` (path now sanitized, but still gosec-flagged as variable-derived).
- [x] 1.3 Add `#nosec G204` with a justification comment on the `exec.Command("ffmpeg", ...)` call in `processVideo`.
- [x] 1.4 Add `#nosec G304` with a justification comment on `os.Create(zipPath)` in `createZipFile` (server-generated path, no user input).
- [x] 1.5 Add `#nosec G304` with a justification comment on `os.Open(filename)` in `addFileToZip` (path from internal `filepath.Glob` of the temp dir, no user input).

## 2. gosec: directory permissions (G301)

- [x] 2.1 Change `os.MkdirAll(dir, 0755)` to `0750` in `createDirs`.
- [x] 2.2 Change `os.MkdirAll(tempDir, 0755)` to `0750` in `processVideo`.

## 3. gosec: unhandled errors (G104)

- [x] 3.1 Handle the `os.MkdirAll` error in `createDirs` (log and continue, since it's setup for a loop over multiple dirs).
- [x] 3.2 Handle the `os.MkdirAll` error in `processVideo` (log; if directory creation fails, subsequent `ffmpeg` write will fail anyway and surface via existing error handling).
- [x] 3.3 Handle the `os.Remove(videoPath)` error in `handleVideoUpload` (log; cleanup-only, response already sent as success).

## 4. Verify gosec is clean

- [x] 4.1 Run `gosec ./...` locally and confirm zero findings.

## 5. Dependabot: dependency upgrades

- [x] 5.1 Upgrade `github.com/gin-gonic/gin` to the latest v1.12.x release in `go.mod`.
- [x] 5.2 Run `go mod tidy` to resolve transitive dependency versions.
- [x] 5.3 Diff `go.sum` against the 26 open alert package/version list (`golang.org/x/crypto`, `golang.org/x/net`, `google.golang.org/protobuf`) and confirm none resolve to a flagged vulnerable version; if any remain, `go get` that module directly to its minimum patched version.
- [x] 5.4 Run `go build ./...` to confirm the upgraded dependency set compiles.

## 6. Regression verification

- [x] 6.1 Run `go vet ./...`.
- [x] 6.2 Run `go test ./... -v` and confirm all existing tests pass unchanged.
- [x] 6.3 Manually smoke-test `go run main.go` + an upload via curl or the HTML form, if ffmpeg is available locally.

## 7. Ship

- [ ] 7.1 Create feature branch, commit with a Conventional Commit message (`fix: ...`), push, open PR.
- [ ] 7.2 Confirm `Build & Test` and `SAST (gosec)` CI checks pass on the PR.
