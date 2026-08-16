## MODIFIED Requirements

### Requirement: Monorepo Package Topology Is the Target Structure

The repository SHALL evolve toward a monorepo topology with `cmd/api` and `cmd/worker` as separate entrypoints sharing `internal/` packages. This topology SHALL NOT require a big-bang rewrite; `main.go` MAY remain functional during incremental migration.

#### Scenario: API and worker share domain and application packages

- **GIVEN** `cmd/api` and `cmd/worker` both exist in the repository
- **WHEN** they both need to work with `VideoJob`
- **THEN** they both import from `internal/video/domain` and `internal/video/application` — the domain logic is not duplicated

#### Scenario: Each cmd entrypoint produces an independent deployable binary

- **GIVEN** the monorepo topology is in place
- **WHEN** `go build ./cmd/api` and `go build ./cmd/worker` are run
- **THEN** each produces an independent binary that can be containerized and deployed separately

#### Scenario: cmd/api is the actual HTTP composition root

- **GIVEN** `main.go`, `identity.go`, and their test files have been moved into `cmd/api/`
- **WHEN** the server is built or run
- **THEN** `go build -o app ./cmd/api` and `go run ./cmd/api` produce the same HTTP behavior the repo-root `main.go` produced before the move, and no `main.go` exists at the repo root anymore

### Requirement: Frontend as Presentation/Delivery Layer

The web frontend (HTML/CSS/JavaScript in `cmd/api/web/index.html`, `cmd/api/web/styles.css`, and `cmd/api/web/app.js`, embedded into the binary via `go:embed` and served as `GET /`, `GET /styles.css`, and `GET /app.js` respectively) SHALL be treated as a presentation/delivery layer, not as a bounded context. It SHALL remain functional throughout all phases of the DDD migration, and any backend contract change that affects its consumed endpoints SHALL include an explicit task to update it.

#### Scenario: Frontend is not a bounded context

- **GIVEN** the system is organized into bounded contexts
- **WHEN** the HTML/CSS/JS served by `GET /` is evaluated
- **THEN** it SHALL NOT be assigned domain responsibilities, aggregate roots, or domain events; it is a delivery layer that consumes the Video Processing context's HTTP API

#### Scenario: Frontend extraction preserves GET / behavior

- **GIVEN** `cmd/api/web/index.html`, `cmd/api/web/styles.css`, and `cmd/api/web/app.js` have been extracted from `getHTMLForm()` and are served via `go:embed`
- **WHEN** a browser requests `GET /`
- **THEN** the server returns HTTP 200 with the HTML page and the page renders without JavaScript errors

#### Scenario: POST /upload remains available during async migration

- **GIVEN** Phase 6 introduces `POST /jobs` as the canonical async job endpoint
- **WHEN** an existing client sends a request to `POST /upload`
- **THEN** the endpoint SHALL remain available and SHALL accept the same multipart form data; only the response schema changes (returns job ID + status URL instead of a direct download link)

#### Scenario: Backend contract change must not silently break the frontend

- **GIVEN** a backend change adds, renames, or removes an HTTP endpoint consumed by the frontend
- **WHEN** the change is being specified and implemented
- **THEN** the same OpenSpec change SHALL include a task to update `cmd/api/web/app.js` to reflect the new contract

#### Scenario: Full-flow non-regression passes at each phase

- **GIVEN** any phase change that modifies API contracts or routing
- **WHEN** implementation is complete and before the PR is merged
- **THEN** uploading a video through the web UI must result in a downloadable zip — verified via browser interaction or a curl sequence simulating the complete upload → poll → download flow
