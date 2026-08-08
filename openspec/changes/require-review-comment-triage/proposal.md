## Why

`kakadlec/video-processor` now has a repository ruleset that automatically requests a GitHub Copilot code review the first time each pull request opens. In practice, review comments from that automatic pass (and from human reviewers) have only been checked and addressed when the user explicitly asked for it in the current session, not as a default step of finishing a PR-related task. This is currently enforced only as a private, tool-specific memory for one coding assistant, not as a rule any agent working in this repository can see and follow.

## What Changes

- Add a new `development-workflow` requirement: before a PR-related task is reported complete, an agent SHALL check the pull request for review comments (automatic code review, e.g. GitHub Copilot, and human reviewers) and address the ones that make sense, resolving the corresponding review threads.
- Document this explicitly in `AGENTS.md` so it is visible to and binding on any coding agent working in this repository, not dependent on a single tool's private memory.
- Document the existing `copilot_code_review` repository ruleset (auto-review on first PR open, no re-review on later pushes) in `AGENTS.md` as the context this rule responds to.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `development-workflow`: adds a requirement that PR review comments must be checked and triaged before a PR-related task is considered complete, and that this rule is documented in `AGENTS.md`.

## Impact

- `AGENTS.md`: new section documenting the rule and the Copilot auto-review ruleset context.
- No application code, tests, CI configuration, or canonical spec behavior for the video-processing service itself is affected — this is a process/documentation-only change.
