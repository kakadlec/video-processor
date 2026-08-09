## Context

`openspec/specs/development-workflow/spec.md` is the canonical, normative source for this repo's PR-sequence, branch-protection, quality-gate, commit-convention, and merge-authorization rules. `CLAUDE.md`, `AGENTS.md`, and `docs/development.md` each independently restate a large subset of that spec in full prose. `CLAUDE.md` is always injected into every Claude Code conversation regardless of task size, so a one-line fix pays the same ~40 lines of process prose as a multi-file feature. The three prose copies have already drifted: a same-day draft (PR #96, left untouched by this change) introduced a case-sensitive broken `gosec` doc link, an inaccurate cross-reference from `CLAUDE.md` to `docs/development.md`, and exposed that "Change Completion Requires A Passing Test Run" is worded as unconditional even for diffs touching no Go file — its own test plan visibly contradicted the rule it was introducing, and Copilot's review caught it. PR #96 also never went through OpenSpec or the 3-PR sequence itself, despite being exactly the kind of non-trivial workflow/infrastructure change those rules are meant to cover.

## Goals / Non-Goals

**Goals:**
- One canonical prose source (`docs/development.md`) for these rules, with `CLAUDE.md`/`AGENTS.md` reduced to pointers.
- A Claude-Code-specific mechanism that surfaces the compact, actionable version of these rules only when the current task shape calls for it, not on every turn.
- Close the lifecycle gap: no existing document or skill currently encodes the roadmap-item sequence (explore if complex → discuss → propose → wait for propose-PR approval → apply → verify CI/review comments before calling a task done → archive/finalize) end to end.
- Fix the specific bugs PR #96 introduced/exposed (broken link, inaccurate pointer, missing `go test` in trigger scope, unconditional test-completion requirement) so they aren't silently reintroduced.
- This change itself follows the propose → implementation → finalization sequence it defines — no shortcutting, unlike PR #96.

