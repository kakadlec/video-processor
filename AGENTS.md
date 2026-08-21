# AGENTS.md

## Repository governance

This repository has often used OpenSpec for larger changes: new features, behavior changes, bug fixes with design decisions, refactors, infrastructure, workflow, schema, and contract changes. That's a documented pattern, not a rule this file (or any doc) imposes — whether a given change goes through OpenSpec, a lighter version of it, or a single direct PR is the maintainer's call, made per change. (Separately, any change whose diff touches a Go module input — `.go`/`go.mod`/`go.sum` — still needs a passing local test run before being reported complete, regardless of process.)

Read the relevant files before acting:

- `CLAUDE.md` — project context, architecture, commands.
- `docs/development.md` — reference for the OpenSpec pattern, PR separation, branch protection, commit messages, quality gates (including local test-run requirements), merge rule, PR review-comment checking, validation/handoff commands. Spread across its "Code Quality Gates" and "Contribution Conventions" sections — not all in one place.
- Claude Code agents specifically: `.claude/skills/repo-workflow/SKILL.md` and `.claude/skills/change-lifecycle/SKILL.md` carry the actual scoping/sequencing judgment for Claude Code's own work — deciding when the pattern above applies, when it doesn't, and deferring to the maintainer's explicit direction over their own default. `docs/development.md` is what they draw on, not a gate they (or an automated review) enforce independently of the maintainer.
- `openspec/specs/` for canonical requirements; `openspec/changes/` for active changes.

## Merge rule

Do not merge a PR because CI is green, tasks are complete, a review is absent, or another PR was authorized. Merge only after the user explicitly authorizes that specific PR in the current session. Authorization applies to one designated PR only. Never push directly to `main`.
