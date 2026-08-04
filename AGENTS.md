# AGENTS.md

## Repository governance

This repository uses OpenSpec for every non-trivial change: new features, behavior changes, bug fixes with design decisions, refactors, infrastructure, workflow, schema, and contract changes. Trivial typo/comment/dependency-only edits may skip the full flow; when in doubt, create a change first.

Read the relevant files before acting:

- `CLAUDE.md` for repository context and commands;
- `docs/development.md` for contributor workflow;
- `openspec/specs/` for canonical requirements;
- `openspec/changes/` for active changes.

## Required PR sequence

A non-trivial change uses these PR roles, in order:

### 1. Propose PR

Create `openspec/changes/<change-id>/` with the proposal, design, tasks, and delta spec. The propose PR must contain only files under that change folder. It must not contain application code, tests, permanent docs, agent instructions, configuration, CI, or canonical specs. Implementation starts only after this PR is merged.

### 2. Implementation PR

The implementation PR must contain **only application source and test files**. It must not modify:

- `tasks.md` or any other OpenSpec file;
- `README.md`, `docs/`, `CLAUDE.md`, or `AGENTS.md`;
- configuration or CI files;
- canonical specs;
- unrelated files.

Do not mark tasks complete in this PR. Do not mix documentation or governance updates with code. If permanent documentation or agent instructions need updating, use a separate docs PR.

### 3. Finalization/archive PR

After the implementation PR merges, create one closure PR that:

- marks the completed tasks in `tasks.md`;
- promotes the delta into `openspec/specs/`;
- moves the complete change folder to `openspec/changes/archive/<date>-<change-id>/`.

This PR must not contain application source or tests. Tasks and archive belong together in this finalization PR.

## Merge rule

Do not merge a PR because CI is green, tasks are complete, a review is absent, or another PR was authorized. Merge only after the user explicitly authorizes that specific PR in the current session. Authorization applies to one designated PR only.

## Validation and handoff

Before opening or handing off a PR:

```bash
git diff --check
npx --yes @fission-ai/openspec validate <change-id> --strict --no-interactive
```

Before reporting implementation complete, run the repository's required tests and checks. Report the PR number, URL, changed-file scope, and check results. Never direct-push to `main`.
