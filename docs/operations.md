# Operations

## Current Deployment

The application is **five** Go processes built from one image: `cmd/identity-api`, `cmd/video-api` and `cmd/notification-api` (one HTTP service per bounded context, behind a gateway), `cmd/worker` (the frame-extraction consumer and recovery sweeper), and `cmd/notifier` (webhook delivery). None is a prerequisite for another to start, and none switches behaviour on a mode flag — the image's default command runs the Video API, and every other process is started by overriding it (`/app/identity-api`, `/app/notification-api`, `/app/worker`, `/app/notifier`). There is no orchestration. External services are PostgreSQL for authoritative identity and video-job state; Redis for upload idempotency, per-user rate limiting, the non-authoritative status cache, and worker leases; MinIO for source videos and results; and RabbitMQ for dispatch. Every service needs environment-specific configuration — the per-process surface is below.

### Docker

The image is published on every release, so the `docker build` below is one of two ways to obtain it and the slower one:

```bash
docker pull ghcr.io/kakadlec/video-processor:4.0.0   # or :latest
```

`ghcr.io/kakadlec/video-processor` carries all five binaries for `linux/amd64` and `linux/arm64`. A **version tag is immutable** — a published version is never overwritten, and replacing one means deleting it from the registry first, deliberately and by hand — so it is the reference to deploy from. `latest` moves to whatever the highest published version is, and is a convenience for a reader who has no version in hand, not a reproducible reference.

**Publishing is not deploying.** This project has no deployment target, and nothing here rolls the image anywhere: the commands below are still the documented way to run it, whether the image came from a `docker build` or a `docker pull`. Substitute `ghcr.io/kakadlec/video-processor:<version>` for `video-processor` in each of them if you pulled.

Publication happens when `release-please` creates a release; a merge to `main` that bumps no version publishes nothing. A tag that already exists — one released before this pipeline, or one whose publication failed — is published by running the `Release Please` workflow manually with that tag as its input. That path builds the tag's own tree, so a tag predating the multi-platform build emulates the Go toolchain and is slow; the binaries are still correct.

```bash
# Build
docker build -t video-processor .

# One user-defined network. The gateway resolves its upstreams by container
# name through Docker's embedded DNS at 127.0.0.11, which only exists on a
# user-defined network — on the default bridge the names would not resolve
# and every route would 502
docker network create video-processor-net

# Run the Identity API — the only process that holds a private key and so the
# only one that can mint a token. No Redis, no MinIO, no broker. No published
# port: the gateway is the only thing a client reaches
docker run -d --name identity-api --network video-processor-net \
  -e IDENTITY_POSTGRES_DSN="postgres://user:pass@host:5432/identity?sslmode=disable" \
  -e IDENTITY_JWT_PRIVATE_KEY="$(cat identity-private-key.pem)" \
  -e IDENTITY_JWT_KEY_ID="2026-09" \
  -e IDENTITY_JWT_PUBLIC_KEYS='{"2026-09":"-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----\n"}' \
  video-processor /app/identity-api

# Run the Video API — the image's default command, and the service that serves
# the embedded frontend. It verifies tokens and cannot mint one: there is no
# code path in this binary that constructs an issuer, and a private key handed
# to it as IDENTITY_JWT_PUBLIC_KEYS is refused at startup rather than quietly
# accepted
docker run -d --name video-api --network video-processor-net \
  -e IDENTITY_JWT_PUBLIC_KEYS='{"2026-09":"-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----\n"}' \
  -e VIDEO_POSTGRES_DSN="postgres://user:pass@host:5432/video?sslmode=disable" \
  -e REDIS_ADDR="host:6379" \
  -e VIDEO_MINIO_ENDPOINT="host:9000" \
  -e VIDEO_MINIO_ACCESS_KEY="minio-access-key" \
  -e VIDEO_MINIO_SECRET_KEY="minio-secret-key" \
  -e VIDEO_MINIO_BUCKET="video-results" \
  -e RABBITMQ_URL="amqp://user:pass@host:5672/" \
  video-processor

# Run the Notification API — the preference routes and nothing else: a
# verifier, its own DSN, and Redis for the rate-limit counter it shares with
# the other HTTP services. No video database, no bucket, no broker
docker run -d --name notification-api --network video-processor-net \
  -e IDENTITY_JWT_PUBLIC_KEYS='{"2026-09":"-----BEGIN PUBLIC KEY-----\n...\n-----END PUBLIC KEY-----\n"}' \
  -e NOTIFICATION_POSTGRES_DSN="postgres://user:pass@host:5432/notification?sslmode=disable" \
  -e REDIS_ADDR="host:6379" \
  video-processor /app/notification-api

# Run the gateway — the ONLY published port, and not optional. The three
# services above are one origin to a client: the embedded frontend calls
# /api/auth/* and /api/notification-preferences same-origin, so publishing
# any one service directly would answer 404 for the paths the other two own.
# The container names above are the upstreams nginx.conf names
docker run -d --name gateway --network video-processor-net -p 8080:8080 \
  -v "$PWD/docker/nginx/nginx.conf:/etc/nginx/nginx.conf:ro" \
  nginx:1.29-alpine

# Run the worker — same image, different command, no port, and NO IDENTITY_*
# or NOTIFICATION_* variables: it makes no access-control decision and
# registers no preference, so requiring either would misrepresent what the
# process does
docker run \
  -e VIDEO_POSTGRES_DSN="postgres://user:pass@host:5432/video?sslmode=disable" \
  -e REDIS_ADDR="host:6379" \
  -e VIDEO_MINIO_ENDPOINT="host:9000" \
  -e VIDEO_MINIO_ACCESS_KEY="minio-access-key" \
  -e VIDEO_MINIO_SECRET_KEY="minio-secret-key" \
  -e VIDEO_MINIO_BUCKET="video-results" \
  -e RABBITMQ_URL="amqp://user:pass@host:5672/" \
  video-processor /app/worker

# Run the notifier — same image again, no port, and the narrowest surface of
# them all: only the Notification context's own DSN and the broker URL. It
# authenticates no caller, stores no artifact, holds no lease, and runs no
# ffmpeg, so it reads NO IDENTITY_*, NO VIDEO_* (MinIO included), and no
# REDIS_ADDR. NOTIFICATION_ALLOW_INSECURE_DESTINATIONS is deliberately not
# set here: unset is the restrictive default, and setting it in production
# turns the delivery client into an SSRF primitive.
#
# --stop-timeout is not optional dressing: Docker's default is 10 seconds and
# the shutdown drain is one MaxClaimHold() per delivery channel plus a 30s
# grace (90s at the documented budget with two channels), so the default would
# SIGKILL a delivery in flight and leave its claim to be reclaimed. 120
# matches docker-compose.yml's stop_grace_period; raising a delivery term, or
# adding a channel, lengthens the drain and requires raising this too
docker run \
  --stop-timeout 120 \
  -e NOTIFICATION_POSTGRES_DSN="postgres://user:pass@host:5432/notification?sslmode=disable" \
  -e NOTIFICATION_SMTP_ADDR="mail.example.com:587" \
  -e NOTIFICATION_SMTP_FROM="notifier@example.com" \
  -e RABBITMQ_URL="amqp://user:pass@host:5672/" \
  video-processor /app/notifier
```

**No webhook is delivered unless at least one notifier is running.** With the API and worker alone, jobs still finish correctly and their outcomes still reach `video.jobs.terminal.events.v1`; what does not happen is the announcement. The queue accumulates them, bounded by `x-max-length`, so a notifier started later works through the backlog — subject to the enrolment boundary, which is why a first start does not announce outcomes that predate the preferences they would be announced to.

**Uploads are not processed unless at least one worker is running.** With the API alone, `POST /upload` still answers `202` and the job sits in `queued` forever — the submission succeeds because it was accepted, not because it was done. Run at least one worker in every environment where uploads are expected to complete. Scale by adding worker processes, not by raising a concurrency setting: prefetch is one by design, so a worker holds exactly one job at a time.

The Dockerfile is a multi-stage build. The default (final) stage — used by the command above — compiles a static binary in a `golang:1.27-alpine` builder stage (dependencies resolved read-only from the committed `go.sum`), then ships **all five** binaries (`/app/identity-api`, `/app/video-api`, `/app/notification-api`, `/app/worker`, `/app/notifier`) and `ffmpeg` in a minimal `alpine` runtime stage with no Go toolchain or source tree, running as a fixed non-root user (UID 1000). `ffmpeg` is there for the worker rather than for any HTTP service or the notifier, and stays for that reason. One image for five processes is deliberate: they share `internal/` packages — the three HTTP services share Identity's verifier and the `internal/platform` plumbing, and each pairs with a consumer over the same context's domain — so separate images would create a way for the halves of one deploy to be built from different commits of the same domain code. Not every process links every package: the worker and the notifier belong to different contexts and share almost nothing beyond the platform layer. See [docs/development.md](development.md) for the additional `test` stage used to run the suite via Docker.

#### The image's default command

The image carries five binaries and can default to only one. `CMD` names `/app/video-api` — it is the service that serves the frontend, so it is the least surprising thing a bare `docker run` starts. **Every other process must be named explicitly**, as the commands above do (`/app/identity-api`, `/app/notification-api`, `/app/worker`, `/app/notifier`), and `docker-compose.yml` names a command for all five rather than relying on the default for any.

That default is load-bearing in one narrow way worth knowing: removing or renaming the binary it points at, without changing `CMD`, produces an image that builds, passes every scan, and exits immediately under a plain `docker run`. The compose stack would not notice, because it names its own commands — the failure surfaces only here, in the documented deployment commands.

> **Fenced-worker cutover and rollback:** when crossing the version boundary between pre-fence workers and this fenced generation, drain every pre-fence worker before starting any recovery sweeper. Those workers set no lease and do not honor `lease_epoch`, so overlap can let an unconditional terminal write overwrite a successor. A rollback across that same boundary must first stop every fenced worker and sweeper before any pre-fence worker starts. `cmd/worker` waits up to five minutes on SIGTERM; configure more than five minutes of termination grace, with additional margin for the sweeper join and final resource shutdown and verify the departing generation has exited before scaling the other one up. The additive `lease_epoch` column may remain during rollback.
>
> Ordinary later fenced-to-fenced releases do not inherit that overwrite hazard merely because one build is older. Drain them separately when a release can produce different or incompatible extraction output, so an in-flight job finishes on the intended generation rather than being recovered onto the next build.

### Environment Variables

The five processes have deliberately different configuration surfaces. The absences are the point: a dependency a process does not use should not be reachable from it.

**This table enumerates the variables each process reads, which is not the same set as the connections each process opens** — and the difference is large enough to have been a trap. `REDIS_ADDR` is required at three startups, but `internal/platform/redis.Open` constructs a client without connecting to anything. `RABBITMQ_URL` is required at `cmd/video-api`'s startup — loaded as `setupVideo`'s first statement — and `setupVideo` performs no AMQP dial at all; the relay dials inside its own cycle, so the process does hold a connection, transiently, once that goroutine is running. `NOTIFICATION_SMTP_ADDR`/`_FROM` are required at `cmd/notifier`'s startup, which loads the relay configuration and dials nothing until a delivery. Every cell below is correct about what it enumerates; none of them is a claim that a connection exists. `add-health-and-readiness-endpoints` needed the other set and derived it by reading the five `setup*` functions instead — see "Health and readiness probes" for the result, whose rows deliberately do not match this table's.

| | `identity-api` | `video-api` | `notification-api` | `worker` | `notifier` |
|---|---|---|---|---|---|
| `IDENTITY_POSTGRES_DSN` | **required** | **not read** | **not read** | not read | not read |
| `IDENTITY_JWT_PRIVATE_KEY`, `IDENTITY_JWT_KEY_ID` | **required** | **must not be given** — nothing here can mint | **must not be given** — same | not read | not read |
| `IDENTITY_JWT_PUBLIC_KEYS` | **required** — it checks the pair matches | **required** — verification material only | **required** — verification material only | not read | not read |
| `VIDEO_POSTGRES_DSN` | not read | required | not read | required | **not read** — it never touches a `VideoJob` |
| `NOTIFICATION_POSTGRES_DSN` | not read | not read | **required** | **not read** — the worker registers no preference and reads none | **required** |
| `REDIS_ADDR` | **not read** — it caches nothing and mounts no limiter | required | required — for the shared limiter alone | required (status cache, worker leases, and clearing failed-job idempotency keys) | **not read** — it holds no lease and caches nothing |
| `VIDEO_MINIO_ENDPOINT` / `_ACCESS_KEY` / `_SECRET_KEY` / `_BUCKET` | not read | required | not read | required | **not read** — it stores and fetches no artifact |
| `VIDEO_MINIO_PUBLIC_ENDPOINT`, `VIDEO_MINIO_PUBLIC_USE_SSL` | not read | optional | not read | optional, and **read even though unused** — see below | not read |
| `RABBITMQ_URL` | **not read** — it publishes and consumes nothing | required | **not read** | required | required |
| `NOTIFICATION_ALLOW_INSECURE_DESTINATIONS` | not read | not read | optional — **one variable, two readers** | not read | optional — the same variable, the same parser, the same default |
| `NOTIFICATION_WEBHOOK_MAX_ATTEMPTS`, `NOTIFICATION_WEBHOOK_TIMEOUT_SECONDS`, `NOTIFICATION_DELIVERY_RECLAIM_SECONDS` | not read | not read | not read | not read | optional, and **validated against one another at startup** — see below. Despite the `WEBHOOK` in two of the names they govern **every** channel |
| `NOTIFICATION_SMTP_ADDR`, `NOTIFICATION_SMTP_FROM` | not read | not read | **not read** — it validates an address, it sends nothing | not read | **required** |
| `NOTIFICATION_SMTP_USERNAME`, `NOTIFICATION_SMTP_PASSWORD` | not read | not read | not read | not read | optional, and only **together** — one without the other fails startup |
| `RATE_LIMIT_*` | not read — its two routes are how a caller obtains a token, so limiting them would be circular | optional | optional | not read | not read |
| `PORT` | as below | as below | as below | not read — the worker serves no HTTP and exposes no port | not read — same |
| `GIN_MODE` | **read by gin, then overridden** — see below | same | same | not read — no gin, no HTTP surface | not read — same |
| `LOG_LEVEL` | optional | optional | optional | optional | optional |