**Non-Goals:**
- Not modifying the existing vendored `openspec-{explore,propose,apply-change,archive-change}` skills.
- Not adding new slash commands for the new skill(s) in this change — auto-trigger (via the skill's `description` matching the task) and explicit by-name invocation are sufficient for v1; commands can be added later if that proves insufficient.
- Not changing any actual policy substance (PR roles, branch protection, quality gates, merge authorization) — only how it's delivered/surfaced and the one narrow correction to the test-completion requirement described below.
- Not adding a `docs/roadmap.md` Change Backlog row for this change (explicit user decision — out of scope for that document).
- Not touching, closing, or building on PR #96.

## Decisions

### One skill vs. two: **two skills**

- **`repo-workflow`**: the compact "is this PR/commit/change actually done and compliant" checklist — commit conventions, the 3-PR role rules, branch protection, quality gates (`go vet`/`go test`/`gosec`/`govulncheck`, with the `#nosec` and `govulncheck` triage policy), merge authorization, and PR review-comment checking. This is the same shape PR #96 attempted, corrected for its bugs.
- **`change-lifecycle`**: orchestrates the roadmap-item sequence — decide if `/opsx:explore` is warranted (complexity/ambiguity criteria, same as the existing `development-workflow` spec requirement) → discuss → `/opsx:propose` → **stop and wait** for the propose PR to be reviewed/merged before any implementation work starts → `/opsx:apply` → before reporting any task or the change done, invoke `repo-workflow`'s definition-of-done checklist → `/opsx:archive` / finalization PR.

Rationale for splitting rather than one file (PR #96's approach): the "is this done" check recurs at multiple points in `change-lifecycle` (after the propose PR, after the implementation PR, after the finalization PR) and is also useful standalone ("is this PR ready?") outside any lifecycle context. Folding it into one large skill either means restating the check three times inside that file (recreating the exact duplication problem this change exists to fix) or means `change-lifecycle` is the only entry point, which loses the standalone use case. `change-lifecycle` references `repo-workflow` by name rather than restating its content.

**Alternative considered**: single `repo-workflow` skill covering both concerns (PR #96's shape). Rejected for the reasons above — it also doesn't naturally cover "wait for propose-PR approval before applying," which is a lifecycle-sequencing concern, not a done-ness check.

### Content ownership boundaries

| Location | Content | Loaded |
|---|---|---|
| `openspec/specs/development-workflow/spec.md` | Normative source of truth | On demand (OpenSpec tooling) |
| `docs/development.md` | Full prose, for humans and non-Claude-Code agents | On demand (explicit read) |
| `CLAUDE.md` | App/project context + 2-3 line pointer | Always (every conversation) |
| `AGENTS.md` | Short pointer only, framed as the non-Claude-Code fallback | On demand |
| `.claude/skills/repo-workflow/SKILL.md`, `.claude/skills/change-lifecycle/SKILL.md` | Compact, agent-actionable version | On demand (auto-trigger or by name) |

The skills are a distilled, differently-shaped version of `docs/development.md` (checklist/trigger format vs. reference prose), not a byte-identical copy — some duplication of facts (rule thresholds, commands, links) is unavoidable across the two formats. Mitigation: both are authored/reviewed together in this change's implementation PR, and any future policy change must touch both in the same PR (documented as a rule inside `repo-workflow` itself).

### Fixing PR #96's specific bugs

- `repo-workflow`'s frontmatter trigger list explicitly includes `go test` alongside `go vet`/`gosec`/`govulncheck` (PR #96 omitted it in 4 places).
- Any `gosec` G304 doc reference uses the correct lowercase, case-sensitive path (`g304.html`), matching the existing correct usage in `openspec/changes/archive/2026-07-28-fix-gosec-and-dependabot-findings/design.md`.
- `CLAUDE.md`'s pointer to `docs/development.md` is verified against that file's actual contents at write time — no claim of coverage that isn't true (PR #96 claimed release-please mechanics lived there when they didn't).
- `docs/development.md` retains the "report PR number, URL, changed-file scope, and check results" requirement that PR #96 accidentally dropped when moving the validation/handoff section.

### Test-completion requirement gets a Go-file carve-out

`development-workflow`'s "Change Completion Requires A Passing Test Run" requirement is modified (delta spec, `MODIFIED Requirements`) to require `go test ./...` only when the change's diff includes `.go` files. A docs/config-only change (like this one) has no Go tests to run and should not report a false/misleading test-completion claim. Determining "does the diff include `.go` files" is a normal `git diff --name-only` judgment call by the agent — no new tooling needed.

### 3-PR shape confirmed

This change's own subject is configuration/workflow (docs + a new skill file), so per the existing `development-workflow` spec ("Implementation PR Contains Only the Change's Declared Scope"), the implementation PR contains exactly the files named in this proposal's Impact section (`CLAUDE.md`, `AGENTS.md`, `docs/development.md`, the new skill file(s)) — not a 2-PR shortcut. Standard propose → implementation → finalization applies.

## Risks / Trade-offs

- **Skill/doc drift returns over time** → Mitigated by the explicit same-PR-update rule stated inside `repo-workflow` itself, but this is a process convention, not an automated check — residual risk accepted.
- **Trigger under-fires** (skill doesn't engage when it should, recreating silent rule violations like PR #96's) → Mitigated by explicit, broad trigger phrasing including `go test`; residual risk accepted since trigger matching is inherently judgment-based, not mechanical.
- **Trigger over-fires** (skill engages for genuinely simple/direct tasks, recreating the original friction) → Mitigated by an explicit "skip for read-only/pure-exploration/trivial single-file edit" clause in the trigger description, carried over from PR #96's (correct) pattern.
- **`AGENTS.md` becomes an unmaintained pointer** since only Claude Code is in use today → Accepted; kept minimal specifically so staleness cost is low, revisit if/when another agent is adopted.

## Migration Plan

No runtime/deployment migration — docs and agent-config only. Land as: propose PR (this change's artifacts) → implementation PR (`CLAUDE.md`, `AGENTS.md`, `docs/development.md`, new skill files) → finalization PR (archive + any doc touch-ups discovered during implementation). PR #96 is left open and untouched throughout; not part of this migration.

## Open Questions

- Whether to eventually add explicit `/`-commands for `repo-workflow`/`change-lifecycle` (deferred as a non-goal above) — revisit if auto-trigger/by-name invocation proves insufficient in practice.
- What (if anything) happens to PR #96 once this change ships — left for the user to decide at that time; not this change's concern.
