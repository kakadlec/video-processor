## ADDED Requirements

### Requirement: Each Bounded Context Owns Its Own HTTP Process

Every bounded context that exposes an HTTP surface SHALL expose it from its own process, and no process SHALL serve routes belonging to more than one bounded context. A context's HTTP process SHALL require only that context's own configuration, plus the configuration of the cross-cutting infrastructure its middleware genuinely uses, and SHALL be buildable, deployable, startable, and restartable without any other HTTP process running.

The three HTTP processes are `cmd/identity-api` (`/api/auth/*`), `cmd/video-api` (the frontend, `POST /upload`, `GET /download/:filename`, `GET /api/status`, and the `/api/video-jobs` routes, together with the dispatch outbox relay) and `cmd/notification-api` (`/api/notification-preferences`). They SHALL be reached through a single ingress that routes by path prefix, so the external contract — paths, methods, status codes, bodies, and the host port they are published on — is unchanged by the split. No HTTP service SHALL publish a host port of its own; the ingress SHALL be the only one that does.

This is what makes the configuration surfaces meaningful rather than declarative: `cmd/identity-api` SHALL have no object-storage, broker, or video-database configuration to read, and `cmd/video-api` and `cmd/notification-api` SHALL have no identity-database configuration to read. A dependency a process does not use SHALL be absent from it, not merely unread.

#### Scenario: Each HTTP process serves exactly one context's routes

- **GIVEN** the three HTTP composition roots
- **WHEN** the routes each one registers are inspected
- **THEN** every route belongs to that process's own bounded context, and no route is registered by more than one process

#### Scenario: An HTTP process starts with only its own configuration

- **GIVEN** any one of the three HTTP processes
- **WHEN** it is started with only the configuration its own context requires, and with the other contexts' variables absent
- **THEN** it starts and serves its routes

#### Scenario: One context's HTTP process failing does not stop another's

- **GIVEN** all three HTTP processes are running
- **WHEN** one of them is stopped or fails to start
- **THEN** the other two continue serving their own routes

#### Scenario: The ingress preserves the external contract

- **GIVEN** the ingress in front of the three services
- **WHEN** a client performs the documented end-to-end flow — register, log in, upload, poll, read status, request a download URL, redeem it
- **THEN** every request uses the same path, method, and host port it used before the split, and receives the same status code and body shape

#### Scenario: The ingress does not bound or buffer an upload

- **GIVEN** a video upload larger than the ingress software's default request-body limit
- **WHEN** it is sent to `POST /upload` through the ingress
- **THEN** the ingress neither rejects it for size nor buffers the body to its own disk before forwarding, so the extension check runs on the first part and the bytes are streamed into object storage and hashed in a single pass as `videojob-source-storage` requires

## MODIFIED Requirements

### Requirement: Package Dependency Rules

The package structure SHALL enforce a strict dependency hierarchy so that domain logic is never coupled to infrastructure concerns.

#### Scenario: Domain packages have no infrastructure imports

- **GIVEN** any Go file under `internal/<context>/domain/`
- **WHEN** its imports are inspected
- **THEN** it SHALL NOT import any package from `internal/<context>/infrastructure/`, any HTTP framework, any database driver, any message broker client, or any cache client

#### Scenario: Application packages depend only on domain interfaces

- **GIVEN** any Go file under `internal/<context>/application/`
- **WHEN** its imports are inspected
- **THEN** it SHALL NOT import any package from `internal/<context>/infrastructure/` directly; it SHALL depend only on repository and port interfaces defined in `internal/<context>/domain/`

#### Scenario: Infrastructure packages implement domain interfaces

- **GIVEN** any Go file under `internal/<context>/infrastructure/`
- **WHEN** it provides a repository or port implementation
- **THEN** the implementation type SHALL satisfy the interface declared in `internal/<context>/domain/`, not define its own contract

#### Scenario: No direct cross-context domain imports

- **GIVEN** any Go file in any bounded context's packages
- **WHEN** it needs to reference a concept from another bounded context
- **THEN** it SHALL NOT import another context's `domain` or `application` packages directly; each bounded context SHALL define and own its own local value object for any identifier that crosses a context boundary (e.g. each of `internal/identity/domain` and `internal/video/domain` defines its own `UserID` type), and translation between a source context's identifier and a consuming context's local type SHALL happen only at a composition root (`cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`, `cmd/worker`, or `cmd/notifier`) or via consumed integration events — never via a package shared between the two contexts' `domain` layers

