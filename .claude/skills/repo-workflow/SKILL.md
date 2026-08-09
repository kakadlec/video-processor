---
name: repo-workflow
description: Use before opening or merging a pull request, before marking any task/change/OpenSpec change complete, before writing a commit message, before running or reporting quality gates (go vet / go test / gosec / govulncheck), or when checking PR review comments prior to reporting a PR-related task done. Covers this repo's required PR sequence (propose/implementation/finalization), branch protection, commit conventions, release-please, and merge authorization. Skip for read-only questions, pure exploration, or a trivial single-file typo/comment/dependency edit with no PR involved.
---

# Repo workflow (FIAP X video-processor)

This covers everything that wraps around a change in this repo, beyond the OpenSpec change lifecycle itself (see the `openspec-*` skills / `/opsx:propose,apply,archive` for that, and `change-lifecycle` for how they sequence together). Full prose for humans and non-Claude-Code agents lives in `docs/development.md` ("Contribution Conventions" section) — this is the compact, agent-actionable version; don't restate it in `CLAUDE.md` or `AGENTS.md`. If a future policy change touches one of these files, update the other in the same PR — they're meant to stay in sync even though they usually land in different PRs (see "OpenSpec + PR sequence" below).

## Definition of done

Before reporting **any** change complete:

- If the diff includes a Go module input file (`.go` source, `go.mod`, or `go.sum`): `go vet ./...` and `go test ./... -v` pass locally. Tests are integration tests requiring `ffmpeg` on `PATH`; if unavailable, run via `docker compose run --build --rm app-test go test ./... -v`. A dependency-only bump (`go.mod`/`go.sum` with no `.go` file touched) still requires this — it can change compiled/runtime behavior.
- If the diff has no Go module input file (docs, OpenSpec artifacts, agent/skill config only): this requirement doesn't apply — don't claim a test run that didn't happen, and don't skip reporting the change done because of it either.
- If a PR is open for this work, check for review comments (automatic Copilot review + human) and address genuine findings first:
  ```bash
  gh pr view <n> --json reviews
  gh api repos/{owner}/{repo}/pulls/{n}/comments
  ```
  Fix genuine findings and resolve their threads (`resolveReviewThread` GraphQL mutation). If a finding doesn't warrant a change, say why instead of leaving it silently unaddressed. Copilot's review can take a short while to post after a push — an empty check immediately after opening the PR doesn't mean nothing is coming.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/) only: `feat:`, `fix:`, `chore:`, `docs:`, `ci:`, `test:`, `refactor:`. Use `!` after the type or a `BREAKING CHANGE:` footer for breaking changes. `release-please` maintains a single up-to-date Release PR from these commits on every push to `main`; merging that PR — not tagging manually — is what creates the git tag, publishes the GitHub Release, and updates `CHANGELOG.md`. Never tag or version manually.

## OpenSpec + PR sequence for non-trivial changes

Non-trivial changes (new features, behavior changes, bug fixes with real design decisions, refactors, infra/workflow/CI changes) go through OpenSpec **and** land as three separate PRs, in this order:

1. **Propose PR** — only `openspec/changes/<name>/` artifacts (`proposal.md`/`design.md`/`tasks.md`/delta specs). No application code, tests, docs, agent instructions, or unrelated config/CI. Merges before implementation starts.
2. **Implementation PR** — only the files in the proposal's declared scope: application source/tests for a feature, or the specific configuration/CI/infrastructure files named in the proposal for an infra-subject change (e.g. a new skill file). It must **not** touch `tasks.md`, `README.md`, `docs/`, `CLAUDE.md`, `AGENTS.md`, unrelated config/CI, or anything else under `openspec/`. Documentation/agent-instruction updates that result from the change belong in the finalization PR below, even if they'd be trivial to include here — don't bundle them in.
3. **Finalization PR** — opened after implementation merges, bundles *all* of: marking tasks complete, promoting the delta into `openspec/specs/`, moving the change folder to `openspec/changes/archive/`, updating permanent docs (`README.md`, `docs/`, `CLAUDE.md`, `AGENTS.md`) that need to reflect the shipped change, and flipping the change's `docs/roadmap.md` Change Backlog row to `archived` (if that change has one — not every change does, see `docs/roadmap.md`'s own convention). No application code or tests here.

Skip OpenSpec and the 3-PR split only for trivial changes (typo/comment/dependency-only edits) — those go straight to a single PR. When in doubt, don't skip it.

## Branch protection & required checks

`main` rejects direct pushes, no exceptions (including for admins). Every change lands via a feature branch + PR:

```bash
git checkout -b feat/short-description   # or fix/..., chore/..., docs/... — Conventional Commits type
git push -u origin feat/short-description
gh pr create --fill
```

Not mergeable until all three required checks pass **and** the branch is up to date with `main`: `Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`. This applies to every PR, including `release-please`'s own release PR — no special-casing.

## Quality gates

```bash
go vet ./...
gosec ./...
govulncheck ./...
```

All three must pass; CI fails the build on **any** `gosec` finding — that's deliberate policy, not a bug. `#nosec` is a last resort, not the default response: check the rule's own docs (e.g. `securego.io/docs/rules/g304.html` — lowercase, case-sensitive path) for a validation pattern gosec recognizes as safe, and test it (`gosec ./...`) before reaching for suppression. Only suppress a genuine false positive or accepted risk with no recognized fix, using a bare `#nosec G<rule-id>` (no restated prose — that belongs in the commit/PR description). `govulncheck` failures are resolved by upgrading the implicated dependency (check `go mod graph` for which direct dependency pulls it in), then `go mod tidy`.

## Merge rule

Green CI, complete tasks, an absent review, or a prior authorization do **not** authorize a merge. Merge only after the user explicitly authorizes that specific PR in the current session — authorization for one PR does not extend to later PRs.

## Validation before handoff

```bash
git diff --check
npx --yes @fission-ai/openspec validate <change-id> --strict --no-interactive   # only if an OpenSpec change is involved
```

If this change also touches this skill or `docs/development.md`, confirm the two still agree before reporting done. Report the PR number, URL, changed-file scope, and check results before handing off.
