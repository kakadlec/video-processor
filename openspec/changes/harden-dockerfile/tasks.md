## 1. Implementation (implementation PR)

- [ ] 1.1 Add `.dockerignore` excluding `uploads/`, `outputs/`, `temp/`, `.git/`, and other non-build-context files.
- [ ] 1.2 Rewrite `Dockerfile` as a multi-stage build: `golang:1.26-alpine` builder stage running `go mod download` + `CGO_ENABLED=0 go build`, then a pinned `alpine:3.20` runtime stage with `ffmpeg` installed, a fixed non-root UID 1000 user, and `CMD` running the compiled binary. Update the file's own header comment (currently "DOCKERFILE SIMPLES (sem boas práticas - propositalmente!)") to reflect the hardened build.
- [ ] 1.3 Confirm `docker-compose.yml` needs no changes to keep building/running against the hardened image (it shouldn't — same `build: .`, same port, same env vars).

## 2. Verification

- [ ] 2.1 `docker compose up --build` starts successfully; the app serves `/` and identity routes (`/api/auth/register`, `/api/auth/login`) on `127.0.0.1:8080`.
- [ ] 2.2 `docker compose exec app id` shows a non-root UID (1000), confirming the process does not run as root.
- [ ] 2.3 `docker compose run --build --rm app go test ./... -v` still passes against the hardened image.
- [ ] 2.4 Upload a video end-to-end through the running compose stack and confirm the resulting ZIP is written to the host's bind-mounted `./outputs`, verifying the non-root user can write to the bind-mounted directories on this dev machine.
- [ ] 2.5 `gosec ./...` and `govulncheck ./...` still pass (no new findings introduced by the Dockerfile change; these tools don't scan Dockerfiles directly, but confirm CI's required checks stay green on the implementation PR).

## 3. Documentation (separate docs PR, after implementation merges)

- [ ] 3.1 Update `docs/development.md`'s Dockerfile callout (currently: "intentionally a single-stage build without a non-root user... hardening tracked in the Change Backlog") to describe the hardened build instead, including the bind-mount UID-1000 caveat from `design.md`.
- [ ] 3.2 Update `docs/operations.md`'s Docker section (currently describes the Dockerfile as "a single-stage build... runs as root... intentional anti-pattern... hardening planned for Phase 8") to describe the hardened multi-stage, non-root build.
- [ ] 3.3 Update `CLAUDE.md`'s Dockerfile note under "Notable constraints / gotchas" (currently says the Dockerfile is "deliberately written as an anti-pattern example" and "do not treat it as a template") to reflect that it's now hardened.

## 4. Archive

- [ ] 4.1 Mark all tasks above complete.
- [ ] 4.2 Promote `specs/container-image/spec.md` into `openspec/specs/container-image/spec.md`.
- [ ] 4.3 Move the change folder to `openspec/changes/archive/`.
- [ ] 4.4 Update `docs/roadmap.md`'s `harden-dockerfile` Change Backlog row to `archived` (separate docs-only PR, per the backlog's own rule).
