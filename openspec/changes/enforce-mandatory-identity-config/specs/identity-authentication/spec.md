## MODIFIED Requirements

### Requirement: Configuration does not provide insecure defaults

The system SHALL load database and token-signing configuration from the environment or an equivalent explicit configuration source and SHALL fail clearly when identity configuration is partially present, entirely absent, or invalid. There is no supported mode in which the system starts without a fully configured Identity module.

#### Scenario: Missing signing configuration fails startup

- **GIVEN** `IDENTITY_POSTGRES_DSN` is set but the required JWT signing configuration is absent or invalid, or vice versa
- **WHEN** the API composition root starts
- **THEN** startup fails with a clear configuration error and does not use a hard-coded fallback secret

#### Scenario: Identity entirely unconfigured fails startup

- **GIVEN** neither `IDENTITY_POSTGRES_DSN` nor the JWT signing configuration is set
- **WHEN** the API composition root starts
- **THEN** startup fails with a clear configuration error, `/api/auth` routes are never registered, and no video-processing route becomes reachable
