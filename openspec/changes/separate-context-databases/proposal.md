## Why

`notification-persistence` already states the code contract correctly: each context reads its own DSN, must not fall back to another's, and "which physical server the value points at is a deployment decision — pointing all three at one server is permitted and is what local development does — but the code SHALL NOT be the thing that assumes it." Three pools, three variables, three `Migrate` calls, and zero foreign keys anywhere in the three `schema.sql` files. The isolation is built.

What is missing is that no environment **exhibits** it. `docker-compose.yml` points all six DSNs (three runtime, three test) at one database named `identity`, and CI points all three `*_TEST_DSN` at one too. The consequence is not cosmetic: **a cross-context query would work.** A `JOIN` from `video_jobs` to `identity_users`, or a Notification read of a Video table, compiles, runs, and passes its test today — the violation only surfaces in a deployment that separated the databases, which is to say in the one place nobody is watching. This repository pins its architectural rules with tests rather than convention (`internal/notification/dependency_rules_test.go` AST-walks every package to forbid an import); the database boundary is the one rule stated normatively and enforced nowhere.

The second reason is presentational and honest to state: a project-validation review reading `docker-compose.yml` sees three services against one database and reasonably reads "modular monolith", because the separation that exists is invisible at the only place an evaluator looks.

## What Changes

- `docker/postgres-init/` creates a database per context — `video` and `notification` alongside the existing `identity` — and a `_test` counterpart for each, replacing today's single `identity_test`.
- `docker-compose.yml` points each context's DSN at its own database: `app` and `app-test`'s six variables, `worker`'s `VIDEO_POSTGRES_DSN`, and `notifier`'s `NOTIFICATION_POSTGRES_DSN`. **No variable is added, removed, or renamed** — only values change, because the variables were always separate and only their values converged.
- `.github/workflows/ci.yml` provisions the same three test databases and points `IDENTITY_POSTGRES_TEST_DSN`, `VIDEO_POSTGRES_TEST_DSN`, and `NOTIFICATION_POSTGRES_TEST_DSN` at their own, so a cross-context query fails the suite in CI rather than passing.
- One PostgreSQL **server**, three databases — deliberately, per this change's stated scope. Three servers would demonstrate nothing the boundary does not already have and would cost three containers on a developer machine.
- **A documented recovery path for existing local volumes.** `docker-entrypoint-initdb.d` scripts run only when the data directory is empty, so a developer with an existing `postgres_data` volume gets no new databases and their next `docker compose up` fails with "database video does not exist". The change ships the two `CREATE DATABASE` statements to run against a live container, because the alternative — `down -v` — destroys `minio_data` and `rabbitmq_data` too, which `docker-compose.yml`'s own comments already warn against.
- No Go code, no test code, no new dependency.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `ddd-architecture`: gains a requirement that context storage isolation SHALL be exhibited by the environments the project actually runs — local development and CI — and not only permitted by the code. This sits beside the existing "Package Dependency Rules" requirement for the same reason it exists: an architectural boundary that nothing executes against is a boundary that has already drifted by the time anyone notices. It is `ADDED` rather than a modification of an existing requirement, because no current requirement in that spec makes any claim about where data lives.
- `development-workflow`: modifies "Local PostgreSQL Development Service" and the CI-facing "Automated Test Gate", both of which currently name a single test database and a single `IDENTITY_POSTGRES_TEST_DSN` as the provisioned surface. Both must be `MODIFIED` deltas: their present wording describes the one-database arrangement this change replaces, so an added requirement beside them would leave the spec asserting both shapes.

## Impact

- **Changed configuration**: `docker/postgres-init/` (the init SQL), `docker-compose.yml` (eight DSN values across four services), `.github/workflows/ci.yml` (test database provisioning and three DSN values).
- **No code, no tests**: the diff carries no Go module input, so `development-workflow`'s "A non-build change is exempt from the local test-run requirement" applies. Verification is behavioural: the full suite green against the three separate databases (which is itself the proof that no cross-context query exists today), plus the stack starting clean.
- **Breaking for an existing local volume, not for the deployed contract.** The environment variable names, their required-ness, and every startup behaviour are unchanged, so no deployment's configuration contract breaks; a deployment already pointing the three variables at one database keeps working untouched, because the code never assumed otherwise. What breaks is a developer's existing `postgres_data` volume, and the recovery is two `CREATE DATABASE` statements documented with the change.
- **Data left behind**: an existing local `identity` database keeps its `video_jobs`, `video_job_outbox`, and `notification_*` tables. They become inert — nothing reads them once the DSNs move. Local dev data, not a migration concern.
- **Docs** (finalization PR only): `docs/operations.md`'s environment-variable table and its deployment examples, which currently show the three DSNs pointing at one database; `docs/development.md`'s exported-variable block for running the binaries directly; `README.md`'s "Database Schema and Infrastructure Resources" section, whose resource table gains the database each schema lands in; `CLAUDE.md`'s statement that the three pools share "whatever single server `docker-compose.yml` happens to point all three at", which stays true about the server and becomes false about the database; `docs/roadmap.md`'s Change Backlog row.
- **Dependencies**: none new. This change is independent of `split-api-by-bounded-context` and does not block or require it.
