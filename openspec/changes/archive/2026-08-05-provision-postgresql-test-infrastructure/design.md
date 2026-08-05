## Context

`internal/identity/infrastructure/postgres` (introduced in the in-flight `implement-identity-authentication-from-scratch` change, held at PR #36) implements `domain.UserRepository` against PostgreSQL. Its tests (`repository_test.go`) call `t.Skip(...)` unless `IDENTITY_POSTGRES_TEST_DSN` is set. The CI workflow (`.github/workflows/ci.yml`) has no PostgreSQL service, and no contributor's default shell has that variable set, so today this adapter is verified by exactly one thing: an engineer manually starting a throwaway container and remembering to point the env var at it. That is not a repeatable safety net — it is a one-time manual proof that decays the moment nobody repeats it.

The existing `Automated Test Gate` requirement in `development-workflow` already establishes the precedent for this exact problem shape with `ffmpeg`: CI installs `ffmpeg` before running tests, and the suite itself fails loudly (non-zero exit) if `ffmpeg` is absent, rather than silently skipping. PostgreSQL needs the same treatment: CI must provision it, and the test suite must actually exercise it there — not skip.

## Goals / Non-Goals

**Goals:**
- CI's `Build & Test` job provisions a real PostgreSQL instance and the identity adapter's integration tests run against it on every push and pull request — no more silent skip in CI.
- Any contributor can start the same PostgreSQL service locally with one command and run the full test suite, including the identity adapter tests, without hand-rolling a container.
- Keep the local and CI databases as close to identical as practical (same image/version) so "works in CI" and "works locally" mean the same thing.

**Non-Goals:**
- Building the full Phase 8 `docker-compose.yml` stack (Redis, RabbitMQ, MinIO, worker, API) — this change adds only PostgreSQL, scoped to what Identity needs today. Later phases extend the same compose file incrementally.
- Changing `internal/identity/infrastructure/postgres` itself, or its tests' skip-guard mechanism (`IDENTITY_POSTGRES_TEST_DSN`) — that mechanism is correct and stays; what's missing is something actually setting the variable in every environment that should run these tests.
- Production database provisioning/deployment topology — out of scope for a hackathon deliverable; this is dev/test infrastructure only.
- Migrating the video-processing feature's persistence (it has none) or any other bounded context.

## Decisions

**CI: GitHub Actions native `services:` container, not testcontainers.**
The `Build & Test` job gains a `services: postgres:` block (image `postgres:16-alpine`, health check via `pg_isready`, mapped to `localhost:5432`) and an `IDENTITY_POSTGRES_TEST_DSN` environment variable pointing at it. Alternative considered: pulling in `testcontainers-go` to start Postgres from within the test process itself. Rejected for now — it requires Docker-in-Docker semantics inside the runner and a new test-only dependency, where the native `services:` feature already does exactly this with zero extra Go dependencies and is the same mechanism GitHub documents for this scenario. `ffmpeg` in this same job is installed directly on the runner (not containerized) because it's a CLI tool the app shells out to; Postgres is a stateful service the app talks to over the network, so the `services:` container model fits better than an `apt-get install`.

**Local dev/test: a single-service `docker-compose.yml` at the repo root.**
One service (`postgres`), matching the same `postgres:16-alpine` image and default schema as CI, with a named volume for persistence across restarts and a health check. Alternative considered: a bare `docker run` command documented in `docs/development.md` instead of compose. Rejected — compose gives a declarative, versioned definition that's trivially extended in later roadmap phases (Redis, RabbitMQ, MinIO) without contributors needing to memorize new `docker run` incantations each time; it also matches the project's own roadmap, which already earmarks a `docker-compose.yml` for the full local stack (`docs/roadmap.md`, Phase 8) — this change starts that file rather than introducing a throwaway alternative.

**Image and schema ownership.**
Both CI and local compose use the same `postgres:16-alpine` image and rely on the adapter's own idempotent `Migrate` (`CREATE TABLE IF NOT EXISTS`, already implemented) to establish schema — no separate migration tooling or SQL files added by this change. The database/user/password are fixed, non-secret development-only defaults (this is local/CI test infrastructure, not a credential worth protecting); `docs/development.md` documents the exact `IDENTITY_POSTGRES_TEST_DSN` value to export locally to match.

**Spec ownership: extend `development-workflow`, not a new capability.**
This is a workflow/tooling concern (how the test gate is provisioned), the same category as the existing `ffmpeg`-availability and CI-gate requirements already owned by `development-workflow`. A new capability would fragment closely related "what CI must provision before tests run" requirements across two spec files for no benefit.

## Risks / Trade-offs

- **CI job duration increases slightly** (Postgres container startup + health check) → Acceptable; the `services:` health check keeps this to a few seconds, well within the existing job's budget.
- **Local/CI drift if a contributor's Docker Desktop caches an old `postgres:16-alpine` layer** → Documented `docker compose pull` step in `docs/development.md`; low-impact since the schema is idempotent and minimal.
- **Fixed local dev credentials could be mistaken for something that needs protecting** → `docs/development.md` and inline compose comments state explicitly these are non-secret local-only defaults, never used against a real database.
- **This change touches CI configuration, a required status check for every future PR** → Validated by opening this change's own implementation PR and confirming `Build & Test` stays green with the new service wired in before merging.
