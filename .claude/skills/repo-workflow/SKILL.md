---
name: repo-workflow
description: Use when the user asks to open, update, hand off, or merge a pull request, or when you create a pull request yourself. Covers definition of done, branch protection, quality gates, commit conventions, release-please, review comments, and merge authorization. Do not use it to choose or require a development methodology; direct implementation, task lookup, and OpenSpec selection are outside this skill.
---

# Repo workflow (FIAP X video-processor)

Use this skill for every pull request requested by the user or created by the agent. It governs PR quality and delivery only; it does not select, start, or require OpenSpec. Full prose for humans and non-Claude-Code agents lives in `docs/development.md` ("Code Quality Gates" and "Contribution Conventions"); keep that document and this skill in sync.

## Definition of done

Before reporting **any** change complete:

- If the diff includes a Go module input file (`.go` source, `go.mod`, or `go.sum`): `go test ./... -v` passes locally — this is the canonical "change complete" requirement. CI's `Build & Test` job also runs `go vet ./...`, so run that too before pushing. Tests are integration tests requiring `ffmpeg` on `PATH`; if unavailable, run via `docker compose run --build --rm app-test go test ./... -v`. A dependency-only bump (`go.mod`/`go.sum` with no `.go` file touched) still requires this — it can change compiled/runtime behavior.
- If the diff has no Go module input file (documentation or agent/skill configuration only): this requirement doesn't apply — don't claim a test run that didn't happen, and don't skip reporting the change done because of it either.
- If a PR is open for this work, confirm all three required checks (`Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`) are passing (`gh pr checks <n>`) — a PR isn't done with failing checks even if reviews look clean. The branch does not need to be up to date with `main`.
- If a PR is open for this work, check for review comments (automatic Copilot review + human) and address genuine findings first:
  ```bash
  gh pr view <n> --json reviews
  gh api repos/{owner}/{repo}/pulls/{n}/comments
  ```
  Fix genuine findings and resolve their threads (`resolveReviewThread` GraphQL mutation). If a finding doesn't warrant a change, say why instead of leaving it silently unaddressed. Copilot's review can take a short while to post after a push — an empty check immediately after opening the PR doesn't mean nothing is coming. It is requested automatically only the **first** time each PR opens (`review_on_push` is off), so a PR that was force-pushed or reopened gets no fresh pass on its own — request one manually when the follow-up change is substantial.

## Commit messages

[Conventional Commits](https://www.conventionalcommits.org/) only: `feat:`, `fix:`, `chore:`, `docs:`, `ci:`, `test:`, `refactor:`. Use `!` after the type or a `BREAKING CHANGE:` footer for breaking changes. `release-please` computes the version bump from the commit message alone, so a change whose `proposal.md` calls itself breaking has to carry that marker on the implementation commit too — a `fix:` subject on a proposal-declared breaking change ships as a patch bump. `release-please` maintains a single up-to-date Release PR from these commits on every push to `main`; merging that PR — not tagging manually — is what creates the git tag, publishes the GitHub Release, and updates `CHANGELOG.md`. Never tag or version manually.

## Branch protection & required checks

`main` rejects direct pushes, no exceptions (including for admins). Every change lands via a feature branch + PR:

```bash
git fetch origin
git checkout -b feat/short-description origin/main   # or fix/..., chore/..., docs/... — Conventional Commits type
git push -u origin feat/short-description
gh pr create --fill
```

Branch from freshly-fetched `origin/main`, not from whatever happens to be checked out. Branching from stale or unrelated work can carry unrelated commits into the new PR's diff.

Not mergeable until all three required checks pass: `Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`. The branch does not need to be up to date with `main`. This applies to every PR, including `release-please`'s own release PR — no special-casing.

## Quality gates

```bash
go vet ./...       # required when the diff includes a Go module input (.go / go.mod / go.sum) — see Definition of done
go test ./... -v   # same condition — ffmpeg/Docker fallback also in Definition of done
gosec ./...        # scans the whole codebase; CI runs it on every PR regardless of what the diff touches
govulncheck ./...  # same — CI-required on every PR regardless of diff content
```

All four CI-backed checks (`Build & Test` = `go vet` + `go test`, `SAST` = `gosec`, `Vulnerability Scan` = `govulncheck`) must pass in CI on every PR — that's the branch-protection gate, and it's not diff-conditional. Locally, only `go vet`/`go test` are conditional on the diff containing a Go module input (see Definition of done); `gosec`/`govulncheck` scan the full codebase, so a docs/skill-only change rarely needs a fresh local run of those two — but running them costs little, and CI will catch anything missed regardless. CI fails the build on **any** `gosec` finding — that's deliberate policy, not a bug. `#nosec` is a last resort, not the default response: check the rule's own docs (e.g. `securego.io/docs/rules/g304.html` — lowercase, case-sensitive path) for a validation pattern gosec recognizes as safe, and test it (`gosec ./...`) before reaching for suppression. Only suppress a genuine false positive or accepted risk with no recognized fix, using a bare `#nosec G<rule-id>` (no restated prose — that belongs in the commit/PR description). `govulncheck` failures are resolved by upgrading the implicated dependency (check `go mod graph` for which direct dependency pulls it in), then `go mod tidy`.

## Merge rule

Green CI, complete tasks, an absent review, or a prior authorization do **not** authorize a merge. Merge only after the user explicitly authorizes that specific PR in the current session — authorization for one PR does not extend to later PRs.

## Validation before handoff

```bash
git diff --check
```

If this change also touches this skill or `docs/development.md`, confirm the two still agree before reporting done. Report the PR number, URL, changed-file scope, and check results before handing off.
