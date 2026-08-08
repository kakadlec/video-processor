## Context

`docker-compose.yml` currently defines only `postgres`, added to let contributors run PostgreSQL-backed identity tests locally (`Local PostgreSQL Development Service` requirement in `openspec/specs/development-workflow/spec.md`). Running the *application* itself with identity enabled is a separate, undocumented exercise: either `go run .` with manually exported env vars, or `docker build`/`docker run` with the same env vars pointed at a database the developer has to reach some other way (host networking tricks, hardcoded IPs). This was hit directly while manually verifying the application locally this session.

The repository also documents a second, independent Docker entry point today: a plain `docker build` + `docker run` workflow (identity disabled), plus a separate `docker build … && docker run --rm … go test ./... -v` fallback for running tests without a local Go/ffmpeg install. Per explicit direction, this change retires both of those in favor of `docker-compose.yml` as the single documented way to use Docker in this repository — not an additional option alongside them.

## Goals / Non-Goals

**Goals:**
- One documented command (`docker compose up --build`) that starts the application and PostgreSQL together, with identity already configured, for local manual testing and demos.
- `docker-compose.yml` becomes the single source of truth for Docker-based workflows in this repository: building the image, running the application, and running the test suite (including PostgreSQL-backed tests) all go through it. No parallel "quick start" `docker build`/`docker run` path.
- Avoid the app container racing PostgreSQL's startup and hitting the existing fail-fast unreachable-database error.

**Non-Goals:**
- Hardening the `Dockerfile` itself (multi-stage build, non-root user) — tracked as its own backlog item, `harden-dockerfile`, not part of this change's scope. `docker-compose.yml`'s `build: .` still points at the current (unhardened) `Dockerfile`; `harden-dockerfile` will land after this change per the backlog's stated dependency.
- Making identity mandatory — that's `enforce-mandatory-identity-config`, a separate backlog item. This change doesn't touch `setupIdentity`'s behavior; it just means the one documented Docker path always supplies identity configuration.
- Production or CI orchestration — this is a local-dev-only convenience; CI already provisions its own `postgres` service directly in `ci.yml`, not via this compose file, and doesn't use Docker to run the application at all.
- A `.env` file or secrets-management mechanism — the signing key follows the same "fixed, non-secret, local-only default" precedent already established for the `postgres` service's credentials.

## Decisions

**`app` builds from the existing `Dockerfile` via `build: .`, not a pinned image.** Alternative considered: reference a pre-built image tag. Rejected — the repo has no image registry/publishing step, and `build: .` means `docker compose up --build` always reflects the current source tree.

**The `app` service always configures identity — there is no lighter-weight, identity-disabled Docker path anymore.** Previously considered keeping the plain `docker run` (identity-disabled) workflow as a documented alternative; explicitly rejected per direction to make `docker-compose.yml` the single source of truth, with no alternatives. A developer who wants to run without identity/Postgres at all still can via `go run .` directly (no Docker), which is unaffected by this change.

**`IDENTITY_JWT_SIGNING_KEY` is a fixed, hardcoded, non-secret string in `docker-compose.yml`.** Matches the existing precedent for the `postgres` service's `identity`/`identity` credentials, documented there as safe because this file is never used outside a developer's machine or CI. No new precedent introduced.

**`app` depends on `postgres` via `condition: service_healthy`, not a plain `depends_on`.** `postgres` already defines a `pg_isready` healthcheck. Without the condition, `depends_on` only waits for the container to *start*, not for PostgreSQL to accept connections — and `setupIdentity` connects and fails fast at startup on an unreachable database (existing, tested behavior). Gating on the healthcheck avoids a race that would otherwise make `docker compose up` flaky.

**`uploads/` and `outputs/` are bind-mounted from the host.** Alternative considered: named volumes (Docker-managed, not host-visible). Rejected — the point of local dev tooling is to inspect results directly (e.g., extracted ZIPs) without `docker cp`; both directories are already `.gitignore`d, so this introduces no risk of committing generated content.

**`app`'s port is published as `127.0.0.1:8080:8080`, loopback-only.** A Compose `ports` entry without a host IP binds to all interfaces (`0.0.0.0`), reachable from any peer on the same network. Combined with `IDENTITY_JWT_SIGNING_KEY` being a fixed, repository-visible value (see above), an unqualified publish would let any network peer mint accepted bearer tokens for arbitrary user IDs. There's no legitimate reason a local dev convenience service needs LAN reachability, so loopback-only costs nothing functionally and closes this off. Alternative considered: leave the signing key out of the committed file entirely and require it as a manually-set env var — rejected as reintroducing exactly the manual-configuration friction this change exists to remove; loopback binding addresses the actual exposure at lower cost.

**`IDENTITY_POSTGRES_TEST_DSN` is also set on the `app` service, pointed at `postgres` over the compose network.** This lets a single `docker compose run --build --rm app go test ./... -v` exercise the full suite, including the PostgreSQL-backed identity adapter tests — replacing both the old no-Postgres Docker test fallback and the separately-documented "start postgres, export the test DSN, run go test locally" flow with one command.

## Risks / Trade-offs

- **[Risk]** A developer already running Postgres on host port 5432 (for another project) hits a port conflict when `docker compose up` tries to publish `postgres`'s port. → **Mitigation**: none needed in this change — the port publish already exists for the test-only use case (`Local PostgreSQL Development Service`) and is unrelated to adding `app`; documented as a known local-environment consideration, not something this change introduces or need solve.
- **[Risk]** Bind-mounting `uploads/`/`outputs/` into the container could shadow directories the `Dockerfile` creates at build time (`RUN mkdir -p uploads outputs temp`). → **Mitigation**: the application already calls `createDirs()` at startup regardless (idempotent `os.MkdirAll`), so an empty bind-mounted directory is handled the same as a fresh checkout.
- **[Risk]** A repository-visible, hardcoded JWT signing key is dangerous if this `docker-compose.yml` pattern is ever copied into a network-reachable deployment (not just used locally). → **Mitigation**: loopback-only publish (above) prevents *this* service specifically from being LAN/network reachable; the file's existing header comment already documents these credentials as local-only, non-secret by design.
- **[Risk]** Removing the documented plain `docker build`/`docker run` path is a regression for anyone relying on it (e.g., a hackathon evaluator following the current README verbatim, or a deployment script). → **Mitigation**: `docker build -t video-processor .` and `docker run` still work mechanically — the `Dockerfile` isn't deleted, only the *documented, supported* entry point changes. This is accepted as intentional per explicit direction: one documented path, not "does the old one still technically function."

## Migration Plan

Not purely additive — this change removes documented content, not just adds it:
- `README.md`'s "Docker (no Go/ffmpeg install required)" section is replaced by the compose command, not supplemented.
- `docs/development.md`'s "Docker fallback" test command and the separate PostgreSQL-test-DSN walkthrough are replaced by the single `docker compose run` test command; its "Docker Workflow" section's plain `docker build`/`docker run` example is replaced by compose.
- `docs/operations.md`'s plain `docker run` deployment example is replaced by the compose-based one.

Rollback is reverting the doc changes and removing the `app` service block — the `Dockerfile` itself is untouched throughout, so rollback has no build-system impact.

## Open Questions

None.