#### Scenario: A test-only package may import two contexts to pin their shared contract

- **GIVEN** `internal/contracts`, whose sole purpose is to assert that one context's copy of another's integration contract still equals the original
- **WHEN** its imports are inspected
- **THEN** it MAY import packages of more than one bounded context, because no composition root imports both any more and the drift it detects is otherwise silent; and this permission SHALL be conditional on the package declaring nothing outside its `_test.go` files apart from a package comment, and on no other package in the repository importing it — both asserted by a test in the package itself. The first is what makes it undependable, since a package exporting nothing cannot be imported for a symbol; the second closes the blank import the first still permits. Together they keep it from ever becoming a shared domain package

#### Scenario: Composition root is the only DI boundary

- **GIVEN** any of `cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`, `cmd/worker`, or `cmd/notifier`
- **WHEN** it initializes the application
- **THEN** it is the only place where `infrastructure` adapters are instantiated and injected into `application` use cases

### Requirement: Monorepo Package Topology Is the Target Structure

The repository SHALL have a monorepo topology with `cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`, `cmd/worker`, and `cmd/notifier` as separate entrypoints sharing `internal/` packages. All five entrypoints now exist, so the scenarios below are live obligations rather than targets: each SHALL build independently, and each SHALL wire its own composition root requiring only the configuration it uses, rather than one binary switching behavior on a mode flag. Cross-cutting **infrastructure** plumbing that no single bounded context owns (e.g. a shared Redis connection used by more than one context) lives under `internal/platform/`, distinct from any bounded context's own `internal/<context>/infrastructure/`. This is infrastructure sharing only — it does NOT permit sharing `domain` or `application` logic between contexts, which remains forbidden by the "No direct cross-context domain imports" scenario above.

The event consumers sharpen rather than change the rule. `cmd/notifier` consumes an integration event that the Video Processing context emits, which is the sanctioned crossing; it SHALL obtain the names and payload shapes it needs from its own context's declarations, never by importing the emitting context's packages, and the translation from the event's user identifier to its own `UserID` SHALL happen in its composition root. A composition root MAY import more than one context — that is what makes it a composition root — but the packages under `internal/notification/` SHALL NOT, and that constraint SHALL hold for every package of the context, infrastructure included, not for its `domain` and `application` packages alone.

Since the HTTP tier was split by bounded context, **no composition root actually imports two contexts any more**. That permission stands as written, because it is what makes a composition root one, but the pinning tests that relied on a root exercising it live in `internal/contracts` instead — see the dependency rule above, and `notification-event-consumer` and `notification-preferences` for what they pin.

#### Scenario: The video API and the worker share domain and application packages

- **GIVEN** `cmd/video-api` and `cmd/worker` both exist in the repository
- **WHEN** they both need to work with `VideoJob`
- **THEN** they both import from `internal/video/domain` and `internal/video/application` — the domain logic is not duplicated

#### Scenario: Each cmd entrypoint produces an independent deployable binary

- **GIVEN** the monorepo topology is in place
- **WHEN** `go build ./cmd/identity-api`, `go build ./cmd/video-api`, `go build ./cmd/notification-api`, `go build ./cmd/worker`, and `go build ./cmd/notifier` are run
- **THEN** each produces an independent binary that can be containerized and deployed separately

#### Scenario: No HTTP composition root serves more than one context

- **GIVEN** the three HTTP entrypoints
- **WHEN** each one's imports are inspected
- **THEN** none imports the `domain` or `application` packages of a bounded context other than its own, and `cmd/identity-api` links no object-storage, broker, or `ffmpeg`-invoking package at all

#### Scenario: cmd/notifier wires only the Notification context

- **GIVEN** `cmd/notifier` exists as the Notification context's event consumer
- **WHEN** its composition root is built
- **THEN** it requires only the Notification context's own configuration and the broker URL, and it imports no package of the Video Processing or Identity contexts to interpret the events it consumes

