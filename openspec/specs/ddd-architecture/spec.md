# ddd-architecture Specification

## Purpose

Defines the tactical Domain-Driven Design model, bounded context boundaries, aggregate root invariants, package dependency rules, and the phased evolution roadmap for FIAP X. This specification is the authoritative reference for the system's domain structure; all subsequent implementation changes must be consistent with it.

## Requirements

### Requirement: Three Bounded Contexts With Non-Overlapping Responsibilities

The system SHALL be organized into exactly three bounded contexts — `Identity`, `Video Processing`, and `Notification` — each with a clearly delimited responsibility. No domain concept SHALL be owned by more than one context.

#### Scenario: Identity context owns user credentials and token issuance

- **GIVEN** a user wants to authenticate
- **WHEN** they submit credentials
- **THEN** only the Identity context validates credentials and issues tokens; no other context stores passwords or issues tokens

#### Scenario: Video Processing context owns the job lifecycle

- **GIVEN** an authenticated user uploads a video
- **WHEN** the upload is accepted
- **THEN** the Video Processing context creates, tracks, and completes the `VideoJob`; no other context mutates job state

#### Scenario: Notification context reacts to events without being called directly

- **GIVEN** a `VideoJobCompleted` or `VideoJobFailed` event is emitted
- **WHEN** the Notification context receives it
- **THEN** it delivers the notification per the user's preferences without the Video Processing context knowing or caring how delivery works

### Requirement: VideoJob as Aggregate Root With Enforced Invariants

The `VideoJob` SHALL be the aggregate root of the Video Processing bounded context. Its invariants SHALL be enforced at the domain layer, not at the application or infrastructure layer.

#### Scenario: VideoJob is created in pending state

- **GIVEN** a valid video file has been stored
- **WHEN** `CreateVideoJob` is invoked
- **THEN** a `VideoJob` is created with `status: pending`, `FrameCount: 0`, and an empty `ErrorReason`

#### Scenario: FrameCount is zero until job completes

- **GIVEN** a `VideoJob` in any state other than `completed`
- **WHEN** its `FrameCount` is read
- **THEN** it SHALL be zero

#### Scenario: ErrorReason is empty unless job has failed

- **GIVEN** a `VideoJob` in any state other than `failed`
- **WHEN** its `ErrorReason` is read
- **THEN** it SHALL be empty

#### Scenario: StorageKey for the result is set only on completion

- **GIVEN** a `VideoJob` transitions to `completed`
- **WHEN** the transition is applied
- **THEN** `StorageKey` for the result and `FrameCount` MUST be set atomically in the same operation

### Requirement: Valid State Machine Transitions Only

The `VideoJob` status SHALL only advance through the defined state machine. Backwards transitions and undefined transitions SHALL be rejected as domain errors.

#### Scenario: Job advances from pending to queued

- **GIVEN** a `VideoJob` in `pending` state
- **WHEN** `EnqueueVideoJob` is called
- **THEN** the job transitions to `queued`

#### Scenario: Job advances from queued to processing

- **GIVEN** a `VideoJob` in `queued` state
- **WHEN** the worker dequeues the job and calls `StartProcessing`
- **THEN** the job transitions to `processing`

#### Scenario: Job advances from processing to completed

- **GIVEN** a `VideoJob` in `processing` state
- **WHEN** the worker successfully extracts frames and calls `CompleteJob`
- **THEN** the job transitions to `completed` with `FrameCount` and result `StorageKey` populated

#### Scenario: Job advances from processing to failed

- **GIVEN** a `VideoJob` in `processing` state
- **WHEN** the worker encounters an unrecoverable error and calls `FailJob`
- **THEN** the job transitions to `failed` with a non-empty `ErrorReason`

#### Scenario: Backwards transition is rejected

- **GIVEN** a `VideoJob` in `completed` or `failed` state
- **WHEN** any transition command is applied
- **THEN** the domain layer rejects the command with an error; the job state is not mutated

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
- **THEN** it SHALL NOT import another context's `domain` or `application` packages directly; each bounded context SHALL define and own its own local value object for any identifier that crosses a context boundary (e.g. each of `internal/identity/domain` and `internal/video/domain` defines its own `UserID` type), and translation between a source context's identifier and a consuming context's local type SHALL happen only at the composition root (`cmd/api`, `cmd/worker`, or `main.go` during incremental migration) or via consumed integration events — never via a package shared between the two contexts' `domain` layers

#### Scenario: Composition root is the only DI boundary

- **GIVEN** `cmd/api` or `cmd/worker`
- **WHEN** it initializes the application
- **THEN** it is the only place where `infrastructure` adapters are instantiated and injected into `application` use cases

### Requirement: Domain Events as Cross-Context Integration Contracts

Domain events emitted by one bounded context and consumed by another SHALL be defined with a stable, versioned schema. Event payloads SHALL be immutable once emitted.

#### Scenario: VideoJobCompleted event carries required fields

- **GIVEN** a `VideoJob` transitions to `completed`
- **WHEN** a `VideoJobCompleted` integration event is emitted
- **THEN** it SHALL contain at minimum: `type`, `job_id`, `user_id`, `frame_count`, `storage_key`, and `occurred_at`

