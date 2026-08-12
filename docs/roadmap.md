# Roadmap

FIAP X evolves from the current synchronous monolith (`main.go`) into a fully structured DDD system across exactly **8 phases**. Each phase decomposes into one or more focused OpenSpec changes — see [Change Backlog](#change-backlog) below for the concrete, right-sized units and their order. A phase is a grouping for human understanding, not a promise that it ships as a single change.

> **Canonical source:** The authoritative roadmap — including full scope descriptions, ADRs, dependency rules, and the complete domain model — lives in [`openspec/specs/ddd-architecture/spec.md`](../openspec/specs/ddd-architecture/spec.md). Phase 2's own scope is likewise authoritative in [`openspec/specs/identity-authentication/spec.md`](../openspec/specs/identity-authentication/spec.md).
>
> This file is a summary for human readers. If the Phase Summary or any architecture/target-state description below conflicts with the canonical source, the canonical source takes precedence.
>
> The **Change Backlog** section below is different: it's a planning/sequencing artifact with no counterpart in `openspec/specs/` — canonical specs describe target system behavior, not the order in which OpenSpec changes get proposed. It can't "conflict" with the canonical source in the same sense; it's authoritative for change sequencing in its own right. See [`openspec/specs/development-workflow/spec.md`](../openspec/specs/development-workflow/spec.md) for how it's maintained.

## Phase Summary

| Phase | Scope | Status |
|---|---|---|
| **1** | OpenSpec artifacts only: bounded contexts, `VideoJob` aggregate, package topology, ADRs, dependency rules, frontend/presentation layer contract, permanent project documentation. No application code changes. Shipped as a single change, `establish-ddd-architecture-foundation`. | Done |
| **2** | `internal/identity/domain` and `application`; JWT bearer middleware; `RegisterUser` and `AuthenticateUser` use cases; PostgreSQL adapter for Identity; explicit per-artifact ownership enforcement. Wired into the existing `main.go` composition root rather than a new `cmd/api` — that migration stays in Phase 3. Shipped as a single change, `implement-identity-authentication-from-scratch`; a design correction is now tracked separately (see Change Backlog). | Done, correction pending |
| **3** | `internal/video/domain` and `application`; PostgreSQL schema for `video_jobs` and `outbox`; `CreateVideoJob` and `GetJobStatus` use cases; synchronous `ffmpeg` call migrated from `main.go` into application layer; frontend extracted from `getHTMLForm()` into `web/index.html`, `web/styles.css`, `web/app.js`. Decomposed into multiple changes — see Change Backlog. | Planned |
| **4** | Redis adapter; idempotency keys on `POST /upload`; rate-limiting middleware; status cache for `GetJobStatus`; distributed lock for worker job pickup. PostgreSQL remains source of truth. Not yet decomposed. | Planned |
| **5** | MinIO storage adapter behind the `StoragePort` interface; migrate upload and result storage from local filesystem to MinIO; presigned download URLs. Not yet decomposed. | Planned |
| **6** | RabbitMQ infrastructure; `EnqueueVideoJob` publishes to queue; transactional outbox relay; `cmd/worker` picks up messages and runs `ffmpeg`; `POST /upload` becomes non-blocking (returns job ID immediately); `web/app.js` updated for async polling flow. Not yet decomposed. | Planned |
| **7** | `internal/notification/domain` and `application`; RabbitMQ event subscription; email and webhook delivery; `NotificationPreference` per user; retry logic and HMAC webhook signatures. Not yet decomposed. | Planned |
| **8** | Structured logging; Prometheus metrics; `/health` and `/ready` endpoints; `docker-compose.yml` for full local dev stack (PostgreSQL, Redis, RabbitMQ, MinIO). Not yet decomposed. Dockerfile hardening was originally planned here but has been pulled forward — see `harden-dockerfile` in the Change Backlog. | Planned |

## Change Backlog

This is the single source of truth for the product/architecture-scope OpenSpec work ahead — the 8-phase DDD roadmap above, decomposed to the finer-grained `openspec/changes/<name>/` level. For which changes get a row here, when this document is touched, and the rest of the OpenSpec/PR process, see [`openspec/specs/development-workflow/spec.md`](../openspec/specs/development-workflow/spec.md).

### Phase 2 corrections (already shipped — fixing a design mistake)

| Change | Scope | Depends on | Status |
|---|---|---|---|
| `enforce-mandatory-identity-config` | Remove the "runs unauthenticated when entirely unconfigured" path (`setupIdentity`'s current behavior). Startup must fail without both `IDENTITY_POSTGRES_DSN` and `IDENTITY_JWT_SIGNING_KEY`. Every protected route always requires a valid bearer token — no bypass. Corrects the "entirely unconfigured runs video-processing only" scenario in `openspec/specs/identity-authentication/spec.md`. | none | [archived](../openspec/changes/archive/2026-08-10-enforce-mandatory-identity-config/), promoted into [`openspec/specs/identity-authentication/spec.md`](../openspec/specs/identity-authentication/spec.md) |

### Frontend (orthogonal to everything else, low risk)

| Change | Scope | Depends on | Status |
|---|---|---|---|
| `extract-frontend-to-static-files` | Move `getHTMLForm()`'s HTML/CSS/JS out of `main.go` into `web/index.html`, `web/app.js`, `web/styles.css`, served via `go:embed`. No behavior change. | none | [archived](../openspec/changes/archive/2026-08-11-extract-frontend-to-static-files/), promoted into [`openspec/specs/ddd-architecture/spec.md`](../openspec/specs/ddd-architecture/spec.md) |

### Local development tooling (orthogonal to everything else, low risk)

| Change | Scope | Depends on | Status |
|---|---|---|---|
| `add-docker-compose-app-service` | Add an `app` service to `docker-compose.yml` alongside the existing `postgres` service: builds the existing Dockerfile, wires `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` over the compose network, gated on Postgres's healthcheck so it doesn't hit the fail-fast unreachable-DB error before Postgres is ready, bind-mounts `uploads/`/`outputs/` to the host, publishes the port loopback-only. `docker-compose.yml` becomes the **sole** documented Docker workflow — the plain `docker build`/`docker run` path and the separate Docker-based test fallback are retired from documentation, not kept alongside it (`docs/operations.md`'s deployment-focused Docker section is the one exception, explicitly out of scope). A separate `identity_test` database (via a postgres init script) keeps the test command from truncating the running app's data. Config/docs only, no application code. | none | [archived](../openspec/changes/archive/2026-08-08-add-docker-compose-app-service/), promoted into [`openspec/specs/development-workflow/spec.md`](../openspec/specs/development-workflow/spec.md) |

### Dockerfile hardening (pulled forward from Phase 8, orthogonal, low risk)

| Change | Scope | Depends on | Status |
|---|---|---|---|
| `harden-dockerfile` | Replaced the single-stage, root-user `Dockerfile` (documented anti-pattern; ran `go mod tidy` at build time against whatever the module proxy currently serves, rather than resolving deterministically from the committed `go.sum`, and shipped the full Go toolchain in the final image since there was no separate runtime stage) with a three-stage build: `builder` (read-only `go.sum`-verified deps, static binary), `test` (`builder` + `ffmpeg`, backs the new `docker-compose.yml` `app-test` service), and `runtime` (pinned `alpine:3.24`, `ffmpeg`, non-root UID 1000, no Go toolchain). Pulled forward from Phase 8 at the user's explicit request — no longer bundled with that phase's observability work. | `add-docker-compose-app-service` | [archived](../openspec/changes/archive/2026-08-09-harden-dockerfile/), promoted into [`openspec/specs/container-image/spec.md`](../openspec/specs/container-image/spec.md) and [`openspec/specs/development-workflow/spec.md`](../openspec/specs/development-workflow/spec.md) |

### Phase 3 — Video Processing persistence & `cmd/api` extraction

| Change | Scope | Depends on | Status |
|---|---|---|---|
| `add-videojob-domain-and-application` | `VideoJob` aggregate, state machine, value objects, repository/port interfaces, and exactly three use cases: `CreateVideoJob`, `GetJobStatus`, `ListUserJobs`. No infra, no HTTP. `EnqueueVideoJob`, `StartProcessing`, `CompleteJob`, and `FailJob` are explicitly out of scope here — they're worker/queue commands that belong to `implement-rabbitmq-and-worker` (Phase 6), which doesn't exist yet in this synchronous slice. | none | not-started |
| `add-videojob-infrastructure` | PostgreSQL adapter for `VideoJob`, jobs + outbox schema/migration. | `add-videojob-domain-and-application` | not-started |
| `extract-cmd-api-entrypoint` | Move the HTTP composition root from `main.go` into `cmd/api`, preserving current routes/behavior 1:1. | `extract-frontend-to-static-files` (no Go string literal to drag along) | not-started |
| `wire-videojob-http-endpoints` | New job-oriented endpoints backed by `VideoJob`, added alongside the legacy synchronous flow (not yet replacing it — this row only wires the new read/create paths). | `add-videojob-infrastructure`, `extract-cmd-api-entrypoint` | not-started |
| `migrate-ffmpeg-execution-to-videojob-application` | Cut `POST /upload`'s `ffmpeg` invocation over to run through the `VideoJob` application layer (still synchronous — no queue/worker until Phase 6) and retire the legacy in-`main.go`/`cmd/api` exec path. This is what actually fulfills Phase 3's "synchronous `ffmpeg` call migrated from `main.go` into application layer" promise; without this row the prior four only add a parallel path and never complete Phase 3. | `wire-videojob-http-endpoints` | not-started |

### Phases 4–8 — not yet decomposed

Listed at phase granularity only. Decomposing these into concrete changes now would mean designing Redis/MinIO/RabbitMQ/Notification/Observability details that haven't been discussed yet — that happens when each phase is actually next up, using the same discipline as above.

### OpenSpec process itself (workflow change, docs only)

| Change | Scope | Depends on | Status |
|---|---|---|---|
| `require-explore-before-propose` | Make `/opsx:explore` required before `/opsx:propose` for Change Backlog rows that are complex or ambiguous (cross-cutting impact, a new architectural pattern/dependency, security/performance/migration complexity, or open design questions) — a narrower distinction than, and layered on top of, the existing trivial-edit exemption that skips OpenSpec entirely. Simple, already-scoped rows may still go straight to `/opsx:propose`. Updates `CLAUDE.md`'s workflow description and this file's "How to use this" guidance, and adds a `development-workflow` spec requirement capturing the rule. Config/docs only, no application code. | none | [archived](../openspec/changes/archive/2026-08-09-require-explore-before-propose/), promoted into [`openspec/specs/development-workflow/spec.md`](../openspec/specs/development-workflow/spec.md) |

## Current State (Phases 1–2 done)

Phase 1 established the architectural foundation — bounded contexts, aggregate design, ADRs, dependency rules, and this documentation — without touching any application code.

Phase 2 added the first executable bounded context: `internal/identity/{domain,application,infrastructure}`, PostgreSQL-backed user registration and login, JWT bearer authentication, and per-artifact ownership enforcement for uploads/downloads/status. Identity configuration (`IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY`) is mandatory at startup — `main.go` fails to start rather than falling back to unauthenticated video processing (see `enforce-mandatory-identity-config` in the Change Backlog above, correcting the opt-in behavior Phase 2 originally shipped with). The composition root is still `main.go`, not a separate `cmd/api`; that split is deferred to Phase 3. See [docs/architecture.md](architecture.md) for the current request flow and [docs/operations.md](operations.md) for the identity configuration.

Phases 3–8 are planned and sequenced by dependency: each phase safely assumes the prior phase's code is merged. The phases do not need to be fully completed to deliver value — even partial implementation (e.g., through Phase 3 or 4) produces a meaningfully more structured and capable system.
