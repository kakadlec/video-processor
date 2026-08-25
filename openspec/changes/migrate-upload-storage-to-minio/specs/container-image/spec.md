## MODIFIED Requirements

### Requirement: Non-Root Runtime User

The runtime stage SHALL run the application process as a non-root user, not `root`, with a working directory and a `temp` subdirectory that user owns and can write to. Neither `outputs` nor `uploads` is among them: result artifacts and uploaded source videos both live in object storage, and the application no longer creates or writes either directory.

#### Scenario: Container process runs unprivileged

- **WHEN** a container is started from the built image
- **THEN** the application process's effective user is a non-root user

#### Scenario: Non-root user can create its runtime directory

- **WHEN** the application starts for the first time and creates `temp/` relative to its working directory
- **THEN** it succeeds, because the runtime stage pre-creates and owns that directory (and the working directory containing it) for the non-root user before switching to it

#### Scenario: The image pre-creates no storage-backed directories

- **WHEN** the built image is inspected
- **THEN** its working directory contains `temp` and neither `uploads` nor `outputs`
