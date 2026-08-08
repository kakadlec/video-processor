## 1. Implementation

- [ ] 1.1 Add an `app` service to `docker-compose.yml`: `build: .`, port published as `127.0.0.1:8080:8080` (loopback-only — see design.md), `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY`/`IDENTITY_POSTGRES_TEST_DSN` pointed at the `postgres` service over the compose network, `depends_on: postgres: condition: service_healthy`.
- [ ] 1.2 Bind-mount `uploads/` and `outputs/` into the `app` container.

## 2. Verification

- [ ] 2.1 Run `docker compose up --build` and confirm `app` waits for `postgres`'s healthcheck before starting.
- [ ] 2.2 Confirm `GET /` returns 200 via `http://127.0.0.1:8080/` (and confirm it is not reachable via the host's non-loopback interfaces).
- [ ] 2.3 Confirm `POST /api/auth/register` and `POST /api/auth/login` succeed against the compose-provisioned PostgreSQL, with no manual env var exports.
- [ ] 2.4 Run `docker compose run --build --rm app go test ./... -v` and confirm the PostgreSQL-backed identity adapter tests run (not skipped) alongside the rest of the suite.
- [ ] 2.5 Tear down (`docker compose down -v`) and confirm no stray test data or containers remain.

## 3. Documentation (separate PR from Implementation)

- [ ] 3.1 `README.md`: replace the plain `docker build`/`docker run` Docker quickstart with the compose-based workflow.
- [ ] 3.2 `docs/development.md`: replace the "Docker fallback" test command and the separate PostgreSQL-test-DSN walkthrough with the single `docker compose run` test command; replace the "Docker Workflow" section's plain `docker build`/`docker run` example with compose.
- [ ] 3.3 `docs/operations.md`: replace the plain `docker run` deployment example with the compose-based one.
- [ ] 3.4 Confirm no remaining documentation references a bare `docker build`/`docker run` as a supported entry point (grep for `docker run` and `docker build` across `README.md`/`docs/`).
