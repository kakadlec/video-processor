## Context

The current `Dockerfile` is a single stage (`golang:1.26-alpine`): `COPY . .`, `RUN go mod tidy`, `CMD ["go", "run", "."]`. It runs as root, resolves dependencies with `go mod tidy` (can mutate `go.mod`/`go.sum` against whatever the module proxy serves at build time, rather than verifying the committed `go.sum`), and ships the full Go toolchain plus source tree in the final image since there's no separate runtime stage. It's documented (its own header comment, `CLAUDE.md`, `docs/operations.md`) as an intentional anti-pattern for study.

Two things build from it today: `docker-compose.yml`'s `app` service (`build: .`, from `add-docker-compose-app-service`) and the deployment commands in `docs/operations.md` (`docker build`/`docker run`). A third, less obvious consumer: `docs/development.md` documents `docker compose run --build --rm app go test ./... -v` as the way to run the test suite via Docker — this runs `go test` *inside* whatever image the `app` service builds. Stripping the Go toolchain out of that image (the whole point of hardening) breaks this command outright; it needs a real replacement, not just a note.

Verified locally: the existing root-run container already leaves root-owned files under the host's bind-mounted `./uploads`/`./outputs` (host user is UID 1000 here). Switching to a non-root container user makes this concrete: the runtime user's UID must be able to write to whatever host directory `docker-compose.yml` bind-mounts onto `/app/uploads` and `/app/outputs`, and the runtime stage must actually create `uploads/`, `outputs/`, and `temp/` under a working directory that user owns — a multi-stage build does not inherit `WORKDIR` or directory state from the builder stage; each stage starts from its own base image.

## Goals / Non-Goals

