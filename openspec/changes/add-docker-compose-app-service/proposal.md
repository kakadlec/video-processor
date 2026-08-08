## Why

`docker-compose.yml` only starts PostgreSQL today. Running the application itself with identity enabled requires either `go run .` with hand-exported `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY`, or building and running the Docker image manually with those same env vars pointed at a reachable Postgres — there is no single documented command that gives a contributor the full stack (app + Postgres, identity wired) to exercise the application locally.

## What Changes

- Add an `app` service to `docker-compose.yml`, alongside the existing `postgres` service: builds the repository's existing `Dockerfile`, sets `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` to reach `postgres` over the compose network, and depends on `postgres`'s existing healthcheck so it doesn't start before the database is reachable.
- Bind-mount `uploads/` and `outputs/` into the `app` container so processed results are inspectable from the host without `docker cp`.
- Document `docker compose up --build` as the full-stack local option in `README.md`, `docs/development.md`, and `docs/operations.md`, alongside the existing plain `docker build` + `docker run` (identity-disabled) path, which is unchanged and remains documented.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `development-workflow`: adds a requirement that the repository provide a single documented command to run the full application stack locally (app + PostgreSQL, identity enabled), extending the existing "Local PostgreSQL Development Service" requirement's `docker-compose.yml` rather than replacing it.

## Impact

- `docker-compose.yml`: new `app` service definition.
- `README.md`, `docs/development.md`, `docs/operations.md`: new "Docker Compose" sections documenting the full-stack option.
- No application code (`.go` files), no CI workflow changes, no canonical spec changes beyond the `development-workflow` delta above.
- The existing `Dockerfile` is unchanged — this only adds orchestration on top of it, it does not touch or "fix" the intentionally simple single-stage build.
