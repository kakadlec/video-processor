## ADDED Requirements

### Requirement: Propose PR Contains Only Change Artifacts and Must Merge Before Implementation

A propose PR for a change SHALL contain only the artifacts belonging to that change under `openspec/changes/<name>/` (`proposal.md`, `design.md`, `tasks.md`, and any delta specs under `openspec/changes/<name>/specs/`). It SHALL NOT contain application code, test files, CI configuration, `CLAUDE.md`, `AGENTS.md`, README files, or modifications to canonical specs under `openspec/specs/`. The propose PR SHALL be merged before implementation work on the change begins.

#### Scenario: Propose PR with change artifacts only is valid

- **WHEN** a PR modifies only files under `openspec/changes/<name>/`
- **THEN** it is a valid propose PR for that change

#### Scenario: Propose PR containing application code is invalid

- **WHEN** a PR contains both change artifacts and application code or test files
- **THEN** it is NOT a valid propose PR and SHALL be split before implementation proceeds

#### Scenario: Implementation does not start before the propose PR merges

- **WHEN** a propose PR for a change has been opened but not merged
- **THEN** no implementation work for that change SHALL begin

### Requirement: Implementation PR Contains Only Application Code and Tests

An implementation PR SHALL contain only changes to application source files and test files. It SHALL NOT contain task checkoffs or other modifications to `tasks.md`, README or documentation files, `CLAUDE.md` or `AGENTS.md`, configuration files, CI files, spec files, or any other file under `openspec/`.

#### Scenario: Implementation PR with only source and test changes is valid

- **WHEN** a PR modifies only application source files and test files
- **THEN** it is a valid implementation PR

#### Scenario: Implementation PR containing task checkoffs is invalid

- **WHEN** a PR contains application code and also modifies `tasks.md` to check off completed items
- **THEN** it is NOT a valid implementation PR; task checkoffs SHALL be deferred to the finalization/archive PR

#### Scenario: Implementation PR containing project guidance is invalid

- **WHEN** a PR contains application code and also modifies `CLAUDE.md`, `AGENTS.md`, README, docs, configuration, or CI
- **THEN** it is NOT a valid implementation PR; those changes SHALL be moved to a separate non-implementation PR

#### Scenario: Implementation PR containing OpenSpec files is invalid

- **WHEN** a PR contains application code and also modifies any file under `openspec/`
- **THEN** it is NOT a valid implementation PR

### Requirement: Finalization and Archive Occur in One Closure PR

After the implementation PR for a change is merged, an agent SHALL use one finalization/archive PR to mark the change's completed tasks, promote its delta specs into canonical specs, and move the complete change folder from `openspec/changes/<name>/` to `openspec/changes/archive/<date>-<name>/`. This closure PR SHALL contain no application code or tests. Permanent documentation or agent-instruction changes MAY be delivered in a separate docs PR and SHALL NOT be added to the implementation PR.

#### Scenario: Closure PR after implementation contains only closure operations

- **WHEN** the implementation PR is merged and a subsequent PR marks tasks complete, updates canonical specs from the delta, and moves the change folder to archive
- **THEN** it is a valid finalization/archive PR

#### Scenario: Closure PR before implementation merges is premature

- **WHEN** a PR checks off tasks or archives a change whose implementation PR has not merged
- **THEN** that PR SHALL NOT be merged

#### Scenario: Closure PR containing application code is invalid

- **WHEN** a finalization/archive PR modifies application source or test files
- **THEN** it is NOT a valid finalization/archive PR and the code changes SHALL be removed

#### Scenario: Documentation is not silently bundled into implementation

- **WHEN** permanent documentation, agent instructions, or configuration must change as a consequence of implementation
- **THEN** those files SHALL be delivered in a separate non-implementation PR, never bundled with the application code PR

### Requirement: Merge Requires Explicit User Authorization

An agent SHALL NOT merge any pull request, or take any action that causes a pull request to be merged, based on inferred authorization. Merge authorization requires an explicit instruction from the user in the current session and applies only to the designated PR. Completion signals — including all required CI checks passing, completed tasks, absence of blocking comments, or authorization for another PR — do NOT constitute authorization for a different PR.

#### Scenario: All checks pass but no explicit instruction — agent does not merge

- **WHEN** all required CI checks for a PR are passing
- **THEN** the agent SHALL report the ready state and wait for an explicit merge instruction

#### Scenario: Explicit user instruction authorizes a specific merge

- **WHEN** the user explicitly instructs the agent to merge a named or clearly designated PR in the current session
- **THEN** the agent may merge that PR after verifying its required conditions

#### Scenario: Authorization for one PR does not extend to another

- **WHEN** the user authorizes merging one PR in a sequence
- **THEN** the agent SHALL wait for a separate explicit instruction before merging each subsequent PR
