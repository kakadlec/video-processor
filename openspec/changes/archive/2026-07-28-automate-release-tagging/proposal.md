## Why

There's no versioning process for this project yet — no tags, no releases, and no defined way to decide whether a change is a patch, minor, or major bump. Doing that by hand doesn't scale and is easy to forget. We want tag/release creation to happen automatically, based on a structured signal in commit messages, rather than someone manually running `git tag` and guessing the right number.

## What Changes

- Adopt [Conventional Commits](https://www.conventionalcommits.org/) (`feat:`, `fix:`, `feat!:`/`BREAKING CHANGE:`, `chore:`, `docs:`, `ci:`, `test:`, `refactor:`, etc.) as the required commit message format going forward, so bump type can be derived automatically. Documented in `CLAUDE.md`.
- Add a `release-please` GitHub Actions workflow (`googleapis/release-please-action`) that, on every push to `main`, maintains an up-to-date "Release PR" summarizing unreleased changes and their computed next version. Merging that PR is what actually cuts the release: creates the git tag, a GitHub Release with generated changelog, and updates `CHANGELOG.md`.
- Add `release-please-config.json` and `.release-please-manifest.json` (release-type `simple`, starting version `0.0.0`, since there are no existing tags).

## Capabilities

### New Capabilities
- `development-workflow` is already an existing capability (from `add-ci-testing-and-sast`); this adds new requirements to it rather than a new capability.

### Modified Capabilities
- `development-workflow`: adds requirements for commit message convention and automated release tagging.

## Impact

- New files: `.github/workflows/release-please.yml`, `release-please-config.json`, `.release-please-manifest.json`.
- `CLAUDE.md`: documents the Conventional Commits requirement and how releases now happen (merge the bot-maintained Release PR, don't hand-tag).
- Existing commit history predates this convention and won't retroactively categorize correctly — release-please's first Release PR may lump prior history in as uncategorized; this is a one-time cosmetic artifact of adopting the convention now rather than at repo creation, not something this change fixes retroactively.
- No production code changes.
