# Roadmap

FIAP X evolves from the current synchronous monolith (`main.go`) into a fully structured DDD system across exactly **8 phases**. Each phase is a distinct OpenSpec change, proposed and implemented separately.

> **Canonical source:** The authoritative roadmap — including full scope descriptions, ADRs, dependency rules, and the complete domain model — lives in [`openspec/specs/ddd-architecture/spec.md`](../openspec/specs/ddd-architecture/spec.md). Phase 2's own scope is likewise authoritative in [`openspec/specs/identity-authentication/spec.md`](../openspec/specs/identity-authentication/spec.md).
>
> This file is a summary for human readers. If this summary conflicts with the canonical source, the canonical source takes precedence.

## Phase Summary

| Phase | Change name | Scope | Status |
|---|---|---|---|
| **1** | `establish-ddd-architecture-foundation` | OpenSpec artifacts only: bounded contexts, `VideoJob` aggregate, package topology, ADRs, dependency rules, frontend/presentation layer contract, permanent project documentation. No application code changes. | Done |
| **2** | `implement-identity-authentication-from-scratch` | `internal/identity/domain` and `application`; JWT bearer middleware; `RegisterUser` and `AuthenticateUser` use cases; PostgreSQL adapter for Identity; explicit per-artifact ownership enforcement. Wired into the existing `main.go` composition root rather than a new `cmd/api` — that migration stays in Phase 3. | Done |
| **3** | `implement-videojob-persistence` | `internal/video/domain` and `application`; PostgreSQL schema for `video_jobs` and `outbox`; `CreateVideoJob` and `GetJobStatus` use cases; synchronous `ffmpeg` call migrated from `main.go` into application layer; frontend extracted from `getHTMLForm()` into `web/index.html`, `web/styles.css`, `web/app.js`. | Planned |
| **4** | `implement-redis-capabilities` | Redis adapter; idempotency keys on `POST /upload`; rate-limiting middleware; status cache for `GetJobStatus`; distributed lock for worker job pickup. PostgreSQL remains source of truth. | Planned |
| **5** | `implement-minio-object-storage` | MinIO storage adapter behind the `StoragePort` interface; migrate upload and result storage from local filesystem to MinIO; presigned download URLs. | Planned |
| **6** | `implement-rabbitmq-and-worker` | RabbitMQ infrastructure; `EnqueueVideoJob` publishes to queue; transactional outbox relay; `cmd/worker` picks up messages and runs `ffmpeg`; `POST /upload` becomes non-blocking (returns job ID immediately); `web/app.js` updated for async polling flow. | Planned |
| **7** | `implement-notifications` | `internal/notification/domain` and `application`; RabbitMQ event subscription; email and webhook delivery; `NotificationPreference` per user; retry logic and HMAC webhook signatures. | Planned |
| **8** | `implement-observability-and-delivery` | Structured logging; Prometheus metrics; `/health` and `/ready` endpoints; Dockerfile hardening (multi-stage, non-root); `docker-compose.yml` for full local dev stack (PostgreSQL, Redis, RabbitMQ, MinIO). | Planned |

## Current State (Phases 1–2 done)

Phase 1 established the architectural foundation — bounded contexts, aggregate design, ADRs, dependency rules, and this documentation — without touching any application code.

Phase 2 added the first executable bounded context: `internal/identity/{domain,application,infrastructure}`, PostgreSQL-backed user registration and login, JWT bearer authentication, and per-artifact ownership enforcement for uploads/downloads/status. It's optional at deployment time — `main.go` still starts and serves video processing unauthenticated when `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` aren't set, preserving the pre-Phase-2 workflow. The composition root is still `main.go`, not a separate `cmd/api`; that split is deferred to Phase 3. See [docs/architecture.md](architecture.md) for the current request flow and [docs/operations.md](operations.md) for the identity configuration.

Phases 3–8 are planned and sequenced by dependency: each phase safely assumes the prior phase's code is merged. The phases do not need to be fully completed to deliver value — even partial implementation (e.g., through Phase 3 or 4) produces a meaningfully more structured and capable system.
