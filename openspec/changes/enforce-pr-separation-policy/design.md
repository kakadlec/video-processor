## Context

The `development-workflow` spec already mandates that changes land via PR and that required status checks must pass before merging. What it does not specify is the internal structure of a multi-PR change sequence: which PR may contain which files, in which order, and under what conditions an agent may proceed with a merge. `CLAUDE.md` contains informal prose describing a 3-PR sequence, but prose is not machine-checkable — it does not use SHALL/SHALL NOT language and provides no scenarios an agent can verify against its own plan. Two gaps stand out: (1) task-tracking checkoffs and documentation updates are bundled into the implementation PR in the current description, polluting code review diffs with housekeeping noise; (2) merge authorization is not addressed, leaving agents free to infer permission from completion signals. This change closes both gaps as proper spec requirements.

## Goals / Non-Goals

**Goals:**
- Formally specify the 4-PR delivery sequence (propose → implement → tracking/docs → archive) as `development-workflow` requirements with SHALL/SHALL NOT language and strict scenarios.
- Enumerate what each PR type may and may not contain, so deviations are unambiguous violations, not judgment calls.
- Specify that merge authorization requires an explicit user instruction in the current session; no agent may merge based on inferred permission.

**Non-Goals:**
- Modifying `CLAUDE.md`, `AGENTS.md`, `README`, CI configuration, application code, or canonical specs under `openspec/specs/`.
- Adding automated tooling to enforce the sequence (the requirements are policy constraints, not GitHub Actions gates).
- Changing any requirement already present in the `development-workflow` spec.
- Specifying what counts as a "trivial" change or when steps may be skipped — only the policy for non-trivial, multi-PR changes is in scope.

## Decisions

**4-PR sequence, not 3.**
The existing `CLAUDE.md` prose bundles task-tracking checkoffs into the implementation PR. Extracting those and any documentation/configuration updates into a dedicated tracking/docs PR means code reviewers see only code diffs in the implementation PR. The tracking/docs PR is a natural post-implementation record: it confirms what was done (tasks checked off) and documents any resulting configuration changes. The 4 steps are propose → implement → tracking/docs → archive.

**Explicit exclusion list for the implementation PR.**
"Code and tests only" is ambiguous in practice. The spec names what the implementation PR must not contain: `tasks.md` checkoffs, README or documentation files, `CLAUDE.md` or `AGENTS.md`, CI configuration, spec files under `openspec/`. Naming exclusions explicitly removes ambiguity that a short positive description would leave open.

**Propose PR must be merged — not merely opened — before implementation begins.**
This is the intent in the current prose but is not expressed as a testable scenario. Requiring the merge (not just the opening) prevents an agent from starting implementation work on a speculative proposal that may still change after review.

**Merge authorization as an explicit spec requirement.**
An agent completing a task has access to signals that resemble implicit permission: all CI checks green, all tasks checked off, no blocking comments, review absence. Without an explicit rule, an agent following only the existing spec has no reason to pause before merging. The new requirement states that merge authorization requires an explicit user instruction in the current session, and that no combination of completion signals constitutes that authorization.

**Tracking/docs PR after implementation, before archive.**
Task checkoffs are a record of what the implementation PR accomplished; they belong after the code lands, not before. Similarly, any `CLAUDE.md` or configuration update that documents the new behavior is a post-implementation concern. Archive is last because it folds the delta into canonical specs and closes out the change — it should see the complete, checked-off state of the change.

## Risks / Trade-offs

- [4 PRs instead of 3 is more overhead per change] → For spec-only changes (like this one), the implementation PR and tracking/docs PR are vacuous and may be skipped. The spec does not require steps that produce no diff. The overhead applies only when there is actual code to write.
- [Policy without automated enforcement relies on agent discipline] → Accepted. Adding automated checks (e.g., a PR title or label convention enforced in CI) would require CI changes, which are out of scope. The scenarios define what is expected; the propose PR review and the archive PR are the natural audit points.
- [An agent may ask "is this trivial enough to skip steps?" and make the wrong call] → The scope of this change is the policy itself, not the definition of "trivial." That judgment is left to the agent and user in context. When in doubt, use the full sequence.
