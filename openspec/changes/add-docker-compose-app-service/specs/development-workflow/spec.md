## ADDED Requirements

### Requirement: Local Full-Stack Development Service
The repository SHALL provide a documented single command that starts the application together with PostgreSQL, with identity enabled, so a contributor can exercise registration, login, and bearer-protected video-processing routes locally without hand-configuring environment variables or manually wiring network access to the database.

#### Scenario: Contributor starts the full stack with one command
- **WHEN** a contributor runs the documented `docker compose up --build` command
- **THEN** the application container builds from the repository's `Dockerfile`, starts only after PostgreSQL's healthcheck reports healthy, and serves `/api/auth/register` and `/api/auth/login` without any additional configuration

#### Scenario: Full-stack option does not replace the lighter-weight option
- **WHEN** a contributor only needs to exercise video processing without identity
- **THEN** the plain `docker build` + `docker run` workflow (identity disabled) documented in `docs/development.md` and `docs/operations.md` remains available and unchanged
