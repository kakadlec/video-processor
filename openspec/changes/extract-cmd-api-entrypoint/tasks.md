## 1. Move source files

- [ ] 1.1 `git mv main.go identity.go main_test.go identity_test.go cmd/api/` (create `cmd/api/` as part of the move)
- [ ] 1.2 `git mv web cmd/api/web`
- [ ] 1.3 Confirm no other file references `web` via a path outside `cmd/api` (e.g. Dockerfile, docker-compose.yml, .dockerignore) and update any that do — expected: none, since `COPY . .` and volume mounts already operate on the whole repo tree

## 2. Fix the go test working-directory mismatch

- [ ] 2.1 In `cmd/api/main_test.go`'s `TestMain`, `os.Chdir("../..")` to the repo root before `createDirs()`, with a comment explaining why (go test's per-package working directory vs. the app's repo-root-relative paths)
- [ ] 2.2 Confirm no other cwd-relative path in `cmd/api/main_test.go` or `cmd/api/identity_test.go` besides `uploads`/`outputs`/`temp` is affected (already checked via grep during design; re-verify after the move)

## 3. Build tooling

- [ ] 3.1 Update `Dockerfile`'s `RUN CGO_ENABLED=0 go build -o /out/app .` to `RUN CGO_ENABLED=0 go build -o /out/app ./cmd/api`

## 4. Validation

- [ ] 4.1 `go vet ./...` passes
- [ ] 4.2 `go test ./... -v` passes (ffmpeg on `PATH`, or via `docker compose run --build --rm app-test go test ./... -v`)
- [ ] 4.3 `docker compose up --build` starts successfully and `GET /` / `POST /upload` work through the running container (manual smoke check)
- [ ] 4.4 `gosec ./...` and `govulncheck ./...` clean, verified by CI's `SAST (gosec)` and `Vulnerability Scan (govulncheck)` required checks
