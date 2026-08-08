# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A minimal Go web service ("FIAP X - Processador de Vídeos") that accepts a video upload, extracts frames at 1fps via `ffmpeg`, zips them, and serves the zip back for download. It's the code deliverable for a POSTECH/FIAP hackathon (see `docs/project-requirements.pdf` for the assignment brief — a binary PDF, not readable as text).

The entire application lives in `main.go` (single package `main`, no internal packages/modules), plus `main_test.go` for integration tests. CI runs on GitHub Actions (`.github/workflows/ci.yml`); there is no linter config beyond `go vet` and `gosec`.

## Development process: OpenSpec is mandatory

This project uses [OpenSpec](https://github.com/Fission-AI/OpenSpec) for spec-driven development, and it is the **default process for every non-trivial change** — new features, behavior changes, bug fixes with real design decisions, refactors. Do not go straight to editing `main.go` for that kind of work.

- Workflow: `/opsx:propose` (creates `proposal.md` + `design.md` + `tasks.md` under `openspec/changes/<name>/`) → implement against `tasks.md` (`/opsx:apply`) → `/opsx:archive` once shipped, which folds the change into `openspec/specs/`.
- Specs live in `openspec/specs/`; in-flight change proposals live in `openspec/changes/`; completed ones move to `openspec/changes/archive/`.
- Project context/conventions for OpenSpec artifact generation are configured in `openspec/config.yaml` (`context:` block) — keep it in sync if the tech stack or conventions here change.
- Skip the full flow only for trivial, obviously-scoped edits (typo fixes, comment tweaks, dependency bumps) — when in doubt, propose first.
- **PRs SHALL separate spec content from code content — never bundle both in one PR.** Each non-trivial change uses three PR roles, in order: (1) a **propose** PR containing only the new `openspec/changes/<name>/` artifacts (`proposal.md`/`design.md`/`tasks.md`/delta specs), with no code; merge this first. (2) An **implementation** PR containing only application source and test changes — no `tasks.md`, README/docs, `CLAUDE.md`/`AGENTS.md`, configuration, CI, or any file under `openspec/`. (3) One **finalization/archive** PR, opened after implementation merges, that marks tasks complete, folds the delta into `openspec/specs/`, and moves the change folder under `openspec/changes/archive/` — with no application code or tests. Permanent documentation or agent-instruction changes use a separate docs PR and never enter the implementation PR. A reviewer should never have to wade through `openspec/` diffs to review a `main.go` change, or vice versa.
- `openspec/specs/**` and `openspec/changes/**` are marked `linguist-generated=true` in `.gitattributes`, so GitHub collapses them by default in the PR diff view (still fully tracked and reviewable on demand — this is a display aid, not a substitute for the PR-splitting rule above).

## Branch protection: PRs only, no direct pushes to `main`

`main` is protected: direct pushes are rejected, including for repo admins (no bypass). Every change — yours or Claude Code's — lands via a feature branch and a pull request:

```bash
git checkout -b feat/short-description   # or fix/..., chore/..., matching Conventional Commits type
git push -u origin feat/short-description
gh pr create --fill
```

A PR is **not mergeable** until all three required status checks pass — `Build & Test`, `SAST (gosec)`, and `Vulnerability Scan (govulncheck)` — and the branch is up to date with `main`. This applies to every PR, including `release-please`'s own automated release PR — no special-casing. If any of these is red because of an unrelated pre-existing finding elsewhere in the codebase, that still blocks your PR; the fix is to triage the findings (see below), not to bypass the check.

## Quality gates: tests, SAST, and dependency vulnerabilities must pass

A change is **not complete** until `go test ./...` has been run and passes locally — this applies before reporting any change as done, not just before pushing. CI (`.github/workflows/ci.yml`) enforces this plus a SAST gate and a dependency-vulnerability gate on every push to `main` and every pull request:

- **`test` job** (`Build & Test`): `go vet ./...` + `go test ./... -v` (installs `ffmpeg` on the runner first).
- **`sast` job** (`SAST (gosec)`): [`gosec`](https://github.com/securego/gosec) against the whole codebase. **The build fails on any finding** — this is a deliberate policy, not a bug. **`#nosec` is a last resort, not the default response to a finding** — check the rule's own docs (e.g. `securego.io/docs/rules/g304.html`) for a validation pattern gosec recognizes as safe, and test it (`gosec ./...`) before reaching for suppression; several findings that looked like they needed `#nosec` turned out to be fixable with a real containment check instead. Only suppress a finding that's genuinely a false positive or an accepted risk with no recognized fix pattern, using a bare inline `#nosec G<rule-id>` comment (no restated prose — that's what commit messages and PR descriptions are for). Never disable the SAST job or exclude whole files/rules to make it pass.
- **`vulncheck` job** (`Vulnerability Scan (govulncheck)`): [`govulncheck`](https://go.dev/security/vuln) against the module. It only fails when a known vulnerability is reachable from code actually called by this project (not merely present in `go.sum`) — resolve a failure by upgrading the implicated dependency, generally by bumping the direct dependency that pulls it in transitively (see `go mod graph`), then `go mod tidy`.
- All `gosec` findings and reachable `govulncheck` vulnerabilities present as of this writing have been resolved (see `openspec/changes/archive/fix-gosec-and-dependabot-findings/` once archived, or `openspec/specs/development-workflow/spec.md`). Dependabot alerts should be resolved the same way — upgrade the flagged module or the direct dependency pulling it in — as soon as they're opened, not left to accumulate.

## Commit messages and releases

Commit messages **must** follow [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `chore:`, `docs:`, `ci:`, `test:`, `refactor:`, `!` after the type or a `BREAKING CHANGE:` footer for breaking changes) — this isn't just style, it's the signal [`release-please`](https://github.com/googleapis/release-please) (`.github/workflows/release-please.yml`) uses to compute the next version automatically.

- Versioning is **not** manual. Nobody runs `git tag` by hand. On every push to `main`, release-please maintains a single up-to-date "Release PR" showing the next version and changelog computed from Conventional Commits since the last release.
- Merging that PR is what actually cuts a release: it creates the git tag, publishes a GitHub Release with generated notes, and updates `CHANGELOG.md`. Until it's merged, nothing is tagged or released.
- Config: `release-please-config.json` (`release-type: simple` — this app has no package-manager manifest to version-bump) and `.release-please-manifest.json` (tracks the current released version per path).

## Commands

```bash
go run main.go          # run the server directly (listens on :8080)
go build -o app .       # build a binary
go mod tidy             # sync go.mod/go.sum after dependency changes
go test ./... -v        # run the integration test suite (requires ffmpeg on PATH; exits 1 with an error if absent)
docker build -t video-processor .
docker run -p 8080:8080 video-processor
```

`ffmpeg` must be installed and on `PATH` — the app shells out to it (`exec.Command("ffmpeg", ...)`) and has no fallback or embedded copy. This is also true for running the tests in `main_test.go`; if `ffmpeg` isn't available (e.g. locally on non-Linux setups), run tests inside the Docker image instead: `docker build -t video-processor . && docker run --rm video-processor go test ./... -v`.

## Architecture

Request flow, all in `main.go`:

1. `setupRouter()` wires a single `gin` router: static file serving for `/uploads` and `/outputs`, a permissive CORS middleware (allows `*`), and routes `GET /`, `POST /upload`, `GET /download/:filename`, `GET /api/status`. `main()` just calls `setupRouter()` then `.Run(":8080")` — the split exists so `main_test.go` can drive the real handlers via `httptest.NewServer` without binding the real port.
2. `GET /` returns a hardcoded HTML page (`getHTMLForm()`) — an inline upload form with vanilla JS (fetch calls to `/upload` and `/api/status`). There is no separate frontend build; editing the UI means editing the Go string literal.
3. `POST /upload` (`handleVideoUpload`) validates the file extension, saves the upload to `uploads/<timestamp>_<original-filename>`, then calls `processVideo`.
4. `processVideo` is the core pipeline: creates a per-request scratch dir under `temp/<timestamp>`, runs `ffmpeg -i <video> -vf fps=1 -y temp/<timestamp>/frame_%04d.png`, globs the resulting PNGs, zips them into `outputs/frames_<timestamp>.zip` via `createZipFile`/`addFileToZip`, then removes the temp dir (`defer os.RemoveAll`). On success the original upload in `uploads/` is deleted too, so `outputs/*.zip` is the only durable artifact.
5. `GET /download/:filename` and `GET /api/status` just stat/serve files out of `outputs/`.

Three directories are created at startup (`createDirs`) and used as the app's only state: `uploads/` (transient input), `temp/` (per-request working dir, always cleaned up), `outputs/` (durable zip results, listed by `/api/status` and served by `/download`). There is no database — file listing is done by `filepath.Glob` against `outputs/*.zip` at request time.

Processing is synchronous and in-request: `handleVideoUpload` blocks on the full ffmpeg run and zip creation before responding. There is no job queue, no async/webhook flow, and no per-request concurrency limiting — large videos or concurrent uploads will hold multiple `ffmpeg` processes at once.

## Language policy: English for new code (as of 2026-07-28)

Code, error messages, and comments in **new or changed code** SHALL be written in English, going forward. This is a change from the project's original convention (see below) — it applies prospectively, not retroactively:

- Do not translate the existing Portuguese (pt-BR) strings already in `main.go` (the HTML form in `getHTMLForm()`, existing JSON `Message`/`error` fields, existing `fmt.Printf`/`log.Printf` calls) just because you're touching a nearby line. Leave them as-is unless a change specifically asks for that.
- Any error message, log line, or other string you add or rewrite from now on should be in English, even inside an otherwise-Portuguese function.
- Comments: default to none. Add one only when it explains something genuinely non-obvious (a hidden constraint, a workaround, a subtle invariant) — not to restate what the code already says. When you do add one, write it in English.
- Exception: new **user-facing UI copy** in `getHTMLForm()` (labels, buttons, status messages shown on the page itself) stays in Portuguese, matching the rest of that page — it's written for the same pt-BR hackathon audience as the existing form, and mixing languages within one UI reads as inconsistent to that audience. This exception is scoped to visible page copy only; error messages, log lines, and comments in the surrounding Go code (including inside `getHTMLForm()`) still follow the English rule above.

## Notable constraints / gotchas

- The Dockerfile is deliberately written as an anti-pattern example (see its header comment: "sem boas práticas - propositalmente!" — no multi-stage build, no non-root user, `go mod tidy` at container build time). Do not treat it as a template to copy elsewhere; if asked to fix/harden the Dockerfile, that's expected, in-scope work, not a misunderstanding of the file's intent.
- The app's *existing* user-facing strings (error messages, HTML) are in Portuguese (pt-BR) — this was the original convention for this pt-BR hackathon audience. It's superseded for new code by the language policy above; existing pt-BR strings are grandfathered in and not being retroactively translated.
- File type validation (`isValidVideoFile`) is by extension only (`.mp4 .avi .mov .mkv .wmv .flv .webm`), not content sniffing.
- `handleDownload` and `handleStatus` join user-controlled `:filename`/glob results directly into filesystem paths under `outputs/` with `filepath.Join` — there's no path-traversal check beyond that; be careful if extending this to accept arbitrary filenames.
