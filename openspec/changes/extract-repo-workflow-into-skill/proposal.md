## Why

The `development-workflow` spec's rules (PR sequence, branch protection, quality gates, commit/release conventions, merge authorization) are restated in full prose three times — in `CLAUDE.md` (always loaded into every Claude Code conversation, regardless of task), `AGENTS.md`, and `docs/development.md`. This makes even a trivial, direct task pay the cost of ~40KB of always-loaded process prose before reaching any project context, and the three independently-maintained copies have already drifted (a same-day draft PR, #96, introduced a broken doc link in one copy, an inaccurate cross-reference in another, and exposed that the spec's own "tests must pass before done" requirement is unconditional even for diffs that touch no Go code — a self-contradiction Copilot's review caught). Consolidating to one canonical prose source plus a Claude-Code-specific skill that only engages when the task shape actually calls for it (opening/merging a PR, wrapping a change, running quality gates) fixes both the drift risk and the "always in the way" friction, without weakening any rule.

## What Changes

- `docs/development.md` becomes the single canonical prose source of these rules for humans and non-Claude-Code agents. It gains the content currently unique to `AGENTS.md` (PR review-comment checking, validation/handoff commands) and is corrected/completed so it has no gaps relative to what's removed elsewhere (including fixing the broken `gosec` G304 doc-link casing surfaced by PR #96's review).
- `CLAUDE.md` is trimmed to project/app context (what the app is, architecture, gotchas, language policy) plus short pointers to `docs/development.md` and the new skill below. The full PR-sequence/quality-gate/commit-convention prose currently duplicated there is removed, not restated elsewhere in the file.
- `AGENTS.md` is kept (not deleted) and trimmed to short pointers, explicitly framed as the reference path for non-Claude-Code agents and humans, since the new skill below only auto-engages for Claude Code.
- A new Claude Code skill (or small set of skills) is added under `.claude/skills/`, distinct from and not modifying the existing vendored `openspec-{explore,propose,apply-change,archive-change}` skills. It carries the compact, agent-actionable version of the workflow rules and auto-triggers only for matching task shapes (PR open/merge, wrapping up a change, writing a commit, running/reporting quality gates, checking PR review comments before declaring a task done) — including `go test`, which a same-day prior draft (PR #96) omitted from its own trigger list. It also covers the roadmap-item lifecycle (explore when complex/ambiguous → discuss → propose → wait for propose-PR approval → apply → verify CI and review comments before calling a task done → archive/finalize), which no existing document or skill currently encodes end to end.
- **Spec correction**: `development-workflow`'s "Change Completion Requires A Passing Test Run" requirement currently mandates `go test ./...` before any change is reported complete, with no carve-out for changes that touch no Go source — this is the requirement PR #96's own test plan silently violated. Narrow it to apply only when the change's diff includes `.go` files.
- Does not touch, reference, or depend on PR #96 (`docs/extract-workflow-into-skill`) — that PR is left open and untouched; this change supersedes it with a version that itself follows the propose → implementation → finalization sequence it defines.
- No Change Backlog row is added to `docs/roadmap.md` for this change — by explicit user decision, since this change is about working process, not `docs/roadmap.md`'s DDD-architecture-evolution scope, and backlog curation for this class of change was itself adding friction.

## Capabilities

### New Capabilities
(none)

### Modified Capabilities
- `development-workflow`: adds a requirement that Claude-Code-facing workflow guidance is delivered via an auto-triggered skill rather than duplicated as always-loaded prose in `CLAUDE.md`, with defined trigger coverage (including `go test`) and explicit skip conditions for read-only/exploration/trivial-single-file work; adds a requirement covering the roadmap-item lifecycle sequencing (explore-if-complex → propose → PR approval gate → apply → done-verification gate → archive); narrows the existing "Change Completion Requires A Passing Test Run" requirement to apply only to diffs that include `.go` files.

## Impact

- Affected files: `CLAUDE.md`, `AGENTS.md`, `docs/development.md`, new file(s) under `.claude/skills/` (new directory, name TBD in design), `openspec/specs/development-workflow/spec.md` (via this change's delta).
- Not affected: application code (`main.go`, `identity.go`, `*_test.go`), CI workflow files, `docker-compose.yml`, `docs/roadmap.md`.
- Existing vendored skills `.claude/skills/openspec-*` are explicitly out of scope — not modified.
