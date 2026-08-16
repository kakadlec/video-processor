## Context

`main.go`/`identity.go` (plus their test files) currently sit at the repo root as `package main`, importing `internal/identity/*` but not `internal/video/*` (nothing wires the Video Processing context into HTTP yet). `docs/architecture.md`'s Target Package Topology already names `cmd/api/` as the intended HTTP entrypoint, replacing `main.go`. `wire-videojob-http-endpoints`, the next Change Backlog row, needs a real `cmd/api` to add its new endpoints to.

Two mechanics make this more than a bare `git mv`:

1. `//go:embed web` embeds a directory relative to the embedding source file's own location, and cannot reach outside (or above) that location — `web/` must move to `cmd/api/web/` for a `cmd/api/main.go` to embed it.
2. `go test` sets a package's test working directory to that package's own directory (not the module root, not the invoking shell's cwd). `main_test.go`/`identity_test.go` use cwd-relative literals (`"uploads"`, `"outputs"`, `"temp"`) that assume the repo root — verified by grep that every cwd-relative path in both test files resolves to one of these three directories (all other file I/O in the tests uses `t.TempDir()`, which is unaffected by working directory).

`go run`/`go build`/the Dockerfile are not affected by mechanic 2: none of them change the process's working directory based on source file location, so `uploads/`, `outputs/`, `temp/` continue to resolve at the repo root (or Docker's `WORKDIR /app`) exactly as today.

## Goals / Non-Goals

**Goals:**
- `cmd/api` becomes the real HTTP composition root, byte-for-byte the same routes/handlers/behavior as today's `main.go`/`identity.go`.
- `go test ./...` from the repo root behaves identically to today — same directories touched, same assertions valid, no new tracked state directories.
- `docker compose up --build` and `docker compose run --build --rm app-test go test ./... -v` keep working unchanged from the outside (image build path updates internally only).

**Non-Goals:**
- No behavior change to any route, request, or response.
- No new endpoints, no wiring of `internal/video/*` into HTTP (that's `wire-videojob-http-endpoints`).
- No change to `cmd/worker` (doesn't exist yet; Phase 6).
- No restructuring of `main.go`/`identity.go`'s internal code — same functions, same file boundaries, just a new directory.

## Decisions

**Move as one `package main`, multiple files, mirroring today's shape.** `cmd/api/` gets `main.go`, `identity.go`, `main_test.go`, `identity_test.go` — the same four files, same package, just relocated. Considered splitting into a separate importable package (e.g. `internal/api`) with `cmd/api/main.go` reduced to a thin wrapper, but that's the kind of restructuring `wire-videojob-http-endpoints` or a later change should decide deliberately, informed by what that change actually needs to wire in — doing it here would be scope creep unrelated to "move the composition root," and would make the diff much harder to review as a behavior-preserving move.

**Fix the `go test` working-directory mismatch with a `TestMain` chdir, not parallel state directories.** `TestMain` in `cmd/api/main_test.go` calls `os.Chdir("../..")` before `createDirs()`, so the moved tests exercise the exact same `uploads/`, `outputs/`, `temp/` directories the running app uses at the repo root. Considered creating a second set of `.gitkeep`'d directories under `cmd/api/` instead — rejected because it produces two candidate locations for where processed artifacts land, one of which is real and one of which only exists to satisfy `go test`'s directory convention; that's confusing to debug and adds `.gitignore` churn for no behavioral benefit. The chdir is one line, scoped to tests, and needs no other file added or ignored.

**`web/` moves physically into `cmd/api/web/`.** Not optional — `go:embed` enforces this. No alternative considered; it's a hard constraint of the toolchain, not a design choice.

**Dockerfile build path becomes `./cmd/api`, everything else in the build stays as-is.** `COPY . .` already copies the whole repo (including the new `cmd/api/` tree), so only the `go build` invocation's target changes. `gosec ./...`, `govulncheck ./...`, `go vet ./...`, and `go test ./...` all already operate recursively from the module root and need no change.

## Risks / Trade-offs

- [Risk] Missing a cwd-relative path somewhere in `main_test.go`/`identity_test.go` that the chdir fix doesn't cover, causing a silent test-data location bug. → Mitigation: the design was informed by an explicit grep across both files for `ReadFile`/`WriteFile`/`filepath.Join`/`ReadDir`/`Open`/`MkdirAll`/`TempDir`; every hit is either one of the three known directories or `t.TempDir()`. `go test ./... -v` passing locally (or via the Docker fallback) after the move is the acceptance check, not just a code-review read-through.
- [Risk] Forgetting a doc/config file that still references the old `main.go`/`web/` paths. → Mitigation: this proposal's Impact section enumerates every permanent doc with a stale reference (`CLAUDE.md`, `README.md`, `docs/architecture.md`, `docs/development.md`, `docs/domain-model.md`, `docs/flows.md`, `docs/operations.md`, `docs/roadmap.md`), scoped to the finalization PR per this repo's PR-sequencing rules, and `tasks.md` tracks the update there — not just the three files an earlier draft of this proposal named.
- [Risk] The canonical `ddd-architecture` spec's "Frontend as Presentation/Delivery Layer" requirement names `web/index.html`/`web/styles.css`/`web/app.js` directly — if the delta only touched "Monorepo Package Topology," archiving this change would leave that requirement describing paths that no longer exist. → Mitigation: this change's delta spec includes a full `MODIFIED` block for that requirement too, with every path updated to `cmd/api/web/...`.

## Migration Plan

Single PR-sequence, no rollback complexity beyond a normal revert: `git mv` the four Go files and `web/` into `cmd/api/`, add the `TestMain` chdir, update the Dockerfile, verify `go vet ./...` and `go test ./... -v` pass (via Docker if `ffmpeg` isn't on `PATH`), then `docker compose run --build --rm app-test go test ./... -v` to confirm the containerized path also still works end-to-end.
