## Context

`/opsx:explore` started from a validation report finding — "Desenvolvimento de microsserviços ⚠️ Parcial: um módulo Go, uma imagem Docker com três binários, `internal/` compartilhado, um servidor PostgreSQL" — and the exploration disagreed with three quarters of it. One module and one image are packaging decisions with a stated reason (`container-image`: the three binaries share every internal package, so a split image lets halves of one deploy drift). The shared `internal/` is not shared across contexts: measured with `go list -deps`, `cmd/worker` and `cmd/notifier` have exactly one package in common, `internal/platform/rabbitmq`, which is connection plumbing that `ddd-architecture` puts there deliberately.

What the report is right about is the part it states least directly. `cmd/api` is one process serving three bounded contexts. It opens three PostgreSQL pools, wires Redis, MinIO and RabbitMQ, and registers `/api/auth/*`, the video routes and `/api/notification-preferences` on one `gin` router behind one `http.Server`. Every consequence of a monolith follows from that and from nothing else in the report's list: the contexts cannot be scaled, deployed, restarted or failed independently, and the union of their configuration is the startup requirement of all three.

The user chose this option explicitly over the alternatives (leave as is and argue the packaging; split the image only; split the module into three), and settled three decisions during the exploration: nginx as the ingress, RS256 for tokens, and the public key distributed by configuration rather than by JWKS.

## Goals / Non-Goals

**Goals:**

- Each bounded context's HTTP surface runs as its own process, requiring only its own configuration, and can be built, deployed, scaled and restarted without the others.
- No HTTP service other than Identity can mint an access token.
- The external contract is byte-identical: same paths, same methods, same status codes, same bodies, same host port. The frontend is not modified.
- The cross-context contract pins survive the loss of the composition root that justified them.

**Non-Goals:**

- Splitting the Go module, the repository, or the image. `container-image`'s reasoning is unchanged by this change and is not being revisited.
- Touching `cmd/worker` or `cmd/notifier`. They are already single-context services.
- Service discovery, a service mesh, mTLS between services, or distributed tracing. The gateway routes by path prefix against compose DNS names; anything more is a deployment concern this repository does not have.
- A JWKS endpoint. See the decision below.
- Splitting the databases. `separate-context-databases` owns that and lands first.
- Any per-service rate-limit budget. The counter stays shared.

## Decisions

### Three `main` packages, not one binary with a mode flag

`cmd/identity-api`, `cmd/video-api`, `cmd/notification-api`. `ddd-architecture` already requires that each entrypoint "wire its own composition root requiring only the configuration it uses, rather than one binary switching behavior on a mode flag", and that rule is what makes the split worth doing: the point is that `cmd/identity-api` cannot be given a MinIO endpoint because it has nowhere to put one, not that it chooses not to read it.

The per-context handler files already exist and are already separated — `identity.go`, `video.go`, `notification.go` — so the move is mostly mechanical. What actually divides is `main.go`, which today holds one `setupRouter`, one signal context, and one shutdown sequence whose ordering is load-bearing. Each service gets its own. Only `cmd/video-api` inherits the ordering constraint that made the original sequence non-alphabetical: `server.Shutdown` → cancel the outbox relay → **join** its goroutine → close the video pool → close Redis. `cmd/identity-api` has a pool and nothing that borrows it; `cmd/notification-api` has a pool and Redis and, as `notification-persistence` already records, nothing that holds either for a whole operation.

### nginx at the edge, and the two defaults that would break uploads

A `gateway` service publishes `127.0.0.1:8080:8080` and is the only service that publishes anything. Routing is by path prefix:

```
                        ┌──────────────────────────┐
   127.0.0.1:8080 ─────▶│  gateway (nginx)         │
                        └───┬──────┬───────────┬───┘
        /api/auth/*         │      │           │   /api/notification-preferences
              ┌─────────────┘      │           └──────────────┐
              ▼                    ▼                          ▼
      ┌───────────────┐   ┌─────────────────┐        ┌──────────────────────┐
      │ identity-api  │   │   video-api     │        │  notification-api    │
      │  :8080        │   │    :8080        │        │      :8080           │
      │  identity DB  │   │ video DB, Redis │        │ notification DB,     │
      │  private key  │   │ MinIO, RabbitMQ │        │ Redis, public key    │
      │  public key   │   │ public key, web │        │                      │
      └───────────────┘   └─────────────────┘        └──────────────────────┘
                                   │  everything else: / /styles.css /app.js
                                   │  /upload /download/* /api/status /api/video-jobs*
```

`video-api` takes the default route rather than an enumerated one, because it owns the frontend and because `/download/:filename` and `/api/status` have no shared prefix with anything else. Identity and Notification are the two exact-prefix rules.

