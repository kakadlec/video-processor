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
