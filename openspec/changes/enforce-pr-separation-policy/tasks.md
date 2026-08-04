## 1. Spec-only — no implementation PR required

- [ ] 1.1 Verify that the propose PR diff contains only files under `openspec/changes/enforce-pr-separation-policy/` (`.openspec.yaml`, `proposal.md`, `design.md`, `tasks.md`, `specs/development-workflow/spec.md`). Confirm no application code, `CLAUDE.md`, CI configuration, or canonical spec files under `openspec/specs/` are modified.

## 2. Finalization/archive

- [ ] 2.1 Open a finalization/archive PR after the proposal is merged: mark the tasks in this change complete, merge the additions from `openspec/changes/enforce-pr-separation-policy/specs/development-workflow/spec.md` into `openspec/specs/development-workflow/spec.md`, then move the entire `openspec/changes/enforce-pr-separation-policy/` folder to `openspec/changes/archive/2026-08-04-enforce-pr-separation-policy/`. The PR must contain only these OpenSpec closure operations and no application code, tests, permanent documentation, agent instructions, or CI changes.
