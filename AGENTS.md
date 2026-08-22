# AGENTS.md

## Project context

Read `CLAUDE.md` for the application architecture, commands, and technical constraints. Canonical product requirements live in `openspec/specs/`; active and archived OpenSpec artifacts live in `openspec/changes/`.

## Workflow

The user chooses the workflow for each change. Do not require OpenSpec, tests, branches, pull requests, commits, or any other process unless the user asks for it.

Optional workflows are available as skills under `.claude/skills/`, including `change-lifecycle` for the full OpenSpec flow and `repo-workflow` for PR, validation, commit, and release procedures. Apply them only when the user explicitly invokes or requests that workflow.
