# AGENTS.md

## Repository governance

This repository uses OpenSpec for every non-trivial change: new features, behavior changes, bug fixes with design decisions, refactors, infrastructure, workflow, schema, and contract changes. Trivial typo/comment-only edits may skip the full flow; a dependency bump is not automatically trivial, since it still requires a passing test run. When in doubt, create a change first.

Read the relevant files before acting:

- `CLAUDE.md` — project context, architecture, commands.
- `docs/development.md` ("Contribution Conventions" section) — the full workflow: OpenSpec process, PR separation rule, branch protection, commit messages, quality gates, merge rule, PR review-comment checking, validation/handoff commands.
- Claude Code agents specifically: `.claude/skills/repo-workflow/SKILL.md` and `.claude/skills/change-lifecycle/SKILL.md` auto-apply this workflow at the right moments (opening/merging a PR, reporting a change complete, writing a commit, running quality gates, sequencing explore/propose/apply/archive) — they're the compact, agent-actionable version of the same rules in `docs/development.md`.
- `openspec/specs/` for canonical requirements; `openspec/changes/` for active changes.

## Merge rule

Do not merge a PR because CI is green, tasks are complete, a review is absent, or another PR was authorized. Merge only after the user explicitly authorizes that specific PR in the current session. Authorization applies to one designated PR only. Never push directly to `main`.
