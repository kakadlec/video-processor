## 1. Implementation

- [x] 1.1 Add a first-init SQL script (e.g. `docker/postgres-init/create-test-db.sql`) containing `CREATE DATABASE identity_test;`, mounted into the `postgres` service at `/docker-entrypoint-initdb.d/`.
- [x] 1.2 Add an `app` service to `docker-compose.yml`: `build: .`, port published as `127.0.0.1:8080:8080` (loopback-only — see design.md), `IDENTITY_POSTGRES_DSN` pointed at the `postgres` service's `identity` database, `IDENTITY_POSTGRES_TEST_DSN` pointed at the same server's `identity_test` database, `IDENTITY_JWT_SIGNING_KEY` set, `depends_on: postgres: condition: service_healthy`.
- [x] 1.3 Bind-mount `uploads/` and `outputs/` into the `app` container.

## 2. Verification

- [x] 2.1 Run `docker compose up --build` and confirm `app` waits for `postgres`'s healthcheck before starting.
- [x] 2.2 Confirm `GET /` returns 200 via `http://127.0.0.1:8080/` (and confirm it is not reachable via the host's non-loopback interfaces).
- [x] 2.3 Confirm `POST /api/auth/register` and `POST /api/auth/login` succeed against the compose-provisioned PostgreSQL, with no manual env var exports.
- [x] 2.4 With `app` still running and at least one registered user, run `docker compose run --build --rm app go test ./... -v` and confirm the PostgreSQL-backed identity adapter tests run (not skipped) — then confirm the previously-registered user can still log in via the running `app` service afterward (proving the test run didn't touch the runtime database).
- [x] 2.5 Tear down (`docker compose down -v`) and confirm no stray test data or containers remain.

## 3. Documentation (separate PR from Implementation)

- [x] 3.1 `README.md`: replace the plain `docker build`/`docker run` Docker quickstart with the compose-based workflow.
- [x] 3.2 `docs/development.md`: replace the "Docker fallback" test command and the separate PostgreSQL-test-DSN walkthrough with the single `docker compose run` test command; replace the "Docker Workflow" section's plain `docker build`/`docker run` example with compose.
- [x] 3.3 `CLAUDE.md`: replace the "Commands" section's `docker build`/`docker run` lines with the compose command; fix the adjacent `go run main.go` → `go run .` in the same block.
- [x] 3.4 Do **not** modify `docs/operations.md` — its Docker section documents deployment, not local dev, and is out of scope (see design.md Non-Goals).
- [x] 3.5 Grep `README.md`, `docs/development.md`, and `CLAUDE.md` for `docker run` / `docker build` to confirm no live-workflow reference remains outside this task list's own description text.
