## Context

`docker-compose.yml` currently defines only `postgres`, added to let contributors run PostgreSQL-backed identity tests locally (`Local PostgreSQL Development Service` requirement in `openspec/specs/development-workflow/spec.md`). Running the *application* itself with identity enabled is a separate, undocumented exercise: either `go run .` with manually exported env vars, or `docker build`/`docker run` with the same env vars pointed at a database the developer has to reach some other way (host networking tricks, hardcoded IPs). This was hit directly while manually verifying the application locally this session.

## Goals / Non-Goals

**Goals:**
- One documented command (`docker compose up --build`) that starts the application and PostgreSQL together, with identity already configured, for local manual testing and demos.
- This change itself doesn't touch or remove the existing plain `docker build` + `docker run` (identity-disabled) path — it's additive. Note that path's continued existence is not a guarantee of this change specifically: `enforce-mandatory-identity-config`, a separate backlog item, plans to remove the "runs unauthenticated when unconfigured" behavior entirely, which would make the identity-disabled `docker run` path stop working regardless of anything here.
- Avoid the app container racing PostgreSQL's startup and hitting the existing fail-fast unreachable-database error.

**Non-Goals:**
- Hardening the `Dockerfile` itself (multi-stage build, non-root user) — tracked as its own backlog item, `harden-dockerfile`, not part of this change's scope.
- Making identity mandatory — that's `enforce-mandatory-identity-config`, a separate backlog item; this change just makes the already-optional identity easy to turn on locally.
- Production or CI orchestration — this is a local-dev-only convenience; CI already provisions its own `postgres` service directly in `ci.yml`, not via this compose file.
- A `.env` file or secrets-management mechanism — the signing key follows the same "fixed, non-secret, local-only default" precedent already established for the `postgres` service's credentials.

## Decisions

**`app` builds from the existing `Dockerfile` via `build: .`, not a pinned image.** Alternative considered: reference a pre-built image tag. Rejected — the repo has no image registry/publishing step, and `build: .` means `docker compose up --build` always reflects the current source tree, matching how the plain `docker build`/`docker run` path already works.

**Identity is configured by default in the `app` service, not left to the developer.** Alternative considered: a Compose "profile" or separate `app-no-auth` service so users opt into identity. Rejected as unnecessary complexity — the plain `docker run video-processor` path already exists and stays documented as the lighter-weight, identity-disabled option; `docker compose up --build`'s whole purpose is to be the "give me everything wired" path, so it should default to the fuller behavior.

**`IDENTITY_JWT_SIGNING_KEY` is a fixed, hardcoded, non-secret string in `docker-compose.yml`.** Matches the existing precedent for the `postgres` service's `identity`/`identity` credentials, documented there as safe because this file is never used outside a developer's machine or CI. No new precedent introduced.

**`app` depends on `postgres` via `condition: service_healthy`, not a plain `depends_on`.** `postgres` already defines a `pg_isready` healthcheck. Without the condition, `depends_on` only waits for the container to *start*, not for PostgreSQL to accept connections — and `setupIdentity` connects and fails fast at startup on an unreachable database (existing, tested behavior). Gating on the healthcheck avoids a race that would otherwise make `docker compose up` flaky.

**`uploads/` and `outputs/` are bind-mounted from the host.** Alternative considered: named volumes (Docker-managed, not host-visible). Rejected — the point of local dev tooling is to inspect results directly (e.g., extracted ZIPs) without `docker cp`; both directories are already `.gitignore`d, so this introduces no risk of committing generated content.

**`app`'s port is published as `127.0.0.1:8080:8080`, loopback-only — not the unqualified `8080:8080` used by the plain `docker run` path.** A Compose `ports` entry without a host IP binds to all interfaces (`0.0.0.0`), reachable from any peer on the same network. Combined with `IDENTITY_JWT_SIGNING_KEY` being a fixed, repository-visible value (see above), an unqualified publish would let any network peer mint accepted bearer tokens for arbitrary user IDs — a materially different risk than the plain `docker run` path, which has no signing key to forge at all. There's no legitimate reason a local dev convenience service needs LAN reachability, so loopback-only costs nothing functionally and closes this off. Alternative considered: leave the signing key out of the committed file entirely and require it as a manually-set env var — rejected as reintroducing exactly the manual-configuration friction this change exists to remove; loopback binding addresses the actual exposure at lower cost.

## Risks / Trade-offs

- **[Risk]** A developer already running Postgres on host port 5432 (for another project) hits a port conflict when `docker compose up` tries to publish `postgres`'s port. → **Mitigation**: none needed in this change — the port publish already exists for the test-only use case (`Local PostgreSQL Development Service`) and is unrelated to adding `app`; documented as a known local-environment consideration, not something this change introduces or need solve.
- **[Risk]** Bind-mounting `uploads/`/`outputs/` into the container could shadow directories the `Dockerfile` creates at build time (`RUN mkdir -p uploads outputs temp`). → **Mitigation**: the application already calls `createDirs()` at startup regardless (idempotent `os.MkdirAll`), so an empty bind-mounted directory is handled the same as a fresh checkout.
- **[Risk]** A repository-visible, hardcoded JWT signing key is dangerous if this `docker-compose.yml` pattern is ever copied into a network-reachable deployment (not just used locally). → **Mitigation**: loopback-only publish (above) prevents *this* service specifically from being LAN/network reachable; the file's existing header comment already documents these credentials as local-only, non-secret by design, and this change doesn't change that framing — it just makes the port-binding enforce it instead of only asserting it in a comment.

## Migration Plan

Purely additive — no existing service definition changes, no data migration. Rollback is deleting the `app` service block.

## Open Questions

None.
