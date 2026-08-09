## 1. Implementation (implementation PR)

- [ ] 1.1 Add `.dockerignore` excluding `uploads/`, `outputs/`, `temp/`, `.git/`, and other non-build-context files.
- [ ] 1.2 Rewrite `Dockerfile` as a three-stage build: `builder` (`golang:1.26-alpine`, `GOFLAGS=-mod=readonly` `go mod download` + `CGO_ENABLED=0 go build`), `test` (`FROM builder`, adds `ffmpeg`), and `runtime` (pinned `alpine:3.24`, `ffmpeg`, fixed non-root UID 1000 user, `WORKDIR /app` with `uploads`/`outputs`/`temp` pre-created and `chown`-ed to that user before `USER` switches, `CMD` running the compiled binary). `runtime` must be the last stage declared so it's the default target for a plain `docker build .`. Update the file's own header comment (currently "DOCKERFILE SIMPLES (sem boas práticas - propositalmente!)") to reflect the hardened build.
- [ ] 1.3 Add an `app-test` service to `docker-compose.yml`: same `environment`/`depends_on` as `app`, `build.target: test`. The `app` service itself needs no changes.

## 2. Verification

- [ ] 2.1 `docker compose up --build` starts successfully; the app serves `/` and identity routes (`/api/auth/register`, `/api/auth/login`) on `127.0.0.1:8080`.
- [ ] 2.2 `docker compose exec app id` shows a non-root UID (1000), confirming the process does not run as root.
- [ ] 2.3 `docker compose run --build --rm app-test go test ./... -v` passes against the `test` stage image.
- [ ] 2.4 Upload a video end-to-end through the running compose stack and confirm the resulting ZIP is written to the host's bind-mounted `./outputs`, verifying the non-root user can write to the bind-mounted directories on this dev machine (pre-`chown` the host `uploads`/`outputs` dirs to UID 1000 first if this is a fresh clone).
- [ ] 2.5 Confirm the builder stage fails closed: temporarily corrupt an entry in `go.sum` and verify `docker compose build app` fails instead of silently rewriting it.
- [ ] 2.6 `gosec ./...` and `govulncheck ./...` still pass (no new findings introduced by the Dockerfile change; these tools don't scan Dockerfiles directly, but confirm CI's required checks stay green on the implementation PR).

## 3. Documentation (separate docs PR, after implementation merges)

- [ ] 3.1 Update `docs/development.md`'s Dockerfile callout and test-running command (currently `docker compose run --build --rm app go test ./... -v`; must become `docker compose run --build --rm app-test go test ./... -v`) to describe the hardened build instead, including the bind-mount UID-1000 caveat from `design.md`.
- [ ] 3.2 Update `docs/operations.md`'s Docker section (currently describes the Dockerfile as "a single-stage build... runs as root... intentional anti-pattern... hardening planned for Phase 8") to describe the hardened multi-stage, non-root build.
- [ ] 3.3 Update `CLAUDE.md`'s Dockerfile note under "Notable constraints / gotchas" (currently says the Dockerfile is "deliberately written as an anti-pattern example" and "do not treat it as a template") to reflect that it's now hardened.

## 4. Archive

- [ ] 4.1 Mark all tasks above complete.
- [ ] 4.2 Promote `specs/container-image/spec.md` into `openspec/specs/container-image/spec.md` and merge the `specs/development-workflow/spec.md` delta into `openspec/specs/development-workflow/spec.md`.
- [ ] 4.3 Move the change folder to `openspec/changes/archive/`.
- [ ] 4.4 Update `docs/roadmap.md`'s `harden-dockerfile` Change Backlog row to `archived` (separate docs-only PR, per the backlog's own rule).
