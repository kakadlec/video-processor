## ADDED Requirements

### Requirement: Conventional Commit Messages
Commit messages on this project SHALL follow the [Conventional Commits](https://www.conventionalcommits.org/) format (`type: description`, e.g. `feat:`, `fix:`, `chore:`, `docs:`, `ci:`, `test:`, `refactor:`), with a `!` after the type or a `BREAKING CHANGE:` footer for breaking changes. This is the signal the automated release process uses to compute the next version.

#### Scenario: Feature commit triggers a minor version bump
- **WHEN** a commit with type `feat:` lands on `main`
- **THEN** the next proposed release version increments the minor component

#### Scenario: Fix commit triggers a patch version bump
- **WHEN** a commit with type `fix:` lands on `main`
- **THEN** the next proposed release version increments the patch component

#### Scenario: Breaking change triggers a major version bump
- **WHEN** a commit includes `!` after its type or a `BREAKING CHANGE:` footer
- **THEN** the next proposed release version increments the major component

### Requirement: Automated Release Pull Request
On every push to `main`, the system SHALL maintain a single up-to-date "Release PR" that aggregates unreleased Conventional Commits and shows the computed next version and changelog. No tag or GitHub Release SHALL be created until this PR is merged by a human.

#### Scenario: Release PR updates on new commits
- **WHEN** a new Conventional Commit is pushed to `main` while a Release PR is already open
- **THEN** the existing Release PR is updated in place to include the new commit and any resulting version change, rather than opening a duplicate PR

#### Scenario: Merging the Release PR cuts a release
- **WHEN** the Release PR is merged
- **THEN** a git tag matching the computed version is created, a GitHub Release with generated release notes is published, and `CHANGELOG.md` is updated
