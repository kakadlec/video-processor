## 1. Consolidate `docs/development.md` as the canonical prose source

- [ ] 1.1 Merge `AGENTS.md`'s currently-unique content (PR review-comment checking via `gh pr view --json reviews` / `gh api .../pulls/{n}/comments`, `resolveReviewThread` guidance, validation/handoff commands) into `docs/development.md`, keeping the existing "Contribution Conventions" section structure.
- [ ] 1.2 Restore the "report PR number, URL, changed-file scope, and check results" requirement in the validation/handoff section (dropped by PR #96 when this section was moved — do not repeat that omission).
- [ ] 1.3 Fix the `gosec` G304 doc-reference casing to lowercase `g304.html` everywhere it appears in `docs/development.md`.
- [ ] 1.4 Add the release-please mechanics detail (single Release PR maintained per push, merging it creates the tag/GitHub Release/`CHANGELOG.md` update) to `docs/development.md` so any pointer to it for "commit/release mechanics" is accurate — cross-check against `docs/operations.md` and `openspec/specs/development-workflow/spec.md` for consistency.
- [ ] 1.5 Add the `.go`-file carve-out to the local test-run requirement text in `docs/development.md`, matching the spec delta in this change.
- [ ] 1.6 Re-read the finished `docs/development.md` end-to-end and confirm it has no content gaps relative to what's being removed from `CLAUDE.md` and `AGENTS.md` in the tasks below.

## 2. Trim `CLAUDE.md`

- [ ] 2.1 Remove the "Development process: OpenSpec is mandatory", "Branch protection", "Quality gates", and "Commit messages and releases" sections' full prose, replacing with a short pointer to `docs/development.md` and the `repo-workflow`/`change-lifecycle` skills.
- [ ] 2.2 Verify every remaining pointer/cross-reference in `CLAUDE.md` (to `docs/development.md`, to the new skills) is accurate against those files' actual final content — do not repeat PR #96's inaccurate-pointer bug.
- [ ] 2.3 Confirm project/app context (architecture, gotchas, language policy) is untouched by this trim.

## 3. Trim `AGENTS.md`

- [ ] 3.1 Reduce `AGENTS.md` to short pointers: `CLAUDE.md` for app context, `docs/development.md` for the full workflow (humans and non-Claude-Code agents), and a note that Claude Code agents specifically get this auto-applied via the `repo-workflow`/`change-lifecycle` skills.
- [ ] 3.2 Do not delete `AGENTS.md`.

## 4. Add the `repo-workflow` skill

- [ ] 4.1 Create `.claude/skills/repo-workflow/SKILL.md` with frontmatter `description` covering: opening/merging a PR, marking any task/change/OpenSpec change complete, writing a commit message, running or reporting quality gates (explicitly listing `go vet`, `go test`, `gosec`, `govulncheck` — include `go test`, which PR #96 omitted), and checking PR review comments before reporting a PR-related task done; plus an explicit skip clause for read-only/pure-exploration/trivial-single-file-no-PR work.
- [ ] 4.2 Body sections: definition of done (with the `.go`-file carve-out for the test-run requirement), commit message conventions, OpenSpec + 3-PR sequence, branch protection & required checks, quality gates (including the `#nosec` and `govulncheck` triage policy, with the corrected lowercase `g304.html` link), merge authorization rule, PR review-comment checking, validation before handoff (including the PR number/URL/scope/checks reporting requirement).
- [ ] 4.3 State explicitly in the skill body that it is the compact counterpart of `docs/development.md` and that future policy changes must update both files in the same PR.

## 5. Add the `change-lifecycle` skill

- [ ] 5.1 Create `.claude/skills/change-lifecycle/SKILL.md` encoding the roadmap-item sequence: evaluate `/opsx:explore` need (same complexity/ambiguity criteria as `openspec/specs/development-workflow/spec.md`'s "Explore Precedes Propose" requirement) → discuss → `/opsx:propose` → wait for the propose PR to merge → `/opsx:apply` → `/opsx:archive`/finalization.
- [ ] 5.2 At each point where a task or PR must be verified done, have this skill invoke/reference `repo-workflow`'s definition-of-done checklist rather than restating it.
- [ ] 5.3 State explicitly that this skill does not modify or replace the vendored `openspec-{explore,propose,apply-change,archive-change}` skills — it orchestrates when to call them.

## 6. Cross-checks

- [ ] 6.1 Grep the diff for any remaining reference to removed `CLAUDE.md`/`AGENTS.md` sections (stale cross-references) in the rest of the repo.
- [ ] 6.2 Confirm the new skills are discoverable via the skill listing.
- [ ] 6.3 Confirm this change's own diff contains no application code — docs/config/skill files only, matching the proposal's declared scope.
