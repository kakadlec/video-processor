## 1. OpenSpec artifacts

- [x] 1.1 Create `openspec/changes/establish-ddd-architecture-foundation/.openspec.yaml` with `schema: spec-driven` and today's date.
- [x] 1.2 Write `proposal.md`: why (structural debt ahead of planned features), what changes (spec-only, no code), capabilities (new `ddd-architecture`), impact (no application files modified).
- [x] 1.3 Write `design.md`: context, goals/non-goals, bounded contexts (Identity, Video Processing, Notification), VideoJob aggregate (value objects, state machine, invariants), use cases per context, domain events and integration contracts, package topology (including `web/`), frontend/presentation layer section, dependency rules, ADRs (1–7), risks, open questions, and evolution roadmap (phases 1–8).
- [x] 1.4 Write `specs/ddd-architecture/spec.md`: delta spec formalizing the DDD architecture requirements in OpenSpec requirement/scenario format, covering bounded context isolation, aggregate root invariants, dependency rules, and state machine transitions.
- [x] 1.5 Write this `tasks.md`.
- [x] 1.6 Update all artifacts to incorporate the frontend/presentation layer: `proposal.md` (What Changes + capability description), `design.md` (goals, package topology with `web/`, new "Frontend / Presentation Layer" section with extraction direction, compatibility strategy, non-regression criteria, and contract rule; roadmap phases 1, 3, and 6), `tasks.md` (this update and acceptance criteria 2.12–2.18), `specs/ddd-architecture/spec.md` (new Requirement for frontend as presentation/delivery layer).
- [x] 1.7 Update `proposal.md` to add permanent project documentation as a new deliverable in "What Changes", a new capability (`permanent-project-documentation`) in "New Capabilities", and an updated "Impact" section that distinguishes the spec PR (only OpenSpec artifacts) from the documentation PR (creates `README.md` and `docs/**`).
- [x] 1.8 Add "Permanent Project Documentation" section to `design.md` defining the 7 artifacts, the OpenSpec-vs-docs distinction, per-file content requirements, content rules (including prohibition on undeclared components), and consistency criteria.
- [x] 1.9 Add section 3 (documentation PR tasks 3.1–3.7) and section 4 (documentation PR acceptance criteria 4.1–4.10) to `tasks.md`; add acceptance criteria 2.19–2.25 for the spec-level additions to section 2.
- [x] 1.10 Add Requirement for permanent project documentation to `specs/ddd-architecture/spec.md` with scenarios covering current-vs-target distinction, prohibition on undeclared components, runnable README commands, OpenSpec–docs separation, roadmap phase count, and documentation PR isolation.

## 2. Acceptance criteria verification

> These items verify this change is complete and correct. They do NOT require code changes — they verify the artifact content is internally consistent and matches the design.

