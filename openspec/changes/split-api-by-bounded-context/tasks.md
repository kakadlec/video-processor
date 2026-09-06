# Tasks

Implementation order is incremental and keeps the stack working after every group: the token change lands while `cmd/api` is still whole, then the gateway appears in front of it, then each context's service is extracted from behind the gateway one at a time, with `cmd/api` deleted only when the last one leaves. Each numbered group 1–6 is sized to be one implementation PR. Group 7 is verification and belongs to the last implementation PR; group 8 is finalization and belongs to the change's closure PR.

**Prerequisite:** `separate-context-databases` is merged and finalized before group 1 begins. Do not start against a single shared `identity` database.

## 1. Asymmetric access tokens (`cmd/api` still whole)

- [ ] 1.1 Split `internal/identity/infrastructure/jwtauth` into two constructors over the same package: `NewIssuer(privateKeyPEM string, keyID string) (Issuer, error)` parsing a PKCS#8 RSA private key, and `NewVerifier(publicKeysByKeyID map[string]string) (Verifier, error)` parsing PEM public keys. `Issuer` implements `domain.TokenIssuer`; `Verifier` implements `domain.TokenVerifier`. Neither type may satisfy both interfaces — that is the whole point, and it should be a compile error rather than a convention.
- [ ] 1.2 `signingMethod` becomes `jwt.SigningMethodRS256`, and `jwt.WithValidMethods` keeps pinning it by name. Pinning matters more here than it did with HMAC: the public key is not secret, so an unpinned verifier would accept it as an HMAC key and validate a token an attacker signed. Add a test that a token presenting `alg: HS256` signed with the public key's PEM bytes is rejected.
- [ ] 1.3 `Issue` sets the `kid` header from the issuer's key id. `Verify` reads `kid` from the token header, looks it up in the verifier's map, and rejects a token whose `kid` is missing or unknown as `domain.ErrInvalidToken` — the same error every other invalid token returns, so a caller cannot probe which key ids exist.
- [ ] 1.4 `NewVerifier` refuses an empty map and refuses PEM that parses as a *private* key. The refusal is scoped to the PEM handed to `NewVerifier`, not to whether the process holds a private key anywhere: `cmd/identity-api` legitimately has one in its environment and passes it only to `NewIssuer`. What must fail at startup is a service handed the full key pair *as its verification material*, because that service is one line away from being able to mint.
- [ ] 1.5 Replace `IDENTITY_JWT_SIGNING_KEY` with `IDENTITY_JWT_PRIVATE_KEY`, `IDENTITY_JWT_PUBLIC_KEY` and `IDENTITY_JWT_KEY_ID` in `cmd/api/identity.go`'s configuration loading. All three required; startup fails clearly when any is absent or malformed, with no fallback key of any kind. Identity requires the public key even though it registers no bearer-authenticated route, and the reason has to be written down or it will be deleted as dead configuration: startup asserts the two halves are one pair. A mismatched pair is silent otherwise — Identity mints happily and every other service rejects every token it issues, which reads as an authentication bug anywhere but here.
- [ ] 1.6 Generate a fixed development key pair and set the three variables on `app` and `app-test` in `docker-compose.yml`, replacing `local-dev-signing-key`. It is local-only material and belongs in the repository for the same reason the compose PostgreSQL password does; name it as such in the compose comment so nobody promotes it.
- [ ] 1.7 Test fixtures: one helper that generates a key pair once per test binary and returns an issuer and a verifier over it. Existing identity tests keep asserting what they assert; only their construction changes.
- [ ] 1.8 `go vet ./...` and `go test ./... -v` pass. The whole suite still runs against one `cmd/api`.

## 2. The gateway, in front of the unsplit API

