## Why

Tests and specs only protect the project if they actually run on every change, and security issues compound the longer they go unnoticed. Right now nothing enforces that `go test ./...` passes before a change is considered done, and there's no static security scanning at all. Both need to exist before the planned refactor starts touching more code.

## What Changes

- Add a GitHub Actions CI workflow (`.github/workflows/ci.yml`) that runs on every push to `main` and every pull request:
  - **Test job**: installs `ffmpeg`, runs `go vet ./...` and `go test ./... -v`.
  - **SAST job**: runs `gosec` (the standard Go static analysis security scanner) against the codebase.
- **BREAKING (process, not code)**: SAST failures block CI. `gosec` already finds 9 issues in the current code (subprocess invocation, path-derived file access, directory permissions) — CI will start red until each is fixed or explicitly suppressed with a justified `#nosec` comment. This is a deliberate choice, not an oversight.
- Document in `CLAUDE.md` that a change isn't complete until `go test ./...` passes locally and CI (tests + SAST) is green.
- Create the public GitHub repository `kakadlec/video-processor` and push the existing history to it.

## Capabilities

### New Capabilities
- `development-workflow`: CI-enforced requirements that every change must satisfy — automated tests and SAST scanning, both gating.

### Modified Capabilities
(none)

## Impact

- New file: `.github/workflows/ci.yml`.
- `CLAUDE.md`: updated policy section.
- New GitHub repository `kakadlec/video-processor` (public), with this local repo's history pushed to it.
- CI will initially fail on the SAST job due to pre-existing findings; this is expected and tracked, not silently fixed by this change (fixing them is a separate, deliberate decision — likely part of the upcoming refactor).