- [x] 2.1 All three bounded contexts (Identity, Video Processing, Notification) are named and have distinct, non-overlapping responsibilities documented in `design.md`.
- [x] 2.2 `VideoJob` value objects table lists at least `VideoJobID`, `UserID`, `OriginalFilename`, `StorageKey`, `FrameCount`, `ErrorReason`, and `JobStatus`.
- [x] 2.3 The `JobStatus` state machine in `design.md` includes exactly these states: `pending`, `queued`, `processing`, `completed`, `failed`, with arrows showing valid transitions only.
- [x] 2.4 `design.md` lists at least five domain events (`VideoJobCreated`, `VideoJobQueued`, `VideoJobStarted`, `VideoJobCompleted`, `VideoJobFailed`), each with their JSON field signatures.
- [x] 2.5 Dependency rules in `design.md` explicitly state that `domain` must not import `application` or `infrastructure`, and that no bounded context may import another context's `domain` package.
- [x] 2.6 ADR-1 explicitly states that RabbitMQ is the async transport and that Redis does NOT substitute for it.
- [x] 2.7 ADR-4 explicitly lists Redis's four responsibilities: idempotency, rate limiting, status cache, and distributed locks — and states that PostgreSQL is the source of truth.
- [x] 2.8 The roadmap table in `design.md` contains exactly eight phases (1–8) covering: DDD foundation, identity/auth, VideoJob persistence, Redis capabilities, MinIO, RabbitMQ + worker, notifications, observability.
- [x] 2.9 `specs/ddd-architecture/spec.md` contains at least one formally stated Requirement and one Scenario per bounded context, plus requirements for dependency rules and aggregate invariants.
- [x] 2.10 No file outside `openspec/changes/establish-ddd-architecture-foundation/` has been modified by this change.
- [x] 2.11 `npx --yes @fission-ai/openspec validate establish-ddd-architecture-foundation` exits without error (or the flags `--strict --no-interactive` are not supported and the basic validate passes).
- [x] 2.12 `design.md` includes a "Frontend / Presentation Layer" section that explicitly classifies the frontend as a delivery/presentation layer and states it is not a bounded context.
- [x] 2.13 `design.md` specifies the incremental extraction path: `web/index.html`, `web/styles.css`, and `web/app.js`, with Go continuing to serve them via a static file handler — no separate build toolchain introduced.
- [x] 2.14 `design.md` documents the API contract compatibility strategy for Phase 6: `POST /upload` remains available and its multipart form shape is unchanged; only the response schema changes (returns job ID + status URL instead of a download link).
- [x] 2.15 `design.md` contains a non-regression criteria table covering `GET /`, static assets, `POST /upload`, `GET /api/status`, `GET /download/:filename`, and a full frontend-to-API flow test (upload → poll → download).
- [x] 2.16 `design.md` states the rule that any change adding, renaming, or removing an HTTP endpoint consumed by the frontend must include a task to update `web/app.js` (or the inline JS until extraction) accordingly.
- [x] 2.17 Package topology in `design.md` includes the `web/` directory with `index.html`, `styles.css`, and `app.js`.
- [x] 2.18 The roadmap table still contains exactly eight phases; frontend extraction is incorporated into Phase 3 and frontend adaptation into Phase 6 — no ninth phase was added.
- [x] 2.19 `proposal.md` "What Changes" names permanent project documentation as a deliverable, distinguishes it from OpenSpec artifacts, and states that the documentation files will be created in a separate PR.
- [x] 2.20 `proposal.md` "New Capabilities" includes `permanent-project-documentation` with a one-line description covering architecture, domain model, flows, development, operations, and roadmap.
- [x] 2.21 `proposal.md` "Impact" distinguishes the spec PR (only files inside `openspec/changes/establish-ddd-architecture-foundation/`) from the documentation PR (creates `README.md` and `docs/**`), and states that no application code or CI is modified in either PR.
- [x] 2.22 `design.md` "Permanent Project Documentation" section lists all seven artifacts (`README.md`, `docs/architecture.md`, `docs/domain-model.md`, `docs/flows.md`, `docs/development.md`, `docs/operations.md`, `docs/roadmap.md`) with their required content.
- [x] 2.23 `design.md` content rules explicitly prohibit describing PostgreSQL, Redis, RabbitMQ, MinIO, authentication, or async processing as implemented before they exist in the codebase, and require present-vs-target labeling throughout.
- [x] 2.24 `design.md` states that `docs/roadmap.md` is a summary and the canonical roadmap lives in `design.md` and `specs/ddd-architecture/spec.md`; no documentation file may introduce a ninth phase or additional phases.
- [x] 2.25 `specs/ddd-architecture/spec.md` contains a Requirement for permanent project documentation with at least one Scenario enforcing current-vs-target distinction, one Scenario prohibiting undeclared components as present, one Scenario for runnable README commands, and one Scenario for documentation PR isolation.

## 3. Documentation PR deliverable

