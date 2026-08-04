## ADDED Requirements

### Requirement: Propose PR Contains Only Change Artifacts and Must Merge Before Implementation

A propose PR for a change SHALL contain only the artifacts belonging to that change under `openspec/changes/<name>/` (`proposal.md`, `design.md`, `tasks.md`, and any delta specs under `openspec/changes/<name>/specs/`). It SHALL NOT contain application code, test files, CI configuration, `CLAUDE.md`, `AGENTS.md`, `README` files, or any modifications to canonical specs under `openspec/specs/`. The propose PR SHALL be merged before any implementation work on the change begins.

#### Scenario: Propose PR with change artifacts only is valid

- **WHEN** a PR modifies only files under `openspec/changes/<name>/`
- **THEN** it is a valid propose PR for that change

#### Scenario: Propose PR containing application code is invalid

- **WHEN** a PR contains both `openspec/changes/<name>/` artifacts and changes to application code or test files
- **THEN** it is NOT a valid propose PR and SHALL be split so that application code appears in a separate implementation PR

#### Scenario: Implementation does not start before the propose PR merges

- **WHEN** a propose PR for a change has been opened but not yet merged
- **THEN** no implementation work (code changes, test changes, configuration changes) SHALL be started for that change

### Requirement: Implementation PR Contains Only Application Code and Tests

An implementation PR SHALL contain only changes to application source files and test files. It SHALL NOT contain any of the following: checkoffs or other modifications to `tasks.md`; changes to `README`, documentation, or other non-code files; changes to `CLAUDE.md` or `AGENTS.md`; changes to CI configuration; changes to spec files or any file under `openspec/`.

#### Scenario: Implementation PR with only source and test changes is valid

- **WHEN** a PR modifies only Go source files (e.g., `main.go`) and test files (e.g., `main_test.go`)
- **THEN** it is a valid implementation PR

#### Scenario: Implementation PR containing task checkoffs is invalid

- **WHEN** a PR contains changes to application code and also modifies `tasks.md` to check off completed items
- **THEN** it is NOT a valid implementation PR; the `tasks.md` checkoffs SHALL be removed and deferred to the tracking/docs PR

#### Scenario: Implementation PR containing a CLAUDE.md update is invalid

- **WHEN** a PR contains changes to application code and also modifies `CLAUDE.md`
- **THEN** it is NOT a valid implementation PR; the `CLAUDE.md` changes SHALL be removed and deferred to the tracking/docs PR

#### Scenario: Implementation PR containing spec changes is invalid

- **WHEN** a PR contains changes to application code and also modifies any file under `openspec/`
- **THEN** it is NOT a valid implementation PR; the `openspec/` changes SHALL be removed

### Requirement: Tracking and Documentation PR Follows Implementation

A tracking/docs PR SHALL be opened only after the implementation PR for the same change has been merged. It SHALL contain: completed task checkoffs in `tasks.md` (marking items done by the implementation PR), and any documentation or configuration updates (`CLAUDE.md`, `AGENTS.md`, `README`, config files) that document the completed implementation. It SHALL NOT contain application code changes or test changes.

#### Scenario: Tracking/docs PR opened before implementation PR merges is premature

- **WHEN** a PR modifies `tasks.md` checkoffs for a change whose implementation PR has not yet merged
- **THEN** that PR SHALL NOT be merged; it SHALL wait until the implementation PR is merged first

#### Scenario: Valid tracking/docs PR contains only task checkoffs and documentation

- **WHEN** a PR marks tasks as complete in `tasks.md` and updates `CLAUDE.md` to document a behavior introduced by the implementation PR, and contains no application code changes
- **THEN** it is a valid tracking/docs PR

#### Scenario: Tracking/docs PR containing code changes is invalid

- **WHEN** a PR modifies `tasks.md` and also modifies `main.go`
- **THEN** it is NOT a valid tracking/docs PR; the code changes SHALL be removed and placed in the implementation PR

### Requirement: Archive PR Contains Only OpenSpec Archive Operations

An archive PR SHALL be opened only after the tracking/docs PR for the same change has been merged. It SHALL contain only: (a) merging the delta spec additions from `openspec/changes/<name>/specs/` into `openspec/specs/`, and (b) moving the change folder from `openspec/changes/<name>/` to `openspec/changes/archive/<date>-<name>/`. It SHALL NOT contain application code, test files, documentation changes, CI configuration changes, or task updates.

#### Scenario: Valid archive PR contains only OpenSpec archive operations

- **WHEN** a PR modifies only files under `openspec/specs/` (adding content from the delta) and moves `openspec/changes/<name>/` to `openspec/changes/archive/`
- **THEN** it is a valid archive PR

#### Scenario: Archive PR opened before tracking/docs PR merges is premature

- **WHEN** a PR performs archive operations for a change whose tracking/docs PR has not yet merged
- **THEN** that PR SHALL NOT be merged; it SHALL wait until the tracking/docs PR is merged first

#### Scenario: Archive PR containing a CLAUDE.md update is invalid

- **WHEN** a PR performs archive operations and also modifies `CLAUDE.md`
- **THEN** it is NOT a valid archive PR; the `CLAUDE.md` changes SHALL be removed and placed in the tracking/docs PR

### Requirement: Merge Requires Explicit User Authorization

An agent SHALL NOT merge any pull request, or take any action that causes a pull request to be merged, based on inferred authorization. Merge authorization requires an explicit instruction from the user in the current session. Completion signals — including all required CI checks passing, all `tasks.md` items checked off, absence of blocking comments, or any other implicit indicator — do NOT constitute merge authorization.

#### Scenario: All checks pass but no explicit instruction — agent does not merge

- **WHEN** all required CI checks for a PR are passing and all tasks in `tasks.md` are checked off
- **THEN** the agent SHALL NOT merge the PR; it SHALL report the ready state to the user and wait for an explicit merge instruction

#### Scenario: Explicit user instruction authorizes a specific merge

- **WHEN** the user says, in the current session, to merge a specific PR (e.g., "merge it", "go ahead and merge the propose PR")
- **THEN** the agent may proceed with merging that specific PR

#### Scenario: Authorization for one PR does not extend to subsequent PRs

- **WHEN** the user has explicitly authorized merging one PR in the 4-PR sequence
- **THEN** that authorization applies only to the designated PR; the agent SHALL wait for a separate explicit instruction before merging each subsequent PR in the sequence
