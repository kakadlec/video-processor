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

**Fix all 3 G304 findings with real path-containment checks, not suppression.** Empirically verified against gosec's own analyzer (not just its docs): `gosec` recognizes the `filepath.Clean` + `strings.HasPrefix(cleaned, root+separator)` guard pattern from its [G304 docs](https://securego.io/docs/rules/g304.html) as proof a path can't escape its intended root, and stops flagging the line entirely — no `#nosec` needed. This is applied at all 3 sites: `handleVideoUpload`'s `videoPath` (also gets `filepath.Base(header.Filename)` first, so a traversal-crafted upload filename can't escape `uploads/` — the one site with real attacker-controlled input), `createZipFile`'s `zipPath` (must stay under `outputs/`), and `addFileToZip`'s `filename` (must stay under `temp/`). The latter two never had attacker-reachable input, but the containment check is cheap, gosec-recognized, and cheap insurance against a future refactor introducing one.

**G204 on the `ffmpeg` invocation stays suppressed — confirmed irreducible, not just inconvenient.** Tested directly: gosec's G204 check flags any non-literal argument to `exec.Command`, and unlike G304 it has no recognized validation-guard exception — the same `Clean`+`HasPrefix` pattern that clears G304 does not clear G204. Worse, it fires purely because `videoPath` arrives as a function *parameter* to `processVideo`; gosec doesn't do cross-function data-flow analysis, so even a provably-safe parameter trips it. There is no rule ID exception page for G204 on `securego.io` (404) to check against. Since `exec.Command` never invokes a shell (no metacharacter injection is possible regardless), and `framePattern` is 100% server-generated, a bare `#nosec G204` is the correct, minimal answer here — not a shortcut around a fixable issue.

**G301 (directory perms) and G104 (unhandled errors): straightforward fixes, no suppression.** Change `0755` → `0750` on the two `os.MkdirAll` calls (no reason these need to be group/other-readable). For the three unhandled errors (`os.MkdirAll` x2 in `createDirs`/`processVideo`, `os.Remove` in `handleVideoUpload`), log via the existing `log`/`fmt` pattern already used elsewhere in the file rather than introducing a new logging dependency; these are best-effort setup/cleanup operations where the request should still proceed (or already has, for `os.Remove`) even if the call fails, so errors are logged, not propagated as request failures.

**Dependency fix: bump `gin` to latest v1.12.x, then `go mod tidy`.** Rather than manually pinning `golang.org/x/crypto`/`x/net`/`protobuf` to arbitrary patched versions (which `go mod tidy` would likely revert or conflict with on the next dependency touch), upgrading the direct dependency (`gin`) to its latest release naturally pulls patched transitive versions, since gin's own `go.mod` has been updated against these CVEs. `go mod tidy` afterward cleans up `go.sum`. If any alert survives after the gin bump (i.e., gin's own transitive floor is still below the patched version), explicitly `go get` that specific module to the minimum patched version.

## Risks / Trade-offs

- [Risk] Upgrading gin two minor versions (v1.9.1 → v1.12.x) could change response/binding behavior. → Mitigation: `go test ./...` (existing integration suite covers `/upload`, `/download/:filename`, `/api/status`) must pass unchanged before this is considered done; no gin API used in `main.go` is deprecated between these versions (checked: `gin.Default()`, `c.Request.FormFile`, `c.JSON`, `c.Header`, `c.String`, `c.File`, `r.Static`, `r.GET/POST` are all stable across this range).
- [Risk] `filepath.Base` on the upload filename changes stored filenames if a client ever sent a path-like filename (rare, but it means the sanitized name may differ from the raw `Content-Disposition` filename). → Mitigation: acceptable; this is the correct security behavior, and no test or documented contract depends on preserving directory components in the filename.
- [Risk] Suppressing G204 with `#nosec` could mask a *future* real finding at that line if the code changes underneath it. → Mitigation: it's a bare `#nosec G204` tag per `openspec/specs/development-workflow/spec.md`'s requirement — the rule ID alone is enough for a reviewer to look up gosec's rule and re-evaluate whether it still applies when that line changes; the "why it was safe at the time" lives in this design doc and the commit history, not duplicated inline. This is now the *only* remaining suppression in the file — the 3 G304 findings were converted to real containment checks instead.

## Migration Plan

1. Apply `main.go` fixes (sanitization, permissions, error handling, `#nosec` annotations).
2. Bump `gin`, run `go mod tidy`, re-verify `go.sum` against the Dependabot alert list.
3. Run `go vet ./...`, `go test ./... -v`, `gosec ./...` locally — all must be clean.
4. Open PR; CI (`Build & Test`, `SAST (gosec)`) must pass before merge, per branch protection.

No rollback complexity: this is a same-behavior code change plus a dependency bump, revertible via a follow-up PR if CI or production surfaces a regression.

## Open Questions

None — scope is bounded by the two tools' current findings.
