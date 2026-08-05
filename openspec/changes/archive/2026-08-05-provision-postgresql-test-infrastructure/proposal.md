## Why

The in-flight `implement-identity-authentication-from-scratch` change added a PostgreSQL persistence adapter (`internal/identity/infrastructure/postgres`) whose adapter tests only run against a real database when the `IDENTITY_POSTGRES_TEST_DSN` environment variable is explicitly set. That variable is not set in CI, nor in any contributor's default local environment, so those tests currently skip silently everywhere except a one-off manual run against a hand-started container. A shared repository cannot rely on that: the persistence adapter has effectively no repeatable, environment-independent verification. This must be fixed before that change's implementation PR is allowed to merge.

## What Changes

- Add a `docker-compose.yml` providing a local PostgreSQL service for development and for running the identity adapter's tests locally, documented in `docs/development.md`.
- Wire a PostgreSQL service container into the existing `Build & Test` CI job (`.github/workflows/ci.yml`) using GitHub Actions' native `services:` support, with a health check, and set `IDENTITY_POSTGRES_TEST_DSN` in that job so the postgres adapter's integration tests execute on every push and pull request instead of skipping.
- Scoped to what the Identity context needs right now (a single PostgreSQL service); later roadmap phases (Redis, RabbitMQ, MinIO) extend this incrementally rather than this change building the full Phase 8 stack up front.

## Capabilities

### New Capabilities

(none — this extends the existing development workflow, it does not introduce a new user-facing capability)

### Modified Capabilities

- `development-workflow`: the `Automated Test Gate` requirement is extended so CI provisions a PostgreSQL service and the identity persistence adapter's tests are no longer allowed to skip in CI; a new requirement covers the local PostgreSQL development/test service.

## Impact

- `.github/workflows/ci.yml`: `Build & Test` job gains a PostgreSQL `services:` entry, a health check, and an `IDENTITY_POSTGRES_TEST_DSN` environment variable.
- New `docker-compose.yml` at the repository root.
- `docs/development.md`: documents starting the local PostgreSQL service and running the identity adapter tests against it.
- No application source or test files change — `internal/identity/infrastructure/postgres` itself was already implemented in the held PR and is out of scope here.