Two nginx defaults are wrong for this system and are written into the spec rather than left to a config file review:

- **`client_max_body_size` defaults to 1 MB.** Every upload larger than that returns `413` from the gateway, before `cmd/video-api` sees the request, so the extension check and the streaming hash never run. `upload-file-validation` and `videojob-source-storage` both describe behavior that would simply not occur.
- **`proxy_request_buffering` defaults to `on`.** nginx would read the entire request body to its own disk before opening a connection to `video-api`. `videojob-source-storage` requires that `POST /upload` stream into the bucket while hashing on the same pass, with "nothing touching local disk on the way in"; buffering reintroduces exactly that write one layer up, where no code in this repository would clean it up. It is turned off.

A third default is wrong for a different reason, and it is the one that would discredit the whole change. **nginx resolves a hostname written literally in `proxy_pass` once, when it loads its configuration, and caches the result for the life of the worker.** Recreating a backend container gives it a new address; the gateway keeps sending traffic to the old one until it is reloaded. A change whose central claim is that each service can be deployed and restarted on its own cannot ship a gateway that must be restarted whenever one of them is — and the failure presents as the backend being down, not as the gateway being stale. The configuration therefore uses Docker's embedded resolver with the upstream address in a variable (`resolver 127.0.0.11 valid=10s;` and `set $backend "video-api:8080"; proxy_pass http://$backend;`), which defers resolution to request time. Verification recreates each backend without touching the gateway, because no other check in the suite distinguishes the two forms.

Response buffering is left at its default: `GET /download/:filename` returns a small JSON document naming a presigned URL, and the bytes it authorizes are fetched from MinIO directly, so no large response ever passes through the gateway.

### RS256, and why the public key comes from configuration

The current `jwtauth.Adapter` holds one `[]byte` and both signs and verifies with it. Copying that into three services would hand `cmd/video-api` and `cmd/notification-api` the ability to issue a token for any user — a capability they have no use for, and one that is invisible today only because there is a single process. The split is what makes it a real privilege, so the split is where it is removed.

`jwtauth` divides into an issuer built from a PKCS#8 private key and a key id, and a verifier built from a map of key id to public key. The **configuration** mirrors that division rather than the key pair: `IDENTITY_JWT_PRIVATE_KEY` and `IDENTITY_JWT_KEY_ID` are Identity's alone, and `IDENTITY_JWT_PUBLIC_KEYS` carries a set of `kid`-to-PEM entries that every verifying service reads. A single public-key variable would make the rotation described below impossible in either order — verifier first rejects every token in circulation, issuer first mints tokens nothing accepts — so the set exists from the first commit rather than being widened later, since the widening would itself need the window it is there to remove. Tokens carry `kid` from the first commit even though there is one key: adding it later means every verifier must accept unkeyed tokens during the migration, which is the state a rotation is supposed to avoid. `jwt.WithValidMethods` continues to pin the algorithm — now `RS256` — so a token presenting `alg: none` or `alg: HS256` is rejected before any key lookup, which matters more with asymmetric keys than it did with symmetric ones, since the public key is not a secret and an unpinned verifier would accept it as an HMAC key.

**Why configuration and not JWKS.** JWKS is the conventional answer and it is the wrong one here. Each verifier would fetch Identity's key set at startup or on cache miss, which makes Identity's availability a precondition for every other service's authorization decision — a synchronous, runtime coupling, reintroduced at exactly the moment the change removes the compile-time one. A public key in the environment gives a property most real microservice deployments do not have: `cmd/video-api` can verify tokens with `cmd/identity-api` down. The cost is that rotation is a coordinated configuration change rather than an automatic refresh, which the `kid` set makes a two-deploy operation (publish both keys, then switch the issuer, then drop the old one) rather than a flag day. JWKS remains the documented alternative if a key set ever has to change without a deploy.

The verifier refuses a private key and refuses an empty set, so a service misconfigured with Identity's full key pair fails at startup rather than starting with minting capability it was not meant to have. That refusal is a property of the material handed to `NewVerifier`, not of what the process holds: `cmd/identity-api` has a private key in its environment and hands it only to `NewIssuer`.

It also holds the public key, which looks like dead configuration in a service that registers no authenticated route and is why the reason is written here as well as in the spec: startup checks that the two halves are one pair. A mismatched pair is otherwise silent in the one place it could be caught and loud everywhere it cannot be attributed — Identity mints successfully, every other service rejects every token, and the failure appears to belong to the services that are correct.

### Extraction is copy-then-delete, and the middlewares are the reason

