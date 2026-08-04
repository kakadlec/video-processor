## ADDED Requirements

### Requirement: Identity Owns User Credentials and Token Issuance

The Identity bounded context SHALL own user registration, credential hashing, authentication, and access-token issuance. No other bounded context SHALL store plaintext credentials, password hashes, or issue identity tokens.

#### Scenario: Registration stores only a password hash

- **GIVEN** an anonymous client submits a valid email and password
- **WHEN** `RegisterUser` succeeds
- **THEN** a user is persisted with a normalized email and password hash, and the plaintext password is not persisted, returned, or logged

#### Scenario: Duplicate normalized email is rejected

- **GIVEN** a user already exists for a normalized email
- **WHEN** another registration uses the same email with different casing or whitespace
- **THEN** registration returns a conflict error and does not create a second user

#### Scenario: Authentication returns a bearer token

- **GIVEN** a registered user submits valid credentials
- **WHEN** `AuthenticateUser` succeeds
- **THEN** Identity returns a signed token containing the user's `UserID` and expiry metadata

#### Scenario: Invalid credentials do not reveal account existence

- **GIVEN** credentials are invalid because the email is unknown or the password is wrong
- **WHEN** authentication is attempted
- **THEN** the external response is the same unauthorized error in both cases

### Requirement: Identity Ports Preserve Dependency Direction

Identity domain and application packages SHALL depend on interfaces owned by the domain/application boundary and SHALL NOT import Gin, PostgreSQL drivers, JWT libraries, or other infrastructure adapters.

#### Scenario: Application uses injected ports

- **GIVEN** the registration or authentication use case is constructed
- **WHEN** it executes
- **THEN** persistence, hashing, token issuance, ID generation, and time are supplied through injected ports that can be replaced by fakes in tests

#### Scenario: Infrastructure implements domain contracts

- **GIVEN** a PostgreSQL repository or JWT adapter is compiled
- **WHEN** its type is checked
- **THEN** it satisfies the interface declared by the Identity boundary rather than defining a competing contract

### Requirement: API Authentication Protects Processing Routes

The API SHALL expose registration and login endpoints and SHALL require a verified bearer token for video-processing routes while keeping the landing page public.

#### Scenario: Public landing page remains available

- **GIVEN** a client has no token
- **WHEN** it requests `GET /`
- **THEN** the API returns the existing upload page with HTTP 200

#### Scenario: Registration and login contracts are available

- **GIVEN** an anonymous client submits valid auth JSON
- **WHEN** it calls `POST /auth/register` or `POST /auth/login`
- **THEN** the API returns the status and response shape defined by the design without credential data

#### Scenario: Missing or invalid bearer token is rejected

- **GIVEN** a client calls `POST /upload`, `GET /download/:filename`, or `GET /api/status` without a valid bearer token
- **WHEN** middleware runs
- **THEN** the API returns HTTP 401 before the processing handler executes

#### Scenario: Verified UserID reaches the handler

- **GIVEN** a client presents a valid signed token
- **WHEN** a protected route executes
- **THEN** the handler receives the `UserID` derived from the verified token and does not trust a client-supplied identity value

### Requirement: Identity Persistence Uses a Durable Normalized User Record

The application-owned identity schema SHALL persist a UUID user ID, normalized unique email, password hash, and creation timestamp with non-null constraints. Repository queries SHALL use parameters.

#### Scenario: User lookup uses normalized unique email

- **GIVEN** a login request contains an email with casing differences
- **WHEN** the repository looks up the user
- **THEN** it normalizes the email before performing a parameterized query against the unique email field

#### Scenario: Database failure is not exposed as credential detail

- **GIVEN** the user repository is unavailable during authentication
- **WHEN** the API handles the failure
- **THEN** it returns a generic server error and does not expose SQL, connection, hash, or token internals
