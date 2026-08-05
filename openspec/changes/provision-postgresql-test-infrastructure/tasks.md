## 1. Local development/test service

- [ ] 1.1 Add `docker-compose.yml` at the repo root with a single `postgres` service (`postgres:16-alpine`), a named volume, and a health check.
- [ ] 1.2 Document the local workflow in `docs/development.md`: starting the service, the `IDENTITY_POSTGRES_TEST_DSN` value to export, and running `go test ./...` against it.

## 2. CI provisioning

- [ ] 2.1 Add a PostgreSQL `services:` entry (matching image/version) to the `test` job in `.github/workflows/ci.yml`, with a health check.
- [ ] 2.2 Set `IDENTITY_POSTGRES_TEST_DSN` in the `test` job's environment so it points at the service container.

## 3. Verification

- [ ] 3.1 Confirm the identity `postgres` adapter tests execute (not skip) in CI: check the CI log for `TestRepository_*` actually running, not `--- SKIP`.
- [ ] 3.2 Confirm the same tests run locally via the documented `docker-compose` workflow.
- [ ] 3.3 Run `go test ./...`, `go vet ./...`, `gosec ./...`, and `govulncheck ./...`; resolve any findings before this change's implementation PR is opened.
- [ ] 3.4 Confirm the rest of the suite (video processing, other identity packages) is unaffected — `go test ./...` passes end-to-end both locally and in CI.
