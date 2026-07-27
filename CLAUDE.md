# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A minimal Go web service ("FIAP X - Processador de Vídeos") that accepts a video upload, extracts frames at 1fps via `ffmpeg`, zips them, and serves the zip back for download. It's the code deliverable for a POSTECH/FIAP hackathon (see `POSTECH - SOAT - Fase 5 - Hacka.pdf` for the assignment brief — a binary PDF, not readable as text).

The entire application lives in `main.go` (single package `main`, no internal packages/modules). There is no test suite, no CI config, and no linter config in the repo.

## Commands

```bash
go run main.go          # run the server directly (listens on :8080)
go build -o app .       # build a binary
go mod tidy             # sync go.mod/go.sum after dependency changes
docker build -t video-processor .
docker run -p 8080:8080 video-processor
```

`ffmpeg` must be installed and on `PATH` — the app shells out to it (`exec.Command("ffmpeg", ...)`) and has no fallback or embedded copy. There are no test files (`*_test.go`) currently in the repo.

## Architecture

Request flow, all in `main.go`:

1. `main()` wires a single `gin` router: static file serving for `/uploads` and `/outputs`, a permissive CORS middleware (allows `*`), and routes `GET /`, `POST /upload`, `GET /download/:filename`, `GET /api/status`.
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
