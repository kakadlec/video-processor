## Why

The composition root for HTTP is still `main.go` at the repo root, a leftover from before Phase 3's monorepo topology (`cmd/api` / `cmd/worker`) was introduced. `wire-videojob-http-endpoints` (the next Change Backlog row) is scoped to add new endpoints to the real composition root, not to `main.go` — so the move has to land first, on its own, as a pure structural change with no behavior difference, to avoid conflating "where the code lives" with "what the code does" in one diff.

## What Changes

- Move `main.go`, `identity.go`, `main_test.go`, and `identity_test.go` from the repo root into `cmd/api/`, unchanged except for the package's new location (still `package main`, same file names, same code).
- Move `web/` (`index.html`, `styles.css`, `app.js`) into `cmd/api/web/`, since `//go:embed web` can only embed a directory at or below the embedding file's own location — `cmd/api/main.go` cannot embed a `web/` directory that lives outside `cmd/api/`.
- Add a `TestMain` chdir to the repo root (`os.Chdir("../..")`) before `createDirs()`, because `go test` runs a package's tests with the package's own directory as the working directory — without this, the moved tests would create `cmd/api/uploads`, `cmd/api/outputs`, `cmd/api/temp` instead of the real `uploads/`, `outputs/`, `temp/` the running app actually uses.
- Update the Dockerfile's build step from `go build -o /out/app .` to `go build -o /out/app ./cmd/api`.
- No route, request/response shape, or runtime behavior changes. `go run ./cmd/api` from the repo root still creates `uploads/`, `outputs/`, `temp/` at the repo root exactly as `go run .` does today, because those directories are resolved relative to the process's working directory, not the source file's location.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `ddd-architecture`: the "Monorepo Package Topology Is the Target Structure" requirement's scenario "Each cmd entrypoint produces an independent deployable binary" moves from anticipated to actually true for `cmd/api` — `main.go` no longer exists at the repo root once this change ships. The "Frontend as Presentation/Delivery Layer" requirement also needs its `web/index.html`/`web/styles.css`/`web/app.js` path references updated to `cmd/api/web/...`, since those files move too.

## Impact

- **Moved**: `main.go`, `identity.go`, `main_test.go`, `identity_test.go`, `web/*` → `cmd/api/`.
- **Changed**: `Dockerfile` build command.
- **Not in scope for the implementation PR** (finalization PR only, per this repo's PR-scope rules) — every permanent doc with a stale `main.go`/`go run .`/`go build -o app .`/`web/` reference once the move lands: `CLAUDE.md`, `README.md`, `docs/architecture.md` (Target Package Topology tree, Dependency Rules note, Current Implementation section), `docs/development.md` (run/build commands), `docs/domain-model.md` (composition-root reference), `docs/flows.md` (sequence-diagram labels, frontend section), `docs/operations.md` (`go run .`, `PORT` description), `docs/roadmap.md` (Change Backlog row status).
- No change to `go.mod`, `docker-compose.yml`, or `.github/workflows/ci.yml` — all three already operate on `./...` or the module root and need no path update.
