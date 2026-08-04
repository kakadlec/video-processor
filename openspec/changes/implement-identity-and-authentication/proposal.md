## Why

FIAP X currently has no identity boundary: every client can upload and access processing endpoints anonymously, and the single `main.go` package has no place to host authentication without coupling HTTP handlers to credential and token concerns. The DDD foundation defines Identity as the owner of users, credentials, and token issuance; this change implements the first production slice of that boundary.

## What Changes

- Add the Identity bounded context with a `User` aggregate and domain value objects.
- Add `RegisterUser`, `AuthenticateUser`, and `VerifyToken` application use cases.
- Persist users and hashed credentials in PostgreSQL through interfaces owned by the domain/application boundary.
- Issue and verify signed JWT access tokens without exposing credential storage to other contexts.
- Add authentication middleware at the API composition root.
- Introduce `cmd/api` as the composition root while preserving the current synchronous video-processing behavior and public route compatibility.
- Require authenticated identity for protected processing routes; keep `GET /` public.

## Non-goals

- No asynchronous processing, RabbitMQ, Redis, MinIO, notifications, or worker implementation.
- No migration of the video-processing pipeline into its final Phase 3 topology beyond the minimum wiring needed for API startup.
- No refresh tokens, password reset, email verification, roles, admin users, or social login.
- No breaking removal of existing routes without an explicit compatibility task.
- No permanent documentation, CI, agent-instruction, or OpenSpec canonical-spec changes in the implementation PR.

## Capabilities

### New Capabilities

- `identity-authentication`: User registration, password hashing, PostgreSQL persistence, JWT issuance/verification, and API authentication middleware.

### Modified Capabilities

- `video-processing-access`: Processing endpoints require a verified `UserID`; the public landing page remains accessible.

## Impact

The propose PR contains only this change folder. The later implementation PR will contain only Go application and test files. Database schema/migration files are included only if they are application-owned and explicitly covered by the implementation tasks; infrastructure provisioning and deployment configuration remain outside this change.
