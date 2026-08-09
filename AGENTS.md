# AGENTS.md

## Repository governance

This repo enforces a workflow beyond ad-hoc edits: OpenSpec for non-trivial changes, a 3-PR sequence (propose/implementation/finalization), CI-enforced quality gates, and Conventional Commits driving automated releases.

Read the relevant files before acting:

- `CLAUDE.md` — project context, architecture, commands.
- `docs/development.md` ("Contribution Conventions" section) — the full workflow: OpenSpec process, PR separation rule, branch protection, commit messages, quality gates, merge rule, PR review-comment checking, validation/handoff commands.
- Claude Code agents specifically: `.claude/skills/repo-workflow/SKILL.md` auto-applies this workflow at the right moments (opening/merging a PR, reporting a change complete, writing a commit, running quality gates) — it's the compact, agent-actionable version of the same rules in `docs/development.md`.
- `openspec/specs/` for canonical requirements; `openspec/changes/` for active changes.

## Merge rule

Do not merge a PR because CI is green, tasks are complete, a review is absent, or another PR was authorized. Merge only after the user explicitly authorizes that specific PR in the current session. Authorization applies to one designated PR only. Never push directly to `main`.
