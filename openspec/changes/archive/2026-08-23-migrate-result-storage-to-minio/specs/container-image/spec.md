## MODIFIED Requirements

### Requirement: Non-Root Runtime User

The runtime stage SHALL run the application process as a non-root user, not `root`, with a working directory and `uploads`/`temp` subdirectories that user owns and can write to. `outputs` is no longer among them: result artifacts live in object storage, and the application no longer creates or writes that directory.

#### Scenario: Container process runs unprivileged

- **WHEN** a container is started from the built image
- **THEN** the application process's effective user is a non-root user

#### Scenario: Non-root user can create its runtime directories

- **WHEN** the application starts for the first time and creates `uploads/` and `temp/` relative to its working directory
- **THEN** it succeeds, because the runtime stage pre-creates and owns these directories (and the working directory containing them) for the non-root user before switching to it

### Requirement: Unchanged External Contract

Hardening the image SHALL NOT change its external contract: the application SHALL still listen on port 8080 and create the runtime directories it needs on first run — so `docker-compose.yml`'s `app` service and the deployment commands documented in `docs/operations.md` keep working.

The application requires environment configuration to start, and SHALL fail fast with a clear error when it is missing rather than starting in a degraded mode. This requirement previously asserted that the application needed no environment variables at all; that has been false since Phase 2 made `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` mandatory, and it is corrected here rather than left standing as a known-false normative claim. Which variables are required is specified by the capabilities that own them, not by this one.

#### Scenario: Existing compose service keeps working

- **WHEN** `docker compose up --build` runs
- **THEN** the `app` service builds successfully and serves the application on port 8080, with the environment the compose file supplies

#### Scenario: Missing configuration fails fast rather than degrading

- **WHEN** a container is started without the environment variables the application requires
- **THEN** it exits with an error naming what is missing, rather than starting and failing at request time
