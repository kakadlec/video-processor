# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

A minimal Go web service ("FIAP X - Processador de Vídeos") that accepts a video upload, extracts frames at 1fps via `ffmpeg`, zips them, and serves the zip back for download. It's the code deliverable for a POSTECH/FIAP hackathon (see `docs/project-requirements.pdf` for the assignment brief — a binary PDF, not readable as text).

The HTTP composition root lives in `cmd/api/main.go` (package `main`), alongside `identity.go` and their `_test.go` files; `internal/identity` and `internal/video` hold the DDD bounded contexts introduced in later phases (see `docs/architecture.md`). CI runs on GitHub Actions (`.github/workflows/ci.yml`); there is no linter config beyond `go vet` and `gosec`.

## Development workflow

This repo has often organized larger changes through [OpenSpec](https://github.com/Fission-AI/OpenSpec) (`/opsx:propose` → `/opsx:apply` → `/opsx:archive`, `/opsx:explore` first when warranted), landing as a propose/implementation/finalization PR sequence. That's a documented pattern, not a rule this file enforces: whether a given change needs it, a lighter version of it, or a single direct PR is the maintainer's call, made per change — the `change-lifecycle`/`repo-workflow` skills carry the judgment Claude Code applies on its own, and defer to the maintainer's explicit direction over their own default. `main` has no direct pushes — every change lands via a feature branch + PR, gated by three required CI checks (`Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`); that part is enforced by GitHub branch protection, not by this document. Commit messages follow Conventional Commits, which drives automated versioning via `release-please`.

The full rules (quality-gate triage incl. `#nosec` policy, PR review-comment checking, merge authorization, commit/release mechanics, and the OpenSpec/PR-separation pattern above when it's in use) are **not** repeated here — they live in `docs/development.md`'s "Code Quality Gates" and "Contribution Conventions" sections for human contributors and non-Claude-Code agents, and are what the `repo-workflow` and `change-lifecycle` skills (`.claude/skills/`) draw on for Claude Code, at the right moments (opening/merging a PR, wrapping up a change, writing a commit, running quality gates) — always subject to the maintainer's explicit direction, so this file stays out of the way for simple, direct tasks. `AGENTS.md` carries the same pointers for non-Claude-Code agents. Specs live in `openspec/specs/`; in-flight proposals in `openspec/changes/`; completed ones in `openspec/changes/archive/`.

One rule worth restating because it applies to every change, trivial or not: **`go test ./... -v` must pass locally before reporting a change complete, whenever the diff includes a Go module input (`.go`/`go.mod`/`go.sum`)** — tests require `ffmpeg` on `PATH`, or run via `docker compose run --build --rm app-test go test ./... -v`.

## Commands

```bash
go run ./cmd/api        # run the server directly (listens on :8080)
go build -o app ./cmd/api  # build a binary
go mod tidy             # sync go.mod/go.sum after dependency changes
go test ./... -v        # run the integration test suite (requires ffmpeg on PATH; exits 1 with an error if absent)
docker compose up --build   # full stack via Docker (app + PostgreSQL, identity enabled)
```

`ffmpeg` must be installed and on `PATH` — the app shells out to it (`exec.Command("ffmpeg", ...)`) and has no fallback or embedded copy. This is also true for running the tests in `cmd/api/main_test.go`; if `ffmpeg` isn't available (e.g. locally on non-Linux setups), run tests inside Docker instead: `docker compose run --build --rm app-test go test ./... -v` (`app-test` builds from the `Dockerfile`'s `test` stage — the hardened `app` service's image has no Go toolchain to run tests with). `docker-compose.yml` is the sole documented way to build, run, or test the application via Docker **for local development** — there is no separate plain `docker build`/`docker run` workflow documented for that purpose. Container deployment is a separate, intentionally-retained concern documented in `docs/operations.md`.

## Architecture

Request flow, all in `cmd/api/main.go`, `cmd/api/identity.go`, and `cmd/api/video.go`:

1. `setupRouter(identity, video)` wires a single `gin` router: static file serving for `/uploads` and `/outputs`, a permissive CORS middleware (allows `*`), and routes `GET /`, `POST /upload`, `GET /download/:filename`, `GET /api/status`, `POST /api/auth/register`, `POST /api/auth/login`, `POST /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /api/video-jobs`. `main()` calls `setupIdentity`/`setupVideo` then `setupRouter(identity, video)` then `.Run(":8080")` — the split exists so `main_test.go`/`identity_test.go`/`video_test.go` can drive the real handlers via `httptest.NewServer` without binding the real port.
2. `GET /` serves `cmd/api/web/index.html` (with `cmd/api/web/styles.css` and `cmd/api/web/app.js` at `/styles.css` and `/app.js`), embedded into the binary via `go:embed` — an upload form with vanilla JS (fetch calls to `/upload` and `/api/status`). There is no separate frontend build; editing the UI means editing the files under `cmd/api/web/`. The frontend does not consume `/api/video-jobs` — it keeps using the legacy `/upload` flow exclusively.
3. `POST /upload` (`(*videoModule).handleVideoUpload` in `cmd/api/video.go`) validates the file extension, saves the upload to `uploads/<uploadID>_<original-filename>`, creates a `VideoJob` via `CreateVideoJob`, then runs it through `ProcessVideoJob`.
4. `ProcessVideoJob` (`internal/video/application`) is the core pipeline: `EnqueueVideoJob` → `StartProcessing` → `FrameExtractor.ExtractFrames`, driving the `VideoJob` from `pending` through `queued`/`processing`. It deliberately does **not** call `CompleteJob` itself on extraction success — it leaves the job `processing` and returns the result, since `handleVideoUpload` still has more to do (recording output artifact ownership) before the job can be considered done. `handleVideoUpload` calls `CompleteJob` itself once that succeeds, or `FailJob` if it doesn't (`completed → failed` isn't a valid transition, so `CompleteJob` can't run until the result is confirmed durable). `ProcessVideoJob` does call `FailJob` directly if extraction itself fails. The actual `ffmpeg` exec/zip work lives in `internal/video/infrastructure/ffmpeg`'s `Extractor`: creates a per-job scratch dir under `temp/<jobID>`, runs `ffmpeg -i <video> -vf fps=1 -y temp/<jobID>/frame_%04d.png` via `exec.CommandContext` (so a canceled request context kills the subprocess), globs the resulting PNGs, zips them into `outputs/frames_<jobID>.zip`, then removes the temp dir (`defer os.RemoveAll`). On success the original upload in `uploads/` is deleted too, so `outputs/*.zip` is the only durable artifact.
5. `GET /download/:filename` and `GET /api/status` (both in `cmd/api/main.go`) just stat/serve files out of `outputs/`.
6. `POST /api/video-jobs`, `GET /api/video-jobs/:id`, and `GET /api/video-jobs` (all bearer-authenticated, owner-scoped) wrap `internal/video/application`'s `CreateVideoJob`/`GetJobStatus`/`ListUserJobs` use cases — a preview job-lifecycle API, separate from the legacy flow above. A job created *through this API* has no processing trigger and stays `pending` forever; do not present this as a working end-to-end path (see `openspec/specs/videojob-http-api/spec.md`). Because `/upload` also calls `CreateVideoJob`, though, `GET /api/video-jobs`/`GET /api/video-jobs/:id` do show real `completed`/`failed` jobs for a user who has used `/upload` — same aggregate, same repository.

Three directories are created at startup (`createDirs`) and used only by the `/upload` flow above — `POST /api/video-jobs` accepts a JSON filename and never touches the filesystem: `uploads/` (transient input), `temp/` (per-job working dir, always cleaned up), `outputs/` (durable zip results, listed by `/api/status` and served by `/download`) — file listing there is done by `filepath.Glob` against `outputs/*.zip` at request time. `VideoJob`s (created via either `/upload` or `/api/video-jobs`) are persisted in PostgreSQL (`VIDEO_POSTGRES_DSN`, required at startup like `IDENTITY_POSTGRES_DSN`).

Processing is synchronous and in-request: `handleVideoUpload` blocks on the full `ProcessVideoJob` sequence (including the `ffmpeg` run and zip creation) before responding. There is no job queue, no async/webhook flow, and no per-request concurrency limiting — large videos or concurrent uploads will hold multiple `ffmpeg` processes at once.

## Language policy: English for new code (as of 2026-07-28)

Code, error messages, and comments in **new or changed code** SHALL be written in English, going forward. This is a change from the project's original convention (see below) — it applies prospectively, not retroactively:

- Do not translate the existing Portuguese (pt-BR) strings already in `cmd/api/main.go` and `cmd/api/web/index.html` (the HTML form, existing JSON `Message`/`error` fields, existing `fmt.Printf`/`log.Printf` calls) just because you're touching a nearby line. Leave them as-is unless a change specifically asks for that.
- Any error message, log line, or other string you add or rewrite from now on should be in English, even inside an otherwise-Portuguese function.
- Comments: default to none. Add one only when it explains something genuinely non-obvious (a hidden constraint, a workaround, a subtle invariant) — not to restate what the code already says. When you do add one, write it in English.
- Exception: new **user-facing UI copy** in `cmd/api/web/index.html` (labels, buttons, status messages shown on the page itself) and user-facing strings in `cmd/api/web/app.js` stay in Portuguese, matching the rest of that page — it's written for the same pt-BR hackathon audience as the existing form, and mixing languages within one UI reads as inconsistent to that audience. This exception is scoped to visible page copy only; error messages, log lines, and comments in the surrounding Go code still follow the English rule above.

## Notable constraints / gotchas

- The Dockerfile is a multi-stage, non-root build (`builder` → `test` → `runtime`) — it is no longer the intentional single-stage/root anti-pattern this project originally shipped with.
- The app's *existing* user-facing strings (error messages, HTML) are in Portuguese (pt-BR) — this was the original convention for this pt-BR hackathon audience. It's superseded for new code by the language policy above; existing pt-BR strings are grandfathered in and not being retroactively translated.
- File type validation (`isValidVideoFile`) is by extension only (`.mp4 .avi .mov .mkv .wmv .flv .webm`), not content sniffing.
- `handleDownload` and `handleStatus` join user-controlled `:filename`/glob results directly into filesystem paths under `outputs/` with `filepath.Join` — there's no path-traversal check beyond that; be careful if extending this to accept arbitrary filenames.
