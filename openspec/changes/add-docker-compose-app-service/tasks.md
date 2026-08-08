## 1. Implementation

- [ ] 1.1 Add an `app` service to `docker-compose.yml`: `build: .`, port published as `127.0.0.1:8080:8080` (loopback-only, not the unqualified `8080:8080` used by the plain `docker run` path — see design.md), `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` pointed at the `postgres` service over the compose network, `depends_on: postgres: condition: service_healthy`.
- [ ] 1.2 Bind-mount `uploads/` and `outputs/` into the `app` container.

## 2. Verification

- [ ] 2.1 Run `docker compose up --build` and confirm `app` waits for `postgres`'s healthcheck before starting.
- [ ] 2.2 Confirm `GET /` returns 200 via `http://127.0.0.1:8080/` (and confirm it is not reachable via the host's non-loopback interfaces).
- [ ] 2.3 Confirm `POST /api/auth/register` and `POST /api/auth/login` succeed against the compose-provisioned PostgreSQL, with no manual env var exports.
- [ ] 2.4 Confirm the plain `docker build` + `docker run` (identity-disabled) path is unaffected.
- [ ] 2.5 Tear down (`docker compose down -v`) and confirm no stray test data or containers remain.

## 3. Documentation (separate PR from Implementation)

- [ ] 3.1 Document `docker compose up --build` in `README.md`, alongside the existing plain Docker quickstart.
- [ ] 3.2 Document it in `docs/development.md`'s Docker Workflow section.
- [ ] 3.3 Document it in `docs/operations.md`'s Docker section.
