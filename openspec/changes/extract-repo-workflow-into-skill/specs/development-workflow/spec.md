## MODIFIED Requirements

### Requirement: Change Completion Requires A Passing Test Run
A change whose diff includes one or more `.go` files SHALL NOT be considered complete until `go test ./...` has been run and passes, in addition to any applicable CI checks. A change whose diff includes no `.go` files (for example, documentation, OpenSpec artifacts, or agent-configuration-only changes) is exempt from this requirement.

#### Scenario: A Go change is not marked done without a passing local test run
- **WHEN** a change whose diff includes one or more `.go` files is made
- **THEN** `go test ./...` is run and passes before the change is reported as complete

#### Scenario: A non-Go change is exempt from the local test-run requirement
- **WHEN** a change's diff includes no `.go` files
- **THEN** the change may be reported as complete without running `go test ./...`, and this requirement does not apply

## ADDED Requirements

### Requirement: Claude Code Workflow Guidance Is Delivered Via An Auto-Triggered Skill
Claude Code agents SHALL receive the compact, actionable version of this repo's PR-sequence, branch-protection, quality-gate, commit-convention, and merge-authorization rules through a dedicated skill under `.claude/skills/`, rather than through full prose duplicated in always-loaded project instructions (`CLAUDE.md`). The skill's trigger description SHALL cover, at minimum: opening or merging a pull request; marking any task, change, or OpenSpec change complete; writing a commit message; running or reporting quality gates (`go vet`, `go test`, `gosec`, `govulncheck`); and checking PR review comments before reporting a PR-related task done. The skill's trigger description SHALL also state explicit conditions under which it does not need to engage: read-only questions, pure exploration, and trivial single-file edits with no PR involved.

#### Scenario: Skill trigger covers all required quality gates
- **WHEN** an agent is asked to run or report the results of `go test`, `go vet`, `gosec`, or `govulncheck`
- **THEN** the skill's trigger description matches the request for each of those four gates

#### Scenario: Skill does not engage for a trivial, PR-less edit
- **WHEN** a single-file typo or comment edit is requested with no pull request involved
- **THEN** the skill's trigger description explicitly does not require engaging

#### Scenario: CLAUDE.md does not restate the full ruleset
- **WHEN** `CLAUDE.md` is loaded into a conversation
- **THEN** it contains project/app context and short pointers, not the full PR-sequence/quality-gate/commit-convention prose

### Requirement: Roadmap-Item Lifecycle Sequencing Is Encoded As A Skill
The sequence for taking a `docs/roadmap.md` Change Backlog item (or any comparable non-trivial change) from idea to shipped — evaluate whether `/opsx:explore` is warranted, discuss, `/opsx:propose`, wait for the propose PR to merge before implementation begins, `/opsx:apply`, verify CI status and PR review comments before reporting any task or the change done, then `/opsx:archive`/finalize — SHALL be encoded in a dedicated skill, distinct from the per-step vendored OpenSpec skills, so an agent or user can invoke the end-to-end sequence directly rather than relying on prose scattered across multiple documents.

#### Scenario: Lifecycle skill defers done-verification to the workflow skill
- **WHEN** the lifecycle skill reaches a point where a task or PR must be verified done
- **THEN** it invokes the same done-verification checklist used elsewhere, rather than restating it

#### Scenario: Implementation does not start before the propose PR merges
- **WHEN** the lifecycle skill is guiding a change whose propose PR is open but not yet merged
- **THEN** it does not proceed to `/opsx:apply`
