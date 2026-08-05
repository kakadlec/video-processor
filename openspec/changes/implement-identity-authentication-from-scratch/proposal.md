# Proposal: Implement Identity and Authentication

## Why

The DDD foundation now defines Identity as the only bounded context responsible for users, credentials, and identity tokens, but the running service still has no user model or access control. The next roadmap step is to add a minimal, explicit identity capability without prematurely coupling video processing to authentication internals or expanding into unrelated infrastructure.

## What changes

- Add the Identity bounded context with a `User` aggregate and opaque `UserID` value object.
- Add registration and credential-authentication use cases behind domain ports.
- Persist users and password hashes through a PostgreSQL adapter, with schema/migration support appropriate to the repository's composition root.
- Issue and verify bearer JWTs through an explicit token port and HTTP middleware.
- Protect video-processing routes while keeping `GET /` public.
- Preserve the existing synchronous video-processing behavior; authenticated identity is the only feature in this change.

## Scope boundaries

This change does not implement RabbitMQ, Redis, MinIO, background workers, notifications, refresh tokens, password reset, roles/permissions, social login, or the full Video Processing migration. It also does not redesign the frontend beyond the minimum contract changes needed to authenticate requests.

## Delivery sequence

This is the proposal PR only. It contains OpenSpec artifacts and must be merged before any application code or tests are changed. The subsequent implementation PR must contain only application source and test files. A later finalization PR will mark tasks, promote the delta, and archive this change after implementation merges.
