## Context

The current application is a single-package Gin service with synchronous video processing and local filesystem state. The canonical DDD foundation requires Identity to own user credentials and token issuance, while Video Processing receives only an opaque `UserID`. This change is the first implementation slice and must establish a usable composition boundary without prematurely implementing later infrastructure.

## Goals

- Make user identity a domain-owned concept.
- Store only password hashes, never plaintext passwords.
- Provide deterministic application ports for persistence and token services.
- Keep domain and application packages independent from Gin, PostgreSQL drivers, and JWT libraries.
- Wire authentication at `cmd/api` while preserving the existing synchronous video flow.
- Make auth behavior testable without requiring a live PostgreSQL instance.

## Non-goals

- Async jobs or worker entrypoints.
- Redis, RabbitMQ, MinIO, email, webhooks, or observability infrastructure.
- Full migration of every current handler into bounded-context packages.
- Production secret management or infrastructure provisioning.

## Target topology

```text
cmd/api/                         # composition root; adapters and Gin wiring only
internal/identity/domain/        # User, Email, PasswordHash, UserRepository, errors
internal/identity/application/   # RegisterUser, AuthenticateUser, VerifyToken
internal/identity/infrastructure/# PostgreSQL repository and JWT/password adapters
internal/identity/presentation/  # Gin handlers and auth middleware
pkg/identity/                    # opaque UserID contract shared with other contexts
```

`main.go` may remain as a compatibility entrypoint during this slice, but `cmd/api` must be independently buildable and must reuse the existing HTTP behavior rather than duplicate the entire server.

## Domain model

- `UserID` is an opaque UUID value, immutable and serializable.
- `Email` is normalized to lowercase and must satisfy the repository's email validation rules.
- `User` owns `UserID`, normalized email, password hash, and creation timestamp.
- A user is created only with a valid email and a password that satisfies the minimum policy defined by the application boundary.
- Plaintext passwords may exist only transiently inside the registration/authentication use-case call and must never cross a repository interface or be logged.
- Duplicate normalized emails produce a typed conflict error.
- Invalid credentials produce the same externally visible authentication error regardless of whether the email exists.

## Ports and adapters

Domain/application interfaces define:

- `UserRepository`: create a user and find a user by normalized email or ID.
- `PasswordHasher`: hash and compare passwords.
- `TokenIssuer`: issue a token for a `UserID` and verify a token into a `UserID`.
- `Clock` and `IDGenerator` may be injected where needed for deterministic tests.

Infrastructure implements these interfaces:

- PostgreSQL repository with parameterized queries and a unique normalized-email constraint.
- Password hashing using an adaptive, password-specific algorithm available through a maintained Go library.
- JWT adapter configured from environment at the composition root; signing secret is never hard-coded.

## HTTP contract

Public endpoints:

- `GET /` remains unauthenticated.
- `POST /auth/register` accepts `{ "email": "...", "password": "..." }` and returns `201` with a public user identifier and no credential data.
- `POST /auth/login` accepts the same credential shape and returns `200` with a bearer access token and expiry metadata.

Protected endpoints:

- `POST /upload`, `GET /download/:filename`, and `GET /api/status` require `Authorization: Bearer <token>` and receive the verified `UserID` through request context.
- Missing or invalid credentials return `401` without revealing whether a user exists.
- Registration validation failures return `400`; normalized-email conflicts return `409`.

The existing video-processing response shapes remain unchanged in this phase except for authentication status behavior.

## Security constraints

- Passwords are never persisted, logged, returned, or included in domain events.
- JWT validation checks signature algorithm, signature, expiry, issuer, and subject/user ID format.
- Secrets come from runtime configuration; missing required auth configuration fails API startup rather than silently using a default.
- SQL queries use parameters; user-controlled values are not interpolated.
- Auth middleware must not trust a user ID supplied by the client outside the verified token.

## Database contract

Application-owned migration creates an `identity_users` table with:

- UUID primary key;
- normalized unique email;
- password hash;
- creation timestamp;
- appropriate non-null constraints.

The exact migration mechanism must match the repository's existing conventions; no external database provisioning is introduced by this change.

## Verification

- Unit tests cover value-object validation, registration, duplicate email handling, authentication success/failure, token verification, and middleware outcomes.
- Integration tests cover auth HTTP contracts and protected-route behavior using fakes or a disposable database strategy available in the repository.
- `go test ./...`, `go vet ./...`, `gosec ./...`, and `govulncheck ./...` must pass.
- `go build ./cmd/api` must pass.
- Existing synchronous upload/download/status tests must continue to pass with a valid authenticated request where protection is introduced.

## Rollout and rollback

Roll out behind the API composition root and apply the identity migration before enabling protected routes. Roll back by reverting the implementation PR and leaving the additive identity table unused; do not delete user data as part of application rollback.
