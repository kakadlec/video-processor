## Context

Solo hackathon repo, no CI/CD, no security scanning, tests only run when someone remembers `go test ./...`. This is about closing that gap before the code churns more in the upcoming refactor.

## Goals / Non-Goals

**Goals:**
- CI runs the full test suite (with `ffmpeg` available) on every push/PR.
- CI runs `gosec` on every push/PR, and fails the build on any finding.
- The policy ("tests must pass to call a change done") is written down, not just tribal knowledge.

**Non-Goals:**
- Fixing the 9 existing `gosec` findings — that's a real code change with its own trade-offs (e.g. some may be false positives worth a `#nosec` justification, others real fixes), out of scope for a CI/tooling change.
- Branch protection rules (requiring the CI check before merge) — not set up here; this repo has no PR-based workflow yet (solo, pushing to `main` directly). Worth revisiting once collaboration/PRs are actually in play.
- Vulnerability scanning of dependencies (`govulncheck`, Dependabot) — SAST here means static analysis of the code itself (`gosec`), matching what was asked for. Dependency scanning is a reasonable follow-up, not bundled in.

## Decisions

**`gosec` via `go install` in the workflow, not a marketplace Action.**
Alternatives considered: `securego/gosec-action` or similar third-party Actions. Rejected: adding a security-scanning tool via an unpinned third-party Action introduces exactly the kind of supply-chain trust a SAST tool is meant to reduce. `go install github.com/securego/gosec/v2/cmd/gosec@latest` pulls directly from the tool's own official module path — one fewer intermediary to trust. Trade-off: `@latest` isn't reproducible across runs (a new `gosec` release could change what's flagged); acceptable for now, worth pinning to a specific version once the noisy initial findings are triaged.

**`ffmpeg` via `apt-get install` on the runner, not a marketplace Action.**
Same reasoning as above, applied consistently: Ubuntu's `ffmpeg` package is good enough for test purposes and avoids trusting an extra Action just to fetch a binary `apt` already provides.

**SAST failures block the build (per explicit decision), starting immediately.**
This means CI is red on day one — 9 known findings (`G204` subprocess with variable args from the `ffmpeg` call, `G304` file access via variable path in upload/zip handling, `G301` directory permissions `0755`). This is intentional: a SAST gate that's green by default because nothing enforces it yet isn't actually protecting anything. The alternative (report-only) was considered and explicitly rejected for this project.

**Two separate CI jobs (test, sast) instead of one.**
Runs in parallel, and a failure in one is unambiguous about which gate broke (test regression vs. new security finding) without reading through combined logs.

## Risks / Trade-offs

- [CI starts red and stays red until findings are triaged] → Accepted per explicit decision; tracked as follow-up work, not silently ignored or auto-fixed here.
- [`gosec@latest` is a moving target] → Low risk short-term; revisit pinning once the initial findings are resolved and the noise floor is known.
- [No branch protection means the CI gate is informational, not enforced by GitHub itself, until PRs are used] → Acceptable for a solo repo; policy is documented in `CLAUDE.md` so it's still the expected practice even without a hard technical block.

## Open Questions

- Whether/when to add branch protection and PR-based workflow — deferred until collaboration actually happens.
