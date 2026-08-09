## Context

`setupIdentity` (`identity.go:50`) currently has three outcomes: (1) neither `IDENTITY_POSTGRES_DSN` nor `IDENTITY_JWT_SIGNING_KEY` set → returns `(nil, nil, nil)`, identity disabled; (2) exactly one set, or both set but invalid/unreachable → returns an error, startup fails; (3) both set and valid → returns a working `identityModule`. `main.go:34-40` treats outcome (1) as expected and logs that video processing runs unauthenticated. `setupRouterWithIdentity` (`main.go:57`) then threads `identity != nil` through every route group to decide whether to attach `requireBearerAuth()`/`requireArtifactOwnership()` middleware at all.

`main_test.go`'s entire video-processing integration suite calls `setupRouter()`, a thin wrapper defined right above it (`main.go:53-55`) around `setupRouterWithIdentity(nil)` — i.e. today's tests exercise the unauthenticated path exclusively. `identity_test.go` already has a parallel, richer suite that stands up a real `identityModule` via `newTestIdentityModule(t)`/`newTestIdentityModuleWithTokens(t)`, backed by an in-memory fake `domain.UserRepository` (no live PostgreSQL needed) plus the real password/JWT/ID-generation adapters, and mints tokens for authenticated requests.

Once `main_test.go`'s requests are authenticated, every successful `/upload` records an `outputs/<zip>.owner` sidecar and every failed `/upload` retains an `uploads/<file>.owner` sidecar (`recordArtifactOwner`, `main.go:132`). `uploads/` and `outputs/` are not per-test tmpdirs — they're the app's real durable directories — and none of the existing cleanup helpers (`assertTempDirClean`, the `filepath.Glob` leftover checks) remove `.owner` files. Left unaddressed, every CI run of the newly-authenticated suite would leave stray sidecar files behind indefinitely.

## Goals / Non-Goals

**Goals:**
- Startup fails (non-nil error from `setupIdentity`, `log.Fatal` in `main`) whenever `IDENTITY_POSTGRES_DSN` or `IDENTITY_JWT_SIGNING_KEY` is missing — no configuration state left starts the server unauthenticated.
- Remove the now-dead "identity disabled" branch from `setupRouterWithIdentity` and its callers so there is exactly one router shape: every video-processing route always requires a valid bearer token.
- Keep `main_test.go`'s existing video-processing test suite exercising real behavior (real ffmpeg, real filesystem) by routing it through a configured `identityModule` and valid bearer tokens instead of the disabled path that's being removed.

**Non-Goals:**
- No change to the `identityModule` internals (registration, login, token verification, ownership enforcement) — those already work correctly when configured; this change only removes the bypass around them.
- No change to `docker-compose.yml` (its `app`/`app-test` services already set both variables) or to the Identity domain/application/infrastructure packages.
- Updating `README.md`, `docs/operations.md`, `docs/architecture.md`, `docs/development.md`, `docs/flows.md` — real behavior changes, but per this repo's PR-splitting rule these permanent-documentation updates land in the change's finalization PR, not the implementation PR.
- Redesigning how identity configuration is loaded (env vars stay env vars) — only the "both missing" branch's outcome changes.

## Decisions

**Decision: `setupIdentity` treats "both missing" the same as "one missing" — always an error.**
Today the function special-cases `errors.Is(pgErr, postgres.ErrDSNRequired) && signingKey == ""` to return `(nil, nil, nil)`. That branch is deleted; the case falls through to the existing `pgErr != nil` check, which already produces a clear `fmt.Errorf("identity: %w", pgErr)`. This reuses the existing "partial configuration" error path rather than inventing a new message, so the two failure modes (missing one variable vs. missing both) read consistently.
- *Alternative considered*: keep a distinct error message for "both missing" vs. "one missing." Rejected — the caller (`main.go`) already treats any non-nil `setupIdentity` error identically (`log.Fatal`), and a distinct message adds no operational value over "DSN is required" / "signing key is required."

**Decision: `setupRouterWithIdentity` requires a non-nil `identityModule`; the nil-safe conditionals are removed, not kept as dead code.**
`setupRouter()` (the no-identity wrapper used only by tests) is deleted along with the `identity != nil` branches in `setupRouterWithIdentity`. Since `setupIdentity` can no longer return a nil module without an error, and `main` already `log.Fatal`s on error, there is no remaining production caller that could pass nil — keeping the conditional would be unreachable code the compiler can't prove unreachable, quietly reintroducing the disabled path.
- *Alternative considered*: leave `setupRouter()`/nil-handling in place "just for tests." Rejected per this project's convention of not keeping backwards-compatibility shims for paths that can't occur in production; it's also precisely the shape of bug this change exists to close off.

