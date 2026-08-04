## Why

`CLAUDE.md` describes a 3-PR sequence for changes in informal prose. Two gaps remain as a result. First, task-tracking checkoffs and documentation/configuration updates are bundled into the implementation PR alongside code diffs, making code reviews harder to scan: reviewers must mentally separate application changes from housekeeping. Second, merge authorization is not addressed anywhere in the spec — an agent that has just finished an implementation and sees all checks green has no explicit rule prohibiting it from merging, and may infer permission from completion signals instead of waiting for an explicit user instruction.

This change formalizes the PR delivery sequence as a binding, scenario-driven requirement in the `development-workflow` spec, binding all agents working in this repository. It also extends the sequence from 3 to 4 PRs by extracting task-tracking and documentation updates into their own dedicated step between implementation and archive.

## What Changes

- `openspec/changes/enforce-pr-separation-policy/specs/development-workflow/spec.md`: adds five new requirements specifying:
  1. A propose PR must contain only change artifacts and must be merged before implementation begins.
  2. An implementation PR must contain only application code and tests, with an explicit list of what it must not include.
  3. A tracking/docs PR, opened after the implementation PR merges, carries completed task checkoffs and any documentation or configuration updates.
  4. An archive PR, opened after the tracking/docs PR merges, carries only the OpenSpec archive operations (delta merge and folder move).
  5. Merge authorization must be an explicit instruction from the user in the current session; agents must not infer it from any completion signal.

## Capabilities

### Modified Capabilities

- `development-workflow`: adds requirements that formalize the PR delivery sequence and the merge authorization policy for all agents.

## Impact

- Spec-only change. No application code, CI configuration, `CLAUDE.md`, `AGENTS.md`, `README`, or files outside `openspec/changes/enforce-pr-separation-policy/` are modified.
- When agents follow this spec, code review diffs are clean (code PRs contain only code; tracking PRs contain only task and doc diffs) and PRs are merged only when the user explicitly says so.
