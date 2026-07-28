## 1. gosec: path traversal / subprocess (G204, G304)

- [x] 1.1 In `handleVideoUpload`, sanitize `header.Filename` with `filepath.Base` before building `videoPath`, so the saved upload path can't escape `uploads/`.
- [x] 1.2 (superseded by 1.2b) ~~Add `#nosec G304` on `os.Create(videoPath)`~~
- [x] 1.2b Replace the `#nosec G304` on `os.Create(videoPath)` in `handleVideoUpload` with a real `filepath.Clean` + `strings.HasPrefix(videoPath, "uploads"+separator)` containment check (gosec's own documented G304 fix pattern) — empirically confirmed this makes gosec stop flagging the line, zero suppression needed.
- [x] 1.3 Add a bare `#nosec G204` (no prose) on the `exec.Command("ffmpeg", ...)` call in `processVideo` — confirmed via direct testing against gosec's analyzer that this finding cannot be resolved by any validation pattern (it flags any non-literal exec.Command arg, with no recognized safe-guard exception, and additionally can't trace `videoPath` since it's a function parameter).
- [x] 1.4 (superseded by 1.4b) ~~Add `#nosec G304` on `os.Create(zipPath)`~~
- [x] 1.4b Replace the `#nosec G304` on `os.Create(zipPath)` in `createZipFile` with a `filepath.Clean` + `strings.HasPrefix(zipPath, "outputs"+separator)` containment check — zero suppression needed.
- [x] 1.5 (superseded by 1.5b) ~~Add `#nosec G304` on `os.Open(filename)`~~
- [x] 1.5b Replace the `#nosec G304` on `os.Open(filename)` in `addFileToZip` with a `filepath.Clean` + `strings.HasPrefix(filename, "temp"+separator)` containment check — zero suppression needed.

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

- [x] 7.1 Create feature branch, commit with a Conventional Commit message (`fix: ...`), push, open PR.
- [x] 7.2 Confirm `Build & Test` and `SAST (gosec)` CI checks pass on the PR.

## 8. Vulnerability Scan gate (discovered blocking merge: branch protection requires a third check, "Vulnerability Scan (govulncheck)", that no workflow produced)

- [x] 8.1 Add a `vulncheck` job to `.github/workflows/ci.yml` named `Vulnerability Scan (govulncheck)` that installs and runs `govulncheck ./...`.
- [x] 8.2 Update `CLAUDE.md` to document the third required check and the dependency-vulnerability quality gate.
- [x] 8.3 Sync `openspec/config.yaml` context block (stale Go 1.21 / "no CI" notes) to reflect current Go version and the three CI jobs.
- [x] 8.4 Add a `Vulnerability Scan Gate` requirement to the `development-workflow` delta spec.
- [x] 8.5 Push, confirm all three checks (`Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`) pass, and confirm the PR becomes mergeable.
