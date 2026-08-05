## Why

The current codebase is a single `main.go` with no internal structure: processing is synchronous and in-request, state lives on the local filesystem, and there are no domain concepts beyond raw HTTP handlers. The hackathon requirements (video frame extraction, async processing pipeline, user authentication, notifications) demand an evolution that a flat package cannot safely absorb without accumulating regressions and coupling debt.

Before any infrastructure (PostgreSQL, Redis, RabbitMQ, MinIO) or user-facing features (auth, async jobs, webhooks) are added, the codebase needs a domain model and a package topology that can host them. Retrofitting DDD after three or four infrastructure changes are already wired together is significantly harder than establishing the foundation first.

## What Changes

This is a **planning-only change** — no application code, no infrastructure, no tests are modified. It establishes:

- **Bounded contexts** and their responsibilities, documented in OpenSpec specs: `Identity`, `Video Processing`, and `Notification`.
- **`VideoJob` aggregate root** for the Video Processing context, with its value objects, valid state machine transitions, and invariants.
- **Package topology** for the monorepo evolution: `cmd/api`, `cmd/worker`, `internal/<context>/domain`, `internal/<context>/application`, `internal/<context>/infrastructure`.
- **Use cases** for each bounded context (what handlers will delegate to, not how they're implemented yet).
- **Domain events and integration contracts** between contexts.
- **Dependency rules** (domain must not import infrastructure; application must not import HTTP).
- **Architecture Decision Records** for the seven key decisions the roadmap will encounter: async transport (RabbitMQ vs Redis Streams), object storage (MinIO vs local volume), identity (JWT vs sessions), status delivery (polling vs webhook), repo topology (monorepo vs multi-repo), Redis responsibilities (idempotency, rate limiting, status cache, distributed locks), and PostgreSQL as source of truth.
- **Frontend / Presentation Layer** — the existing inline HTML/CSS/JavaScript (`getHTMLForm()`) is documented as a delivery/presentation layer, not a bounded context. Non-regression criteria, backend contract-compatibility rules, and an incremental extraction path to `web/index.html`, `web/styles.css`, and `web/app.js` are established.
- **Permanent project documentation** — seven Markdown files (`README.md`, `docs/architecture.md`, `docs/domain-model.md`, `docs/flows.md`, `docs/development.md`, `docs/operations.md`, `docs/roadmap.md`) specified by this change and created in a separate documentation PR. Unlike OpenSpec artifacts (which govern how changes are proposed, designed, and tracked), the permanent docs are the stable reference for contributors, evaluators, and operators encountering the project for the first time. Documentation must distinguish the current implementation state from the target DDD architecture and must not declare unimplemented infrastructure (PostgreSQL, Redis, RabbitMQ, MinIO, authentication, async processing) as existing.

## Capabilities

### New Capabilities

- `ddd-architecture`: Tactical DDD model, bounded contexts, aggregate root (`VideoJob`), domain events, dependency rules, frontend presentation layer, and evolution roadmap for FIAP X.
- `permanent-project-documentation`: Stable Markdown reference files covering project overview, architecture (current and target), domain model, request and event flows, development setup, infrastructure responsibilities, and the eight-phase evolution roadmap.

### Modified Capabilities

(none — no existing capabilities change behavior)

## Impact

- New OpenSpec change artifacts only (spec PR): `proposal.md`, `design.md`, `tasks.md`, `specs/ddd-architecture/spec.md`. No application code, CI, or documentation files are created or modified in this PR.
- A **documentation PR** follows this spec PR: it creates `README.md`, `docs/architecture.md`, `docs/domain-model.md`, `docs/flows.md`, `docs/development.md`, `docs/operations.md`, and `docs/roadmap.md`. That PR contains only documentation — no OpenSpec artifacts, no application code.
- No changes to `main.go`, `main_test.go`, `go.mod`, `Dockerfile`, or CI workflows in either PR.
- This change is the prerequisite for every subsequent infrastructure and feature change in the roadmap. It must be reviewed and merged before implementation begins.
