# Tasks

## Proposal and architecture

- [x] 1.1 Confirm the implementation scope and HTTP contracts against the canonical DDD architecture spec.
- [x] 1.2 Define the `User` aggregate, normalized email rules, password policy, and opaque `UserID` value object.
- [x] 1.3 Define repository, password-hasher, and token ports without infrastructure imports in domain/application packages.
- [x] 1.4 Define the PostgreSQL schema/migration and configuration contract, including required JWT signing configuration.

## Identity implementation

- [x] 2.1 Implement `UserID` and `User` domain behavior with unit tests for validation and normalization.
- [x] 2.2 Implement `RegisterUser` with duplicate detection and password hashing.
- [x] 2.3 Implement `AuthenticateUser` with generic credential failures and token issuance.
- [x] 2.4 Implement PostgreSQL persistence using parameterized queries and adapter tests.
- [x] 2.5 Implement password-hash and JWT adapters with explicit algorithm and expiry validation.

## HTTP integration and security

- [x] 3.1 Add registration and login endpoints with documented status codes and non-sensitive responses.
- [x] 3.2 Add bearer middleware that rejects missing, malformed, expired, and invalid tokens.
- [x] 3.3 Protect all video-processing routes while keeping `GET /` public.
- [ ] 3.4 Enforce authenticated ownership for upload, status, and download operations.
- [ ] 3.5 Update the existing UI/client contract only as required for authentication, without unrelated frontend migration.

## Verification

- [ ] 4.1 Add unit and HTTP tests for registration, duplicate email, login success/failure, and token failures.
- [ ] 4.2 Add regression tests proving public landing-page access and protected-route behavior.
- [ ] 4.3 Add tests proving one authenticated user cannot read or download another user's processing artifacts.
- [ ] 4.4 Verify domain/application package dependency rules and configuration failure behavior.
- [ ] 4.5 Run the existing video upload → processing → download regression flow with authentication.
- [ ] 4.6 Run `go test ./...`, `go vet ./...`, `gosec ./...`, and `govulncheck ./...`; resolve all findings before the implementation PR.
