# AGENTS.md

## Repository governance

This repository uses OpenSpec for every non-trivial change: new features, behavior changes, bug fixes with design decisions, refactors, infrastructure, workflow, schema, and contract changes. Trivial typo/comment/dependency-only edits may skip the full flow; when in doubt, create a change first. (Separately, any change whose diff touches a Go module input — `.go`/`go.mod`/`go.sum` — still needs a passing local test run before being reported complete, regardless of whether it goes through OpenSpec.)

Read the relevant files before acting:

- `CLAUDE.md` — project context, architecture, commands.
- `docs/development.md` — the full workflow: OpenSpec process, PR separation rule, branch protection, commit messages, quality gates (including local test-run requirements), merge rule, PR review-comment checking, validation/handoff commands. Spread across its "Code Quality Gates" and "Contribution Conventions" sections — not all in one place.
- Claude Code agents specifically: `.claude/skills/repo-workflow/SKILL.md` and `.claude/skills/change-lifecycle/SKILL.md` auto-apply this workflow at the right moments (opening/merging a PR, reporting a change complete, writing a commit, running quality gates, sequencing explore/propose/apply/archive) — they're the compact, agent-actionable version of the same rules in `docs/development.md`.
- `openspec/specs/` for canonical requirements; `openspec/changes/` for active changes.

## Merge rule

Do not merge a PR because CI is green, tasks are complete, a review is absent, or another PR was authorized. Merge only after the user explicitly authorizes that specific PR in the current session. Authorization applies to one designated PR only. Never push directly to `main`.
