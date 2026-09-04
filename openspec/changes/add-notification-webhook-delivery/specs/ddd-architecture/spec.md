## MODIFIED Requirements

### Requirement: Monorepo Package Topology Is the Target Structure

The repository SHALL have a monorepo topology with `cmd/api`, `cmd/worker`, and `cmd/notifier` as separate entrypoints sharing `internal/` packages. All three entrypoints now exist, so the scenarios below are live obligations rather than targets: each SHALL build independently, and each SHALL wire its own composition root requiring only the configuration it uses, rather than one binary switching behavior on a mode flag. Cross-cutting **infrastructure** plumbing that no single bounded context owns (e.g. a shared Redis connection used by more than one context) lives under `internal/platform/`, distinct from any bounded context's own `internal/<context>/infrastructure/`. This is infrastructure sharing only — it does NOT permit sharing `domain` or `application` logic between contexts, which remains forbidden by the "No direct cross-context domain imports" scenario above.

The third entrypoint is the Notification context's event consumer, and its existence sharpens rather than changes the rule. It consumes an integration event that the Video Processing context emits, which is the sanctioned crossing; it SHALL obtain the names and payload shapes it needs from its own context's declarations, never by importing the emitting context's packages, and the translation from the event's user identifier to its own `UserID` SHALL happen in its composition root. A composition root MAY import more than one context — that is what makes it a composition root — but the packages under `internal/notification/` SHALL NOT, and that constraint SHALL hold for every package of the context, infrastructure included, not for its `domain` and `application` packages alone.

#### Scenario: API and worker share domain and application packages

- **GIVEN** `cmd/api` and `cmd/worker` both exist in the repository
- **WHEN** they both need to work with `VideoJob`
- **THEN** they both import from `internal/video/domain` and `internal/video/application` — the domain logic is not duplicated

#### Scenario: Each cmd entrypoint produces an independent deployable binary

- **GIVEN** the monorepo topology is in place
- **WHEN** `go build ./cmd/api`, `go build ./cmd/worker`, and `go build ./cmd/notifier` are run
- **THEN** each produces an independent binary that can be containerized and deployed separately

#### Scenario: cmd/api is the actual HTTP composition root

- **GIVEN** `main.go`, `identity.go`, and their test files have been moved into `cmd/api/`
- **WHEN** the server is built or run
- **THEN** `go build -o app ./cmd/api` and `go run ./cmd/api` produce the same HTTP behavior the repo-root `main.go` produced before the move, and no `main.go` exists at the repo root anymore

#### Scenario: cmd/notifier wires only the Notification context

- **GIVEN** `cmd/notifier` exists as the Notification context's event consumer
- **WHEN** its composition root is built
- **THEN** it requires only the Notification context's own configuration and the broker URL, and it imports no package of the Video Processing or Identity contexts to interpret the events it consumes

#### Scenario: Shared infrastructure with no owning context lives under internal/platform

- **GIVEN** a piece of infrastructure (e.g. a Redis connection) is used by more than one bounded context or by transport-level middleware with no bounded-context relationship at all
- **WHEN** it is added to the codebase
- **THEN** it lives under `internal/platform/`, not under any single `internal/<context>/infrastructure/`, and it contains only connection/lifecycle plumbing — never domain or application logic for a specific context's use case
