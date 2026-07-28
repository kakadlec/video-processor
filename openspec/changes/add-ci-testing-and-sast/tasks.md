## 1. CI workflow

- [x] 1.1 Add `.github/workflows/ci.yml` with a `test` job: checkout, setup Go, install `ffmpeg` via `apt-get`, `go vet ./...`, `go test ./... -v`. Triggers on push to `main` and on pull requests.
- [x] 1.2 Add a `sast` job in the same workflow: checkout, setup Go, `go install github.com/securego/gosec/v2/cmd/gosec@latest`, run `gosec ./...`. Fails the build on any finding (default gosec exit behavior).

## 2. Policy documentation

- [x] 2.1 Update `CLAUDE.md`: a change isn't complete until `go test ./...` passes locally; document that CI now enforces this plus the SAST gate, and the `#nosec` + justification convention for suppressing a specific false positive.

## 3. GitHub repository

- [ ] 3.1 Create the public GitHub repository `kakadlec/video-processor` via `gh repo create`.
- [ ] 3.2 Add it as the `origin` remote and push `main` with full history.
- [ ] 3.3 Confirm the CI workflow actually ran on GitHub Actions after the push, and that the observed result matches expectations (test job green, sast job red due to the known pre-existing findings).
