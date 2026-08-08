# Design: Identity and Authentication

## Context

The repository currently exposes an unauthenticated Gin API from `main.go`, with synchronous local-filesystem video processing. The permanent DDD specification defines three bounded contexts and requires Identity to own credentials and token issuance. This change introduces the first executable Identity slice while keeping the existing processing behavior intact.

## Goals

1. Provide deterministic registration and login behavior.
2. Keep password hashing, persistence, and token implementation behind ports.
3. Keep domain and application packages independent of HTTP, SQL drivers, and JWT libraries.
4. Authenticate requests using bearer tokens and enforce ownership at the API boundary.
5. Keep the change small enough to review and deploy without introducing async processing or unrelated infrastructure.

## Non-goals

- Refresh tokens, logout/token revocation, password reset, email verification, MFA, roles, permissions, social login, or account recovery.
- RabbitMQ, Redis, MinIO, notification delivery, worker processes, or asynchronous video jobs.
- Migrating all existing processing code into the final DDD topology.
- Treating JWT claims as an authorization system beyond identifying the authenticated user.

## Bounded-context boundary

Identity owns `User`, credential verification, password-hash storage, and token issuance/verification. Video Processing receives only the shared opaque `UserID` at its boundary and must not import Identity domain/application packages. The composition root wires adapters and middleware.

## Package shape

Target packages for the implementation PR:

```text
cmd/api/                         # composition root and HTTP wiring
internal/identity/domain/        # User, UserID, repository and token/password ports
internal/identity/application/   # RegisterUser and AuthenticateUser use cases
internal/identity/infrastructure/# PostgreSQL, password hashing, JWT adapters
internal/platform/auth/          # transport-neutral authenticated-user context helpers, if needed
```

The implementation may preserve the current `main.go` entrypoint if moving the composition root is not necessary for this incremental slice, but it must preserve the dependency direction described by the canonical DDD spec.

## Domain model

### User aggregate

- `UserID` is a validated, opaque UUID value object.
- `User` contains an ID, normalized email, password hash, and creation timestamp.
- Email normalization is deterministic and case-insensitive for lookup.
- The domain never receives or stores a plaintext password.
- Password policy is explicit in the application boundary; invalid input returns a typed validation error.

### Ports

The domain/application layer defines interfaces for:

- user persistence: find by ID, find by normalized email, create;
- password hashing and comparison;
- token issuance and token verification.

Infrastructure adapters implement those interfaces. Use cases depend only on the ports.

## Use cases

### RegisterUser

1. Validate and normalize the email and password.
2. Check whether the normalized email already exists.
3. Hash the password with a memory-hard or adaptive password-hashing algorithm supported by the selected Go library.
4. Create and persist the user.
5. Return the user identity without exposing the password hash.

Duplicate registration must return a non-sensitive conflict response and must not overwrite the existing account.

### AuthenticateUser

1. Normalize the email.
2. Load the user by normalized email.
3. Compare the supplied password with the stored hash.
4. Issue a signed access token containing the user ID and standard expiry metadata.
5. Return a token response without exposing credential material.

Unknown users and incorrect passwords must produce the same external authentication failure shape.

## HTTP contract

The implementation must define and test explicit JSON contracts for:

- `POST /api/auth/register`: create a user; return a non-sensitive user representation.
- `POST /api/auth/login`: authenticate credentials; return a bearer access token and expiry metadata.
- `GET /`: remains public.
- Video-processing endpoints remain available only to authenticated callers. The exact existing routes must be audited in implementation and covered by tests.

Authentication failures return `401 Unauthorized`; malformed registration input returns `400 Bad Request`; duplicate email returns `409 Conflict`. Error bodies must not reveal whether a password or account exists beyond the defined contract.

## Middleware and ownership

Bearer middleware extracts `Authorization: Bearer <token>`, verifies signature and expiry through the token port, and stores the authenticated `UserID` in request context. Missing, malformed, expired, or invalid tokens are rejected before handlers run. Handlers must use the authenticated `UserID` rather than accepting caller-controlled ownership identifiers.

For the current synchronous API, uploads and status/download operations must not permit an authenticated user to access another user's artifacts. If the current filesystem model cannot express ownership safely, the implementation must introduce the smallest explicit ownership metadata needed by the change and test it; it must not silently rely on filenames or timestamps as authorization.

## Persistence and configuration

The PostgreSQL adapter must use parameterized queries and a migration/schema mechanism that can be exercised in tests. Connection settings and JWT signing configuration come from environment/configuration, never hard-coded secrets. Startup must fail clearly when identity configuration is partially present but invalid or incomplete, rather than silently running with a default signing key. If identity configuration is entirely absent (neither the PostgreSQL DSN nor the JWT signing key is set), the composition root starts without the Identity module instead of failing — this preserves the pre-Identity video-processing-only workflow for local/Docker runs that have not opted in, and is distinct from the partial/invalid case above.

Unit tests should use fakes for domain/application ports. Integration tests should exercise HTTP behavior and the PostgreSQL adapter using the repository's supported test strategy; they must not require a live external service in the default unit-test path unless the test explicitly provisions it.

## Security decisions

- Passwords are stored only as adaptive password hashes; never log them.
- Tokens are signed and verified with an explicitly configured algorithm; verification must reject algorithm substitution.
- Tokens contain a short, documented access-token lifetime.
- JWT signing secrets/private keys are supplied through environment/configuration.
- Authentication errors are intentionally generic.
- SQL inputs are parameterized.
- New code, comments, and error messages follow the repository's English language policy.

## Compatibility and migration

The existing public landing page remains reachable. Existing video-processing behavior remains synchronous and is not replaced by a queue. The frontend must be updated only as required to obtain and send credentials; if the current inline UI cannot support the new contract without broad redesign, the API contract and migration steps must be documented in the implementation rather than mixing unrelated frontend extraction work into this change.

## Risks and mitigations

| Risk | Mitigation |
|---|---|
| Authentication is wired directly into the legacy handler | Put verification in middleware and pass only `UserID` downstream |
| Password or token implementation leaks into domain | Enforce ports and package import checks |
| Existing artifacts lack ownership metadata | Add explicit ownership persistence/metadata and regression tests |
| Local development becomes impossible without PostgreSQL | Keep unit tests adapter-free and provide a deterministic local/test setup |
| JWT configuration is unsafe | Require configured signing material and reject unsupported algorithms |

## Acceptance evidence

The implementation PR must provide tests for registration, duplicate email, login success/failure, token verification failures, protected-route behavior, public `GET /`, and cross-user artifact isolation. It must also pass `go test ./...`, `go vet ./...`, SAST, vulnerability scanning, and the existing end-to-end video-processing regression flow.
