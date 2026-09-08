## ADDED Requirements

### Requirement: Container Image Build Gate

Every push to `main` and every pull request SHALL build the repository's container image in CI, and the job SHALL fail if the build fails.

The gate SHALL build the runtime image for every platform the image is published for, and SHALL additionally build the test stage on the runner's own platform, because that stage backs the documented local test command (`docker compose run --build --rm app-test go test ./... -v`) and is the one stage a runtime build does not exercise end to end.

The gate SHALL NOT push anything. Its purpose is that a `Dockerfile` which does not build cannot be merged; publication is a separate concern triggered by a release (`container-image-publication`). Not pushing is also what allows the job to run on a pull request from a fork without granting it registry write access.

This gate exists because the image is the artifact the application is delivered as, and until it was added, every other check could pass while the image was unbuildable: the test job compiles and runs the suite on the runner with dependencies installed there, and neither the SAST nor the vulnerability job reads the `Dockerfile` at all.

#### Scenario: CI fails on an unbuildable image

- **WHEN** a commit is pushed whose `Dockerfile`, or the source it copies, does not build into an image
- **THEN** the CI image-build job fails and is visibly reported on the commit or pull request

#### Scenario: CI passes when the image builds

- **WHEN** the image builds for every published platform
- **THEN** the CI image-build job succeeds

#### Scenario: The test stage is covered by the gate

- **WHEN** the CI image-build job runs
- **THEN** it builds the test stage as well as the runtime stage, so a break in the stage that backs the documented local test command fails the pull request rather than surfacing on a contributor's machine

#### Scenario: The gate publishes nothing

- **WHEN** the CI image-build job completes on a pull request
- **THEN** no image or tag has been pushed to any registry
