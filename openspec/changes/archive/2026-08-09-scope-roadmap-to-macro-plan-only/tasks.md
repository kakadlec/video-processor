## 1. Implementation PR — skill files (configuration/infrastructure scope)

- [x] 1.1 In `.claude/skills/repo-workflow/SKILL.md`'s "OpenSpec + PR sequence" finalization-PR bullet, make "flipping the change's `docs/roadmap.md` Change Backlog row to `archived`" conditional on the change having a row (workflow/process-only changes don't)
- [x] 1.2 In `.claude/skills/change-lifecycle/SKILL.md`, update the sequence diagram's "roadmap status" step and Step 5's numbered finalization list (item 3, "Flip the change's `docs/roadmap.md` Change Backlog row to `archived`...") to the same conditional
- [x] 1.3 In `.claude/skills/change-lifecycle/SKILL.md`'s Step 1 ("decide if `/opsx:explore` is warranted"), generalize the "criteria... the row's description doesn't already settle" / "judgment call made when picking up the row" language so it still applies to a workflow/process-only change with no backlog row — mirror the widened wording in the canonical `development-workflow` spec's "Explore Precedes Propose For Complex Or Ambiguous Changes" requirement
- [x] 1.4 Grep the repo for other literal restatements of "Change Backlog row to `archived`" and of the explore-criteria "row's description"/"picking up the row" phrasing, to confirm nothing outside the files already identified needs the same edits

## 2. Finalization PR — documentation, spec promotion, archive

- [x] 2.1 Remove `docs/roadmap.md`'s "How to use this" section's procedural bullets (explore-before-propose pointer, backlog-row-before-propose requirement, status-flip mechanics, "one change = one spec delta" lesson)
- [x] 2.2 Replace the removed section with a short pointer to `openspec/specs/development-workflow/spec.md` for process rules
- [x] 2.3 Update the canonical-source disclaimer blockquote near the top of `docs/roadmap.md` — it currently says the Change Backlog is "updated directly here as changes are proposed and archived"; drop "proposed" to match the two remaining touch points (added/re-scoped, archived)
- [x] 2.4 Update the Change Backlog section's opening line — it currently calls itself "the single source of truth for what OpenSpec change comes next" with no scope qualifier; narrow it to product/architecture-scope work, consistent with the new "Roadmap Change Backlog Is Scoped To Product/Architecture Work" requirement
- [x] 2.5 Confirm the Phase Summary table, Change Backlog tables, and "Current State" section are otherwise unchanged — no re-scoping of existing rows, and the existing `require-explore-before-propose` row is left untouched
- [x] 2.6 In `docs/development.md`'s "PR Separation Rule" finalization-PR bullet, apply the same conditional as tasks 1.1/1.2 to its restatement of the roadmap-flip mechanic
- [x] 2.7 Merge this change's `specs/development-workflow/spec.md` delta (two ADDED requirements: "Roadmap Change Backlog Is Scoped To Product/Architecture Work", "One Change Equals One Coherent Spec Delta"; two MODIFIED requirements: "Finalization PR Bundles Archive, Documentation, and Roadmap Status", "Explore Precedes Propose For Complex Or Ambiguous Changes") into `openspec/specs/development-workflow/spec.md`
- [x] 2.8 Run `openspec validate scope-roadmap-to-macro-plan-only --strict --no-interactive` before archiving to confirm the delta is well-formed
- [x] 2.9 Check off all tasks in this file
- [x] 2.10 Move `openspec/changes/scope-roadmap-to-macro-plan-only/` to `openspec/changes/archive/<date>-scope-roadmap-to-macro-plan-only/`
- [x] 2.11 Confirm no `docs/roadmap.md` Change Backlog row is added for this change itself (per its own scope decision — workflow/process-only)
