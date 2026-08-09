# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A minimal Go web service ("FIAP X - Processador de Vídeos") that accepts a video upload, extracts frames at 1fps via `ffmpeg`, zips them, and serves the zip back for download. It's the code deliverable for a POSTECH/FIAP hackathon (see `docs/project-requirements.pdf` for the assignment brief — a binary PDF, not readable as text).

The entire application lives in `main.go` (single package `main`, no internal packages/modules), plus `main_test.go` for integration tests. CI runs on GitHub Actions (`.github/workflows/ci.yml`); there is no linter config beyond `go vet` and `gosec`.

## Development workflow

Non-trivial changes (new features, behavior changes, bug fixes with real design decisions, refactors) go through [OpenSpec](https://github.com/Fission-AI/OpenSpec) (`/opsx:propose` → `/opsx:apply` → `/opsx:archive`, with `/opsx:explore` first for complex/ambiguous ones) and land as a 3-PR sequence (propose / implementation / finalization). `main` has no direct pushes — every change lands via a feature branch + PR, gated by three required CI checks (`Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`). Commit messages follow Conventional Commits, which drives automated versioning via `release-please`.

The full rules (PR role boundaries, branch protection, quality-gate triage incl. `#nosec` policy, PR review-comment checking, merge authorization, commit/release mechanics) are **not** repeated here — they live in `docs/development.md` ("Contribution Conventions") for human contributors, and are auto-applied by the `repo-workflow` skill at the right moments (opening/merging a PR, wrapping up a change, writing a commit, running quality gates) so this file stays out of the way for simple, direct tasks. `AGENTS.md` carries the same pointers for non-Claude agents. Specs live in `openspec/specs/`; in-flight proposals in `openspec/changes/`; completed ones in `openspec/changes/archive/`.

One rule worth restating because it applies to every change, trivial or not: **`go vet ./...` and `go test ./... -v` must pass locally before reporting any change complete** — tests require `ffmpeg` on `PATH`, or run via `docker compose run --build --rm app-test go test ./... -v`.

## Commands

```bash
go run .                # run the server directly (listens on :8080)
go build -o app .       # build a binary
go mod tidy             # sync go.mod/go.sum after dependency changes
go test ./... -v        # run the integration test suite (requires ffmpeg on PATH; exits 1 with an error if absent)
docker compose up --build   # full stack via Docker (app + PostgreSQL, identity enabled)
```

`ffmpeg` must be installed and on `PATH` — the app shells out to it (`exec.Command("ffmpeg", ...)`) and has no fallback or embedded copy. This is also true for running the tests in `main_test.go`; if `ffmpeg` isn't available (e.g. locally on non-Linux setups), run tests inside Docker instead: `docker compose run --build --rm app-test go test ./... -v` (`app-test` builds from the `Dockerfile`'s `test` stage — the hardened `app` service's image has no Go toolchain to run tests with). `docker-compose.yml` is the sole documented way to build, run, or test the application via Docker **for local development** — there is no separate plain `docker build`/`docker run` workflow documented for that purpose. Container deployment is a separate, intentionally-retained concern documented in `docs/operations.md`.

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

- The Dockerfile is a multi-stage, non-root build (`builder` → `test` → `runtime`) — it is no longer the intentional single-stage/root anti-pattern this project originally shipped with.
- The app's *existing* user-facing strings (error messages, HTML) are in Portuguese (pt-BR) — this was the original convention for this pt-BR hackathon audience. It's superseded for new code by the language policy above; existing pt-BR strings are grandfathered in and not being retroactively translated.
- File type validation (`isValidVideoFile`) is by extension only (`.mp4 .avi .mov .mkv .wmv .flv .webm`), not content sniffing.
- `handleDownload` and `handleStatus` join user-controlled `:filename`/glob results directly into filesystem paths under `outputs/` with `filepath.Join` — there's no path-traversal check beyond that; be careful if extending this to accept arbitrary filenames.
