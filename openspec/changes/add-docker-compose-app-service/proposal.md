## Why

`docker-compose.yml` only starts PostgreSQL today. Running the application itself with identity enabled requires either `go run .` with hand-exported `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY`, or building and running the Docker image manually with those same env vars pointed at a reachable Postgres — there is no single documented command that gives a contributor the full stack (app + Postgres, identity wired) to exercise the application locally.

## What Changes

- Add an `app` service to `docker-compose.yml`, alongside the existing `postgres` service: builds the repository's existing `Dockerfile`, sets `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` to reach `postgres` over the compose network, and depends on `postgres`'s existing healthcheck so it doesn't start before the database is reachable.
- Bind-mount `uploads/` and `outputs/` into the `app` container so processed results are inspectable from the host without `docker cp`.
- **Retire the plain `docker build` + `docker run` workflow from documentation entirely, for local development.** `docker-compose.yml` becomes the single, sole documented way to build, run, and test the application locally via Docker — no parallel "quick start" path. This also replaces `docs/development.md`'s separate `docker build … && docker run --rm … go test ./... -v` fallback with a compose-based equivalent.
- Create a separate `identity_test` database on the `postgres` service (via a first-init SQL script) and fold `IDENTITY_POSTGRES_TEST_DSN` into the `app` service's environment, pointed at that isolated database — not the runtime `identity` database — so the test command can't `TRUNCATE` data the running application actually uses.
- Document the resulting compose-only local workflow in `README.md`, `docs/development.md`, and `CLAUDE.md`. `docs/operations.md`'s deployment-focused Docker section is explicitly out of scope — it documents container deployment, not local dev, and presenting a loopback-bound, repository-known-signing-key stack there would read as deployment guidance.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `development-workflow`: adds a requirement that the repository provide a single documented command to run the full application stack locally (app + PostgreSQL, identity enabled) and to run the test suite (including PostgreSQL-backed tests) via Docker, and modifies the existing "Local PostgreSQL Development Service" requirement so the documented entry point is exclusively `docker compose`, not a standalone `docker build`/`docker run`.

## Impact

- `docker-compose.yml`: new `app` service definition; new `postgres` init-script mount to create `identity_test`.
- A new small SQL init script file (e.g. `docker/postgres-init/create-test-db.sql`).
- `README.md`: replaces the plain `docker build`/`docker run` Docker quickstart with the compose-only workflow, rather than adding compose alongside it.
- `docs/development.md`: replaces the "Docker fallback" test command and the separate PostgreSQL-test-DSN instructions with a single compose-based command; replaces the "Docker Workflow" section's plain `docker build`/`docker run` with compose.
- `CLAUDE.md`: replaces the "Commands" section's `docker build`/`docker run` lines with the compose command; fixes the adjacent already-stale `go run main.go` → `go run .` in the same block.
- `docs/operations.md` is **not** modified by this change.
- No application code (`.go` files), no CI workflow changes, no canonical spec changes beyond the `development-workflow` delta above.
- The existing `Dockerfile` is unchanged — this only adds orchestration on top of it, it does not touch or "fix" the intentionally simple single-stage build (that's `harden-dockerfile`, a separate backlog item).
