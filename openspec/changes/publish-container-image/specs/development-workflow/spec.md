## MODIFIED Requirements

### Requirement: Merge Requires Passing Status Checks
A pull request against `main` SHALL NOT be mergeable unless `Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`, and the container image build all report success. The PR branch SHALL NOT be required to be up to date with `main` before merging.

Adding a job to the CI workflow does not by itself make it required: branch protection enumerates the checks it blocks on, so a job outside that list runs, reports, and blocks nothing. A gate that cannot block a merge is a notification, and "Container Image Build Gate" would be a promise nothing keeps — which is why registering the new check with branch protection is part of this change rather than an afterthought.

#### Scenario: Merge blocked by a failing required check
- **WHEN** a PR has `Build & Test`, `SAST (gosec)`, `Vulnerability Scan (govulncheck)`, or the image build failing
- **THEN** GitHub blocks the merge button/API for that PR

#### Scenario: Merge allowed once all checks pass
- **WHEN** a PR has all four required checks passing
- **THEN** the PR is mergeable even if its branch is behind `main`

#### Scenario: The image build is registered as required, not merely present
- **WHEN** branch protection for `main` is inspected
- **THEN** the image-build job's check name is among the required status checks, so a red image build blocks the merge rather than only reporting

#### Scenario: Automated release PRs are subject to the same gate
- **WHEN** `release-please` opens its own release PR against `main`
- **THEN** that PR is mergeable only under the same conditions as any other PR — no special bypass

## ADDED Requirements

### Requirement: Container Image Build Gate

Every push to `main` and every pull request SHALL build the repository's container image in CI, and the job SHALL fail if the build fails.

The gate SHALL build the runtime image for every platform the image is published for, and SHALL additionally build the test stage on the runner's own platform, because that stage backs the documented local test command (`docker compose run --build --rm app-test go test ./... -v`) and is the one stage a runtime build does not exercise end to end.

The gate SHALL also verify, for each platform it builds, that every binary in the resulting runtime image is compiled for that platform. A build that succeeds is not evidence of this: the builder chains five compilations, and one of them missing the target architecture yields an image that builds, scans and pushes cleanly while one of five processes dies at `exec` on one platform only. A gate that would stay green through that failure does not gate the requirement it exists for (`container-image`'s "Multi-Platform Build"), so the check belongs in the recurring job rather than in a one-time manual verification.

The gate SHALL NOT push anything. Its purpose is that a `Dockerfile` which does not build cannot be merged; publication is a separate concern triggered by a release (`container-image-publication`). Not pushing is also what allows the job to run on a pull request from a fork without granting it registry write access.

This gate exists because the image is the artifact the application is delivered as, and until it was added, every other check could pass while the image was unbuildable: the test job compiles and runs the suite on the runner with dependencies installed there, and neither the SAST nor the vulnerability job reads the `Dockerfile` at all.

#### Scenario: CI fails on an unbuildable image

- **WHEN** a commit is pushed whose `Dockerfile`, or the source it copies, does not build into an image
- **THEN** the CI image-build job fails and is visibly reported on the commit or pull request

#### Scenario: CI passes when the image builds

- **WHEN** the image builds for every published platform
- **THEN** the CI image-build job succeeds

#### Scenario: A wrong-architecture binary fails the gate

- **WHEN** a commit builds an image in which one of the five binaries is compiled for a platform other than the image's own
- **THEN** the CI image-build job fails, rather than passing because the image built

#### Scenario: The test stage is covered by the gate

- **WHEN** the CI image-build job runs
- **THEN** it builds the test stage as well as the runtime stage, so a break in the stage that backs the documented local test command fails the pull request rather than surfacing on a contributor's machine

#### Scenario: The gate publishes nothing

- **WHEN** the CI image-build job completes on a pull request
- **THEN** no image or tag has been pushed to any registry
