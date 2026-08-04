## Context

The `development-workflow` spec already mandates that changes land via PR and that required status checks pass before merging. It does not yet specify which files belong in each PR of a multi-step change, the required order, or when an agent may merge. Existing repository prose also allowed task-tracking updates in the implementation PR, which makes code review less precise.

This change turns the workflow into auditable requirements that any agent can apply from the repository itself.

## Goals / Non-Goals

**Goals:**
- Specify the delivery sequence as propose → implement → finalization/archive.
- Require the propose PR to contain only the new change artifacts and to merge before implementation starts.
- Require the implementation PR to contain only application source and test changes.
- Combine task checkoffs, canonical spec promotion, and moving the change folder into one finalization/archive PR after implementation merges.
- Require explicit user authorization before merging each designated PR.

**Non-Goals:**
- Modifying `CLAUDE.md`, `AGENTS.md`, `README`, CI configuration, application code, or canonical specs under `openspec/specs/`.
- Adding automated tooling to enforce the sequence.
- Defining which changes are trivial enough to skip the full workflow.

## Decisions

**Three-step sequence, with finalization and archive combined.**
The repository uses three PR roles for a non-trivial change:
1. Propose: only the change folder under `openspec/changes/<name>/`.
2. Implement: only application code and tests.
3. Finalize/archive: task checkoffs, canonical spec promotion, and moving the completed change folder to `openspec/changes/archive/`.

A documentation/configuration PR may be opened separately when permanent project documentation or agent instructions must change, but that is not a reason to put those files in the implementation PR.

**Explicit exclusion list for the implementation PR.**
"Code and tests only" is ambiguous in practice. The requirement names task files, documentation, agent instructions, configuration, CI, and OpenSpec files as forbidden in the implementation PR.

**Propose PR must merge before implementation begins.**
This prevents implementation from starting against a proposal that is still under review or may change.

**Finalization/archive is one PR.**
The task checkoffs record what the merged implementation delivered, while canonical spec promotion and folder movement close the change. Keeping these operations together avoids an unnecessary extra PR and leaves one auditable closure point.

**Merge authorization is explicit and per PR.**
Green checks, completed tasks, lack of comments, or prior authorization for another PR never count as authorization. The user must explicitly authorize the designated PR in the current session.

## Risks / Trade-offs

- [The finalization/archive PR mixes task bookkeeping with OpenSpec archive operations] → Accepted because both are change-closure operations, contain no application code, and are reviewed together.
- [Policy without automated enforcement relies on agent discipline] → Accepted for now. The explicit file-scope requirements and PR review provide the repository-level audit trail.
- [Agents may misclassify trivial work] → When in doubt, use the full workflow; the definition of trivial work is outside this change.
