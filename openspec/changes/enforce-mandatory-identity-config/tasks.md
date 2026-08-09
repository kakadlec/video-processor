## 1. Startup configuration

- [ ] 1.1 In `setupIdentity` (`identity.go`), remove the `errors.Is(pgErr, postgres.ErrDSNRequired) && signingKey == ""` special case so both-missing falls through to the existing `pgErr != nil` error path.
- [ ] 1.2 In `main.go`, remove the `identity == nil` branch and its log line; `setupIdentity` returning a nil module without an error is no longer a reachable case.

## 2. Router wiring

- [ ] 2.1 In `setupRouterWithIdentity` (`main.go`), remove the `identity != nil` conditionals around `requireBearerAuth()` and `requireArtifactOwnership()` so those middlewares are always attached.
- [ ] 2.2 Delete `setupRouter()` (the no-identity wrapper defined in `main.go`); confirm via `grep -rn "setupRouter()"` that no remaining caller exists outside the test files being updated in section 3.

## 3. Test suite

- [ ] 3.1 In `main_test.go`, change `startTestServer` to build a configured `identityModule` (reusing `identity_test.go`'s `newTestIdentityModuleWithTokens(t)`) and call `httptest.NewServer(setupRouterWithIdentity(module))`, returning both the server and a valid bearer token for the test's user.
- [ ] 3.2 Update every request-issuing helper/call site in `main_test.go` (`uploadVideo`, `/api/status`, `/download/:filename`, static `/uploads`/`/outputs` requests) to attach `Authorization: Bearer <token>` using the token from 3.1.
- [ ] 3.3 Update `main_test.go`'s cleanup assertions (`assertTempDirClean` and the `filepath.Glob` leftover checks in the upload tests) to also remove/account for the `<artifact>.owner` sidecar files that `recordArtifactOwner` now writes for every authenticated request, so `uploads/`/`outputs/` end each test run exactly as clean as before this change.
- [ ] 3.4 In `identity_test.go`, replace `TestSetupIdentity_NeitherConfigured_ReturnsNilModuleNoError` with a test asserting `setupIdentity` returns a non-nil error when both `IDENTITY_POSTGRES_DSN` and `IDENTITY_JWT_SIGNING_KEY` are unset.
- [ ] 3.5 In `identity_test.go`, delete `TestSetupRouter_IdentityRoutesNotRegisteredWithoutModule` (asserts behavior that no longer exists once `setupRouter()` is removed).

## 4. Verification

- [ ] 4.1 Run `go vet ./...` and confirm it passes.
- [ ] 4.2 Run `go test ./... -v` (or `docker compose run --build --rm app-test go test ./... -v` if `ffmpeg` isn't on `PATH` locally) and confirm the full suite passes.
- [ ] 4.3 Manually confirm `go run .` without `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` set exits non-zero with a clear configuration error, and that `docker compose up --build` (which already sets both) still starts and serves authenticated requests normally.