**Decision: `main_test.go`'s test server helper switches to the existing in-memory-backed `identityModule` fixture, not a real PostgreSQL instance.**
`identity_test.go` already provides `newTestIdentityModule(t)` / `newTestIdentityModuleWithTokens(t)`, built on `inMemoryUserRepository` plus the real password/JWT adapters — no I/O, no test-database dependency. `main_test.go`'s `startTestServer` (currently `httptest.NewServer(setupRouter())`) changes to build a module the same way and call `httptest.NewServer(setupRouterWithIdentity(module))`; every helper in `main_test.go` that issues a request (`uploadVideo`, status/download checks, the static-mount test) gains an `Authorization: Bearer <token>` header using a token minted once per test via `tokens.Issue(userID, ...)`, matching the pattern `identity_test.go`'s `TestVideoRoutes_FullFlowWithValidToken` already uses.
- *Alternative considered*: spin up a real PostgreSQL for `main_test.go` (matching production more closely). Rejected — it would require wiring `IDENTITY_POSTGRES_DSN` into CI for a suite whose actual subject (ffmpeg/zip pipeline) has nothing to do with the identity storage layer, and `identity_test.go`'s existing repository-level tests already cover the real PostgreSQL adapter (`internal/identity/infrastructure/postgres/repository_test.go`) separately.

**Decision: remove `TestSetupRouter_IdentityRoutesNotRegisteredWithoutModule` and `TestSetupIdentity_NeitherConfigured_ReturnsNilModuleNoError`; add their replacements asserting startup failure instead.**
Both tests currently assert the exact behavior this change removes. `TestSetupIdentity_NeitherConfigured_ReturnsNilModuleNoError` (`identity_test.go:763`) is replaced by a test asserting `setupIdentity` returns a non-nil error when both variables are unset. `TestSetupRouter_IdentityRoutesNotRegisteredWithoutModule` (`identity_test.go:411`) is deleted outright — there's no longer a `setupRouter()` no-identity entry point to exercise it against.

**Decision: `main_test.go`'s cleanup helpers remove `.owner` sidecar files, not just the artifact they annotate.**
`assertTempDirClean` and the `filepath.Glob` leftover checks in `main_test.go` (currently matching only the video/zip filename pattern) are extended to also account for the `<artifact>.owner` sidecar that `recordArtifactOwner` now writes alongside every artifact created by an authenticated request. This keeps `uploads/`/`outputs/` in the same clean state after each test run that the pre-change suite already guaranteed, rather than silently accumulating sidecar files run over run.
- *Alternative considered*: leave sidecar cleanup unaddressed since it doesn't fail any existing assertion today. Rejected — CI runs the suite repeatedly against the same checked-out `uploads/`/`outputs/` directories, so unbounded accumulation is a real (if slow) resource leak, and it's inconsistent with this project's existing discipline of asserting on leftover state (see `assertTempDirClean`, the `TestProcessing_*_CleansTempDir` tests).

## Risks / Trade-offs

- **[Risk] Any deployment currently relying on the unauthenticated fallback (e.g. a `go run .` without exported env vars) breaks on upgrade.** → Mitigation: this is the explicit intent of the change (tracked as a design-mistake correction in `docs/roadmap.md`); the finalization PR updates all affected documentation so the new hard requirement is discoverable, and `docker-compose.yml`'s services are already correctly configured today so the primary documented local workflow is unaffected.
- **[Risk] Deleting `setupRouter()` could break other test files that reference it.** → Mitigation: grep confirms `main_test.go` is the only caller; the design's task list includes re-running `go build ./...` and `go vet ./...` after the rename to catch any other reference.
- **[Risk] Authenticated test requests leave `.owner` sidecar files in `uploads/`/`outputs/` that existing cleanup assertions don't check for.** → Mitigation: covered by the sidecar-cleanup decision above and its own implementation task.

## Migration Plan

1. Update `setupIdentity` to remove the both-missing special case.
2. Update `setupRouterWithIdentity` to drop the nil-module branches; delete `setupRouter()`.
3. Update `main.go`'s startup log line (the `identity == nil` branch becomes unreachable and is removed).
4. Update `main_test.go` to build a configured test `identityModule` and attach bearer tokens to every request.
5. Extend `main_test.go`'s cleanup assertions to remove/account for `.owner` sidecar files.
6. Replace the two identity tests whose assertions describe the removed behavior.
7. Run `go vet ./...` and `go test ./... -v` locally to confirm the full suite passes against the new mandatory-config behavior.

No data migration or rollback beyond a standard revert — this changes only startup validation and route wiring, no persisted state or schema.

## Open Questions

None — the scope is fully bounded by the existing `setupIdentity`/`setupRouterWithIdentity` functions and their direct test coverage.
