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

The implementation PR must contain **only the files that implement the change's declared proposal scope**: application source and test files for a feature/behavior change, or the specific configuration/CI/infrastructure files named in the proposal for a change whose own subject is configuration, infrastructure, or CI (e.g. `docker-compose.yml`, `.github/workflows/ci.yml`). It must not modify:

- `tasks.md` or any other OpenSpec file;
- `README.md`, `docs/`, `CLAUDE.md`, or `AGENTS.md`;
- configuration or CI files unrelated to this change's declared proposal scope;
- canonical specs;
- unrelated files.

Do not mark tasks complete in this PR. Do not mix documentation or governance updates with code — those belong in the finalization PR below, not here.

### 3. Finalization PR (documentation, archive, and roadmap together)

After the implementation PR merges, create **one** closure PR that contains all of the following together — do not split them into separate PRs:

- marks the completed tasks in `tasks.md`;
- promotes the delta into `openspec/specs/`;
- moves the complete change folder to `openspec/changes/archive/<date>-<change-id>/`;
- updates any permanent documentation (`README.md`, `docs/`, `CLAUDE.md`, `AGENTS.md`) that needs to reflect the shipped change;
- flips the change's `docs/roadmap.md` Change Backlog row to `archived` with links to the archive folder and promoted spec(s).

This PR must not contain application source or tests — only the closure operations above.

## Merge rule

Do not merge a PR because CI is green, tasks are complete, a review is absent, or another PR was authorized. Merge only after the user explicitly authorizes that specific PR in the current session. Authorization applies to one designated PR only.

## Pull request review comments

This repository has a `copilot_code_review` branch ruleset that automatically requests a GitHub Copilot review the first time each pull request opens. `review_on_push` is off, so later commits pushed to an already-reviewed PR do **not** trigger a fresh automatic review — request one manually if a substantial follow-up change warrants a new pass.

Before reporting a PR-related task complete, check that PR for review comments (automatic and human) and address the ones that make sense:

```bash
gh pr view <n> --json reviews
gh api repos/{owner}/{repo}/pulls/{n}/comments
```

Fix genuine findings and resolve their threads (`resolveReviewThread` GraphQL mutation). If a finding doesn't warrant a change, say why rather than leaving it unaddressed silently. Copilot's review can take a short while to post after a push — an empty check immediately after opening the PR doesn't mean there's nothing coming.

## Validation and handoff

Before opening or handing off a PR:

```bash
git diff --check
npx --yes @fission-ai/openspec validate <change-id> --strict --no-interactive
```

Before reporting implementation complete, run the repository's required tests and checks. Report the PR number, URL, changed-file scope, and check results. Never direct-push to `main`.