> These tasks define what must be created in the documentation PR (separate from and following the spec PR that contains this change's OpenSpec artifacts). None of these files exist yet. Mark each task checked only after the corresponding file is created, reviewed, and passes the criteria in § 4.

- [x] 3.1 Create `README.md` at the repository root: project name and description, prerequisites section listing Go version and ffmpeg, quickstart commands (verified runnable against the current codebase), links to every file in `docs/`, and an explicit current-limitations callout (synchronous, no auth, no async, local filesystem only).
- [x] 3.2 Create `docs/architecture.md`: current implementation description (single `main.go`, synchronous pipeline, local filesystem); target DDD structure (bounded contexts, package topology); 8-phase roadmap summary; explicit current-vs-target labeling on every architecture element.
- [x] 3.3 Create `docs/domain-model.md`: `VideoJob` aggregate and value objects table; state machine diagram (`pending → queued → processing → completed / failed`); domain events with JSON field signatures; bounded context responsibilities; `UserID` cross-context contract.
- [x] 3.4 Create `docs/flows.md`: current synchronous request flow (upload → ffmpeg → zip → download); target async flow (upload → queue → worker → MinIO → notify); frontend interaction sequence (what the browser calls at each step, current and planned); current-vs-target labeling throughout.
- [x] 3.5 Create `docs/development.md`: local setup prerequisites; step-by-step local run (`go run main.go`); test execution (`go test ./... -v`) with ffmpeg caveat and Docker fallback command; Docker build/run commands; CLAUDE.md conventions summary (Conventional Commits, OpenSpec workflow, PR-separation rule).
- [x] 3.6 Create `docs/operations.md`: Docker deployment instructions; environment variables; runtime directory structure (`uploads/`, `temp/`, `outputs/`); future infrastructure responsibilities (PostgreSQL, Redis, RabbitMQ, MinIO — each labeled "planned, Phase N" with a one-sentence description of its role).
- [x] 3.7 Create `docs/roadmap.md`: 8-phase summary table with change name, scope, and current status per phase (Phase 1: specifying; Phases 2–8: planned); reference to `openspec/changes/establish-ddd-architecture-foundation/design.md` as the canonical roadmap source; explicit statement that exactly 8 phases are defined.

## 4. Documentation PR acceptance criteria

> These criteria verify the documentation PR content. They are checked during and after the documentation PR review — not during this spec change's PR review.

- [x] 4.1 `README.md` exists at the repository root and contains a project description, a prerequisites section naming Go and ffmpeg versions, a quickstart section with commands matching those in CLAUDE.md, and links to every file in `docs/`.
- [x] 4.2 Every command in `README.md` and `docs/development.md` has been verified to run successfully against the current codebase (`go run main.go` starts the server; `go test ./... -v` runs without error or has the ffmpeg-absence note; `docker build` and `docker run` complete without error).
- [x] 4.3 `docs/architecture.md` explicitly labels the current implementation (synchronous, single `main.go`) as distinct from the target DDD package topology; every component requiring a future phase (PostgreSQL, Redis, RabbitMQ, MinIO, async worker) is clearly marked "planned" or "Phase N."
- [x] 4.4 `docs/domain-model.md` contains the `VideoJob` value objects table, the state machine showing all five states and valid transitions, and the domain events table — each consistent with `design.md` in this change.
- [x] 4.5 `docs/flows.md` contains a description of the current synchronous flow and a description of the target async flow, labeled "current" and "target" (or "Phase 6+") respectively, plus a frontend interaction sequence covering both states.
- [x] 4.6 `docs/development.md` documents how to run tests locally including the ffmpeg prerequisite caveat and the Docker fallback (`docker build -t video-processor . && docker run --rm video-processor go test ./... -v`).
- [x] 4.7 `docs/operations.md` describes future infrastructure (PostgreSQL, Redis, RabbitMQ, MinIO) in the future tense or labeled as "planned, Phase N" — none appear as deployed or currently running.
- [x] 4.8 `docs/roadmap.md` shows exactly 8 phases (1–8); Phase 1 is described as specifying or complete and Phases 2–8 as planned; the file references `openspec/changes/establish-ddd-architecture-foundation/design.md` as the canonical source.
- [x] 4.9 No documentation file uses present-tense language implying that PostgreSQL, Redis, RabbitMQ, MinIO, JWT authentication, or async processing are currently implemented.
- [x] 4.10 The documentation PR modifies no files inside `openspec/changes/`, `openspec/specs/`, `main.go`, `main_test.go`, `go.mod`, `Dockerfile`, or CI workflow files.