A composition root is `package main`, so nothing in it can be imported by another one, and `cmd/api` goes on serving whatever has not left yet. Two files make that concrete. `requireBearerAuth` and `authenticatedUserID` live in `cmd/api/identity.go` but are used by the video and notification routes; `rateLimitMiddleware` lives in `cmd/api/ratelimit.go` and is used by both groups. Moving either out with the first service to leave would break the process still serving the rest. So each group *writes* its service's files from `cmd/api`'s and removes from `cmd/api` only the route registrations that just moved, with `cmd/api` deleted whole in the last group. The transient duplication resolves to one thin gin wrapper per HTTP service — which is what `rateLimitMiddleware` already is today, over the shared `internal/platform/ratelimit`. The security-carrying half of authentication (algorithm pinning, `kid` lookup, indistinguishable rejection) is not duplicated: it stays in `internal/identity/infrastructure/jwtauth`, and what each service carries is the ~30 lines that read a header and set a value on the request context.

The test suites are the same problem, sharper. Every `_test.go` under `cmd/api` uses fixtures declared in the others — `identity_test.go` calls `newTestVideoModule`, `newNoopNotificationModule` and `alwaysAllowRateLimiter`; `notification_test.go` calls `newTestIdentityModuleWithTokens` and `alwaysAllowRateLimiter`; `video_test.go` calls all of them. That is a natural consequence of one router with three modules, and it means no suite can be relocated as a file. Each is rewritten against the fixtures its own service needs, which for the two verifying services means minting a token with the test private key rather than building an identity module at all.

### `internal/contracts`, a package with no production code

Three tests in `cmd/api/notification_test.go` import both `internal/video/infrastructure/{postgres,messaging}` and `internal/notification/...` to assert that Notification's copies of the terminal event types, the topology and the payload structs still equal Video Processing's originals. `notification-preferences` and `notification-event-consumer` both justify them by their location — "a test in the composition root, which legitimately sees both contexts". After the split there is no such root, and the alternatives are worse:

| Option | Why not |
| --- | --- |
| Put them in `cmd/notification-api` | It would import `internal/video` in a test file, which is the import `internal/notification/dependency_rules_test.go` exists to forbid and which a reader would reasonably copy into production code |
| Put them in `cmd/video-api` | Same problem, mirrored, and the copies being pinned are Notification's |
| Drop them | The drift they catch is silent by construction: a renamed exchange leaves the notifier bound to a queue nothing publishes to, and a renamed payload field decodes as its zero value |
| Generate both sides from one schema | A real answer, and a much larger change than this one. Not proposed here |

So: `internal/contracts`, containing a `doc.go` with a package comment and nothing else, plus the moved test files. It is not a bounded context, defines no types, and is imported by nothing. `ddd-architecture`'s dependency rule currently enumerates the places where a cross-context translation may happen (`cmd/api`, `cmd/worker`, `main.go`); it is amended to admit this package explicitly, with the constraint that makes it safe — it contains no non-test declarations, which a test in the package itself asserts. Without the amendment the package is a spec violation, and a `doc.go` does not fix a spec violation. That emptiness is also what makes the package undependable rather than merely undepended-on: a package exporting nothing cannot be imported for a symbol. The one thing it still permits is a blank import, so the same test also walks `cmd/` and `internal/` and asserts nobody imports the path at all.

It is extracted **before** `cmd/api` is deleted, not after. The tests being moved live in `cmd/api/notification_test.go`; taking the package out first would produce an implementation PR in which the contract guards are gone and the following group has nothing left to move.

`TestTheHTTPCompositionRootDoesNotLoadTheSecret` is a different case and does **not** move. It reads the sources of the HTTP composition root and asserts none of them calls `FindDeliverable`. Its subject becomes `cmd/notification-api`, and the claim it makes gets stronger rather than weaker: the other two services no longer link `internal/notification/infrastructure/postgres` at all, so the property they used to be checked for is now enforced by the compiler.

### The shared rate-limit counter

`internal/platform/ratelimit` is a Redis-backed fixed-window counter keyed by user. Both `cmd/video-api` and `cmd/notification-api` mount it, against the same Redis, with the same key format — so 60 requests per minute stays 60 across the system. Namespacing per service would silently triple every user's budget, which is a behavior change nobody asked for hiding inside a refactor. `cmd/identity-api` mounts nothing, matching today: the two `/api/auth/*` routes are unauthenticated and have no user to key on.

One counter is not one budget on its own: `RATE_LIMIT_MAX_REQUESTS` and `RATE_LIMIT_WINDOW_SECONDS` are read per process, so two services configured differently would compare one shared count against two thresholds, and the window's length would be decided by whichever service's request created the key. The effective limit would then depend on which route a user's requests took and in what order. The values are therefore supplied to both services from one place in the deployment configuration, and the verification checks that rather than assuming it.

This is why `cmd/notification-api` requires `REDIS_ADDR` despite owning no cache and no idempotency store. It is a genuine dependency of the middleware, not a leftover.