**Goals:**
- Multi-stage build: a builder stage that compiles a static binary, a runtime stage with no Go toolchain.
- Deterministic dependency resolution: `go mod download` under `-mod=readonly` against the committed `go.sum`, failing the build on any mismatch or missing entry — not `go mod tidy`, and not bare `go mod download`, which can silently add missing checksums to `go.sum` instead of failing.
- Runtime stage runs as a non-root user, with `WORKDIR /app` and `uploads/`, `outputs/`, `temp/` pre-created and owned by that user before `USER` switches — preserving `main.go`'s relative-path first-run contract.
- A working way to run `go test ./...` (which needs both the Go toolchain and `ffmpeg`, per `main_test.go`'s integration tests) against the hardened build, without shipping either into the runtime image.
- Pin base image tags (all stages) to a currently-supported release instead of floating `:latest` or an already-EOL version, for reproducible builds and stable `govulncheck`/`gosec` baselines.
- Keep the container's external contract identical: listens on `:8080`, no required env vars, creates `uploads/`/`temp/`/`outputs/` on first run, `ffmpeg` on `PATH`.

**Non-Goals:**
- No change to `main.go`/`main_test.go` or any application behavior.
- No change to `docker-compose.yml`'s `app` service definition (still `build: .`, defaulting to the final/runtime stage) — the only compose change is a new, separate `app-test` service for running tests (see Decisions).
- No change to `docs/operations.md`'s deployment *commands* (`docker build -t ...`, `docker run -p ...` stay the same) — only the prose describing the image's internals updates, and only insofar as it's now inaccurate.
- No distroless/scratch runtime base — `ffmpeg` needs a package manager to install, and this project's runtime dependency is exactly one package (`ffmpeg`) plus CA certs, which alpine covers well.
- No OS-package vulnerability scanning (e.g. Trivy/Grype) added to CI — `gosec`/`govulncheck` only cover Go code and Go module dependencies, not Alpine packages; adding image scanning is a distinct concern for a future change, not folded in here.

## Decisions

**Three-stage layout: `golang:1.26-alpine` builder → `test` (builder + ffmpeg) → pinned `alpine` runtime.**
- `builder`: `WORKDIR /app`, `COPY go.mod go.sum ./`, `RUN go mod download` (see readonly decision below), `COPY . .`, `RUN CGO_ENABLED=0 go build -o /out/app .`.
- `test`, `FROM builder`: `RUN apk add --no-cache ffmpeg`. Never copied from or shipped; exists solely so `go test ./...` has both the Go toolchain and `ffmpeg` available, matching what `main_test.go` needs.
- `runtime` (declared last, so it's the default target for a plain `docker build .`): pinned `alpine:3.24` (see version decision below), `RUN apk add --no-cache ffmpeg`, create a non-root user, `WORKDIR /app`, create `uploads outputs temp` and `chown` the whole `/app` tree to that user, `COPY --from=builder /out/app /app/app`, `USER` the non-root user, `EXPOSE 8080`, `CMD ["/app/app"]`.

Alternative considered: distroless runtime base (`gcr.io/distroless/static`) — rejected because it has no package manager, so `ffmpeg` would need to be vendored as a static binary copied in manually, adding build complexity disproportionate to what this hackathon deliverable needs. Alpine keeps the runtime stage simple while still cutting out the entire Go toolchain and source tree.

**`docker-compose.yml` gets a new `app-test` service, built from the `test` stage.**
Same environment variables and `depends_on: postgres` as `app`, but `build.target: test`. `docs/development.md`'s documented test command becomes `docker compose run --build --rm app-test go test ./... -v`. This was the only viable option once the runtime image genuinely has no Go toolchain — running tests *outside* Docker entirely would regress the `add-docker-compose-app-service` requirement that the full suite (including PostgreSQL-backed adapter tests) runs via Docker without installing Go/ffmpeg locally. A single service built to different targets per invocation was considered and rejected: Compose's CLI has no per-`run` target override, only a fixed `target:` per service in the file.

**`go mod download` under `-mod=readonly` (not `go mod tidy`, not bare `go mod download`) in the builder.**
`go mod tidy` can rewrite `go.mod`/`go.sum` from whatever the module proxy currently serves. Plain `go mod download` is *closer* to what's needed but can still add missing checksums to an incomplete `go.sum` rather than failing — it verifies existing entries but doesn't refuse to patch gaps. Setting `GOFLAGS=-mod=readonly` (or passing `-mod=readonly` explicitly) makes `go mod download`/`go build` refuse to add or change anything in `go.mod`/`go.sum`, failing the build immediately if the committed files don't already fully describe the dependency graph. This is the actual fail-closed behavior the requirement needs.

**Non-root user, fixed UID 1000, with an explicitly provisioned, owned working directory.**
`adduser -D -u 1000 appuser` (alpine's `adduser`) in the runtime stage, then `WORKDIR /app`, `RUN mkdir -p uploads outputs temp && chown -R appuser:appuser /app`, then `USER appuser`. Without an explicit `WORKDIR` and pre-created, `chown`-ed directories, the non-root process would try to create `uploads`/`outputs`/`temp` relative to `/` (each stage starts with its own default working directory, not the builder's), which it has no permission to do. UID 1000 is chosen because it's the conventional first non-root UID on single-user Linux/WSL dev machines (matches this repo's own dev environment) — the same convention official images like `node` use. This is a local-dev/study-project pragmatic choice, not a hard security requirement, and is called out as a risk below.

**Runtime base pinned to `alpine:3.24`, not `3.20`.**
Alpine 3.20 reached end-of-life and stopped receiving security patches; using it would undermine the hardening goal, since `gosec`/`govulncheck` don't scan OS packages at all — an EOL base would carry known-unpatched OS-level vulnerabilities invisible to this repo's existing CI gates. Alpine 3.24 is a currently-supported branch. Pinning to the minor version (not an exact patch like `3.24.1`) matches this repo's existing convention (`postgres:16-alpine`) and still gets in-branch security patches on rebuild, unlike a fully floating `alpine`/`alpine:latest` tag.

**`.dockerignore` excludes `uploads/`, `outputs/`, `temp/`, `.git/`.**
These are runtime-generated/VCS directories that don't belong in the build context; excluding them shrinks the context sent to the daemon and avoids ever baking stale local artifacts into an image layer.

## Risks / Trade-offs

**[Risk] Non-root UID 1000 may not have write access to `docker-compose.yml`'s bind-mounted `./uploads`/`./outputs` on a host where the invoking user isn't UID 1000** → Mitigation: document the constraint in `docs/development.md` (same pattern as the existing `postgres_data` volume callout): before first run, ensure the host `uploads/` and `outputs/` directories are owned by (or writable by) UID 1000 — e.g. `chown -R 1000:1000 uploads outputs`. Note that simply deleting these directories and letting Docker recreate them does **not** fix this: a bind mount with a missing host source is auto-created by the Docker daemon (running as root) as a root-owned directory regardless of which user invokes `docker compose`, so the same permission failure would recur. This is a known, pre-existing rough edge of bind-mounting host directories into a container with a fixed UID; it does not affect `docs/operations.md`'s deployment path, which doesn't bind-mount these directories.

**[Risk] Pinned `alpine:3.24` (and the existing `golang:1.26-alpine`) will eventually go stale or reach EOL relative to upstream security patches** → Mitigation: this is the same trade-off already accepted for every other pinned base image/dependency in this repo (Go modules, `postgres:16-alpine`); `govulncheck`/`gosec`/Dependabot remain the mechanism for catching and bumping stale Go-level pins, but neither scans OS packages — base-image EOL tracking for `alpine`/`golang` tags is a manual/Dependabot-adjacent concern to revisit periodically, not a reason to leave tags floating.

**[Risk] `CGO_ENABLED=0` static build could break if a future dependency requires cgo** → Mitigation: none of the current dependencies (`gin`, `pgx`, `golang-jwt/jwt`, `bcrypt`) require cgo; if one is added later that does, this is a one-line Dockerfile change to drop the flag, not a design constraint worth building in speculatively now.

**[Risk] A three-stage Dockerfile with a second Compose service (`app-test`) is more moving parts than the original one-stage/one-service setup** → Mitigation: the `test` stage adds one line (`RUN apk add --no-cache ffmpeg`) on top of `builder`; the `app-test` service mirrors `app`'s existing environment/`depends_on` block with only `build.target` differing. This is the minimum needed to keep "run the full suite via Docker" working once the runtime image is genuinely hardened — there's no simpler design that satisfies both a Go-toolchain-free runtime image and a Docker-based test command.

## Migration Plan

1. Rewrite `Dockerfile` as three stages (`builder`, `test`, `runtime`), add `.dockerignore`, add `app-test` to `docker-compose.yml` (implementation PR).
2. Verify: `docker compose up --build` still serves the app and identity routes correctly; `docker compose run --build --rm app-test go test ./... -v` passes; image runs as non-root (`docker compose exec app id` shows UID 1000); an intentionally corrupted `go.sum` entry makes the builder stage fail rather than silently patch itself.
3. Update `docs/development.md`, `docs/operations.md`, `CLAUDE.md` to describe the hardened build and the new test command instead of the anti-pattern (separate docs PR, per the repo's PR-separation rule).
4. Archive: promote the `container-image` spec and the `development-workflow` delta, mark tasks complete.

No rollback complexity beyond reverting the Dockerfile and `docker-compose.yml`'s `app-test` addition — no data migration, no schema change, no persisted state affected.

## Open Questions

None — scope is narrow and self-contained.
