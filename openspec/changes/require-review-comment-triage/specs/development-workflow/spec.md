## ADDED Requirements

### Requirement: Pull Request Review Comments Must Be Triaged Before Completion

Before an agent reports a pull-request-related task as complete, it SHALL check that pull request for review comments — from automatic code review (e.g. GitHub Copilot) and from human reviewers — and address the ones that make sense, resolving the corresponding review threads once addressed. This rule SHALL be documented in `AGENTS.md` so it is visible to and binding on any agent working in this repository, not dependent on a single tool's private memory.

#### Scenario: Task is not reported done before review comments are checked

- **WHEN** an agent opens or pushes a commit to a pull request as part of completing a task
- **THEN** the agent checks that pull request for review comments before reporting the task complete

#### Scenario: Genuine findings are fixed and their threads resolved

- **WHEN** a review comment identifies a genuine issue in the changed code
- **THEN** the agent fixes it and resolves the corresponding review thread

#### Scenario: A finding judged not applicable is explained, not silently ignored

- **WHEN** a review comment does not warrant a code change (false positive, out of scope, or a deliberate design trade-off)
- **THEN** the agent states why before leaving it unaddressed, rather than ignoring it without explanation

#### Scenario: The rule is discoverable outside any single tool's private memory

- **WHEN** any coding agent (not only the one that authored this rule) reads `AGENTS.md`
- **THEN** it finds this requirement documented there

### Requirement: Copilot Automatic Review Behavior Is Documented

`AGENTS.md` SHALL document that this repository has a `copilot_code_review` branch ruleset that automatically requests a Copilot review the first time each pull request opens, and that `review_on_push` is disabled — later commits pushed to an already-reviewed pull request do not trigger a fresh automatic review.

#### Scenario: Agent does not assume automatic re-review on push

- **WHEN** an agent pushes additional commits to a pull request that has already received an automatic Copilot review
- **THEN** the agent does not assume a new automatic review will be posted, per the documented ruleset behavior in `AGENTS.md`

#### Scenario: Agent requests a fresh review manually when needed

- **WHEN** an agent judges that substantial follow-up changes warrant a new Copilot review pass
- **THEN** the agent requests it manually rather than waiting for an automatic re-review that will not occur
