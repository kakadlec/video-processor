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

## Capabilities

### New Capabilities

- `ddd-architecture`: Tactical DDD model, bounded contexts, aggregate root (`VideoJob`), domain events, dependency rules, and evolution roadmap for FIAP X.

### Modified Capabilities

(none — no existing capabilities change behavior)

## Impact

- New OpenSpec change artifacts only: `proposal.md`, `design.md`, `tasks.md`, `specs/ddd-architecture/spec.md`.
- No changes to `main.go`, `main_test.go`, `go.mod`, `Dockerfile`, CI workflows, or any other file outside `openspec/changes/establish-ddd-architecture-foundation/`.
- This change is the prerequisite for every subsequent infrastructure and feature change in the roadmap. It must be reviewed and merged before implementation begins.