### Tests: three suites, and no cross-service Go test

`cmd/api`'s 4863 test lines divide with their handlers — `identity_test.go` and `ratelimit_test.go`'s identity-adjacent cases to `cmd/identity-api`, `video_test.go` to `cmd/video-api`, `notification_test.go` to `cmd/notification-api` — with `main_test.go` splitting three ways, since each service needs its own `TestMain` gate over the prerequisites it actually uses (`cmd/identity-api`'s no longer requires `ffmpeg` or MinIO, which is the clearest evidence the split is real).

The flows that today register, log in, and then call a protected route become **single-service** tests that mint a token directly with the test private key. That is not a coverage loss dressed up: the assertion those tests make is that a protected route accepts a valid token and rejects an invalid one, and calling `/api/auth/login` first to obtain the token tested Identity's route a second time inside Video Processing's suite. Signing directly is the more honest fixture, and it is only available because the token is asymmetric. The end-to-end path through the gateway — register, log in, upload, poll, download — is verified as a compose-level curl sequence, which is what `ddd-architecture`'s "Full-flow non-regression passes at each phase" scenario already requires of a change like this one.

### The spec sweep that is not a delta

`cmd/api` appears 72 times across 15 canonical specs. Eight capabilities have requirements whose *meaning* changes, and those carry delta specs. The rest — `videojob-result-storage`, `videojob-outbox-relay`, `videojob-worker`, `videojob-source-storage`, `rabbitmq-infrastructure`, `videojob-terminal-events`, `minio-infrastructure`, `videojob-lifecycle`, plus `videojob-http-api`'s Purpose line — name `cmd/api` as the process that happens to open a MinIO client, run a relay, or not run one. Substituting `cmd/video-api` leaves every one of those requirements saying exactly what it says now.

Copying roughly 28 requirements verbatim into `specs/` so that a name can be substituted inside them would make the deltas that carry real requirement changes unreviewable, which is the opposite of what a delta is for. They are handled instead as a documentation-accuracy sweep in the finalization PR, alongside `docs/` and `CLAUDE.md`, which `development-workflow` already scopes that PR to. Task 8.2 lists them so the sweep is a checklist rather than a grep.

## Risks / Trade-offs

- **The implementation PR is large.** ~6500 lines move. Most of it is mechanical relocation, and `git` will render much of it as renames, but the review burden is real. The documented fallback seam is to land RS256 first as its own change — `jwtauth` splitting into issuer and verifier, `cmd/api` using both, no topology change — and then split against an already-asymmetric token. That halves the largest PR at the cost of one extra propose/apply/finalize cycle. Taken as a fallback rather than the plan because the interim state it creates is fine only in the *ordering* RS256-first gives; the reverse order would put a shared HMAC key in three processes.
- **A deploy invalidates every issued token.** Deliberate. A dual-verification window would require every service to carry the HMAC verifier and therefore the symmetric key, which is the capability being removed. Users log in again.
- **The gateway is a new single point of failure.** It is also the only thing binding the host port, which is what lets the three services scale. `docker-compose.yml` is a local development stack; a production ingress is `docs/operations.md`'s concern and is out of scope here.
- **Three services, one PostgreSQL server.** Per the user's constraint, and per `separate-context-databases`: one server, one database per context. The engine boundary is the database, so nothing schema-qualified crosses it.
- **`docker compose up` gets slower and heavier.** Five application containers plus nginx, PostgreSQL, Redis, MinIO and RabbitMQ. The image is built once and run five times, so the build cost is unchanged; the memory cost is three Go HTTP servers instead of one.

## Migration Plan

1. `separate-context-databases` merges (proposal, implementation, finalization).
2. RS256 lands as part of group 1 below, with `cmd/api` still whole — so the token change is provably isolated from the routing change, and a failure in either is attributable.
3. The three services are extracted one at a time, in dependency order (Identity first, since the other two verify its tokens), each behind the gateway. `internal/contracts` is extracted after Notification and before Video Processing, so the cross-context pin tests leave `cmd/api` before `cmd/api` is deleted; the deletion happens in the last of these steps, with Video Processing.
4. The end-to-end curl sequence runs against the gateway before the change is reported complete.

There is no rollback beyond `git revert`: no data changes, so reverting the code reverts the system.

## Open Questions

- Whether `cmd/identity-api` should also mount a rate limiter for `/api/auth/login`. It has no user to key on, so it would have to key on the client address, which is a different mechanism than `internal/platform/ratelimit` implements and a different threat (credential stuffing) than `rate-limiting` addresses. Out of scope; noted as a candidate for its own change.
- Whether the gateway should terminate CORS instead of each service. Left per service, unchanged, because moving it would make the split observable to a browser client for the first time.
