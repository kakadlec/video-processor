# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A minimal Go web service ("FIAP X - Processador de Vídeos") that accepts a video upload, extracts frames at 1fps via `ffmpeg`, zips them, and serves the zip back for download. It's the code deliverable for a POSTECH/FIAP hackathon (see `docs/project-requirements.pdf` for the assignment brief — a binary PDF, not readable as text).

The entire application lives in `main.go` (single package `main`, no internal packages/modules), plus `main_test.go` for integration tests. There is no CI config and no linter config in the repo.

## Development process: OpenSpec is mandatory

This project uses [OpenSpec](https://github.com/Fission-AI/OpenSpec) for spec-driven development, and it is the **default process for every non-trivial change** — new features, behavior changes, bug fixes with real design decisions, refactors. Do not go straight to editing `main.go` for that kind of work.

- Workflow: `/opsx:propose` (creates `proposal.md` + `design.md` + `tasks.md` under `openspec/changes/<name>/`) → implement against `tasks.md` (`/opsx:apply`) → `/opsx:archive` once shipped, which folds the change into `openspec/specs/`.
- Specs live in `openspec/specs/`; in-flight change proposals live in `openspec/changes/`; completed ones move to `openspec/changes/archive/`.
- Project context/conventions for OpenSpec artifact generation are configured in `openspec/config.yaml` (`context:` block) — keep it in sync if the tech stack or conventions here change.
- Skip the full flow only for trivial, obviously-scoped edits (typo fixes, comment tweaks, dependency bumps) — when in doubt, propose first.

## Commands

```bash
go run main.go          # run the server directly (listens on :8080)
go build -o app .       # build a binary
go mod tidy             # sync go.mod/go.sum after dependency changes
go test ./... -v        # run the integration test suite (requires ffmpeg on PATH; skips with a message if absent)
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

## Notable constraints / gotchas

- The Dockerfile is deliberately written as an anti-pattern example (see its header comment: "sem boas práticas - propositalmente!" — no multi-stage build, no non-root user, `go mod tidy` at container build time). Do not treat it as a template to copy elsewhere; if asked to fix/harden the Dockerfile, that's expected, in-scope work, not a misunderstanding of the file's intent.
- All user-facing strings (error messages, HTML) are in Portuguese (pt-BR) — match that when adding user-visible text.
- File type validation (`isValidVideoFile`) is by extension only (`.mp4 .avi .mov .mkv .wmv .flv .webm`), not content sniffing.
- `handleDownload` and `handleStatus` join user-controlled `:filename`/glob results directly into filesystem paths under `outputs/` with `filepath.Join` — there's no path-traversal check beyond that; be careful if extending this to accept arbitrary filenames.
