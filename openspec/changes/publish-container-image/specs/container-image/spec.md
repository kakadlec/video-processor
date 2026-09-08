## ADDED Requirements

### Requirement: Multi-Platform Build

The `Dockerfile` SHALL be buildable for `linux/amd64` and `linux/arm64`, and **every** binary in a built runtime image SHALL be compiled for that image's own platform.

The compilation SHALL NOT depend on emulating the target platform. The builder stage SHALL run on the build platform and produce target-platform binaries by cross-compiling — which `CGO_ENABLED=0` already makes exact — so that adding a platform costs a compile, not an emulated toolchain.

The test stage SHALL continue to run on the **build** platform. It exists so `docker compose run --build --rm app-test go test ./... -v` can run the suite (see "A Go- and ffmpeg-capable stage exists for running tests"), and a Go toolchain running under emulation would make that documented path unusably slow. A change that made the test stage follow the target platform would satisfy every other requirement here and break the local test loop, which is why this is stated rather than left to follow from the build's structure.

The set of platforms SHALL be exactly these two. The architecture value passed to the compiler is taken directly from the build's target architecture, and that direct substitution is only correct where the toolchain's architecture names coincide with the builder's; a 32-bit ARM platform would additionally need a variant to be specified and is out of scope.

#### Scenario: The image builds for both platforms

- **WHEN** the runtime image is built for `linux/amd64` and for `linux/arm64`
- **THEN** both builds succeed

#### Scenario: Every binary matches the image's platform

- **WHEN** a runtime image built for one platform is inspected
- **THEN** each of the five binaries — `identity-api`, `video-api`, `notification-api`, `worker`, `notifier` — is compiled for that platform, and none is compiled for the other

#### Scenario: The Go toolchain is not emulated

- **WHEN** the image is built for a platform other than the build host's
- **THEN** the stage that runs the Go toolchain runs on the build platform, and only the runtime stage's own package installation runs on the target platform

#### Scenario: The test stage stays native

- **WHEN** the Dockerfile is built targeting its test stage on any host
- **THEN** that stage runs on the build platform, so the suite runs under a native Go toolchain

#### Scenario: `ffmpeg` is present and runnable on each platform

- **WHEN** a runtime image built for either platform runs `ffmpeg`
- **THEN** it executes, because the worker shells out to it and a missing or wrong-architecture `ffmpeg` would otherwise surface only when a job is processed
