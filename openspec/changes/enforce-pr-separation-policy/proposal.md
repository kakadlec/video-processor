## Why

`CLAUDE.md` describes the OpenSpec workflow informally, but it does not provide a binding rule that every agent can apply consistently. In particular, the implementation PR must remain reviewable as a code-only change: task checkoffs, documentation, agent instructions, configuration, and OpenSpec bookkeeping do not belong beside application code. The repository also needs an explicit rule preventing agents from inferring merge authorization from green checks or other completion signals.

This change formalizes the delivery sequence as a scenario-driven requirement in the `development-workflow` spec. It keeps task completion and archive operations together in one finalization/archive PR after implementation has merged.

## What Changes

- `openspec/changes/enforce-pr-separation-policy/specs/development-workflow/spec.md`: adds requirements specifying:
  1. A propose PR must contain only change artifacts and must be merged before implementation begins.
  2. An implementation PR must contain only application code and tests, with an explicit exclusion list.
  3. A finalization/archive PR follows the implementation PR and combines completed task checkoffs with canonical spec updates and moving the change folder to archive; it contains no application code.
  4. Merge authorization must be an explicit instruction from the user in the current session, scoped to the designated PR.

## Capabilities

### Modified Capabilities

- `development-workflow`: formalizes the PR delivery sequence and merge authorization policy for all agents.

## Impact

- Spec-only change. No application code, CI configuration, `CLAUDE.md`, `AGENTS.md`, `README`, or files outside `openspec/changes/enforce-pr-separation-policy/` are modified.
- When agents follow this spec, implementation diffs contain only code/tests, finalization is auditable in one OpenSpec-only PR, and merges occur only after explicit user authorization.
