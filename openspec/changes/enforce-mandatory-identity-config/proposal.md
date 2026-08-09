## Why

Phase 2 shipped `setupIdentity` so that when neither `IDENTITY_POSTGRES_DSN` nor `IDENTITY_JWT_SIGNING_KEY` is set, the server starts anyway and serves video processing unauthenticated — preserving the pre-Identity workflow. Authentication is a hackathon requirement, not an opt-in feature, so a deployment that simply forgets to set identity configuration silently runs open instead of failing loudly. This was flagged as a design mistake when Phase 2 archived (see `docs/roadmap.md` Change Backlog) and needs correcting before Phase 3 builds further on top of it.

## What Changes

- **BREAKING**: `setupIdentity` no longer returns `(nil, nil, nil)` for the "entirely unconfigured" case. Startup fails with a clear configuration error whenever `IDENTITY_POSTGRES_DSN` or `IDENTITY_JWT_SIGNING_KEY` is missing, matching the existing "partially configured" failure behavior.
- `setupRouterWithIdentity` no longer accepts a nil `identityModule`; the `identity != nil` conditionals that currently make bearer auth and per-artifact ownership checks optional are removed, so every protected route always requires a valid bearer token.
- `main_test.go`'s integration suite, which currently drives `setupRouter()` (identity disabled) directly, needs a configured identity module so the suite keeps exercising real behavior rather than a code path that no longer exists in production.
- Local/Docker workflows that don't set both identity variables (e.g. plain `go run .`) now fail fast at startup instead of quietly running unauthenticated — permanent documentation (`README.md`, `docs/operations.md`, `docs/architecture.md`, `docs/development.md`, `docs/flows.md`) describing the old optional behavior is out of scope for this change's implementation PR and is corrected in the finalization PR per this repo's PR-splitting rule.

## Capabilities

### New Capabilities

(none)

### Modified Capabilities

- `identity-authentication`: the "Configuration does not provide insecure defaults" requirement changes from "entirely unconfigured identity starts without an Identity module" to "identity configuration is always required; any missing or invalid piece fails startup." (The "Authentication protects video-processing access" requirement is already worded unconditionally in the current spec and needs no change.)

## Impact

- **Code**: `identity.go` (`setupIdentity`, `setupRouterWithIdentity`'s `identity != nil` branches), `main.go` (the `identity == nil` log line and nil-check at startup).
- **Tests**: `main_test.go` (currently calls `setupRouter()`, which forwards to `setupRouterWithIdentity(nil)` — needs a real configured `identityModule` and a way to obtain a valid bearer token for existing video-processing assertions).
- **Deployments**: any environment currently running without `IDENTITY_POSTGRES_DSN`/`IDENTITY_JWT_SIGNING_KEY` set (e.g. bare `go run .` without exporting them) stops starting until both are configured. `docker-compose.yml`'s `app`/`app-test` services already set both, so they're unaffected.
- **Docs**: `README.md`, `docs/operations.md`, `docs/architecture.md`, `docs/development.md`, `docs/flows.md` all describe the current optional behavior and will need updates — deferred to this change's finalization PR.