**`LOG_LEVEL` is the one row with no absences**, which in a table whose point is the absences is worth saying out loud: every process this repository builds emits structured records, so every one of them reads it. It is also the one variable that is deliberately *not* required to agree across services — unlike `RATE_LIMIT_*` below, which governs one shared per-user counter, this threshold governs only the process that reads it. Raising the worker's verbosity while leaving the three HTTP services at the default is the ordinary use of it, not a misconfiguration. See "Logging" under Implemented Infrastructure for what a record looks like.

**`RATE_LIMIT_*` must hold the same value everywhere it is read, or be unset everywhere.** The counter is shared — one budget per user across the whole system, keyed `ratelimit:<userID>` by every service that mounts the middleware — so services holding different thresholds would compare one count against two of them and the effective limit would depend on which route a request happened to take. `docker-compose.yml` leaves both unset on every service for exactly that reason: unset-everywhere is one source of truth (the code default, 60 requests / 60 seconds), whereas pinning the value in the compose file would create a second place for it to drift from.

**The three HTTP services all listen on 8080**, each inside its own container, and none of them publishes it. The gateway is the only *application* process that publishes a host port; the worker and the notifier listen on nothing at all. The local compose stack also publishes the mail catcher's inspection port, which is a development-only support service serving no application route and is not part of any deployment.

`NOTIFICATION_ALLOW_INSECURE_DESTINATIONS` is the row that surprises people: it reads like a delivery concern, but `cmd/notification-api` needs it too, because the destination policy is applied both when a preference is written and when its address is dialled. One variable, one parser, and a deployment whose two readers disagree either stores destinations it can never deliver to or refuses at dial what it accepted at write time. The other three processes never see it.

The one row that needs explaining is `VIDEO_MINIO_PUBLIC_*`. The worker never mints a presigned URL — issuing download grants belongs to `cmd/video-api` — but `setupWorker` goes through the same MinIO loader and builds the presign client anyway, so `ResultStorage` is fully constructed rather than holding a nil that would panic the day something calls the other half of its interface. So the variables *are* read on the worker, and a malformed value fails worker startup even though nothing signs with it. Leaving them unset is the normal case (each falls back to its internal counterpart), which is why `docker-compose.yml` sets them only on `video-api`.

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Listening port (hardcoded as `:8080` in each HTTP service's `main.go`; no env var read currently — listed here for future use) |
| `GIN_MODE` | — | **No longer a deployment knob — but not inert either.** gin reads it and calls `SetMode` in its own **package initializer**, which runs before `main()` does. A *recognized* value (`debug`, `release`, `test`) is then overridden a moment later, because all three HTTP `main()`s call `gin.SetMode(gin.ReleaseMode)` unconditionally before registering a route — so setting one of those changes nothing. An *unrecognized* value is the footgun: `SetMode` panics on it inside that initializer, so the process dies with an unstructured Go panic trace on standard error before any `main()` exists to override it, and before the process logger is installed. The mode is pinned rather than configured because debug mode writes `[GIN-debug]` route registrations and its banner to `gin.DefaultWriter`, which is **standard output** — the same stream the JSON records go to, so a debug line lands interleaved among them (gin sends only `debugPrintError` to standard error). |
| `LOG_LEVEL` | `info` | Minimum severity this process records: `debug`, `info`, `warn` (or `warning`), or `error`, case-insensitive. Optional as of Phase 8's `add-structured-logging`, and read by **all five** processes. Absent means informational. A value that is *set* and cannot be parsed is refused rather than silently replaced by the default: the process emits one error-severity record naming the offending value — structured, through a logger built at the default severity carrying the same `service` and `instance` fields as any other record — and exits non-zero. Per-process by design; two services holding different values is a supported configuration, not a drift (contrast `RATE_LIMIT_*` above). The threshold also gates output that is not this repository's: `slog.SetDefault` redirects the standard library's default logger through the same handler, so `net/http`'s own reports — an accept error, a TLS handshake failure, a panic it recovers — arrive as JSON records carrying the same `service` and `instance`, but bridged at informational severity whatever they report, which means `warn` or `error` discards them. |
| `IDENTITY_POSTGRES_DSN` | unset | PostgreSQL connection string for the Identity module (e.g. `postgres://user:pass@host:5432/identity?sslmode=disable`). Required at startup. |
| `IDENTITY_JWT_PRIVATE_KEY` | unset | PKCS#8 RSA private key, in PEM form, that access tokens are signed with (RS256). Held by the one process that mints tokens and by no other. Required at startup; there is no default or embedded key, and startup fails clearly rather than falling back to one. Generate with `openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048`. |
| `IDENTITY_JWT_KEY_ID` | unset | The key id stamped into every issued token's `kid` header, naming which key signed it. Required at startup alongside the private key, and held by the same single process. |
| `IDENTITY_JWT_PUBLIC_KEYS` | unset | JSON object mapping key id to PKIX PEM public key — `{"2026-09":"-----BEGIN PUBLIC KEY-----\n…"}` — read by every process that verifies a token. It is a **set** rather than one key so a rotation can hold the outgoing and the incoming key at once: publish the new public key to every verifier first, switch `IDENTITY_JWT_KEY_ID` and `IDENTITY_JWT_PRIVATE_KEY` on the issuer second, and drop the outgoing entry once every token signed under it has expired. Required at startup; a value carrying **private** key material is refused, so a service handed the full key pair as its verification material fails to start rather than running one line away from being able to mint. The issuing process requires it too, and checks that it holds its own public half under `IDENTITY_JWT_KEY_ID` — a mismatched pair is silent where it could be attributed and loud everywhere it cannot. |
| `VIDEO_POSTGRES_DSN` | unset | PostgreSQL connection string for the Video Processing module's `VideoJob` repository (e.g. `postgres://user:pass@host:5432/video?sslmode=disable` — pointed at the same server as `IDENTITY_POSTGRES_DSN` but at its own **database**, so a query naming another context's table fails as an unknown relation instead of returning rows; sharing the server is a deployment decision rather than a code assumption). Required at startup as of Phase 3's `wire-videojob-http-endpoints`; `cmd/video-api/video.go`'s `setupVideo` is what opens and migrates it. |
| `NOTIFICATION_POSTGRES_DSN` | unset | PostgreSQL connection string for the Notification module's `NotificationPreference` repository (e.g. `postgres://user:pass@host:5432/notification?sslmode=disable`). The split is one pool **and one database** per bounded context, not one server per context: which server this value names is a deployment decision, and local development and Compose point all three DSNs at one server while giving each its own database (see [openspec/specs/notification-persistence/spec.md](../openspec/specs/notification-persistence/spec.md)). Pointing two contexts at the same database would still start, since nothing in the code assumes otherwise — it would just retire the boundary the engine currently enforces for free. Required at the startup of **both** `cmd/notification-api` and `cmd/notifier` as of Phase 7 (`add-notification-domain-and-preferences`, generalized by `add-notification-webhook-delivery`), and connectivity-checked in each. Both also run the context's migration, deliberately: neither process's startup may depend on the other's having run first, so a notifier started against a database no API has yet touched creates what it needs rather than failing. Not read by `cmd/worker`. |
| `REDIS_ADDR` | unset | Address (`host:port`) of the Redis instance backing upload idempotency, rate limiting, status caching, and worker leases (e.g. `redis:6379`). Required by **three** processes: `cmd/video-api` (idempotency, cache), `cmd/notification-api` (the shared limiter alone), and `cmd/worker` (leases, cache, key clearing). Not read by `cmd/identity-api` or `cmd/notifier`. The client is constructed at startup but establishes network connections lazily; reachability failures degrade individual Redis-backed behaviors rather than failing startup. |
| `RATE_LIMIT_MAX_REQUESTS` | `60` | Maximum requests per authenticated user within one rate-limit window before `429` responses start. Optional as of Phase 4's `add-rate-limiting-middleware` — unlike `REDIS_ADDR`, absence is not a startup failure, it just uses the default. |
| `RATE_LIMIT_WINDOW_SECONDS` | `60` | Length (seconds) of the fixed rate-limit window `RATE_LIMIT_MAX_REQUESTS` applies to. Optional, same as above. |
| `VIDEO_MINIO_ENDPOINT` | unset | Address (`host:port`) of the MinIO instance for the Video Processing context (e.g. `minio:9000`). Required at startup as of Phase 5's `migrate-result-storage-to-minio`. |
| `VIDEO_MINIO_ACCESS_KEY` | unset | MinIO access key. Required at startup. |
| `VIDEO_MINIO_SECRET_KEY` | unset | MinIO secret key. Required at startup. |
| `VIDEO_MINIO_BUCKET` | unset | Bucket holding processed ZIP results, keyed `frames_<jobID>.zip`. Required at startup; created automatically if absent. |
| `VIDEO_MINIO_USE_SSL` | `false` | Whether to connect over TLS. Optional, but a value that is *set* and not parseable as a boolean is a configuration error, never a silent `false` — a typo must not quietly downgrade an intended TLS connection to plaintext. |
| `VIDEO_MINIO_PUBLIC_ENDPOINT` | `VIDEO_MINIO_ENDPOINT` | Address (`host:port`) clients reach MinIO at, used **only** to construct presigned download URLs. Optional as of Phase 5's `add-presigned-download-urls`; defaults to the internal endpoint, so a deployment where one address serves both the server and its clients needs nothing here. The server never dials this address. |
| `RABBITMQ_URL` | unset | Full AMQP URI for the shared broker (e.g. `amqp://video:video@rabbitmq:5672/`) — a URI, not a `host:port` pair like `REDIS_ADDR`, because it carries the TLS scheme, the credentials, and the virtual host. **Required at startup by three processes** (`cmd/video-api` as of Phase 6's `add-videojob-source-key-and-outbox-relay`, `cmd/worker` as of `migrate-upload-to-async-processing`, and `cmd/notifier` as of `add-notification-webhook-delivery`): an unset value stops the process with a clear error. A *reachable* broker is not required by any of the three — the four connection loops across them (the Video API's dispatch relay, the worker's consumer and its terminal relay, the notifier's consumer) each own their connection, dial it in their own goroutine, and retry with backoff, so the API serves every route and the worker and notifier both stay up with the broker down. |
| `NOTIFICATION_ALLOW_INSECURE_DESTINATIONS` | `false` | Relaxes the webhook destination policy's **scheme rule and address rule together** — with it, a destination may use `http` and may resolve to a private, loopback, or link-local address. Optional as of Phase 7's `add-notification-webhook-delivery`, read by `cmd/notification-api` and `cmd/notifier` through one parser. One switch rather than two because the two relaxations are wanted in exactly the same situation, and splitting them would invite enabling half of it where neither belongs. A value that is *set* and not parseable as a boolean is a configuration error, never a silent `false`: reading a typo as `false` leaves a local stack refusing every destination with nothing to point at, and reading it as `true` would let a typo open the policy in production. **Set it in local development and the compose stack; never in production** — see "The destination policy" below for what it costs. |
| `NOTIFICATION_WEBHOOK_MAX_ATTEMPTS` | `3` | How many times one delivery is attempted before its outcome is recorded as `failed`. Optional, `cmd/notifier` only. Not independent of the reclaim bound — see below. **The name says `WEBHOOK` and the term governs both channels**: there is one per-attempt budget across channels, kept that way because `MaxClaimHold()` is arithmetic over it and a per-channel term would make the startup validation irreproducible. The name is kept rather than corrected because renaming it is a breaking configuration change for a deployed stack, in exchange for a name. |
| `NOTIFICATION_WEBHOOK_TIMEOUT_SECONDS` | `5` | Per-attempt timeout, for both channels: the webhook client's transport timeout, and the SMTP client's deadline for the dial **and** the whole conversation together. Optional, `cmd/notifier` only. Not independent of the reclaim bound. Five seconds is comfortable for one HTTP request and tighter for a full SMTP exchange (greeting, EHLO, STARTTLS, AUTH, MAIL, RCPT, DATA, QUIT); raise it if your relay is slow, and note that doing so raises `MaxClaimHold()` and therefore the floor the reclaim bound is validated against. |
| `NOTIFICATION_DELIVERY_RECLAIM_SECONDS` | `120` | How long an unresolved claim must be before another consumer may take it over. Optional, `cmd/notifier` only. **Validated at startup against the other two**: a bound below twice one claimant's maximum hold fails startup naming both values. The bound is per *claim*, and each claim is fenced independently, so a second channel does not extend it — what the channel count does move is the shutdown drain, which allows one claim hold per channel. |
| `NOTIFICATION_SMTP_ADDR` | unset | `host:port` of the relay `cmd/notifier` sends e-mail through. **Required** at its startup, and both halves must be non-empty — `mail:` and `:1025` split cleanly and name no endpoint, and accepting either would move the failure from startup to every delivery. A notifier that cannot send is one whose e-mail preferences are stored and silently never honoured, which is the outcome the closed channel set exists to prevent. |
| `NOTIFICATION_SMTP_FROM` | unset | The envelope sender and `From` header. **Required** at `cmd/notifier`'s startup, and validated by the same rule a stored recipient is: a single address, printable ASCII, no display name. It lands in a message header exactly as the recipient does, so a line break here would be a header of the operator's choosing rather than a registrant's — no better. |
| `NOTIFICATION_SMTP_USERNAME`, `NOTIFICATION_SMTP_PASSWORD` | unset | Optional relay credentials, and only meaningful **together** — one without the other fails startup rather than authenticating with an empty half. When they are set the client **requires an encrypted session before authenticating** and fails the attempt rather than sending the credential in the clear; see below. |
| `VIDEO_MINIO_PUBLIC_USE_SSL` | `VIDEO_MINIO_USE_SSL` | Scheme for the URLs issued against `VIDEO_MINIO_PUBLIC_ENDPOINT`. Optional; defaults to the *resolved* `VIDEO_MINIO_USE_SSL`, not to `false` — declaring TLS once must not silently produce `http://` links. Set it explicitly when TLS terminates in front of the public address but the server talks plaintext internally. A set-but-unparseable value is a configuration error, same as above. |

