## 1. Implementation PR — skill files (configuration/infrastructure scope)

- [ ] 1.1 In `.claude/skills/repo-workflow/SKILL.md`'s "OpenSpec + PR sequence" finalization-PR bullet, make "flipping the change's `docs/roadmap.md` Change Backlog row to `archived`" conditional on the change having a row (workflow/process-only changes don't)
- [ ] 1.2 In `.claude/skills/change-lifecycle/SKILL.md`, update the sequence diagram's "roadmap status" step and Step 5's numbered finalization list (item 3, "Flip the change's `docs/roadmap.md` Change Backlog row to `archived`...") to the same conditional
- [ ] 1.3 Grep the repo for other literal restatements of "Change Backlog row to `archived`" (or close variants) to confirm nothing outside the four files already identified needs the same edit

## 2. Finalization PR — documentation, spec promotion, archive

- [ ] 2.1 Remove `docs/roadmap.md`'s "How to use this" section's procedural bullets (explore-before-propose pointer, backlog-row-before-propose requirement, status-flip mechanics, "one change = one spec delta" lesson)
- [ ] 2.2 Replace the removed section with a short pointer to `openspec/specs/development-workflow/spec.md` for process rules
- [ ] 2.3 Confirm the Phase Summary table, Change Backlog tables, and "Current State" section are otherwise unchanged — no re-scoping of existing rows, and the existing `require-explore-before-propose` row is left untouched
- [ ] 2.4 In `docs/development.md`'s "PR Separation Rule" finalization-PR bullet, apply the same conditional as tasks 1.1/1.2 to its restatement of the roadmap-flip mechanic
- [ ] 2.5 Merge this change's `specs/development-workflow/spec.md` delta (two ADDED requirements: "Roadmap Change Backlog Is Scoped To Product/Architecture Work", "One Change Equals One Coherent Spec Delta"; one MODIFIED requirement: "Finalization PR Bundles Archive, Documentation, and Roadmap Status") into `openspec/specs/development-workflow/spec.md`
- [ ] 2.6 Run `openspec validate scope-roadmap-to-macro-plan-only --strict --no-interactive` before archiving to confirm the delta is well-formed
- [ ] 2.7 Check off all tasks in this file
- [ ] 2.8 Move `openspec/changes/scope-roadmap-to-macro-plan-only/` to `openspec/changes/archive/<date>-scope-roadmap-to-macro-plan-only/`
- [ ] 2.9 Confirm no `docs/roadmap.md` Change Backlog row is added for this change itself (per its own scope decision — workflow/process-only)