#### Scenario: Shared infrastructure with no owning context lives under internal/platform

- **GIVEN** a piece of infrastructure (e.g. a Redis connection) is used by more than one bounded context or by transport-level middleware with no bounded-context relationship at all
- **WHEN** it is added to the codebase
- **THEN** it lives under `internal/platform/`, not under any single `internal/<context>/infrastructure/`, and it contains only connection/lifecycle plumbing — never domain or application logic for a specific context's use case

### Requirement: Frontend as Presentation/Delivery Layer

The web frontend (HTML/CSS/JavaScript in `cmd/video-api/web/index.html`, `cmd/video-api/web/styles.css`, and `cmd/video-api/web/app.js`, embedded into the binary via `go:embed` and served as `GET /`, `GET /styles.css`, and `GET /app.js` respectively) SHALL be treated as a presentation/delivery layer, not as a bounded context. It SHALL remain functional throughout all phases of the DDD migration, and any backend contract change that affects its consumed endpoints SHALL include an explicit task to update it.

It is served by the Video Processing context's HTTP process because that is the context whose API it consumes, and it is reached through the same ingress as every other route, on the same origin. Splitting the HTTP tier SHALL NOT make the frontend aware that more than one process exists: every URL it calls SHALL keep its path and its origin, so no cross-origin request and no per-service base URL is introduced.

Where the frontend polls a backend endpoint, that polling SHALL be treated as a consumer of the same per-user request budget as its other calls, and SHALL back off rather than retry at a fixed rate when the backend signals that the budget is exhausted. A delivery layer that turns a rate-limit response into a reported failure, or into a tighter retry loop, is not remaining functional in the sense this requirement means.

#### Scenario: Frontend is not a bounded context

- **GIVEN** the system is organized into bounded contexts
- **WHEN** the HTML/CSS/JS served by `GET /` is evaluated
- **THEN** it SHALL NOT be assigned domain responsibilities, aggregate roots, or domain events; it is a delivery layer that consumes the Video Processing context's HTTP API

#### Scenario: Frontend extraction preserves GET / behavior

- **GIVEN** `cmd/video-api/web/index.html`, `cmd/video-api/web/styles.css`, and `cmd/video-api/web/app.js` have been extracted from `getHTMLForm()` and are served via `go:embed`
- **WHEN** a browser requests `GET /`
- **THEN** the server returns HTTP 200 with the HTML page and the page renders without JavaScript errors

#### Scenario: The split leaves the frontend's source unchanged

- **GIVEN** the HTTP tier has been split into three services behind one ingress
- **WHEN** `cmd/video-api/web/app.js` is compared with the version served before the split
- **THEN** its request URLs are unchanged, because every one of them resolves against the same origin and path it always did

#### Scenario: POST /upload is itself the canonical async endpoint

- **GIVEN** Phase 6's asynchronous migration is complete
- **WHEN** an existing client sends a request to `POST /upload`
- **THEN** the endpoint SHALL remain available at the same path and SHALL accept the same multipart form data; only the response schema changes (returns job ID + status URL instead of a direct download link), and no separate submission endpoint is introduced alongside it

#### Scenario: The pre-existing job endpoint is not the async submission path

- **GIVEN** `POST /api/video-jobs` already exists from Phase 3, accepting a filename in JSON and carrying no uploaded bytes
- **WHEN** the asynchronous migration chooses which endpoint submits work
- **THEN** it is `POST /upload`, because that is the endpoint that receives the bytes; `POST /api/video-jobs` keeps having no processing trigger, and the frontend's submission path is unchanged apart from how it reads the response

#### Scenario: Backend contract change must not silently break the frontend

- **GIVEN** a backend change adds, renames, or removes an HTTP endpoint consumed by the frontend
- **WHEN** the change is being specified and implemented
- **THEN** the same OpenSpec change SHALL include a task to update `cmd/video-api/web/app.js` to reflect the new contract

#### Scenario: Full-flow non-regression passes at each phase

- **GIVEN** any phase change that modifies API contracts or routing
- **WHEN** implementation is complete and before the PR is merged
- **THEN** uploading a video through the web UI must result in a downloadable zip — verified via browser interaction or a curl sequence simulating the complete upload → poll → download flow
