## Context

Solo repo, no tags, no release process. User explicitly chose `release-please` over a hand-rolled tagging script (trade-off discussed: less code to maintain and a mature, widely-used tool, vs. a fully transparent but self-maintained script). This documents how it's wired up.

## Goals / Non-Goals

**Goals:**
- Every push to `main` keeps a single "Release PR" up to date, showing the next version and changelog computed from Conventional Commits since the last release.
- Merging that PR is the only manual step — it creates the tag, the GitHub Release, and updates `CHANGELOG.md` automatically.
- No accidental release: nothing is tagged until a human merges the PR.

**Non-Goals:**
- Building/publishing versioned artifacts (binaries, Docker images) on release — this change only handles the tag/changelog/release metadata. Attaching build artifacts is a natural follow-up, not bundled here.
- Retroactively categorizing this repo's pre-existing commit history into the changelog correctly — those commits predate the convention.
- Branch protection or required-PR-review enforcement — orthogonal to this change (same non-goal as `add-ci-testing-and-sast`).

## Decisions

**`release-type: simple`, not `go`.** This app isn't a published Go module anyone `go get`s — there's no `go.mod` version field to bump (Go doesn't have one) and no package-manager manifest for release-please to touch. `simple` only manages the git tag, `CHANGELOG.md`, and the GitHub Release, which is exactly what's needed. `go` release-type exists in release-please mainly for Go proto/module repos with different conventions that don't apply here.

**Manually anchored a `v0.1.0` git tag instead of relying on `initial-version` in config.** No existing tags at the start of this change. First attempt: set `.release-please-manifest.json` to `"0.0.0"` — release-please's first-release logic special-cases this and jumps straight to `1.0.0` regardless of `bump-minor-pre-major`/`bump-patch-for-minor-pre-major` (those only affect bumping *from* an existing release, per `determineReleaseType` in `strategies/default.ts`). Second attempt: emptied the manifest entirely (`{}`) and set `initial-version: "0.1.0"` in the config, since reading `strategies/base.ts` shows `initialReleaseVersion()` should return `Version.parse(this.initialVersion)` when no prior release is found — still produced `1.0.0` in practice on `release-please-action@v5` / core `17.6.0`, meaning `initial-version` isn't reliably reaching that code path in this version despite looking correctly wired in `manifest.ts`'s config-merging code. Rather than keep chasing an internal wiring gap in a third-party tool, the pragmatic fix: manually create and push a `v0.1.0` tag once, so release-please has an actual `latestRelease` to bump *from*. That code path (`determineReleaseType`) was read directly and does correctly honor `bump-minor-pre-major`/`bump-patch-for-minor-pre-major` when a prior release exists and `version.isPreMajor` is true — so every release *after* this one-time manual tag behaves as intended. `initial-version` was removed from the config since it doesn't reliably do what its name implies here.

**Trade-off of this workaround:** the very first version number (`v0.1.0`) was set by hand instead of computed from commits — a one-time, deliberate exception to "the process is fully automatic," and reasonable since there was no meaningful commit history to compute a "first version" from anyway.

**`googleapis/release-please-action@v5`, pinned to major version** (same pattern as `actions/checkout`/`actions/setup-go`). Verified against the action's `action.yml` and the core `release-please` library's strategy list directly (not just the marketplace listing) that `simple` is a supported `release-type` — the `v5` action input description text only mentions a few examples and is misleadingly incomplete, so this was checked against source rather than trusted at face value.

**Explicit `permissions: contents: write, pull-requests: write`** on the workflow, rather than relying on repo-level default token permissions — makes the requirement visible in the workflow file itself, and avoids depending on whatever the repo's default `GITHUB_TOKEN` permission setting happens to be.

**Separate workflow file (`release-please.yml`), not folded into `ci.yml`.** Different trigger semantics (every push to `main`, not PRs) and different purpose (release bookkeeping vs. test/SAST gates) — keeping them separate means a release-please failure can't be confused with a test/SAST failure in the Actions UI.

## Risks / Trade-offs

- [First Release PR will bundle all pre-Conventional-Commits history as uncategorized] → Accepted; cosmetic, one-time, doesn't block anything.
- [Third-party Action dependency for a Google-maintained but still external tool] → Accepted per explicit user decision; mitigated by pinning to a major version and having verified the config against upstream source rather than assuming.
- [`release-type: simple` doesn't stamp a version into the compiled binary automatically] → Out of scope here; if binary versioning via `-ldflags` is wanted later, it can read the tag from `git describe` in a build step, which composes fine with this.

## Migration Plan

1. Add config files and workflow.
2. Push to `main` (this change's own commits, using Conventional Commits format from here on).
3. Confirm on GitHub that `release-please` opens a Release PR after the push.
4. Merging that PR (whenever the user chooses to cut the first release) is a separate, later action — not part of this change.

## Open Questions

- None blocking.