- [ ] 2.1 `docker/nginx/nginx.conf`: one `server` listening on 8080, `proxy_pass` to `app:8080` for everything. No routing decisions yet — this group proves the hop is transparent before anything depends on it being selective.
- [ ] 2.2 `client_max_body_size 0;` (or an explicit bound above the largest upload the stack is expected to accept — not the 1 MB default, which returns `413` before the extension check runs).
- [ ] 2.3 `proxy_request_buffering off;`. The default spools the whole request body to the gateway's disk before opening the upstream connection, which reintroduces the local-disk write `videojob-source-storage` requires `POST /upload` not to make. Leave `proxy_buffering` (responses) at its default — `GET /download/:filename` returns a small JSON body and the bytes go straight from MinIO to the client.
- [ ] 2.4 Set `proxy_read_timeout`/`proxy_send_timeout` above the longest legitimate request. `POST /upload` returns `202` as soon as the job is queued, so the bound is upload transfer time, not `ffmpeg` time.
- [ ] 2.5 Forward `X-Forwarded-For`/`X-Forwarded-Proto`/`Host`. Nothing reads them today; omitting them makes the first thing that does silently wrong.
- [ ] 2.6 `docker-compose.yml`: add a `gateway` service on the stock nginx image, mounting the config read-only, publishing `127.0.0.1:8080:8080`, depending on `app`. Remove `app`'s `ports` block — from here on, only the gateway publishes.
- [ ] 2.7 Verify the full documented flow through the gateway with `cmd/api` unchanged behind it: register, log in, `POST /upload` with a real video, poll `GET /api/video-jobs/:id`, `GET /api/status`, `GET /download/:filename`, and redeem the presigned URL. Verify an upload larger than 1 MB succeeds — that is the assertion 2.2 exists for, and the default would pass every other step in this list.

## 3. `cmd/identity-api`

- [ ] 3.1 New `cmd/identity-api/` (package `main`): `main.go` (configuration, pool, router, `http.Server`, `signal.NotifyContext`, shutdown) and `identity.go` moved from `cmd/api/`. It requires `IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_PRIVATE_KEY`, `IDENTITY_JWT_PUBLIC_KEY` and `IDENTITY_JWT_KEY_ID` and reads nothing else. No Redis, no MinIO, no broker, no `ffmpeg`.
- [ ] 3.2 Routes: `POST /api/auth/register` and `POST /api/auth/login`, plus the same permissive CORS middleware. No bearer-auth group, no rate limiter — matching what `cmd/api` does for these two routes today.
- [ ] 3.3 Shutdown: `server.Shutdown` then close the pool. Nothing borrows the pool for a whole operation, so there is no ordering constraint to preserve here; say so in a comment rather than copying `cmd/api`'s sequence and leaving a reader to wonder what it is ordered against.
- [ ] 3.4 Move `cmd/api/identity_test.go` to `cmd/identity-api/`, with its own `TestMain` requiring only `IDENTITY_POSTGRES_TEST_DSN`. Drop the `ffmpeg` and `VIDEO_MINIO_*` gate — this suite genuinely does not need them, and keeping the gate would be the loudest possible way of not splitting.
- [ ] 3.5 `cmd/api` keeps its `/api/auth/*` routes for now; the gateway routes `/api/auth/` to `identity-api` and everything else to `app`. Both answer; the gateway decides. Remove the routes from `cmd/api` in this same group once the gateway is switched, so there is no window where two processes serve the same path from two databases.
- [ ] 3.6 `Dockerfile`: build `/app/identity-api` in the builder stage and copy it into the runtime stage. `docker-compose.yml`: an `identity-api` service from the same image with `command: ["/app/identity-api"]`, no `ports`, with only the four identity variables.
- [ ] 3.7 `go vet ./...`, `go test ./... -v`, and the group 2.7 flow all pass.

## 4. `cmd/notification-api`

- [ ] 4.1 New `cmd/notification-api/` (package `main`): `main.go`, plus `notification.go` and `ratelimit.go` moved from `cmd/api/`. It requires `NOTIFICATION_POSTGRES_DSN`, `REDIS_ADDR`, `IDENTITY_JWT_PUBLIC_KEY`, `IDENTITY_JWT_KEY_ID`, and reads `NOTIFICATION_ALLOW_INSECURE_DESTINATIONS` and `RATE_LIMIT_*` as optional. It holds a **verifier only** — there is no code path in this binary that can construct an issuer.
- [ ] 4.2 `REDIS_ADDR` is required because the preference routes are rate limited, not because this service caches anything. Comment it at the point it is loaded; it looks like a copy-paste otherwise and would be "cleaned up".
- [ ] 4.3 Routes: `GET`/`PUT /api/notification-preferences` on one group carrying `requireBearerAuth()` then `rateLimitMiddleware`, in that order — the invariant is the pair and its order, which `cmd/api` states and this service now carries alone. CORS advertises `GET, POST, PUT, OPTIONS` unchanged; the preflight test moves with the routes.
- [ ] 4.4 The destination policy is loaded here through the same `webhook.LoadDestinationPolicyFromEnv` that `cmd/notifier` uses. One parser, two readers — unchanged, and now across two processes that are separately deployable, which is the case the single-parser rule was written for.
- [ ] 4.5 Move `cmd/api/notification_test.go` (minus the three cross-context pin tests, which are group 6) and `cmd/api/ratelimit_test.go` to `cmd/notification-api/`, with a `TestMain` requiring `NOTIFICATION_POSTGRES_TEST_DSN` and `REDIS_ADDR`.
- [ ] 4.6 Re-target `TestTheHTTPCompositionRootDoesNotLoadTheSecret` at `cmd/notification-api`'s own sources. It does not move to `internal/contracts`: it needs no Video Processing import, and its claim is strictly stronger here, since the other two services no longer link `internal/notification/infrastructure/postgres` at all.
- [ ] 4.7 Remove the preference routes from `cmd/api` and add the gateway rule for `/api/notification-preferences` in the same group, for the reason 3.5 gives.
- [ ] 4.8 `Dockerfile` builds `/app/notification-api`; `docker-compose.yml` gains the service, no `ports`.
- [ ] 4.9 `go vet ./...`, `go test ./... -v`, and the group 2.7 flow all pass.

