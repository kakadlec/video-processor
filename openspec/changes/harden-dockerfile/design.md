## Context

The current `Dockerfile` is a single stage (`golang:1.26-alpine`): `COPY . .`, `RUN go mod tidy`, `CMD ["go", "run", "."]`. It runs as root, resolves dependencies with `go mod tidy` (mutates `go.mod`/`go.sum` against whatever the module proxy serves at build time, rather than verifying the committed `go.sum`), and ships the full Go toolchain plus source tree in the final image since there's no separate runtime stage. It's documented (its own header comment, `CLAUDE.md`, `docs/operations.md`) as an intentional anti-pattern for study.

Two things now build from it: `docker-compose.yml`'s `app` service (`build: .`, from `add-docker-compose-app-service`) and the deployment commands in `docs/operations.md` (`docker build`/`docker run`). Both need to keep working unmodified — same port, same env vars, same `CMD` behavior — after hardening.

Verified locally: the existing root-run container already leaves root-owned files under the host's bind-mounted `./uploads`/`./outputs` (host user is UID 1000 here). Switching to a non-root container user makes this concrete: the runtime user's UID must be able to write to whatever host directory `docker-compose.yml` bind-mounts onto `/app/uploads` and `/app/outputs`.

## Goals / Non-Goals

**Goals:**
- Multi-stage build: a builder stage that compiles a static binary, a runtime stage with no Go toolchain.
- Deterministic dependency resolution: `go mod download` against the committed `go.sum` (fails closed on a mismatch), not `go mod tidy`.
- Runtime stage runs as a non-root user.
- Pin base image tags (both stages) instead of floating `:latest`/unpinned `alpine`, for reproducible builds and stable `govulncheck`/`gosec` baselines.
- Keep the container's external contract identical: listens on `:8080`, no required env vars, creates `uploads/`/`temp/`/`outputs/` on first run, `ffmpeg` on `PATH`.

**Non-Goals:**
- No change to `main.go`/`main_test.go` or any application behavior.
- No change to `docker-compose.yml`'s service definition beyond what's needed to keep it working against the hardened image (expected: no change at all).
- No change to `docs/operations.md`'s deployment *commands* (`docker build -t ...`, `docker run -p ...` stay the same) — only the prose describing the image's internals updates, and only insofar as it's now inaccurate.
- No distroless/scratch runtime base — `ffmpeg` needs a package manager to install, and this project's runtime dependency is exactly one package (`ffmpeg`) plus CA certs, which alpine covers well.

## Decisions

**Multi-stage layout: `golang:1.26-alpine` builder → pinned `alpine` runtime.**
Builder stage: `WORKDIR /app`, `COPY go.mod go.sum ./`, `RUN go mod download`, `COPY . .`, `RUN CGO_ENABLED=0 go build -o /out/app .`. Runtime stage: pinned `alpine:3.20`, `RUN apk add --no-cache ffmpeg`, create a non-root user, `COPY --from=builder /out/app /app/app`, `USER` the non-root user, `EXPOSE 8080`, `CMD ["/app/app"]`.
Alternative considered: distroless runtime base (`gcr.io/distroless/static`) — rejected because it has no package manager, so `ffmpeg` would need to be vendored as a static binary copied in manually, adding build complexity disproportionate to what this hackathon deliverable needs. Alpine keeps the runtime stage simple while still cutting out the entire Go toolchain and source tree.

**`go mod download` (not `go mod tidy`) in the builder.**
`go mod tidy` can rewrite `go.mod`/`go.sum` from whatever the module proxy currently serves, silently drifting from what CI/`go vet`/`gosec` already validated against the committed `go.sum`. `go mod download` resolves modules and verifies them against the existing `go.sum` checksums, failing the build if they don't match, which is the correct fail-closed behavior for a reproducible image.

**Non-root user, fixed UID 1000.**
`adduser -D -u 1000 appuser` (alpine's `adduser`) in the runtime stage, then `USER appuser`. UID 1000 is chosen because it's the conventional first non-root UID on single-user Linux/WSL dev machines (matches this repo's own dev environment) — the same convention official images like `node` use. This is a local-dev/study-project pragmatic choice, not a hard security requirement, and is called out as a risk below.

**`.dockerignore` excludes `uploads/`, `outputs/`, `temp/`, `.git/`.**
These are runtime-generated/VCS directories that don't belong in the build context; excluding them shrinks the context sent to the daemon and avoids ever baking stale local artifacts into an image layer.

## Risks / Trade-offs

**[Risk] Non-root UID 1000 may not have write access to `docker-compose.yml`'s bind-mounted `./uploads`/`./outputs` on a host where the invoking user isn't UID 1000** → Mitigation: document the constraint in `docs/development.md` (same pattern as the existing `postgres_data` volume callout) — if the app fails to write on first run, `chmod` (or `chown`) the two host directories, or delete and let Docker recreate them. This is a known, pre-existing rough edge of bind-mounting host directories into a container with a fixed UID; it does not affect `docs/operations.md`'s deployment path, which doesn't bind-mount these directories.

**[Risk] Pinned `alpine:3.20` (and the existing `golang:1.26-alpine`) will eventually go stale relative to upstream security patches** → Mitigation: this is the same trade-off already accepted for every other pinned base image/dependency in this repo (Go modules, `postgres:16-alpine`); `govulncheck`/`gosec`/Dependabot remain the mechanism for catching and bumping stale pins, not a reason to leave tags floating.

**[Risk] `CGO_ENABLED=0` static build could break if a future dependency requires cgo** → Mitigation: none of the current dependencies (`gin`, `pgx`, `golang-jwt/jwt`, `bcrypt`) require cgo; if one is added later that does, this is a one-line Dockerfile change to drop the flag, not a design constraint worth building in speculatively now.

## Migration Plan

1. Rewrite `Dockerfile` and add `.dockerignore` (implementation PR).
2. Verify: `docker compose up --build` still serves the app and identity routes correctly; `docker compose run --build --rm app go test ./... -v` still passes; image runs as non-root (`docker compose exec app id` shows UID 1000).
3. Update `docs/development.md`, `docs/operations.md`, `CLAUDE.md` to describe the hardened build instead of the anti-pattern (separate docs PR, per the repo's PR-separation rule).
4. Archive: promote the `container-image` spec, mark tasks complete.

No rollback complexity beyond reverting the Dockerfile — no data migration, no schema change, no persisted state affected.

## Open Questions

None — scope is narrow and self-contained.
