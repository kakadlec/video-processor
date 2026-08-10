# identity-authentication Specification

## Purpose

Define the first executable Identity bounded-context slice: user registration, credential authentication, signed access tokens, and bearer-token verification.

## Requirements

### Requirement: Register a user with normalized identity and protected credentials

The system SHALL accept a valid email and password, normalize the email deterministically, store only an adaptive password hash, and return a non-sensitive user representation.

#### Scenario: Valid registration creates a user

- **GIVEN** no user exists for the normalized email
- **WHEN** a client submits valid registration credentials
- **THEN** the system creates one user with a generated `UserID`, stores only a password hash, and returns the user identity without credential material

#### Scenario: Duplicate registration is rejected

- **GIVEN** a user already exists for the normalized email
- **WHEN** another registration uses that email with any password
- **THEN** the system returns `409 Conflict` and does not modify the existing user

#### Scenario: Invalid registration input is rejected

- **GIVEN** an email or password violates the documented validation policy
- **WHEN** registration is requested
- **THEN** the system returns `400 Bad Request` and does not persist a user

### Requirement: Authenticate credentials and issue signed access tokens

The system SHALL authenticate a normalized email and password and issue a signed access token containing the authenticated `UserID` and bounded expiry metadata.

#### Scenario: Correct credentials issue a token

- **GIVEN** a registered user and the correct password
- **WHEN** the client calls the login endpoint
- **THEN** the system returns a bearer access token and expiry metadata without exposing the password hash

#### Scenario: Invalid credentials have a generic failure

- **GIVEN** an unknown email or incorrect password
- **WHEN** the client calls the login endpoint
- **THEN** the system returns the same `401 Unauthorized` failure shape for both cases

#### Scenario: Token verification rejects invalid tokens

- **GIVEN** a token is missing, malformed, expired, incorrectly signed, or uses an unsupported signing algorithm
- **WHEN** it is presented to a protected route
- **THEN** the request is rejected with `401 Unauthorized` before the handler executes

### Requirement: Identity uses explicit ports and dependency boundaries

Identity domain and application packages SHALL define and consume ports for persistence, password hashing, and token operations; they SHALL NOT import HTTP frameworks, SQL drivers, JWT libraries, or infrastructure packages.

#### Scenario: Infrastructure is replaceable

- **GIVEN** the application use cases are tested
- **WHEN** fake repository, hasher, and token implementations are injected
- **THEN** the use cases can be tested without PostgreSQL, HTTP, or JWT infrastructure

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

### Requirement: Authentication protects video-processing access

The system SHALL keep `GET /` public while requiring a valid bearer token for video-processing routes and SHALL derive artifact ownership from the authenticated `UserID`, not from caller-controlled identity fields.

#### Scenario: Public landing page remains available

- **GIVEN** no credentials are supplied
- **WHEN** a client requests `GET /`
- **THEN** the server returns the landing page successfully

#### Scenario: Protected route requires authentication

- **GIVEN** no valid bearer token is supplied
- **WHEN** a client requests a video-processing route
- **THEN** the server returns `401 Unauthorized` and does not process the request

#### Scenario: Users cannot access another user's artifacts

- **GIVEN** user A owns a processing artifact
- **WHEN** user B requests its status or download using a valid token
- **THEN** the server denies access and does not disclose the artifact