## 5. `cmd/video-api`, and the end of `cmd/api`

- [ ] 5.1 New `cmd/video-api/` (package `main`): `main.go`, plus `video.go` and a second copy of `ratelimit.go` moved from `cmd/api/`. It requires `VIDEO_POSTGRES_DSN`, `REDIS_ADDR`, the four `VIDEO_MINIO_*` variables, `RABBITMQ_URL`, `IDENTITY_JWT_PUBLIC_KEY` and `IDENTITY_JWT_KEY_ID`. Verifier only, same as group 4.
- [ ] 5.2 Move `cmd/api/web/` to `cmd/video-api/web/` with its `go:embed` intact. `GET /`, `GET /styles.css` and `GET /app.js` are served by this service and reached through the gateway's default route. `app.js` is **not** modified: every URL it calls keeps its path and its origin. Confirm that by reading it, not by assuming it.
- [ ] 5.3 The dispatch outbox relay moves here unchanged, and with it the shutdown ordering that is not alphabetical: `server.Shutdown` → cancel the relay → **join** its goroutine → close the video pool → close Redis. The relay holds a transaction while it runs, so closing the pool before joining aborts an in-flight claim. Carry the existing comment; it is the only place in the new topology where the constraint still applies.
- [ ] 5.4 Routes: `GET /`, `POST /upload`, `GET /download/:filename`, `GET /api/status`, `POST /api/video-jobs`, `GET /api/video-jobs/:id`, `GET /api/video-jobs` — the bearer-auth group carrying `requireBearerAuth()` then `rateLimitMiddleware`, and the static assets outside it.
- [ ] 5.5 `GET /download/:filename`'s entitlement lookup keeps using the **undecorated** PostgreSQL repository rather than the cached one, unchanged. It is easy to lose in a move because it looks like an inconsistency; it is the fix for a stale `processing` cache entry 404-ing a result `GET /api/status` is already listing.
- [ ] 5.6 Move `cmd/api/video_test.go` to `cmd/video-api/`, with a `TestMain` requiring `ffmpeg`, `VIDEO_MINIO_*`, `VIDEO_POSTGRES_TEST_DSN`, `REDIS_ADDR` and `RABBITMQ_URL` — the same fail-with-exit-1 posture, since `POST /upload` still stores into a bucket and a skipped suite would report green while covering none of it.
- [ ] 5.7 Rewrite the register→login→protected-route fixtures in this suite to mint a token directly with the test private key instead of calling `/api/auth/login`. The assertion is that a protected route accepts a valid token; obtaining it through Identity's route tested Identity twice and is not available across a process boundary anyway.
- [ ] 5.8 Delete `cmd/api/` entirely — `main.go`, `main_test.go`, and anything left. Distribute whatever remains of `main_test.go` into the three suites first; nothing in it may be dropped silently.
- [ ] 5.9 `Dockerfile` builds `/app/video-api`; remove `/app/app`. `docker-compose.yml`: remove the `app` service, add `video-api`, and repoint `app-test` (which builds the test stage and runs the whole module's suite — its role is unchanged, but its environment now needs every context's test DSN).
- [ ] 5.10 The gateway's default route now points at `video-api`. No path in `nginx.conf` still names `app`.
- [ ] 5.11 `go vet ./...`, `go test ./... -v`, and the group 2.7 flow all pass.

## 6. `internal/contracts`

