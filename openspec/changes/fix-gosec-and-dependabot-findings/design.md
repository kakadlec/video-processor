## Context

`gosec` currently reports 9 findings in `main.go`, all pre-existing and accepted as known debt when the SAST gate was introduced (`openspec/specs/development-workflow/spec.md`). Dependabot separately reports 26 open alerts, all against `golang.org/x/crypto`, `golang.org/x/net`, and `google.golang.org/protobuf` — none imported directly by `main.go`, all pulled in transitively by `github.com/gin-gonic/gin v1.9.1` (released 2023). `go mod graph` confirms gin is the sole path to the vulnerable versions.

## Goals / Non-Goals

**Goals:**
- Zero `gosec` findings on `main.go`.
- Zero open Dependabot alerts on `go.mod`.
- Close the actual path-traversal risk in the upload path (not just silence the linter), since the uploaded `header.Filename` is attacker-controlled and currently flows unsanitized into a filesystem path.

**Non-Goals:**
- Hardening `handleDownload`/`handleStatus` against path traversal via the `:filename` route param — `gosec` does not flag these (no `os.Open`/`os.Create` call site), and it's an existing documented gap unrelated to this SAST/Dependabot cleanup. Left as a follow-up per the note already in `CLAUDE.md`.
- Introducing async/job-queue processing, content-based file type sniffing, or any other behavior change beyond what's needed to clear the two tools' findings.
- Bumping other indirect dependencies not implicated by an alert.

## Decisions

**Sanitize the upload filename instead of only suppressing G204/G304.** `header.Filename` is client-supplied and gets joined directly into `uploads/<timestamp>_<filename>` (main.go:101-102), which then feeds both `os.Create` (G304) and the `ffmpeg` subprocess (G204). A filename like `../../etc/foo` would let `filepath.Join` escape `uploads/`. Fix: take `filepath.Base(header.Filename)` before building the path, so only the leaf name survives. This is a real fix, not cosmetic — the subsequent `#nosec` comments on the now-safe path document that the remaining risk is accepted, not ignored.

**Suppress G304 on the zip-writing paths, not code changes.** `createZipFile`'s `zipPath` and `addFileToZip`'s `filename` originate from `filepath.Glob(tempDir/*.png)` and a timestamp the server itself generated — there is no user input in these two call sites. Rewriting them to avoid `os.Open`/`os.Create` would add complexity for no security benefit, so each gets a bare inline `#nosec G304` instead; the rationale lives in this design doc and the commit message, not repeated as inline prose.

**Suppress G204 on the `ffmpeg` invocation.** Shelling out to `ffmpeg` with a path argument is the entire purpose of this service; there's no way to extract frames without it. Once `videoPath` is sanitized (previous decision) and `framePattern` is fully server-derived, the remaining "subprocess launched with variable" warning is an accepted, justified risk — a bare `#nosec G204`, not restructured away.

**G301 (directory perms) and G104 (unhandled errors): straightforward fixes, no suppression.** Change `0755` → `0750` on the two `os.MkdirAll` calls (no reason these need to be group/other-readable). For the three unhandled errors (`os.MkdirAll` x2 in `createDirs`/`processVideo`, `os.Remove` in `handleVideoUpload`), log via the existing `log`/`fmt` pattern already used elsewhere in the file rather than introducing a new logging dependency; these are best-effort setup/cleanup operations where the request should still proceed (or already has, for `os.Remove`) even if the call fails, so errors are logged, not propagated as request failures.

**Dependency fix: bump `gin` to latest v1.12.x, then `go mod tidy`.** Rather than manually pinning `golang.org/x/crypto`/`x/net`/`protobuf` to arbitrary patched versions (which `go mod tidy` would likely revert or conflict with on the next dependency touch), upgrading the direct dependency (`gin`) to its latest release naturally pulls patched transitive versions, since gin's own `go.mod` has been updated against these CVEs. `go mod tidy` afterward cleans up `go.sum`. If any alert survives after the gin bump (i.e., gin's own transitive floor is still below the patched version), explicitly `go get` that specific module to the minimum patched version.

## Risks / Trade-offs

- [Risk] Upgrading gin two minor versions (v1.9.1 → v1.12.x) could change response/binding behavior. → Mitigation: `go test ./...` (existing integration suite covers `/upload`, `/download/:filename`, `/api/status`) must pass unchanged before this is considered done; no gin API used in `main.go` is deprecated between these versions (checked: `gin.Default()`, `c.Request.FormFile`, `c.JSON`, `c.Header`, `c.String`, `c.File`, `r.Static`, `r.GET/POST` are all stable across this range).
- [Risk] `filepath.Base` on the upload filename changes stored filenames if a client ever sent a path-like filename (rare, but it means the sanitized name may differ from the raw `Content-Disposition` filename). → Mitigation: acceptable; this is the correct security behavior, and no test or documented contract depends on preserving directory components in the filename.
- [Risk] Suppressing G204/G304 with `#nosec` could mask a *future* real finding at the same line if the code changes underneath it. → Mitigation: each suppression is a bare `#nosec G<rule-id>` tag per `openspec/specs/development-workflow/spec.md`'s requirement — the rule ID alone is enough for a reviewer to look up gosec's rule and re-evaluate whether it still applies when that line changes; the "why it was safe at the time" lives in this design doc and the commit history, not duplicated inline.

## Migration Plan

1. Apply `main.go` fixes (sanitization, permissions, error handling, `#nosec` annotations).
2. Bump `gin`, run `go mod tidy`, re-verify `go.sum` against the Dependabot alert list.
3. Run `go vet ./...`, `go test ./... -v`, `gosec ./...` locally — all must be clean.
4. Open PR; CI (`Build & Test`, `SAST (gosec)`) must pass before merge, per branch protection.

No rollback complexity: this is a same-behavior code change plus a dependency bump, revertible via a follow-up PR if CI or production surfaces a regression.

## Open Questions

None — scope is bounded by the two tools' current findings.