The first four `VIDEO_MINIO_*` variables are **required**; `VIDEO_MINIO_USE_SSL`, `VIDEO_MINIO_PUBLIC_ENDPOINT`, and `VIDEO_MINIO_PUBLIC_USE_SSL` are optional. `setupVideo` loads the configuration, opens the client, pings it, ensures the bucket exists, and discovers the bucket's region for the presign-only client, and **any of those steps failing stops startup**. This is deliberately fail-closed, unlike the Redis-backed features above, which degrade to a slower but correct system when Redis is down: a result that cannot be stored cannot be delivered, so there is nothing to degrade to.

#### The public endpoint, and the failure mode it exists to prevent

Since `add-presigned-download-urls`, `GET /download/:filename` returns a signed URL instead of the ZIP, and the client fetches the object from MinIO itself. Two operational consequences follow.

**MinIO must now be reachable from clients, not only from the API.** Before this change only the API needed a route to the bucket; a browser or script that can reach the API but not the storage service now cannot download a result at all. That is a deployment-topology change, not a tuning knob: plan for the storage endpoint (or a proxy/CDN in front of it) to be publicly resolvable and reachable by whoever downloads.

**A wrong or unset public endpoint produces URLs that are correctly signed and unreachable, and the API cannot detect it.** SigV4 covers the `Host` header, so the host is fixed when the URL is signed and cannot be corrected afterwards — the presigning client must be built against the browser-facing address from the start. The API never follows a URL it issues, so from its side everything looks healthy:

| Where it shows up | What you see |
|---|---|
| The API's logs | Nothing. `GET /download/:filename` returns `200` with a well-formed URL. |
| The API's metrics/health | Nothing. No error, no elevated latency, no failed request. `GET /ready` stays `200` throughout, and correctly: it checks the bucket over the *internal* endpoint, while the presign client built against the public one is deliberately never pinged and never used for an object operation. Readiness is not a check on whether a URL this service issues can be redeemed. |
| The browser | A connection or DNS error on the storage host — often `ERR_NAME_NOT_RESOLVED` or a connection timeout — after a `200` from the API. |
| `curl` against the issued URL | The same failure to connect, before any HTTP status is returned. |

If downloads fail in the browser while the API reports success, read the `url` field of a `GET /download/:filename` response and check whether that host resolves and is reachable *from the client*. The commonest cause is an internal service name leaking into the public endpoint — `docker-compose.yml` sets `VIDEO_MINIO_ENDPOINT: minio:9000` for the server and `VIDEO_MINIO_PUBLIC_ENDPOINT: 127.0.0.1:9000` for the browser precisely because those two audiences reach the same instance by different addresses.

An issued URL also **cannot be revoked**: deleting the job, changing its owner, or changing its status leaves an outstanding URL working until its five-minute lifetime elapses. The lifetime is a compile-time constant with no environment variable, deliberately — issuance happens when a user clicks, so the interval that has to be survived is the gap before the browser navigates.

> **Deployment ordering:** MinIO must be reachable and these variables set *before* the new image starts, or the container exits immediately. Rolling back to a pre-Phase-5 image reverts to the local `outputs/` directory and ignores these variables entirely — results produced while the newer image was live then become unreachable, since they are in the bucket and the older code only looks at local disk.

Their `VIDEO_` prefix marks them as the Video Processing context's own configuration, matching `VIDEO_POSTGRES_DSN` and distinguishing them from `internal/platform/`'s unprefixed `REDIS_ADDR`.

`IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_PRIVATE_KEY`, `IDENTITY_JWT_KEY_ID`, `IDENTITY_JWT_PUBLIC_KEYS`, `VIDEO_POSTGRES_DSN`, `NOTIFICATION_POSTGRES_DSN`, `REDIS_ADDR`, and the four `VIDEO_MINIO_*` variables are all required to be *set*: the process exits at startup with a clear configuration error if any is empty, rather than running with unsafe defaults or an unauthenticated fallback (see [openspec/specs/identity-authentication/spec.md](../openspec/specs/identity-authentication/spec.md), [openspec/specs/videojob-http-api/spec.md](../openspec/specs/videojob-http-api/spec.md), and [openspec/specs/upload-idempotency/spec.md](../openspec/specs/upload-idempotency/spec.md)). Startup validation depth differs by dependency, though: all three PostgreSQL DSNs are also *connectivity*-checked at startup (`db.PingContext`), so an unreachable or malformed database fails fast. `REDIS_ADDR` is not — `platformredis.Open` only constructs the client, and a malformed address or unreachable Redis surfaces later, at the first `POST /upload` request that needs it, not at startup. MinIO sits at the strict end: it is connectivity-checked *and* its bucket is provisioned at startup, so a wrong endpoint or bad credentials stop the process rather than surfacing on the first upload. `RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS` and `VIDEO_MINIO_USE_SSL`, unlike the required variables above, are optional — startup only fails if either is *set* to something malformed (non-integer or non-positive), never for being unset (see `openspec/specs/rate-limiting/spec.md`). `RABBITMQ_URL` is required to be set by both processes but is not connectivity-checked at startup by either (see the RabbitMQ section below).

`cmd/worker` applies the same rules to the subset it reads: `RABBITMQ_URL` is loaded first, before any I/O, then `VIDEO_POSTGRES_DSN` (opened, pinged, migrated), `REDIS_ADDR`, and the four `VIDEO_MINIO_*` variables (opened, pinged, bucket ensured). It also creates `temp/` at startup and **exits if it cannot** — every delivery downloads its source there, so a worker without it would claim jobs and fail each one for a reason unrelated to the job, deleting the source on the way out. Being unavailable is the honest outcome.

`cmd/notifier` reads the narrowest surface of the five, and applies the same rules to it. Configuration first, all of it, before any I/O: `RABBITMQ_URL`, then `NOTIFICATION_POSTGRES_DSN`, then the destination policy, then the delivery budget — so a value the process will refuse to start on is reported as a bad value rather than as a failure to reach something it was never configured to reach. Only then is the pool opened, migrated, and pinged. The broker is deliberately *not* a startup gate: the consumer dials with bounded backoff and redeclares the topology after every dial, so neither this process's startup nor the worker's depends on the other's order. It creates no directory, writes nothing to disk, and opens no port.

#### Access tokens, key material, and rotation

Access tokens are **RS256** as of `split-api-by-bounded-context`. The single `IDENTITY_JWT_SIGNING_KEY` HMAC secret that preceded it is **gone**, replaced by three variables: `IDENTITY_JWT_PRIVATE_KEY` and `IDENTITY_JWT_KEY_ID`, read only by `cmd/identity-api`, and `IDENTITY_JWT_PUBLIC_KEYS`, read by every process that verifies. A deployment still configured with only the old variable will not start — not because the old one is rejected (nothing reads it any more, so it is simply inert) but because the new ones are required and absent. There is no fallback path and no dual-mode.

The change is not cosmetic. Three services cannot share one HMAC secret without giving Video Processing and Notification the ability to **mint** tokens, which is a privilege neither has any reason to hold. With an asymmetric pair, the two verifying services hold material that can only check a signature — and `NewVerifier` refuses a private-key PEM outright, so a private key handed to the wrong service stops that service at startup rather than quietly upgrading it.

**Generating a pair.** For local development, `make dev-keys` writes a per-machine pair into a git-ignored `.env` that Compose loads. It is deliberately not repository material: a committed private key is one copy away from a deployment, and no reader can tell a development key from a promoted one by looking at it. For any other environment, generate the pair with your own tooling and deliver it the way you deliver other secrets:

```bash
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:2048 -out identity-private-key.pem
openssl pkey -in identity-private-key.pem -pubout -out identity-public-key.pem
```

`IDENTITY_JWT_PUBLIC_KEYS` is a **set**, not a single key: a JSON object mapping key id to PEM. That is what makes rotation a configuration change rather than a migration.

**The public key is distributed by configuration, not by a JWKS endpoint.** That is a deliberate trade. JWKS would put a synchronous call to Identity on every verifier's cold cache and make Identity's availability a dependency of every other service's authorization decision — precisely the coupling the split exists to remove. The cost is that rotating a key means a configuration roll instead of a cache expiry, and the benefit is that **a token issued before Identity went down is still accepted while it is down.** That property is verified as part of the change and is the reason the design is what it is.

**Rotation procedure**, in this order — each step is safe to stop at:

1. Generate the new pair and pick its key id (a date, e.g. `2027-01`).
2. Add the new public key to `IDENTITY_JWT_PUBLIC_KEYS` **alongside the outgoing one**, and roll every verifying service (`video-api`, `notification-api`) plus `identity-api`. Both keys are now accepted; nothing is signed with the new one yet.
3. Point `IDENTITY_JWT_PRIVATE_KEY`/`IDENTITY_JWT_KEY_ID` at the new pair and roll `identity-api`. New tokens carry the new `kid`; tokens already issued still verify against the retained old public key.
4. Once every token issued under the old key has expired (the access-token lifetime — not the rotation's duration), drop the old entry from `IDENTITY_JWT_PUBLIC_KEYS` and roll the verifiers again.

Skipping step 2 — swapping the private key while the set still holds only the old public key — rejects every token minted from that moment until the verifiers are rolled. The `kid` header is what makes the overlap work: the verifier looks the key up by id rather than trying each in turn, so an unknown `kid` is a clean rejection rather than an ambiguous one.

**Dropping a public key invalidates every token issued under it, and so does regenerating a pair without retaining the old one.** Rotating the *signing* key does not — that is what step 2's overlap is for, and it is why step 4 waits. Any token whose `kid` is no longer in `IDENTITY_JWT_PUBLIC_KEYS` is rejected — indistinguishably from any other invalid token, by design. That is the intended behaviour for a compromised key and a surprise for anyone who runs `make dev-keys FORCE=1` mid-session: every session in every open browser tab ends at once. There is no revocation list and no way to invalidate one token without invalidating the generation it belongs to; short token lifetimes, not revocation, are what bound the exposure.

#### The destination policy

A webhook destination is judged **twice**: once by `cmd/notification-api` when the preference is written, and again by `cmd/notifier` against the **resolved network address** when the connection is opened. Neither point is redundant. Write-time alone cannot survive a hostname that resolves elsewhere later, nor a policy tightened after the row was stored. Dial-time alone silently accepts a destination that will never be delivered to, which to its owner is indistinguishable from one that works.

The default posture is `https` only, to an address that is globally reachable unicast — everything else is refused, including loopback, private, link-local (which is what covers `169.254.169.254`), shared, benchmarking, documentation, and reserved space, in IPv4 and IPv6 alike. Refusal is reported to the caller as a plain `400` that does **not** say which rule caught it: the rules enumerate the deployment's internal address space, so a caller who could tell them apart by resubmitting would have been handed a probe.

**`NOTIFICATION_ALLOW_INSECURE_DESTINATIONS=true` turns the delivery client into an SSRF primitive.** That is not a warning about a corner case; it is what the switch does. With it set, any authenticated user can register a destination naming any address this process can reach — the cloud metadata endpoint, an internal admin API, a database's HTTP interface — and the notifier will make a request to it and, if the endpoint answers, record the attempt as delivered. Set it in local development and in the compose stack, where there is no TLS and a webhook receiver is a container hostname on a private network. **Never set it anywhere the process can reach something a user should not.** It is one switch rather than two because both relaxations are wanted in exactly the same situation.

Preferences stored before the policy took effect are **neither migrated nor deleted**. One the policy now refuses simply does not deliver, and the recorded reason names the policy; its owner can see it by re-registering the destination, which the write-time check will refuse with the same `400`.

#### The delivery budget and the reclaim bound

Three variables tune the budget, and the third is **not independent** of the first two. The longest one claimant can hold a claim is arithmetic over every term:

| Term | Default | Contribution |
|---|---|---|
| attempts × per-attempt timeout | 3 × 5s | 15s |
| backoff between attempts (2s, doubling) | 2s + 4s | 6s |
| resolve retries × their timeout | 3 × 2s | 6s |
| backoff between resolve retries (1s, doubling) | 1s + 2s | 3s |
| **maximum claim hold** | | **30s** |

The reclaim bound must be at least **twice** that — a floor of 60s at the defaults, which the default bound of 120s clears. `cmd/notifier` computes the floor from the configured terms and **fails startup** naming both values when the bound is below it. This is a startup check rather than a documented convention because these are separately tunable variables and a convention does not survive someone lowering one: the claim token fences the database write but cannot recall a request already on the wire, so a bound under the budget hands a second consumer the claim mid-flight and the receiver gets two requests in *normal* operation, not only after a crash. Raising `NOTIFICATION_WEBHOOK_MAX_ATTEMPTS` or `NOTIFICATION_WEBHOOK_TIMEOUT_SECONDS` therefore raises the floor, and may require raising `NOTIFICATION_DELIVERY_RECLAIM_SECONDS` with it.

Two-to-three times, not ten: the same value bounds how long an abandoned claim stalls the rest of the queue (see the two signals below).

Only three of the seven terms are settable from the environment. The backoff intervals and the resolve-retry terms keep their documented defaults, deliberately — every variable exposed is another way to reach a combination the validator has to refuse, and these are the terms with no operational question attached to them.

The shutdown drain follows from the same arithmetic, once per delivery channel: `cmd/notifier` waits one maximum claim hold for each channel in the closed set, plus a single 30-second grace (90s at the defaults, with two channels), for the message in hand to reach a disposition on every preference it resolved to, then closes the pool. It is per channel because one event can resolve to one preference per channel and the handler works through them sequentially, so a drain sized for a single hold would expire during work that is within budget. The reclaim bound is unaffected and stays per claim — each claim is fenced independently. **If the drain expires, the bound wins and the pool is not closed** — the handler runs on a context the signal does not cancel, so at that point it is still running and can never be joined, and process exit releases the connections anyway. Nothing is lost that the reclaim bound does not already cover. Give the process more termination grace than its drain: `docker-compose.yml` sets `stop_grace_period: 90s` and the `docker run` command above passes `--stop-timeout 90` for exactly this — Docker's own default is 10 seconds, which is below the drain at every supported budget. Raising a delivery term lengthens the drain and requires raising both.

#### Two signals this design deliberately leaves behind

Both are consequences of choices made on purpose, so read them as diagnoses rather than as faults to fix in code.

- **A `notification_deliveries` row still `pending` well past the reclaim bound, with no consumer in flight, means an outcome was delivered but could not be recorded.** The webhook was actually sent; what failed was the write recording it, on every one of its bounded retries. The message is acknowledged in that case rather than requeued, because requeueing would lose the outcome permanently and could re-send the webhook first, and rather than dead-lettered, because that would report a failure for a delivery that succeeded. The row is an accepted accounting loss, not a recoverable state — nothing will ever resolve it. The log line naming the delivery id and the lost outcome is the record of what happened. Query it with:

  ```sql
  SELECT user_id, event_type, job_id, attempts, claimed_at
    FROM notification_deliveries
   WHERE status = 'pending'
     AND claimed_at < now() - interval '120 seconds'  -- the reclaim bound
   ORDER BY claimed_at;
  ```

  The interval is the **default** reclaim bound. On a deployment that sets `NOTIFICATION_DELIVERY_RECLAIM_SECONDS`, substitute that value: run as written against a longer bound and the query reports claims that are still live, against a shorter one it misses stale rows.

  A handful of these after a rough deploy is expected. A steady stream is a sign the Notification database is struggling, not that delivery is broken.

- **A terminal queue that stops draining for up to the reclaim bound is the head-of-line stall of an abandoned claim, not a stuck consumer.** At prefetch 1, a message whose claim is held by another consumer is requeued after a pause and returns to the head of the queue, where the same consumer takes it again. If the holder died without resolving, that repeats until the bound expires and the claim can be granted. Depth stops falling, the consumer looks busy, and nothing is wrong: it clears on its own within the bound. Restarting the notifier does not speed this up — the bound is measured from the abandoned claim's `claimed_at`, not from consumer uptime. A stall materially longer than the bound is a different problem, and the query above is what tells the two apart.

## Runtime Directory Structure

**`cmd/worker`** creates and uses **one** directory relative to its working directory. `cmd/video-api` creates none — it stopped touching the filesystem when extraction moved to the worker:

```
./
  temp/       Per-job scratch, on the WORKER's filesystem: the downloaded
              source video, the extracted PNG frames, and the ZIP built
              from them
```

Neither uploaded source videos nor processed ZIP results are on local disk any more — both are objects in the MinIO bucket named by `VIDEO_MINIO_BUCKET`, separated by key prefix.

| Location | Created by | Contents | Cleaned by |
|---|---|---|---|
| `temp/` (worker only) | `cmd/worker`'s `createDirs()` at startup, plus per-job subpaths | Downloaded source copy, PNG frames, the built ZIP | Always, by `defer` on every path |
| Bucket, `uploads/` prefix | `POST /upload` per request | `uploads/<uploadID>_<filename>` source videos | The request before enqueue; the worker after an applied terminal result or its own completion retry finds that result already present; or the sweeper after an applied abandonment failure. **Not guaranteed**, see below |
| Bucket, flat keys | `ProcessVideoJob` on success | `frames_<jobID>.zip` results | Never (manual cleanup required) |

**Source cleanup is best effort, and since the async cutover it is also not exhaustive.** A source object is owned by the `POST /upload` request until its job commits as `queued`; afterwards only a worker that applied a terminal outcome, or the sweeper that applied terminal abandonment, may delete it. Fenced outcomes and already-present failures delete nothing. The bounded completion retry is the exception: when it finds its own identical `completed` outcome already present after a possibly lost response, it completes that run's source and lease cleanup. Each cleanup uses one `RemoveObject` call with no retry, so a MinIO hiccup can leave residue logged by key.

**A job that is enqueued but never dispatched can leak its source permanently.** The request has given up ownership and no worker ever took it; this occurs when the relay never publishes or a message is dead-lettered before any claim. A worker crash after claim is now recovered by the sweeper, but a crash after a terminal commit and before best-effort cleanup can still leave the source behind.

The bucket **expiration lifecycle rule scoped to the `uploads/` prefix** is therefore no longer a recommended backstop — it is the **only** guarantee residual objects are eventually reclaimed. Configure it, but do not assume one day is universally safe. Retention must exceed the deployment's supported end-to-end job lifetime: maximum queue wait, the initial extraction, up to three recovery attempts, lease-expiry and confirmation delays, and backlog-driven scan rotations. The application enforces no upper bound on queue wait or extraction duration, so no finite lifecycle can both preserve every arbitrarily slow recoverable job and reclaim residue. Define that operational lifetime, monitor the oldest non-terminal jobs, and set a margin above it; a job exceeding the policy can lose its source and fail on recovery. The rule must be scoped to `uploads/`, because result objects live in the same bucket under flat `frames_*.zip` keys and must never expire. No code path assumes the rule exists, which is exactly why its absence is silent.

Results accumulate indefinitely. There is no expiry, no lifecycle rule, no cleanup job, and no size limit for them — growth must be monitored manually in the current deployment. Moving artifacts off local disk removed the container-disk pressure, not the retention gap.

## CI / CD

**Four** required checks run on every push and pull request:

| Check | Tool | What it does |
|---|---|---|
| `Build & Test` | `go vet` + `go test ./... -v` | Compiles the application and runs integration tests (with `ffmpeg` installed on the runner) |
| `SAST (gosec)` | [`gosec`](https://github.com/securego/gosec) | Static security analysis; fails the build on any finding |
| `Vulnerability Scan (govulncheck)` | [`govulncheck`](https://go.dev/security/vuln) | Fails only when a known vulnerability is reachable from code actually called by this project |
| `Container Image Build` | `docker buildx` | Builds the runtime image for both published platforms and the test stage natively, pushing nothing — then reads the ELF headers of all five binaries in each image |

That last check exists because everything above it runs on the runner with dependencies installed there, so until it was added a `Dockerfile` that did not build merged green. Its architecture assertion is the part that earns its keep: the builder chains five compilations, and a target architecture reaching four of them produces an image that builds, scans and pushes cleanly while one process dies with `exec format error` on one platform only. Inspecting the image manifest would not catch that — the manifest reports the platform it *claims*, and never opens a layer.

Releases are automated via `release-please`. On every push to `main`, it maintains a "Release PR" showing the next version computed from Conventional Commits. Merging that PR creates the git tag, publishes a GitHub Release, updates `CHANGELOG.md`, and publishes the container image (see [Docker](#docker) above). The publish job lives in the same workflow as `release-please` rather than in one of its own, and that is not a stylistic choice: **a release created by an action using the workflow's own `GITHUB_TOKEN` triggers no further workflow run**, so a separate workflow keyed on the release event would sit silent through every release.

### Post-deploy step: retire the superseded dispatch generation

The async cutover moved job dispatch to a new generation of the topology — `video.jobs.v2` / `video.jobs.queued.v2`, routing key and outbox `event_type` `video_job.queued.v2`. The previous generation's entities are **not** deleted by the application, and after every replica is running the new build they should be deleted by hand:

```bash
# from a shell that can reach the broker's CLI. --vhost is the vhost from
# RABBITMQ_URL, decoded: the CLI takes it literally, unlike the management API
# path further down. "/" is both rabbitmqctl's default and what the Compose
# stack's URL selects, so it is written out rather than left implicit.
rabbitmqctl --vhost / delete_queue video.jobs.queued.v1
```

**The exchange needs a different tool, and this is the step the runbook used to get wrong.** `rabbitmqctl` has no `delete_exchange` on the `rabbitmq:4-alpine` image `docker-compose.yml` pins — `delete_queue` is there, but the CLI exposes no exchange equivalent at all, so following a two-`rabbitmqctl`-command recipe retires the queue and then fails. Delete it over AMQP from any client, which needs no plugin and no extra port:

```bash
# Run from anywhere with the Go toolchain and network reach to the broker.
# RABBITMQ_URL is the same value the services are configured with, so its
# vhost, credentials and TLS setting need no separate handling here.
cd "$(mktemp -d)"   # a fresh directory: a leftover go.mod fails `go mod init`
cat > main.go <<'GO'
package main

import (
	"log"
	"os"

	amqp "github.com/rabbitmq/amqp091-go"
)

func main() {
	conn, err := amqp.Dial(os.Getenv("RABBITMQ_URL"))
	if err != nil {
		log.Fatalf("dial: %v", err)
	}
	defer conn.Close()

	ch, err := conn.Channel()
	if err != nil {
		log.Fatalf("channel: %v", err)
	}
	defer ch.Close()

	// ExchangeDelete(name, ifUnused, noWait): delete unconditionally and wait
	// for the broker to confirm. The name is a literal so no other exchange
	// is reachable from here.
	if err := ch.ExchangeDelete("video.jobs.v1", false, false); err != nil {
		log.Fatalf("delete exchange: %v", err)
	}
	log.Println("deleted exchange video.jobs.v1")
}
GO
go mod init retire-v1 && go get github.com/rabbitmq/amqp091-go@v1.14.0
RABBITMQ_URL='amqp://user:pass@broker-host:5672/' go run .
```

Where the `rabbitmq_management` plugin is enabled and its port is reachable, the HTTP API does the same. **Substitute the vhost**: the path segment after `/api/exchanges/` is the URL-encoded vhost from `RABBITMQ_URL`, and `%2F` below is the default `/` — a deployment on a named vhost that leaves it at `%2F` gets a `404`, or deletes a same-named exchange in the wrong vhost while the superseded one survives.

```bash
# Fill these in from RABBITMQ_URL's userinfo. This system configures the broker
# through that one variable and defines no separate credential variables, so
# nothing in the environment sets them for you.
user='<user from RABBITMQ_URL>'
pass='<password from RABBITMQ_URL>'
curl -sS --fail-with-body -u "$user:$pass" -X DELETE \
  "http://<broker-host>:15672/api/exchanges/%2F/video.jobs.v1"
```

`--fail-with-body` is load-bearing here, not tidiness. A successful delete answers `204` with an empty body, and plain `curl` exits `0` on a `401` or a `404` too — so a wrong credential or a mis-encoded vhost would leave the exchange in place while the command looked exactly like the successful one. The flag turns those into a non-zero exit and still prints the broker's own error body. On curl older than 7.76 use `-f`, which fails the same way but discards that body.

The local Compose stack publishes no management port and enables no such plugin, so the AMQP route is the one that applies there. Order does not matter: deleting the queue first leaves the exchange with no binding, and deleting the exchange first leaves the queue with nothing able to route to it — still holding and still serving whatever it already had, which is why the queue's own deletion is what discards those messages. Both are idle by the time this step runs.

Nothing publishes to or consumes from them once the rollout completes, so this is housekeeping rather than a correctness step — an unretired generation is a bounded, idle queue, and the system is correct whether or not the deletion has happened.

**It cannot be automated, and the reason is the rollout itself.** A deletion executed at startup would race a not-yet-redeployed replica that is still publishing into the old generation, destroying dispatches for jobs that are legitimately `queued`. The safe moment is *after* every replica is on the new build, which is a fact about the deployment that no process can observe from inside. `video.jobs.dlx` and `video.jobs.dead` carry no generation suffix and must **not** be deleted — both generations share that sink deliberately, so there is one place to look at dead-lettered messages.

Those queues do not drain on their own either: the job queue carries no message TTL by design (see the RabbitMQ section below), so a superseded generation's backlog persists until it is deleted.

**The terminal-event generation retires nothing.** `emit-videojob-terminal-events` introduced `video.jobs.terminal.v1` / `video.jobs.terminal.events.v1` as a first generation, so there is no predecessor to delete after that deploy; the `.v1` suffix is there so a later payload change has an escape hatch, not because anything is being replaced. Do not extend the deletions above to any `terminal` name.

---

## Implemented Infrastructure

### PostgreSQL — Implemented (Phase 2 for identity, Phase 3 for video, Phase 7 for notification), required

Authoritative state store for users (`User` aggregate), `VideoJob`s, and `NotificationPreference`s, configured via `IDENTITY_POSTGRES_DSN`, `VIDEO_POSTGRES_DSN`, and `NOTIFICATION_POSTGRES_DSN` respectively — three independent variables and three independent pools, which this deployment points at one PostgreSQL instance and, on it, one database per context (`identity`, `video`, `notification`). Sharing the server is a deployment decision, not a design constraint: nothing in the code assumes the three resolve to the same server, and moving one to its own server needs no code change. Separate databases are what makes the context boundary enforced rather than merely observed — PostgreSQL has no cross-database query without an extension, so a query reaching from one context into another's tables fails as an unknown relation instead of returning rows. Schema/migrations for all three are applied automatically at startup (`postgres.Migrate`). **The DDL itself lives in three plain `.sql` files** — `internal/identity/infrastructure/postgres/schema.sql` (`identity_users`), `internal/video/infrastructure/postgres/schema.sql` (`video_jobs`, `video_job_outbox`), and `internal/notification/infrastructure/postgres/schema.sql` (`notification_preferences`, `notification_deliveries`) — embedded with `go:embed` and idempotent (`CREATE TABLE IF NOT EXISTS`), so each is also runnable by hand (`psql -f`) against a database that should carry the schema before any process starts. There is no separate migration tool and no ordering requirement between the binaries: whichever starts first creates what it needs. `docker/postgres-init/create-context-databases.sql` is not part of this — it creates the *databases* the local Compose stack needs (a runtime one and a test one per context), never a table. The video processing schema (`video_jobs` and the transactional-outbox `video_job_outbox` table) was added by Phase 3's `add-videojob-infrastructure`; `cmd/video-api/video.go`'s `setupVideo` (added by `wire-videojob-http-endpoints`) is what actually instantiates and migrates it at startup — `VIDEO_POSTGRES_DSN` is required exactly like `IDENTITY_POSTGRES_DSN`. Phase 7's `add-notification-domain-and-preferences` added a third: `notification_preferences`, migrated by `cmd/notification-api/notification.go`'s `setupNotification` from its own pool under `NOTIFICATION_POSTGRES_DSN`, also fatal when absent. `cmd/worker` opens neither the identity nor the notification pool.

- **The three databases are not created by the application.** `Migrate` creates tables inside a database that already exists; a DSN naming a missing database fails at startup with `database "video" does not exist`. Provision them however the deployment provisions databases — locally, `docker/postgres-init/create-context-databases.sql` does it on the Compose volume's first init. **Init scripts run only against an empty data directory**, so a volume that predates the per-context split holds `identity` and `identity_test` alone and needs the rest created against the running container:

  ```bash
  for db in video video_test notification notification_test; do
    docker compose exec -T postgres \
      psql -U identity -d identity -c "CREATE DATABASE $db"
  done
  ```

  Not `docker compose down -v`: that removes `minio_data` and `rabbitmq_data` along with the Postgres volume, discarding every stored result and every unpublished message. The statements above touch no existing data. A database that already exists makes its own statement print `ERROR: database "video" already exists` and exit non-zero without changing anything — harmless to ignore, but enough to abort the loop under `set -e`, so run it in a shell that does not set it.

- **`notification_deliveries` records one delivery per `(user_id, event_type, channel, job_id)`** (Phase 7, `add-notification-webhook-delivery`). It holds the delivery's status, attempt count, claimed-at and resolved-at times, a free-text reason written by this system, and two separate identifiers: a stable `delivery_id` the receiver deduplicates on, and a `claim_token` reissued on every grant that fences the resolving write. It holds **no** secret, no request body, and no part of the destination. It is what makes delivery exactly-once-ish over an at-least-once queue, and it is created by the same advisory-locked migration as the preferences table, in the same transaction.
- **`notification_preferences` holds a plaintext webhook signing secret, and that column is the most sensitive data this system stores.** It cannot be hashed: HMAC signing at delivery time needs the original bytes, so bcrypt — what `identity_users` uses for passwords — is structurally unavailable here. The application never discloses it (no route returns it, no read query even selects it, and `domain.Secret` refuses to render or serialize), so **database access and backups are the disclosure surface**. Treat a dump of this database the way you would treat a credential store: restrict `SELECT` on the column, keep backups encrypted and access-logged, and do not copy production data into a development environment. A leaked secret lets a third party forge a signature that a user's webhook endpoint will accept as coming from this system; rotating one means the user writing a new value through `PUT /api/notification-preferences`, since there is no operator-side rotation path and no way to read the old value back. **As of `add-notification-webhook-delivery` that value is now actually read back out of storage**, by `FindDeliverable` on the delivery path, and used to sign every outbound request — it is no longer write-only in practice, only in the API's view of it. Exactly one repository operation loads it and exactly one function consumes it; no path under `cmd/notification-api` reaches either, and both facts are enforced by tests rather than by convention. What that changes operationally is nothing about the column's exposure and everything about who needs the database: `cmd/notifier` now holds a pool that can read it, so its credentials, its logs, and its host are in the same blast radius the API's already were.
- **A `CHECK (secret <> '')` constraint on that column is an invariant, not a mechanism.** No adapter path depends on catching a violation — the insert statement always carries a non-empty secret and the update statement never names the column — so a violation in the logs means a genuine bug or a second writer, not a rejected request.
- **`Migrate` takes a `pg_advisory_xact_lock`** (class `0x46494158`, object `1`) for the length of its transaction, unlike the identity and video adapters. `CREATE TABLE IF NOT EXISTS` does not serialize two *first-time* creates, so without it two `cmd/notification-api` replicas starting together against a fresh database can race to a catalog uniqueness violation — which at startup means a replica that refuses to boot. If you add an advisory lock elsewhere, take a new object id under that same class; the comment in `internal/notification/infrastructure/postgres/migrate.go` is the registry.

- **Local/CI service:** `docker-compose.yml` at the repo root starts a matching `postgres:16-alpine` instance (`docker compose up -d postgres`) for running identity-dependent tests locally; CI provisions the same image as a service container. See [docs/development.md](development.md).
- **Local/CI credentials** (`identity`/`identity`) are fixed, non-secret defaults — never used outside a developer's machine or CI.

### Redis — Idempotency, rate limiting, status cache, and worker leases implemented

`internal/platform/redis` provides connection plumbing — `Config`/`LoadConfigFromEnv`, `Open`, `Ping`, `Close`. Three processes require `REDIS_ADDR`: `cmd/video-api` (idempotency and the status cache), `cmd/notification-api` (the shared limiter alone), and `cmd/worker` (leases, cache, and clearing a failed job's key). `cmd/identity-api` and `cmd/notifier` read it not at all. Redis remains additive to PostgreSQL, not a replacement. Four responsibilities are implemented:

1. **Idempotency keys** — **Implemented.** `internal/video/infrastructure/idempotency.RedisStore` deduplicates `POST /upload` requests by content hash + `UserID`: a `Reserve`/`Finalize`/`Clear`/`Lookup` protocol backs the "prevent duplicate job creation from client retries" goal. See [docs/architecture.md](architecture.md)'s Request pipeline section and `openspec/specs/upload-idempotency/spec.md`.
2. **Rate limiting** — **Implemented.** `internal/platform/ratelimit.Limiter` enforces a per-user, fixed-window request cap (`RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS`, both optional with defaults) on every authenticated route, mounted via each HTTP service's own `ratelimit.go`. Denied requests get `429` + `Retry-After`; a limiter failure (or an internal bounded timeout) fails open. See [docs/architecture.md](architecture.md)'s Request pipeline section and `openspec/specs/rate-limiting/spec.md`.
3. **Status cache** — **Implemented.** `CachedVideoJobRepository` provides cache-aside polling reads and atomic epoch/status-ordered write-through. Ownership decisions bypass the cache; Redis errors fall back to PostgreSQL correctness. No separate environment variable; the TTL is fixed at five minutes.
4. **Worker leases** — **Implemented.** `internal/video/infrastructure/lease.RedisStore` stores `videojob:lease:<jobID> = <lease_epoch>` with a fixed 90-second TTL. A holder renews every 30 seconds and reacquires an absent equal-epoch lease; the recovery sweeper uses successful absence at the observed epoch as its liveness signal. Lease errors fail open for execution but fail closed for takeover.

During a Redis outage, rate limiting, idempotency, status caching, and lease maintenance fail open for request/execution availability, while each sweeper query fails closed: it records `the lease store was unreachable; taking over none of these jobs` with an `unreachable` count, clears the queried job's prior confirmation, and takes over that job only after two later successful absence observations. PostgreSQL claims and fence predicates continue to prevent state corruption. Marks for jobs outside the failing scan batch are not globally cleared; if one survives the outage, its first successful post-outage absence can complete the pair. This is part of the documented prolonged-stall risk, not a two-fresh-observations guarantee for every job after any Redis outage.

### MinIO — Source and result storage implemented (Phase 5)

S3-compatible object storage. As of `migrate-result-storage-to-minio` it holds every processed ZIP result, and as of `migrate-upload-storage-to-minio` every uploaded source video too — so multiple API instances share one storage backend, a result survives its container, and no artifact class depends on local disk. `add-presigned-download-urls` then took the API out of the result-byte path, completing Phase 5: clients fetch results from MinIO directly under a bounded, signed URL.

`internal/video/infrastructure/storage` holds the connection plumbing (`Config`/`LoadConfigFromEnv`, `Open`, `Ping`, `EnsureBucket`, plus `BucketRegion`/`OpenPresigner` for the presign-only client) and both adapters — `ResultStorage` and `SourceStorage` — implementing their domain ports over the same client and bucket. Properties worth knowing:

- **Startup is fail-closed.** `setupVideo` loads, opens, pings, and ensures the bucket; any failure stops the process. See the environment table above, including the deployment-ordering note.
- **Source objects are transient, with one owner at a time.** `POST /upload` streams the video straight into the bucket without touching local disk. It deletes that object itself only if the job never reached `queued`; once the enqueue commits, the object is the worker's input and the request must leave it alone. See the Runtime Directory Structure section above for the best-effort caveat and for why the `uploads/`-prefix lifecycle rule is now the only guarantee rather than a backstop.
- **`GET /download/:filename` is authorized from the `VideoJob` row**, not from anything stored beside the artifact, and every rejection returns a byte-identical `404` so the endpoint cannot be used to probe for other users' results. It issues a 5-minute presigned URL rather than the bytes, so that authorization is the *complete* decision — nothing re-checks ownership when the URL is redeemed. `GET /api/status` lists a caller's `completed` jobs and reads each object's size and timestamp directly, and never carries a signed URL.
- **Startup makes one extra round trip.** After `Ping` and `EnsureBucket`, `setupVideo` calls `GetBucketLocation` and hands the region to the presign-only client. Without a configured region the signing library would try to discover it over the network on first use — against the *public* endpoint, which the server generally cannot reach. That call joins the fail-closed sequence: a failure stops startup.
- **Result keys are flat** (`frames_<jobID>.zip`) and must stay that way: the key is handed to the browser and used verbatim as `GET /download/:filename`'s single path segment, so a `/` would percent-encode and break the match. That constraint survived the move to presigned URLs — the route did too, and `app.js` still calls it — so it has not lapsed. Giving *results* a bucket prefix requires a frontend change. **Source keys do carry a prefix** (`uploads/<uploadID>_<filename>`) for exactly the complementary reason: no route exposes them, so no key of theirs ever becomes a URL path segment. Anything that re-exposes source objects over HTTP has to drop that prefix in the same change.
- **There is no teardown call**, unlike the Redis and PostgreSQL adapters above. `minio-go`'s client exposes none and keeps its transport unexported, so the package deliberately offers no `Close` rather than one that reports success while releasing nothing. Callers have no teardown obligation.
- **`Open` does not validate credentials or reachability.** Its error covers endpoint parsing and transport construction; wrong credentials and an unreachable server both surface on the first operation. Use `Ping` (a real round trip) to check connectivity.

Unlike Redis's, MinIO's contents are authoritative once results move there: `docker-compose.yml` gives the local service a named `minio_data` volume for that reason, since losing the bucket would leave `completed` `VideoJob` rows pointing at objects that no longer exist.

- **Local/CI service:** `docker-compose.yml` starts a pinned `quay.io/minio/minio` instance (quay.io rather than Docker Hub, whose `minio/minio` repository was withdrawn); CI starts the same image with a `docker run` step (a GitHub Actions service container cannot pass the `server /data` arguments the image requires). The adapter's own tests use `VIDEO_MINIO_TEST_*` against a separate bucket, since they create and delete buckets; `cmd/video-api`'s tests use the runtime variables.
- **Local/CI credentials** (`minioadmin`/`minioadmin`) are fixed, non-secret defaults — never used outside a developer's machine or CI.

---

### RabbitMQ — Topologies declared, published to by two outbox relays, dispatch consumed by `cmd/worker` and terminal events by `cmd/notifier` on both delivery channels (Phase 6; terminal events and delivery Phase 7)

`internal/platform/rabbitmq` opens, health-checks, and closes an AMQP connection and declares a topology; `internal/video/infrastructure/messaging` defines the one this context uses. Both shipped with `add-rabbitmq-infrastructure`.

**Four connections across the five processes.** `cmd/video-api` opens one, for the dispatch outbox relay publishing `video_job.queued.v2` events (`add-videojob-source-key-and-outbox-relay`). `cmd/worker` opens two: the consumer that reads those dispatches (`migrate-upload-to-async-processing`), and — since `emit-videojob-terminal-events` — a second relay publishing `video_job.completed.v1` and `video_job.failed.v1`. `cmd/notifier` opens the fourth, the consumer that reads that terminal stream (`add-notification-webhook-delivery`). Each declares its topology after every successful dial, so no process depends on another having started first, and a broker recreated while one was disconnected gets its entities back. See "The outbox relays" and "The worker" below.

- **`RABBITMQ_URL`** holds a full AMQP URI (`amqp://user:pass@host:5672/vhost`), not a `host:port` pair like `REDIS_ADDR`: the URI carries the scheme that selects TLS, the credentials, and the virtual host. `cmd/video-api`'s `setupVideo`, `cmd/worker`'s `setupWorker` and `cmd/notifier`'s `setupNotifier` each load it through `LoadConfigFromEnv` as their **first** step, before any I/O, so a missing variable fails fast and clearly instead of after PostgreSQL and MinIO have already been opened.
- **`Open` connects**, unlike the Redis and MinIO adapters, which construct a client without touching the network. AMQP has no lazy client, so an unreachable broker or wrong credentials surface immediately rather than on first use.
- **The health check is a real round trip** — it opens a channel and closes it. The client's own `IsClosed()` predicate reports only what the process has already observed, which is stale for a broker that stopped answering without the connection being torn down.

The declared dispatch topology, and the two operational policies in it that are decisions rather than defaults:

| Entity | Name | Arguments |
|---|---|---|
| Job exchange | `video.jobs.v2` | `direct`, durable |
| Routing key | `video_job.queued.v2` | equal to the outbox `event_type` string |
| Job queue | `video.jobs.queued.v2` | `x-max-length` 10 000, `x-overflow` `reject-publish`, dead-letters to `video.jobs.dlx` |
| Dead-letter exchange | `video.jobs.dlx` | `fanout`, durable |
| Dead-letter queue | `video.jobs.dead` | `x-message-ttl` 24 h, `x-max-length` 10 000, `x-overflow` `drop-head`, forwards nowhere |

The terminal-event topology, declared by `cmd/worker`'s relay and sharing the same dead-letter sink:

| Entity | Name | Arguments |
|---|---|---|
| Terminal exchange | `video.jobs.terminal.v1` | `direct`, durable |
| Routing keys | `video_job.completed.v1`, `video_job.failed.v1` | each equal to the outbox `event_type` string it carries |
| Terminal queue | `video.jobs.terminal.events.v1` | bound under **both** routing keys; `x-max-length` 10 000, `x-overflow` `reject-publish`, dead-letters to `video.jobs.dlx` |


- **A full job queue refuses new publishes; it does not drop old ones.** `reject-publish` means the broker nacks the publisher rather than evicting the oldest queued job, so a full queue becomes back-pressure: the publisher leaves its outbox row unstamped, retries, and the system resumes when the queue drains. Nothing is lost. Expect a stalled relay and a growing count of unpublished outbox rows as the symptom, not missing jobs.
- **Job messages never expire**, and that takes two things, not one. The job queue deliberately carries no `x-message-ttl`; publishers must also leave the per-message `expiration` property unset, since RabbitMQ honours it independently of any queue setting. Either one would dead-letter a message without any update to its `video_jobs` row, and the state machine has no transition out of `queued` except to `processing` — so the job would report `queued` to its owner forever. A backlog therefore persists until it is consumed rather than aging out, which is the intended trade.
- **The generation suffix is on the exchange, the queue, *and* the routing key** — which is also the outbox `event_type` string. Versioning the exchange alone was the original plan and it does not work: every Video API replica's relay claims from the one shared `video_job_outbox` table, filtered on `event_type`, so with a single shared string a redeployed replica's relay would claim a not-yet-redeployed replica's row and publish it into the new generation. The exchange bump is kept alongside it because the two close different holes — the event type stops a relay *claiming* the wrong generation's row, the exchange stops the broker *delivering* to the wrong generation's queue.
- **What a generation bump protects is the rolling-deploy window, not stale messages.** Stale messages are already harmless: the claim is conditional on `status = 'queued'`, so a message naming a job that has moved on is refused and dead-lettered with no side effect. What that does not protect is a job that is *legitimately* `queued` while two processing models are live — during the cutover deploy, an old in-request replica and a new worker could both act on one job, and the loser's cleanup would delete the source out from under the winner's running extraction.
- **The dead-letter sink carries no suffix.** `video.jobs.dlx`/`video.jobs.dead` are shared across generations deliberately: a dead-lettered message is for inspection, and one place to look beats one per generation.

Like PostgreSQL's and MinIO's, this broker's contents are authoritative once the relay ships: an acknowledged, `published_at`-stamped message is the only record that a job is waiting. `docker-compose.yml` gives the local service a named `rabbitmq_data` volume and a pinned `hostname` for that reason — RabbitMQ keys its Mnesia directory by hostname, so the volume does nothing without it.

Because the volume persists, a plain `docker compose down` no longer clears queued messages. **Do not reach for `docker compose down -v` to clear them:** that flag removes every named volume in the project, so it destroys the local PostgreSQL database and the MinIO bucket along with the queue. Reset the broker alone instead:

```bash
docker compose stop rabbitmq
docker compose rm -f rabbitmq
docker volume ls --filter name=_rabbitmq_data     # find this project's volume
docker volume rm <name-from-the-line-above>
docker compose up -d rabbitmq
```

The volume is named `<project>_rabbitmq_data`, and Compose derives `<project>` from the directory the file lives in — so it is `video-processor_rabbitmq_data` in a default clone and something else in a differently-named checkout. Look it up rather than guessing: a wrong name makes `docker volume rm` fail and leaves the messages exactly where they were.

- **Local/CI service:** `docker-compose.yml` and CI both start `rabbitmq:4-alpine`. CI uses a service container, unlike MinIO, whose image needs command arguments a service container cannot supply.
- **Local/CI credentials** (`video`/`video`) are fixed, non-secret defaults. They are a dedicated account rather than the built-in `guest` because RabbitMQ confines `guest` to loopback as the broker itself sees it, and every connection here arrives over a Docker network.

#### The outbox relays

`cmd/video-api` starts one relay goroutine (`internal/video/infrastructure/messaging.Relay`) and stops it on `SIGINT`/`SIGTERM`. It exists because `POST /upload` must not depend on the broker: `Repository.Enqueue` commits the `pending → queued` update and a `video_job.queued.v2` outbox row in one transaction, and the relay carries that row to RabbitMQ afterwards.

`cmd/worker` starts a second one, of the same type and with the same cycle, for the terminal events. It runs there rather than in `cmd/video-api` because the worker is the process that writes those rows — its own `CompleteJob`/`FailJob` commits and the sweeper's abandonment write — so an outcome is still announced when no API replica is up. The two relays claim **disjoint sets of event types** from the one shared `video_job_outbox` table (`event_type = ANY(...)`), so neither's backlog can starve the other's, and each publishes every row under the routing key that row's own `event_type` names.

Each cycle it opens a transaction, claims a bounded batch of unpublished rows with `SELECT … FOR UPDATE SKIP LOCKED` (so several replicas can each run one without dispatching the same row twice), publishes them **mandatory** on a confirm-mode channel, stamps `published_at` only for the messages the broker both acknowledged and did not return, and commits. The poll interval (2 s), the batch size (100 rows), the confirmation timeout (15 s), and the dial backoff are compile-time constants — there is no environment variable to tune them, matching the status cache's fixed TTL.

Operationally, three things are worth knowing before they surprise you:

- **A full job queue stalls the relay, by design.** `reject-publish` nacks the publish instead of evicting a queued job, so the row stays unstamped and the next poll retries it. Nothing is lost, and uploads keep being *accepted* — but they stop being *processed*, since the dispatch never reaches the queue. A queue at its 10 000-message limit now means workers are not keeping up (or are not running); add workers rather than raising the limit.
- **Unpublished outbox rows are the symptom to look at**, not a missing-message count on the broker:

  ```sql
  SELECT event_type, count(*), min(occurred_at)
    FROM video_job_outbox
   WHERE published_at IS NULL
   GROUP BY event_type;
  ```

  A growing `video_job.queued.v2` count with an ageing `min(occurred_at)` means the dispatch relay is not publishing — a broker that is down, a full queue, or an unroutable exchange. The same reading applies to `video_job.completed.v1` and `video_job.failed.v1` for the worker's terminal relay, with one difference in the expected steady state: those two should hover near zero and drain within a poll or two regardless of what the consumer is doing, because a stamped row means the broker accepted and routed the message, not that anyone read it — a notifier that is down shows up as queue *depth*, never as unpublished rows. A growing unpublished count for them, paired with a `video.jobs.terminal.events.v1` depth sitting at its 10 000-message limit, is the `reject-publish` back-pressure symptom described below. A large and **steadily growing** `video_job.created` count is normal: one row is written per job created and none is ever marked published, so that number only ever goes up. Those rows are internal events, are never dispatched, and are excluded from the claim by the `event_type` filter and its partial index (`video_job_outbox_unpublished_idx`) — which is exactly why an unbounded backlog there is harmless rather than a leak to chase.
- **Delivery is at-least-once.** The relay commits only after the broker acknowledges, so a crash in between republishes rather than loses. A consumer must tolerate a duplicate regardless, since a nack or a consumer crash produces one too. That holds for the terminal events too: exactly one *row* is recorded per job outcome, but one row can still become more than one message, and deduplication belongs to whatever consumes the queue. `cmd/notifier` discharges that obligation with a durable delivery record keyed on `(user_id, event_type, channel, job_id)` and claimed atomically **before** any request is made — not by trusting the transport, and not by recording afterwards, which would leave a read-then-act window two consumer processes both lose.

Its lifecycle transitions are recorded under a `phase` field — `started`, `connected`, `connection_lost`, `stopped` — because a healthy relay is otherwise invisible, and because `connected` appearing a second time is how a reconnection reads. Repeated dial failures back off from 1 s to a 30 s ceiling, and the topology is redeclared after every successful dial, so a broker that was recreated while the relay was disconnected gets its exchange and queues back before the next publish.

#### The terminal-event queue and its consumer

`video.jobs.terminal.events.v1` is declared, durable, and consumed by `cmd/notifier` as of `add-notification-webhook-delivery`. It was declared ahead of its consumer rather than with it because the relay publishes **mandatory**: an exchange with no bound queue returns every message unroutable, the row is never stamped, and the relay would re-attempt it on every poll forever.

- **Expected depth: near zero with a notifier running, rising without one.** Every job that reaches `completed` or `failed` adds a message; the notifier removes each after it reaches a disposition. Depth is therefore the honest signal for "is anything announcing outcomes" — and a rising depth with jobs still finishing means no notifier is consuming, not that the relay is failing (the unpublished-row query above is what distinguishes those two).
- **A depth that stalls for up to the reclaim bound is not a stuck consumer.** At prefetch 1 an abandoned claim causes head-of-line blocking bounded by `NOTIFICATION_DELIVERY_RECLAIM_SECONDS`; see "Two signals this design deliberately leaves behind" above, which also gives the query that tells the two apart.
- **At the 10 000-message bound it becomes back-pressure, not loss.** The queue carries `x-overflow reject-publish`, so the broker nacks the relay rather than evicting the oldest outcome; the relay leaves the row's `published_at` NULL and retries on the next poll. The symptom pair is a queue pinned at its limit **and** a growing count of unpublished `video_job.completed.v1`/`video_job.failed.v1` rows in the query above. Nothing is lost, and nothing about job processing degrades: uploads, extraction, and downloads are all unaffected, because the only thing stalled is the announcement. Reaching the bound now means the notifier has been down or behind for a long time — start one, and let it work through the backlog.
- **Do not purge this queue.** A purged message is not regenerated: its outbox row was stamped `published_at` the moment the broker acknowledged and routed it, so the relay has no reason to publish it again. `rabbitmqctl purge_queue video.jobs.terminal.events.v1` therefore takes the outcome announcements of every job it holds out of existence. What survives is the authoritative record — the `video_jobs` row and its outbox row — so nothing about a job's state, its artifact, or its download is affected; what is lost is only the notifications those jobs would have produced. This was an acceptable response to the bound while no consumer existed; now that one is deployed, start or fix the notifier and let it drain the buffered burst instead.
- **A first start does not announce the whole backlog indiscriminately.** A delivery is made only to a preference that was **created before the event occurred**, so outcomes that predate a user's enrolment are handled and dropped rather than announced. That is a standing rule, not a one-time deploy cutoff: it keeps holding once the backlog is drained, and it cannot be forgotten at the next deploy.
- **The queue carries no message TTL, and the reason differs from the job queue's.** There, an expired message would leave a job reporting `queued` with nothing able to advance it. Here the job is already terminal and correct in PostgreSQL — what expiry would discard is the only announcement that outcome ever gets.

#### The worker

`cmd/worker` consumes `video.jobs.queued.v2` with a **prefetch of one**: one unacknowledged delivery at a time, because the unit of work is a full `ffmpeg` run and buffering a second delivery would hide it from every other consumer for the duration. Scale out by running more worker processes; there is no concurrency setting to raise. The local `docker-compose.yml` starts three (`worker`'s `deploy.replicas`, overridable per run with `--scale worker=<n>`); a deployment scales the same way, by process count.

A delivery is acknowledged only after a terminal outcome is confirmed. Cleanup depends on whether this actor applied it; everything without a terminal outcome is rejected without requeue and reaches `video.jobs.dlx`:

| Situation | Disposition | Job left as | Source / lease cleanup |
|---|---|---|---|
| Body will not decode, names no source key, or names an unknown job | Reject → DLQ | untouched / n/a | untouched |
| Claim lost (duplicate or stale dispatch) | Reject → DLQ | untouched | kept; this run acquired no lease |
| Run broke before any terminal state committed | Reject → DLQ | usually `processing` | kept; lease left to expire so recovery can act |
| This run applied `failed` | **Ack** | `failed` | one best-effort attempt each to delete the source, conditionally clear the idempotency key, and release the held lease; failures are logged without changing the Ack |
| An identical `failed` outcome was already present | **Ack** | `failed` | no cleanup; this actor did not apply the write |
| Result stored but completion still errors after 4 retries | Reject → DLQ | usually `processing` | source and lease kept; result key logged |
| Terminal write returns `ErrJobFenced` | Reject → DLQ | authoritative winner's state | source/idempotency untouched and no lease released; held epoch is logged, plus the result key when a fenced completion produced one. The current log says `taken over` for both a newer epoch and a same-epoch terminal winner |
| Completion succeeds, including a retry that finds its identical outcome already present | **Ack** | `completed` | one best-effort attempt each to delete the source and release the held lease; failures are logged without changing the Ack |

The AMQP consumer requeues only a delivery pulled off the channel after shutdown, before handling began. Crash recovery does not broker-requeue a `processing` delivery: the sweeper first commits a new `queued` row state and outbox event, and the ordinary relay publishes a fresh dispatch.

**Operator symptom: a job remains `processing`.** After successful acquisition, a claimed job holds Redis key `videojob:lease:<jobID>` with its PostgreSQL `lease_epoch` as the value and a 90-second TTL. Acquisition errors fail open, so a running job may temporarily have no key. The worker renews every 30 seconds. The sweeper runs every 60 seconds, scans at most 50 rows with a rotating keyset cursor, and acts only after two consecutive successful "not held at this epoch" observations. A Redis query error clears the first observation and takes over nothing.

```sql
SELECT id, user_id, status, source_key, lease_epoch, created_at
  FROM video_jobs
 WHERE status = 'processing'
 ORDER BY id ASC;
```

Correlate each candidate with worker logs and, from an authorized Redis shell, `GET videojob:lease:<jobID>` plus `PTTL videojob:lease:<jobID>`:

- the same epoch with a positive TTL shows only that the lease has not expired; sample `PTTL` again to confirm it increases on renewal rather than counting down to zero — extraction duration alone does not imply abandonment;
- no key is one observation, not permission to mutate the row — allow the sweeper a second successful observation;
- a greater key epoch belongs to a successor and fences the older run;
- `the lease store was unreachable; taking over none of these jobs` means Redis recovery failed closed and all pending confirmations for affected jobs were reset;
- `the job was requeued` (with `job_id` and `lease_epoch`) means the row advanced and a new outbox dispatch committed;
- `the job failed after abandonment` means the row exhausted three requeues, or had no source key, and the sweep applied the terminal write.

All three are records from `component: recovery_sweeper`, so `jq 'select(.component == "recovery_sweeper")'` is the filter that shows the sweep's whole account of a cycle; a named job's is `select(.job_id == "<jobID>")`.

Normal recovery latency includes the remaining lease TTL plus up to two sweep intervals. A backlog may add cycles because one cycle examines 50 rows. Restarting a worker also discards its in-memory first-observation marks, deliberately requiring two fresh observations. Confirmation mitigates the ordinary claim-to-acquire race but is not a proof against a process paused across multiple scans: such a run may be treated as abandoned and fenced when it resumes; at the requeue bound, recovery may instead commit `failed` and remove the source.

Do **not** manually change `status` or `lease_epoch`, publish a dispatch, or delete a lease. The requeue and outbox insert must commit together, and the epoch increment is what fences a prior worker. If recovery does not happen, preserve the row and artifacts, inspect the logs above, verify `source_key` still exists, and check for `frames_<jobID>.zip` from a fenced run. Escalate rather than bypassing repository predicates.

Shutdown is `SIGINT`/`SIGTERM`: cancellation tells the consumer to stop taking deliveries and the recovery sweeper to stop concurrently. `run` joins the sweeper first, then stops and joins the terminal-event relay — both hold a database transaction while they run, so the pool must outlive them — and only then waits up to **5 minutes** for the consumer's job in hand. The handler is detached from the shutdown signal, so a normal shutdown does not kill `ffmpeg` or abort its terminal write. If the deadline expires and the process is terminated, the delivery may be redelivered and lose its old claim; after the abandoned lease expires and absence is confirmed, the sweeper either advances the row's epoch and emits a fresh dispatch or, at the requeue bound, commits terminal `failed` without another dispatch. Give worker containers more than five minutes of stop grace, adding margin for the preceding sweeper and relay joins and final resource shutdown, to avoid duplicated extraction even though the fence protects state. The relay is stopped where its cycle stands rather than drained to empty, so a terminal row committed during the drain is published by a later cycle — in the next worker to start, or in another replica — rather than delaying shutdown.


### The e-mail channel

`add-notification-email-delivery` opened the `Channel` set to `email`. A preference on it carries an address instead of a URL, in the same `destination` column, and `cmd/notifier` sends it one plain-text message through the relay `NOTIFICATION_SMTP_ADDR` names. Five things are operationally distinct from the webhook channel.

**The address is self-declared and unverified.** It is the address the user typed on `PUT /api/notification-preferences`, not the verified one Identity holds — Notification never calls Identity, and no `UserRegistered` event exists. A user can therefore register an address they do not control. What bounds the exposure is that a delivery is only ever triggered by a job the *same user* owns, so the volume is the registrant's own uploads rather than an open relay; there is no path that sends mail no job of theirs produced. An address-confirmation flow is a change of its own and has not shipped. If your deployment sends through a relay whose reputation you care about, this is the paragraph to read before pointing it at the public internet.

**A recorded `delivered` means the relay accepted the message, not that it arrived.** On the webhook channel the same status means the receiver answered `2xx`. Here a bounce is asynchronous, is returned to the envelope sender, and is never observed by this system, so `notification_deliveries` cannot report it. One table, two meanings; the row does not say which, so read the `channel` column.

**STARTTLS is opportunistic in attempt and mandatory in outcome.** The client upgrades whenever the relay advertises the extension, and there is deliberately **no plaintext fallback**: if the handshake fails — an expired certificate, a self-signed one, a name that does not match `NOTIFICATION_SMTP_ADDR`'s host — the attempt fails and is recorded as a `transport_failure`, with nothing in the reason naming TLS. That is the intended posture and a known diagnostic gap: **every e-mail delivery failing as `transport_failure` against a relay that is plainly reachable is the symptom of a certificate the client cannot verify.** Check it with `openssl s_client -starttls smtp -connect <NOTIFICATION_SMTP_ADDR>` from the notifier's own network before looking anywhere else.

**Credentials are never sent over an unencrypted session.** Configuring `NOTIFICATION_SMTP_USERNAME`/`PASSWORD` against a relay that offers no encryption fails the attempt as a *policy refusal* rather than authenticating in the clear. This is not the destination policy's question and `NOTIFICATION_ALLOW_INSECURE_DESTINATIONS` deliberately does not reach it: that switch relaxes rules about *user-supplied* destinations, while the relay is operator-supplied and what is protected is this deployment's own credential. The local compose stack configures no credentials, which is why it works against a relay with no TLS.

**Nothing on this path reads a signing secret, and nothing logs an address.** The SMTP adapter never touches `Secret` — asserted at the source level, since a value that was never read cannot be observed at runtime — and the delivery read's projection yields a secret only for a row whose channel signs, so an `email` preference that happens to carry one does not have it loaded. Recorded reasons and log lines are built from this system's own classification: an SMTP reply code is kept (it is a number the relay chose, and it is safe), and the relay's message text is dropped, because relays conventionally quote the envelope recipient back in it.

Locally, `docker compose up --build` starts a mail catcher that accepts everything and forwards nothing; its inbox is on `127.0.0.1:8025`. It publishes its own loopback-bound port rather than taking a route on the gateway, which serves the application's surface only.

### Logging — Implemented (Phase 8)

**All five processes write JSON records to standard output, and nothing else.** There is no second format, no console renderer for development, and no configuration that selects one — `add-structured-logging` deliberately left the format and the destination unconfigurable, so the guarantees below hold in the environment a developer is watching and in the one they are not. Standard output rather than standard error matches the gateway's own split, where the access log is stdout and only nginx's own errors are stderr, and it means `docker compose logs` and any collector that reads a container's stdout get the whole stream. The one thing that is not a record is a Go runtime panic trace from before the logger exists — an unrecognized `GIN_MODE` is the way to provoke one, and its row above says why.

Every record carries `time`, `level`, `msg`, and two identity fields bound once at startup:

| Field | Value |
|---|---|
| `service` | One of `identity-api`, `video-api`, `notification-api`, `worker`, `notifier` — a closed set, fixed in `internal/platform/logging`. These are the binary names, which are also the Compose service names, so a filter matches what you already type. No `cmd/` prefix. |
| `instance` | `<hostname>-<pid>-<n>`, resolved once per process. Under Docker the hostname is the container id; run directly, two `go run ./cmd/worker` processes share a hostname, which is exactly why the process identifier is in there. `docker-compose.yml` runs **three** worker replicas, so this is the field that says *which* worker. |

**Identifiers are fields, never substrings of the message.** The message is a fixed string literal for a given call site — enforced at the source level, not by convention — so grouping by `msg` groups occurrences of one event, and selecting by `job_id`, `delivery_id`, `storage_key`, `lease_epoch`, `user_id`, `event_type` or `channel` selects on the thing itself. `component` names the subsystem (`http_access`, `outbox_relay`, `recovery_sweeper`, `video_upload`, …), replacing the hierarchical `video: worker: sweep:` prefixes the prose used to carry.

```bash
# One job, end to end. It returns records from video-api (the accepted-job
# record), worker, and notifier — that is the point of the field. The five
# service names are named explicitly for the reason stated below the block
docker compose logs --no-log-prefix identity-api video-api notification-api worker notifier \
  | jq -c 'select(.job_id == "<jobID>")'

# Which worker replica did the work
docker compose logs --no-log-prefix worker | jq -r '[.instance, .msg] | @tsv'

# Everything a process complained about
docker compose logs --no-log-prefix notifier | jq -c 'select(.level == "ERROR")'
```

`docker compose logs` also collects nginx, PostgreSQL, Redis, RabbitMQ, MinIO and the mail catcher, none of which this repository builds and none of which emits JSON. Scope the command to the five application services before piping to `jq`, or it fails on the first line of somebody else's format.

**Each HTTP request, save the one exempt class named below, produces exactly one access record** (`component: http_access`) naming the method, the matched route, the status, the duration, the response size, and the authenticated subject where the request carried one. It carries **no query string, no header, and no body**, and no request path when a route matched — the route template is the bounded field, and a matched path adds only parameter values the handler already records. A request that matched *no* route records its path instead, truncated to 256 bytes, and a request method that is not a recognized HTTP method is replaced by `UNRECOGNIZED`: both values are caller-supplied and reach the record before authentication and before the rate limiter, so both are bounded there. A recovered panic is a record like any other (`component: http_recovery`, error severity, carrying the panic value and the stack) — including a panic on a connection the client has already dropped, which gin's own recovery middleware handles on a branch that never reaches a supplied handler, and including a panic raised while serving a probe route, which the exemption below does not reach: what is exempt is the routine per-request record, not the report of a failure.

**Exactly one class of request is exempt from the access record: one that matched `GET /health` or `GET /ready`.** The exemption is a closed list of those two route templates in each root's `logging.go` and there is no general mechanism behind it — no configurable skip list, no per-route option — because a general one would let a future route leave the access log without that ever being reviewed. What the excluded records would have contained is the justification: a liveness record's status is `200` and its duration near zero on every occurrence, so it varies in no field and carries no information, while probes arrive at a fixed interval forever across three services and would become the majority of everything this system emits. Recording them below the default threshold was considered and refused — records invisible at the default setting reappear in bulk exactly when an operator lowers it to investigate something else.

**In their place, one record per readiness *transition*** (`component: readiness_probe`), not one per probe: a `warn` when the verdict turns from ready to not ready, naming the failing dependencies in the `dependencies` field as a comma-joined string drawn from a closed set this repository owns (`postgres`, `object_storage`) plus a `dependency_count`, and an `info` on the return to ready, which names nothing because what recovered is the service. The verdict is swapped by an atomic compare-and-set, so two probers observing one change between them produce one record and not two. A freshly started process holds **ready** before its first probe — startup verified every readiness dependency and is fatal without them — so a healthy start emits nothing rather than announcing a recovery from a degradation it never had.

**No transition record is ever emitted on behalf of a caller that vanished.** Every check derives from the request's context, so one canceled request fails all of them at once, which is indistinguishable at that point from every dependency being down; recorded, it would name the whole inventory in a warning and then announce a recovery from it on the next healthy probe — a pair of events that never happened and that an operator pages on. The handler consults the request context after the checks and, if the caller has gone, records nothing and leaves the verdict where it stands: a real degradation this probe could not confirm is still there for the next probe from a live caller to record. The response is still `503`, because readiness was not established.

**What a record never carries** is unchanged by this and worth restating in one place, because the stream is now easy to grep and therefore easy to over-collect: no presigned download URL (it is a credential — the `StorageKey` is logged instead), no webhook signing secret, no destination query string (which is where a webhook credential legitimately lives, and why every recorded reason on the delivery path is built from this system's own classification rather than from a transport error's text), no request or response body, and no token. A log call may only build a field from a scalar it extracted itself, so a domain aggregate cannot be handed to the logger and serialized by accident.

**Severity is per-process and optional** — `LOG_LEVEL`, documented in the table above. An unparseable value stops the process, structurally: it is reported as a record and the exit code is non-zero, rather than starting quietly at a severity nobody asked for.


### Health and readiness probes — Implemented (Phase 8)

Each of the three HTTP services serves two probe endpoints, and they differ in *criterion* rather than only in path.

| Endpoint | Consults | Answers |
|---|---|---|
| `GET /health` | nothing at all | always `200 {"status":"ok"}` |
| `GET /ready` | that service's readiness dependencies, on every request | `200 {"status":"ready"}` or `503 {"status":"not ready"}` |

**Why two rather than one**, because the pair looks redundant until the failure it prevents is named: the action a runtime takes on a failed liveness answer is to restart the process, and restarting a process cannot repair a dependency. A single endpoint that both consults dependencies and drives restarts turns a bounded dependency outage into a crash loop that outlives it — every replica is restarted, each restart re-runs a startup sequence this document specifies as fatal on exactly that dependency, and each therefore exits. A 30-second PostgreSQL failover would be survived by a process that merely serves `503` for thirty seconds, and not by one that is killed for it. `/health` consults nothing so that the dependency-consulting endpoint can never become the restart trigger. What a `200` from it asserts is correspondingly narrow: the process is scheduled, it is still accepting connections, and its global middleware chain still returns. Nothing about any dependency, any other route, or whether work is progressing.

#### The readiness matrix

| | `identity-api` | `video-api` | `notification-api` |
|---|---|---|---|
| PostgreSQL (that service's own context database) | checked | checked | checked |
| MinIO — **the configured bucket is present**, not merely that the server answers | not held | checked | not held |
| Redis | not held | **deliberately excluded** | **deliberately excluded** |
| RabbitMQ | not held | **deliberately excluded** | not held |

The criterion behind every cell is not invented here. It is the one this stack already applies to its own ingress, written into `docker-compose.yml` beside the gateway's `nginx -t` healthcheck: *the gateway is healthy when it can serve, and tying its health to a backend's would make an unrelated service's restart look like an ingress failure.* Stated generally — a readiness dependency is one whose absence stops the process serving — the per-service answers are derivable rather than a matter of taste.

- **PostgreSQL is a readiness dependency of all three.** Every route that does anything reads or writes the service's own context database, and no route is specified to degrade without it.
- **Object storage is one for `video-api` alone**, and the check asks whether the configured bucket is *present*. `POST /upload`, `GET /api/status` and `GET /download/:filename` all name objects inside one bucket, so a reachable object store whose bucket has been removed fails every one of them while a reachability check still succeeds.
- **Redis is consulted by none of the three, and the verdict is common where the reason is not.** `video-api` and `notification-api` each construct a client at startup and **deliberately keep it out** of readiness: every feature built over it is specified to fail open — the upload idempotency reservation logs and proceeds, the rate limiter allows the request, the status cache falls back to PostgreSQL — so a service with Redis down is slower and unmetered and still correct, and reporting it not ready would remove a serving process from rotation for a condition this document already calls survivable. `identity-api` **has no Redis dependency to exclude**: it reads no `REDIS_ADDR`, constructs no client, and mounts no rate limiter or anything else built over one, which is why its cell reads *not held* rather than *deliberately excluded*. The distinction is the one this matrix exists to draw — a dependency a service owns and tolerates the loss of is not the same thing as one it does not have — and it is the distinction the MinIO and RabbitMQ rows already make.
- **RabbitMQ is deliberately excluded on `video-api` and not held by the other two**, for two independent reasons. `POST /upload` commits the transition to `queued` and its outbox row in one PostgreSQL transaction and answers `202` with the broker unreachable — that is the design, not a tolerated defect — and the relay dispatches when the broker returns. Structurally, the readiness path has no AMQP connection to check even on `video-api`, and the precise reason is **access rather than absence**: that process *does* hold a connection whenever its dispatch relay is serving, but the relay opens it inside its own dial cycle, holds it for that cycle and closes it at the end (`internal/video/infrastructure/messaging/relay.go:104-134`), so the connection is transient and no composition-root handle and no probe handle exposes it. Do not read the row as *this process never opens one* — it opens one per dial, and what the probe lacks is a way to reach it. And `internal/platform/rabbitmq`'s health check takes a live `*amqp.Connection` and no context, so it could not be bounded the way every readiness check must be even if a handle were passed to it.

The database check is `db.PingContext`, which is what startup already uses. The object-storage check is **new** (`storage.CheckBucket`) and `storage.Ping` is deliberately not reused for it — see the MinIO section above for why those two must stay two.

#### The bound and the prober's timeout

Every check is bounded by `readinessCheckTimeout` (2s, in each root's `readiness.go`) derived from the request's own context, so a hung dependency cannot hold a goroutine and a caller that disconnects releases the work. Where a service has two dependencies the checks run concurrently, so the endpoint's worst case is the slower check rather than the sum.

**That bound must stay strictly below the timeout of whatever probes the endpoint** — in this stack, the `timeout: 5s` on each service's compose healthcheck. Stated in both directions because only one of them is safe: with the prober's timeout the larger, an unhealthy dependency produces a `503` the prober reads; with the handler's bound the larger, the prober abandons every request and **every verdict becomes a failure whatever the dependency is doing**. The signal does not degrade, it inverts, and it inverts without emitting anything that says so. The two numbers live in different files, which is why the relationship is written down rather than left to be inferred from either.

#### What a probe response does and does not say

The bodies are fixed per verdict and neither endpoint names the dependency that failed, nor any error text, host, endpoint address, connection string, bucket name, version, or instance identifier. The status code carries the whole answer. A readiness body naming the failing dependency would publish this deployment's dependency inventory to an unauthenticated caller and, by repetition, the times at which each part of it is degraded — the same posture the destination policy's single sentinel and the download route's byte-identical `404` already take. Both responses carry `Cache-Control: no-store`, so no intermediary answers a probe from a stored verdict.

The diagnostic is relocated rather than destroyed: which dependency failed goes to the log, whose reader is already inside the deployment. See the readiness transition record under "Logging" above.

Both endpoints are registered on the engine, outside the bearer group and outside the rate limiter. A probe carries no authenticated subject for the limiter to key on, and a probe at a fixed interval from inside it would eventually exhaust a budget and be answered `429` — the limiter manufacturing the outage it is meant to have no part in.

#### Reaching a probe

**Neither path is reachable through the gateway.** `docker/nginx/nginx.conf` refuses `/health` and `/ready` with two exact-match `location = … { return 404; }` blocks that carry no `set $backend` and no `proxy_pass`, so the refused request reaches no service — no handler runs and no dependency is consulted. Without them the outcome would have been asymmetric rather than chosen: the gateway sends everything it does not otherwise name to `video-api`, so that one service's probes would have been public while the identical paths on the other two were not.

`404` is the status an unserved application path already answers with, and **status-code equivalence is the requirement — the two are not byte-identical and the configuration says so**. nginx generates its refusal while an unserved path is refused by gin and handed back through the proxy, so they differ in body, `Content-Type` and `Content-Length`. Collapsing that would take `proxy_intercept_errors on`, which replaces the body of *every* upstream `404` in the system, including the one `GET /download/:filename` keeps byte-identical across all of its rejections on purpose. That trade was refused.

A prober therefore reaches a service on its own `:8080` from inside the network, and no application service publishes a host port for it.

#### The local stack

Each of the three HTTP services carries a healthcheck against `/ready` — `interval: 10s`, `timeout: 5s`, `retries: 5`, no `start_period`. It targets readiness rather than liveness because a container that is running but cannot serve is what an operator needs to see. The command uses `wget` rather than `curl`, which is not in the Alpine runtime image; the flags issue a `GET` (verified against the service's own access record, since busybox `wget --spider` may issue a `HEAD`, which gin does not answer on a `GET`-only route) and exit non-zero on a `503`.

**Nothing depends on these verdicts.** No `depends_on: condition: service_healthy` was added against them, and the gateway's `depends_on` stays a bare list. Making the gateway wait for a backend to be *ready* would tie ingress startup to that backend's own dependencies — the coupling the gateway's own healthcheck exists to refuse — and it already tolerates a backend that is not yet up, because it resolves each backend's address per request rather than at configuration load. This system has no orchestrator, so a readiness verdict is presently read by a person and by nothing else, and the liveness endpoint has no consumer in this repository at all. It is served anyway, because the pair is what keeps the criterion legible: a lone readiness endpoint invites whatever arrives next to restart on it.

Verified by stopping each backing service in turn: with PostgreSQL down all three report unhealthy and the gateway does not; with MinIO down `video-api` reports unhealthy and the other two do not; with Redis down all three stay healthy. That last one is what proves the criterion was implemented rather than merely written down.


---

## Planned Infrastructure (Not Yet Implemented)

> The components below are planned for future phases and do not exist in the current deployment. Each is labeled with the phase that introduces it.

**E-mail delivery is no longer planned — it shipped** (`add-notification-email-delivery`) and is documented above: the relay variables under "Environment Variables", the local mail catcher under "Docker", and the operational notes under "The e-mail channel", at the end of the implemented-infrastructure section above.

### Observability — Only metrics remain (Phase 8)

**Two of Phase 8's three changes have shipped and are documented above**, not here. `add-structured-logging` is under "Logging", alongside `LOG_LEVEL` in the environment-variable tables; it went first because it is the only one of the three that touches every file the other two will. `add-health-and-readiness-endpoints` is under "Health and readiness probes", with the per-service readiness matrix and the one constraint that spans two files.

**What remains is metrics alone**: Prometheus counters, gauges and latency histograms at `/metrics`. It is not decomposed yet, and it inherits logging's non-disclosure question in the form of label cardinality — an identifier in a label is unbounded cardinality as well as a possible disclosure.

**The question the two remaining changes used to share is now answered, and the answer has a cost worth stating.** `cmd/worker` and `cmd/notifier` acquire **no** HTTP surface — not for probing, not for anything — and that is now a requirement rather than an omission: `container-image` already forbids either of them to expose a port, and each carries an in-package source test asserting something stronger still, that its own non-test sources construct no HTTP server and import no HTTP framework. A raw `net.Listen` is the stated residual neither test catches.

The cost is that **neither process has a liveness signal of any kind**, and no log record fires on an idle stack for either one. The worker's recovery sweeper logs only when it finds something; the notifier's records are all per-message, and a healthy connected consumer is silent. So an idle-but-wedged worker is indistinguishable from an idle-and-healthy one — both are a running container emitting nothing — and the same holds for the notifier. What an operator has instead is indirect and lagging: jobs that stay `queued`, or a `video.jobs.terminal.events.v1` backlog that stops draining. Metrics are where that gap is closed if it is closed, and closing it is not a reason to give either process a port.

`docker-compose.yml` used to be listed here as Phase 8 work. It is not: the full local stack was built up change by change, is documented in `docs/development.md`, and `docs/roadmap.md` records it as delivered.