- [ ] 6.1 New `internal/contracts/doc.go`: the `package contracts` clause (a package comment alone is not a Go file) and above it a comment stating what the package is for — pinning cross-context integration contracts that no production process can pin any more — and that it contains no non-test declarations. Nothing else in the file.
- [ ] 6.2 Move `TestNotificationEventTypesMatchTheEmittedTerminalEventTypes`, `TestNotificationTerminalTopologyMatchesTheEmittedTopology` and `TestNotificationTerminalMessagesDecodeTheEmittedPayloads` here from the old `cmd/api/notification_test.go`, unchanged in what they assert.
- [ ] 6.3 Add a test in the package asserting the package itself declares nothing outside `_test.go` files — AST-walk its own directory, in the spirit of `internal/notification/dependency_rules_test.go`. Without it, "test-only" is a comment, and the exemption `ddd-architecture` grants this package is conditional on it being true.
- [ ] 6.4 Confirm `internal/notification/dependency_rules_test.go` still passes unmodified: `internal/contracts` is not under `internal/notification/`, so the AST walk does not reach it and must not be widened to.
- [ ] 6.5 `go vet ./...` and `go test ./... -v` pass.

## 7. Verification (last implementation PR)

- [ ] 7.1 `go build ./cmd/identity-api`, `./cmd/video-api`, `./cmd/notification-api`, `./cmd/worker`, `./cmd/notifier` — five independent binaries, each built alone.
- [ ] 7.2 Start each of the three HTTP services on its own with only its own configuration and confirm it serves its routes. Then start each with a variable belonging to another service **removed** and confirm the other two are unaffected — the assertion is independence, not just startup.
- [ ] 7.3 Confirm `cmd/video-api` and `cmd/notification-api` cannot mint: grep proves nothing, so assert it by construction — neither binary's build graph contains a call to the issuer constructor, and `NewVerifier` refuses a private-key PEM (task 1.4).
- [ ] 7.4 With `identity-api` **stopped**, confirm a previously-issued, unexpired token is still accepted by `cmd/video-api`. This is the property the configuration-distributed public key buys and the one JWKS would have cost; if it does not hold, the decision in `design.md` is wrong and the change is not done.
- [ ] 7.5 Full flow through the gateway on a clean `docker compose up --build`: register → login → upload a video larger than 1 MB → poll → status → download → redeem the presigned URL, in a browser as well as by curl, since the frontend is `go:embed`ed into a service that moved.
- [ ] 7.6 Confirm three workers still process three distinct videos concurrently (`run-multiple-workers-by-default`'s scenario) — nothing in this change touches the worker, and that is the assertion.
- [ ] 7.7 Confirm the per-user rate-limit budget is shared: exhaust it on `GET /api/status` and confirm `GET /api/notification-preferences` is refused too, from a different process against the same counter.
- [ ] 7.8 Confirm the three `TestMain`s together still satisfy `development-workflow`'s `Missing Test Prerequisites Must Cause a Non-Zero Exit`: run `go test ./...` with `ffmpeg` off `PATH`, then again with each context's test DSN removed in turn, and confirm each run exits non-zero naming the missing prerequisite. The requirement is written against `go test ./...`'s exit code rather than against any one `TestMain`, so splitting one gate into three satisfies it — but only if all three actually fail loudly, which is the thing to check rather than assume.
- [ ] 7.9 `gosec ./...` and `govulncheck ./...` clean.

## 8. Finalization

- [ ] 8.1 Check off every task above.
- [ ] 8.2 Name sweep across the canonical specs where `cmd/api` appears with no requirement change, per `design.md`: `videojob-result-storage`, `videojob-outbox-relay`, `videojob-worker`, `videojob-source-storage`, `rabbitmq-infrastructure`, `videojob-terminal-events`, `minio-infrastructure`, `videojob-lifecycle`, and `videojob-http-api`'s Purpose line. Substitute the service that actually runs the thing described; do not restate or re-scope a requirement while sweeping it.
- [ ] 8.3 `docs/architecture.md`: the request flow starts at the gateway, and there are five processes.
- [ ] 8.4 `docs/operations.md`: five services, the per-service variable table, the RSA key-pair generation step, the removal of `IDENTITY_JWT_SIGNING_KEY`, and the note that a deploy invalidates issued tokens.
- [ ] 8.5 `docs/development.md`: five `go run` targets and their configuration surfaces; `docker compose run --build --rm app-test go test ./... -v` unchanged.
- [ ] 8.6 `README.md` and `docs/flows.md`: the quickstart and the flow walkthrough, both of which name `IDENTITY_JWT_SIGNING_KEY`.
- [ ] 8.7 `CLAUDE.md`: the composition-root inventory, the request-flow numbering, the rate-limiting bullet, the JWT posture, and the frontend path.
- [ ] 8.8 `docs/roadmap.md`: the change's row.
- [ ] 8.9 `npx --yes @fission-ai/openspec validate split-api-by-bounded-context --strict --no-interactive` passes, then `/opsx:archive`.
