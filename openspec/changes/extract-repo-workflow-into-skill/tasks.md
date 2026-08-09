## 1. Add the `repo-workflow` skill (implementation PR scope)

- [ ] 1.1 Create `.claude/skills/repo-workflow/SKILL.md` with frontmatter `description` covering: opening/merging a PR, marking any task/change/OpenSpec change complete, writing a commit message, running or reporting quality gates (explicitly listing `go vet`, `go test`, `gosec`, `govulncheck` — include `go test`, which PR #96 omitted), and checking PR review comments before reporting a PR-related task done; plus an explicit skip clause for read-only/pure-exploration/trivial-single-file-no-PR work.
- [ ] 1.2 Body sections: definition of done (test-run requirement scoped to diffs touching `.go`/`go.mod`/`go.sum` — not `.go` files alone, to avoid exempting dependency-only bumps), commit message conventions, OpenSpec + 3-PR sequence, branch protection & required checks, quality gates (including the `#nosec` and `govulncheck` triage policy, with the corrected lowercase `g304.html` link), merge authorization rule, PR review-comment checking, validation before handoff (including the PR number/URL/scope/checks reporting requirement, and a cross-check that this skill still agrees with `docs/development.md`).
- [ ] 1.3 State explicitly in the skill body that it is the compact counterpart of `docs/development.md`, and that the two are drafted together during implementation even though they land in separate PRs (see task groups 3-5).

## 2. Add the `change-lifecycle` skill (implementation PR scope)

- [ ] 2.1 Create `.claude/skills/change-lifecycle/SKILL.md` encoding the roadmap-item sequence: evaluate `/opsx:explore` need (same complexity/ambiguity criteria as `openspec/specs/development-workflow/spec.md`'s "Explore Precedes Propose" requirement) → discuss → `/opsx:propose` → wait for the propose PR to **merge** (not just review approval) → `/opsx:apply` → `/opsx:archive`/finalization.
- [ ] 2.2 At each point where a task or PR must be verified done, have this skill invoke/reference `repo-workflow`'s definition-of-done checklist rather than restating it.
- [ ] 2.3 State explicitly that this skill does not modify or replace the vendored `openspec-{explore,propose,apply-change,archive-change}` skills — it orchestrates when to call them.
- [ ] 2.4 Confirm task groups 1-2 land as one implementation PR containing only the two new skill files — no `docs/`, `CLAUDE.md`, or `AGENTS.md` changes in that PR (per `development-workflow`'s "Implementation PR Contains Only the Change's Declared Scope").

## 3. Consolidate `docs/development.md` as the canonical prose source (finalization PR scope)

- [ ] 3.1 Merge `AGENTS.md`'s currently-unique content (PR review-comment checking via `gh pr view --json reviews` / `gh api .../pulls/{n}/comments`, `resolveReviewThread` guidance, validation/handoff commands) into `docs/development.md`, keeping the existing "Contribution Conventions" section structure.
- [ ] 3.2 Restore the "report PR number, URL, changed-file scope, and check results" requirement in the validation/handoff section (dropped by PR #96 when this section was moved — do not repeat that omission).
- [ ] 3.3 Fix the `gosec` G304 doc-reference casing to lowercase `g304.html` everywhere it appears in `docs/development.md`.
- [ ] 3.4 Add the release-please mechanics detail (single Release PR maintained per push, merging it creates the tag/GitHub Release/`CHANGELOG.md` update) to `docs/development.md` so any pointer to it for "commit/release mechanics" is accurate — cross-check against `docs/operations.md` and `openspec/specs/development-workflow/spec.md` for consistency.
- [ ] 3.5 Update the local test-run requirement text in `docs/development.md` to the Go-module-input carve-out (`.go`/`go.mod`/`go.sum`, not `.go` files alone), matching the spec delta in this change.
- [ ] 3.6 Re-read the finished `docs/development.md` end-to-end and confirm it has no content gaps relative to what's being removed from `CLAUDE.md` and `AGENTS.md` below, and that it still agrees with the `repo-workflow` skill from task group 1.

## 4. Trim `CLAUDE.md` (finalization PR scope)

- [ ] 4.1 Remove the "Development process: OpenSpec is mandatory", "Branch protection", "Quality gates", and "Commit messages and releases" sections' full prose, replacing with a short pointer to `docs/development.md` and the `repo-workflow`/`change-lifecycle` skills.
- [ ] 4.2 Verify every remaining pointer/cross-reference in `CLAUDE.md` (to `docs/development.md`, to the new skills) is accurate against those files' actual final content — do not repeat PR #96's inaccurate-pointer bug.
- [ ] 4.3 Confirm project/app context (architecture, gotchas, language policy) is untouched by this trim.

## 5. Trim `AGENTS.md` (finalization PR scope)

- [ ] 5.1 Reduce `AGENTS.md` to short pointers: `CLAUDE.md` for app context, `docs/development.md` for the full workflow (humans and non-Claude-Code agents), and a note that Claude Code agents specifically get this auto-applied via the `repo-workflow`/`change-lifecycle` skills.
- [ ] 5.2 Do not delete `AGENTS.md`.

## 6. Finalization

- [ ] 6.1 Mark all tasks in this file complete, promote the delta spec into `openspec/specs/development-workflow/spec.md`, move this change folder to `openspec/changes/archive/`.
- [ ] 6.2 Grep the diff for any remaining reference to removed `CLAUDE.md`/`AGENTS.md` sections (stale cross-references) in the rest of the repo.
- [ ] 6.3 Confirm the new skills are discoverable via the skill listing.
- [ ] 6.4 Confirm the implementation PR (task groups 1-2) contained no `docs/`/`CLAUDE.md`/`AGENTS.md` changes, and this finalization PR contains no application code — matching the corrected PR split in `design.md`.
