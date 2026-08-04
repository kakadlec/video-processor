## 1. OpenSpec artifacts

- [x] 1.1 Create the change metadata file.
- [x] 1.2 Write the proposal with scope and non-goals.
- [x] 1.3 Write the design with domain model, ports, HTTP contracts, security constraints, and rollout.
- [x] 1.4 Add delta specifications for identity and protected processing access.
- [x] 1.5 Validate the change with strict non-interactive OpenSpec validation.

## 2. Identity domain and application

- [ ] 2.1 Add `UserID`, normalized `Email`, `User`, domain errors, and repository/port interfaces under `internal/identity/domain/`.
- [ ] 2.2 Implement `RegisterUser` with password hashing, duplicate-email handling, and no plaintext persistence.
- [ ] 2.3 Implement `AuthenticateUser` with uniform invalid-credential behavior.
- [ ] 2.4 Implement `VerifyToken` through the token port.
- [ ] 2.5 Add unit tests for domain invariants and all identity use cases using fakes.

## 3. Infrastructure and API wiring

- [ ] 3.1 Add the application-owned PostgreSQL migration for `identity_users`.
- [ ] 3.2 Implement the PostgreSQL user repository with parameterized queries and normalized-email uniqueness.
- [ ] 3.3 Implement password hashing and JWT token adapters with explicit runtime configuration and algorithm validation.
- [ ] 3.4 Add `cmd/api` as the composition root and preserve the existing synchronous routes.
- [ ] 3.5 Add registration and login handlers with the specified status codes and response shapes.
- [ ] 3.6 Add bearer authentication middleware and pass verified `UserID` through request context.
- [ ] 3.7 Protect `/upload`, `/download/:filename`, and `/api/status`; keep `GET /` public.
- [ ] 3.8 Add HTTP/integration tests for public auth routes, protected routes, malformed tokens, and compatibility behavior.

## 4. Verification

- [ ] 4.1 `npx --yes @fission-ai/openspec validate implement-identity-and-authentication --strict --no-interactive` passes.
- [ ] 4.2 `go test ./...` passes with required prerequisites.
- [ ] 4.3 `go vet ./...` passes.
- [ ] 4.4 `gosec ./...` passes without global exclusions.
- [ ] 4.5 `govulncheck ./...` passes.
- [ ] 4.6 `go build ./cmd/api` passes.
- [ ] 4.7 Implementation PR contains only application source and test files; this change folder is not modified in that PR.