#### Scenario: VideoJobFailed event carries required fields

- **GIVEN** a `VideoJob` transitions to `failed`
- **WHEN** a `VideoJobFailed` integration event is emitted
- **THEN** it SHALL contain at minimum: `type`, `job_id`, `user_id`, `error_reason`, and `occurred_at`

#### Scenario: Notification context consumes events without coupling to Video Processing internals

- **GIVEN** the Notification context receives a `VideoJobCompleted` or `VideoJobFailed` event
- **WHEN** it processes the event
- **THEN** it SHALL NOT import or call any package from the Video Processing bounded context to do so

### Requirement: Redis Responsibilities Are Additive, Not a Replacement for PostgreSQL or RabbitMQ

Redis SHALL be used as a mandatory performance and reliability layer with four defined responsibilities. It SHALL NOT replace PostgreSQL as the authoritative state store, and it SHALL NOT replace RabbitMQ as the durable job queue.

#### Scenario: Idempotency key prevents duplicate job creation

- **GIVEN** a client retries a `POST /upload` with the same content within the idempotency window
- **WHEN** the API processes the retry
- **THEN** Redis returns the existing `VideoJobID` and the handler returns the existing job without creating a duplicate or re-enqueuing

#### Scenario: Rate limiting rejects excess requests

- **GIVEN** a user exceeds the configured request rate
- **WHEN** their next request arrives
- **THEN** the rate-limiting middleware (backed by Redis) rejects it with HTTP 429 before it reaches the handler

#### Scenario: Status cache absorbs repeated polling reads

- **GIVEN** a client polls `GET /jobs/{id}/status` repeatedly
- **WHEN** the job state has not changed since the last DB write
- **THEN** the response is served from the Redis status cache without a PostgreSQL query

#### Scenario: Cache invalidation is tied to state transition writes

- **GIVEN** a job transitions to a new state and that transition is written to PostgreSQL
- **WHEN** the write succeeds
- **THEN** the Redis status cache entry for that job is invalidated or updated atomically with the DB write (or immediately after, within the same request/transaction scope)

#### Scenario: PostgreSQL is authoritative on cache miss

- **GIVEN** the Redis status cache has no entry for a job
- **WHEN** a status request arrives
- **THEN** the application falls back to PostgreSQL, returns the correct current state, and may repopulate the cache

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

### Requirement: Permanent Project Documentation Is Accurate, Current-State-Faithful, and Separate from OpenSpec Artifacts

The repository SHALL include a set of permanent documentation files (`README.md`, `docs/architecture.md`, `docs/domain-model.md`, `docs/flows.md`, `docs/development.md`, `docs/operations.md`, `docs/roadmap.md`) serving as the stable reference for contributors, evaluators, and operators. These files are distinct from OpenSpec artifacts: OpenSpec governs change proposals and implementation tasks; permanent docs describe the system as it exists and where it is going, in terms readable without knowledge of the OpenSpec workflow.

#### Scenario: Documentation distinguishes current implementation from target architecture

- **GIVEN** any documentation file that describes the system architecture
- **WHEN** it references a component or pattern that does not yet exist in `main.go`
- **THEN** it SHALL explicitly label that component as planned, future, or "Phase N" — never as currently implemented

#### Scenario: Documentation does not claim unimplemented infrastructure as existing

- **GIVEN** the current codebase contains only `main.go` with local filesystem state
- **WHEN** any documentation file mentions PostgreSQL, Redis, RabbitMQ, MinIO, user authentication, async processing, or webhook notifications
- **THEN** these MUST appear only in "planned" or "target architecture" sections, not in any section describing the current running system

#### Scenario: README commands are runnable against the current codebase

- **GIVEN** the `README.md` quickstart section lists commands
- **WHEN** a reader executes those commands against the current repository
- **THEN** each command runs successfully, or is explicitly labeled as requiring a future phase or optional prerequisite

#### Scenario: OpenSpec artifacts and permanent docs serve different audiences without overlap

- **GIVEN** a contributor wants to understand how to propose a change
- **WHEN** they consult the permanent docs
- **THEN** the docs MAY reference the OpenSpec workflow without duplicating or replacing OpenSpec artifact content; OpenSpec artifacts SHALL NOT duplicate the developer-facing content of the permanent docs

#### Scenario: Documentation roadmap contains exactly eight phases

- **GIVEN** `docs/roadmap.md` summarizes the evolution roadmap
- **WHEN** a reader counts the phases
- **THEN** exactly 8 phases are listed (1–8); no ninth or additional phase is present; the file references `openspec/specs/ddd-architecture/spec.md` as the canonical source

#### Scenario: Documentation updates land in the finalization PR, not the implementation PR

- **GIVEN** the PR separation policy (propose PR → implementation PR → finalization PR)
- **WHEN** permanent documentation (`README.md`, files under `docs/`, `CLAUDE.md`, `AGENTS.md`) needs to change as a consequence of a shipped change
- **THEN** it SHALL be updated in that change's finalization PR (alongside task checkoffs, spec promotion, the archive folder move, and the `docs/roadmap.md` Change Backlog status flip) — never in the implementation PR, which contains only the application source, test, and configuration/CI files that change's own declared proposal scope names
