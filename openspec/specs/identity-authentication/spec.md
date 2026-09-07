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

Token-signing configuration is now **per process, and asymmetric**. The Identity HTTP service SHALL require a private key, the matching public key, and a key identifier. Every other service that authenticates callers SHALL require the public key and the key identifier, and SHALL NOT accept a private key as verification material. There SHALL be no default, fallback, or embedded key of either kind, and no mode in which a missing key is tolerated by degrading to unauthenticated access.

The Identity service requires the public key despite registering no authenticated route, and SHALL verify at startup that the two halves are one pair. A mismatched pair produces no error anywhere it can be attributed: Identity mints tokens successfully and every other service rejects all of them, which presents as an authentication fault in the services that are behaving correctly.

#### Scenario: Missing signing configuration fails startup

- **GIVEN** `IDENTITY_POSTGRES_DSN` is set but the required JWT key configuration is absent or invalid, or vice versa
- **WHEN** the Identity HTTP composition root starts
- **THEN** startup fails with a clear configuration error and does not use a hard-coded fallback key

#### Scenario: Identity entirely unconfigured fails startup

- **GIVEN** neither `IDENTITY_POSTGRES_DSN` nor the JWT key configuration is set
- **WHEN** the Identity HTTP composition root starts
- **THEN** startup fails with a clear configuration error, `/api/auth` routes are never registered, and no video-processing route becomes reachable

#### Scenario: A mismatched key pair fails Identity's startup

- **GIVEN** a private key, an active key identifier, and a verifier key set whose entry under that identifier is not the private key's match
- **WHEN** the Identity HTTP composition root starts
- **THEN** startup fails with a clear configuration error, rather than starting and issuing tokens that no other service can verify

#### Scenario: A verifying service without a public key fails startup

- **GIVEN** a service that serves bearer-authenticated routes
- **WHEN** it starts with no public-key configuration
- **THEN** startup fails with a clear configuration error and no route is registered, rather than the service starting and rejecting every request at authentication time

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

### Requirement: Access tokens are asymmetrically signed and only Identity can mint them

Access tokens SHALL be signed with an asymmetric algorithm (RS256), so that the ability to **issue** a token and the ability to **verify** one are separate capabilities backed by separate key material. The private key SHALL be held by exactly one process — the Identity context's HTTP service — and SHALL NOT be readable configuration for any other process. Every other service SHALL hold only the corresponding public key.

The **verifier's key material SHALL be configured as a set** of key identifier to public key, holding one or more entries, and not as a single key. A single-key verifier cannot be rotated: switching it before the issuer rejects every token already in circulation, and switching the issuer first mints tokens no verifier accepts. Holding both keys through the overlap is the whole purpose of the key identifier, so the set is a set from the outset rather than a later widening — the widening would itself need the window it exists to remove. The issuer's active key identifier is separate configuration, held only by the Identity service; a verifier reads the identifier off the token and is never told which key is current.

The public key SHALL be distributed by configuration rather than fetched from a key-set endpoint. A verifier SHALL be able to accept a valid token while the Identity service is unavailable; making verification depend on a call to Identity would reintroduce, at runtime, the coupling that separating the services removes. Rotation is consequently a coordinated configuration change, and tokens SHALL carry a key identifier (`kid`) from the outset so that a verifier can hold more than one public key and a rotation needs no window in which unidentified tokens are accepted.

The accepted algorithm SHALL remain pinned by name at verification. With a symmetric key this guarded against `alg: none`; with an asymmetric one it additionally guards against a token signed with the public key itself treated as an HMAC secret, since that key is not confidential.

#### Scenario: Only the Identity service can issue a token

- **GIVEN** the deployed HTTP services
- **WHEN** each one's configuration and build graph are inspected
- **THEN** only the Identity service is configured with a private key and only it constructs a token issuer; no other service can produce a token that any verifier accepts

#### Scenario: A verifier configured with a private key fails to start

- **GIVEN** a service that only verifies tokens
- **WHEN** it is configured with private-key material as its verification material, or with no key material at all
- **THEN** startup fails with a clear configuration error rather than succeeding with the ability to mint tokens or with no ability to verify them

#### Scenario: Verification survives Identity being down

- **GIVEN** a valid, unexpired token and a stopped Identity service
- **WHEN** the token is presented to a protected route on another service
- **THEN** the request is authorized, because the public key came from configuration and no call to Identity is made

#### Scenario: A rotation needs no window in which tokens are rejected

- **GIVEN** a verifier configured with two entries — the outgoing key and the incoming one — and an issuer still signing with the outgoing key
- **WHEN** the issuer is switched to sign with the incoming key
- **THEN** tokens issued before and after the switch are both accepted, and the outgoing entry can be dropped once every token signed under it has expired

#### Scenario: A token naming an unknown key is rejected indistinguishably

- **GIVEN** a token whose `kid` header names a key the verifier does not hold, or carries no `kid` at all
- **WHEN** it is presented to a protected route
- **THEN** the request is rejected with `401 Unauthorized` and the same failure shape every other invalid token produces, disclosing nothing about which key identifiers exist

#### Scenario: A token signed with the public key as an HMAC secret is rejected

- **GIVEN** a token presenting `alg: HS256`, signed using the verifier's public-key material as the HMAC secret
- **WHEN** it is presented to a protected route
- **THEN** it is rejected before any key lookup, because the verifier accepts only the pinned asymmetric algorithm

