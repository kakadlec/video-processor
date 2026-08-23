# AGENTS.md

## Repository quality requirements

No development methodology is mandatory in this repository. Developers may implement changes directly or explicitly choose tools such as OpenSpec. Do not start or require an OpenSpec lifecycle unless the user asks for OpenSpec, invokes an `/opsx:*` command, or explicitly continues a named active OpenSpec change.

Read the relevant files before acting:

- `CLAUDE.md` — project context, architecture, commands, and the local test requirement.
- `docs/development.md` — quality gates, branch protection, commit messages, release mechanics, PR review-comment checks, validation, and handoff requirements.
- `.claude/skills/repo-workflow/SKILL.md` — mandatory whenever the user requests a PR action or the agent creates a PR.
- `.claude/skills/change-lifecycle/SKILL.md` — optional OpenSpec lifecycle, used only after explicit OpenSpec intent.

A change whose diff touches `.go`, `go.mod`, or `go.sum` is not complete until `go test ./... -v` passes locally. Run `go vet ./...` before pushing such a change. Documentation and agent-configuration-only changes are exempt from the local Go test requirement; do not claim tests that were not run.

Every PR must use a feature branch and pass `Build & Test`, `SAST (gosec)`, and `Vulnerability Scan (govulncheck)`. Check reviews and inline comments, address valid findings, and resolve review conversations before handoff or merge. PR branches do not need to be up to date with `main` before merging. Never push directly to `main`.

## Merge rule

Do not merge a PR because CI is green, tasks are complete, a review is absent, or another PR was authorized. Merge only after the user explicitly authorizes that specific PR in the current session. Authorization applies to one designated PR only.
