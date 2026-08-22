## Why

The automatic PR-branch updater invokes GitHub's update-branch API as `github-actions[bot]`. The resulting `pull_request` CI runs are marked `action_required` by GitHub's workflow-approval policy, even for PRs whose branches are in this repository. The updater adds repeated approval work without preserving a necessary merge-safety guarantee.

## What Changes

- Remove the `Auto-update PR branches` GitHub Actions workflow.
- Change `main` branch protection so required checks must pass, but a PR branch need not be up to date with `main` before it is merged.
- Retain PR-only merges, administrator enforcement, required status checks, and required-conversation resolution.

## Capabilities

### Modified Capabilities
- `development-workflow`: permits merging a PR with successful required checks even when its branch does not contain the latest `main`; removes automatic PR-branch updates.

## Impact

- Deletes `.github/workflows/auto-update-pr-branches.yml`.
- Updates the `main` branch-protection setting through the GitHub API (`required_status_checks.strict: false`).
- No application behavior, dependencies, or CI checks change.
