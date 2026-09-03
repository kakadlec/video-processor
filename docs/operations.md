# Operations

## Current Deployment

The application is **two** Go processes built from one image: `cmd/api` (the HTTP surface) and `cmd/worker` (the frame-extraction consumer and recovery sweeper). Neither is a prerequisite for the other to start, and neither switches behaviour on a mode flag — the image's default command runs the API, and the worker is started by overriding it (`/app/worker`). There is no orchestration. External services are PostgreSQL for authoritative identity and video-job state; Redis for upload idempotency, per-user rate limiting, the non-authoritative status cache, and worker leases; MinIO for source videos and results; and RabbitMQ for dispatch. Every service needs environment-specific configuration — the per-process surface is below.

### Docker

```bash
# Build
docker build -t video-processor .

# Run the API (identity, video, Redis, MinIO, and RabbitMQ configuration are all
# required — see Environment Variables below; the container exits at startup if
# any is missing)
docker run -p 8080:8080 \
  -e IDENTITY_POSTGRES_DSN="postgres://user:pass@host:5432/identity?sslmode=disable" \
  -e IDENTITY_JWT_SIGNING_KEY="change-me" \
  -e VIDEO_POSTGRES_DSN="postgres://user:pass@host:5432/identity?sslmode=disable" \
  -e REDIS_ADDR="host:6379" \
  -e VIDEO_MINIO_ENDPOINT="host:9000" \
  -e VIDEO_MINIO_ACCESS_KEY="minio-access-key" \
  -e VIDEO_MINIO_SECRET_KEY="minio-secret-key" \
  -e VIDEO_MINIO_BUCKET="video-results" \
  -e RABBITMQ_URL="amqp://user:pass@host:5672/" \
  video-processor

# Run the worker — same image, different command, no port, and NO IDENTITY_*
# variables: it makes no access-control decision, so requiring identity
# configuration would misrepresent what the process does
docker run \
  -e VIDEO_POSTGRES_DSN="postgres://user:pass@host:5432/identity?sslmode=disable" \
  -e REDIS_ADDR="host:6379" \
  -e VIDEO_MINIO_ENDPOINT="host:9000" \
  -e VIDEO_MINIO_ACCESS_KEY="minio-access-key" \
  -e VIDEO_MINIO_SECRET_KEY="minio-secret-key" \
  -e VIDEO_MINIO_BUCKET="video-results" \
  -e RABBITMQ_URL="amqp://user:pass@host:5672/" \
  video-processor /app/worker
```

**Uploads are not processed unless at least one worker is running.** With the API alone, `POST /upload` still answers `202` and the job sits in `queued` forever — the submission succeeds because it was accepted, not because it was done. Run at least one worker in every environment where uploads are expected to complete. Scale by adding worker processes, not by raising a concurrency setting: prefetch is one by design, so a worker holds exactly one job at a time.

The Dockerfile is a multi-stage build. The default (final) stage — used by the command above — compiles a static binary in a `golang:1.27-alpine` builder stage (dependencies resolved read-only from the committed `go.sum`), then ships **both** binaries (`/app/app` and `/app/worker`) and `ffmpeg` in a minimal `alpine` runtime stage with no Go toolchain or source tree, running as a fixed non-root user (UID 1000). `ffmpeg` is there for the worker now rather than for the API, and stays for that reason. See [docs/development.md](development.md) for the additional `test` stage used to run the suite via Docker.

> **Fenced-worker rollout and rollback:** drain every previous-build worker before starting any build that runs the recovery sweeper. An old worker sets no lease and does not honor `lease_epoch`, so allowing it to overlap a new sweeper can let its unconditional terminal write overwrite a successor. The reverse also applies: stop every new-build worker (and therefore every sweeper) before rolling workers back. `cmd/worker` waits up to five minutes on SIGTERM; configure at least that much termination grace and verify the departing generation has exited before scaling the other one up. The additive `lease_epoch` column may remain during rollback.

### Environment Variables

The two processes have deliberately different configuration surfaces:

| | `cmd/api` | `cmd/worker` |
|---|---|---|
| `IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_SIGNING_KEY` | **required** | **not read** — the worker makes no access-control decision |
| `VIDEO_POSTGRES_DSN` | required | required |
| `REDIS_ADDR` | required | required (status cache, worker leases, and clearing failed-job idempotency keys) |
| `VIDEO_MINIO_ENDPOINT` / `_ACCESS_KEY` / `_SECRET_KEY` / `_BUCKET` | required | required |
| `VIDEO_MINIO_PUBLIC_ENDPOINT`, `VIDEO_MINIO_PUBLIC_USE_SSL` | optional | optional, and **read even though unused** — see below |
| `RABBITMQ_URL` | required | required |
| `RATE_LIMIT_*` | optional | not read |
| `PORT` / `GIN_MODE` | as below | not read — the worker serves no HTTP and exposes no port |

The one row that needs explaining is `VIDEO_MINIO_PUBLIC_*`. The worker never mints a presigned URL — issuing download grants belongs to the API — but `setupWorker` goes through the same MinIO loader and builds the presign client anyway, so `ResultStorage` is fully constructed rather than holding a nil that would panic the day something calls the other half of its interface. So the variables *are* read on the worker, and a malformed value fails worker startup even though nothing signs with it. Leaving them unset is the normal case (each falls back to its internal counterpart), which is why `docker-compose.yml` sets them only on `app`.

| Variable | Default | Description |
|---|---|---|
| `PORT` | `8080` | Listening port (hardcoded in `cmd/api/main.go` as `:8080`; no env var read currently — listed here for future use) |
| `GIN_MODE` | `debug` | Set to `release` to suppress Gin debug output |
| `IDENTITY_POSTGRES_DSN` | unset | PostgreSQL connection string for the Identity module (e.g. `postgres://user:pass@host:5432/identity?sslmode=disable`). Required at startup. |
| `IDENTITY_JWT_SIGNING_KEY` | unset | Symmetric key used to sign/verify access tokens (HMAC-SHA256). Required at startup. There is no default signing key — startup fails clearly rather than falling back to one. |
| `VIDEO_POSTGRES_DSN` | unset | PostgreSQL connection string for the Video Processing module's `VideoJob` repository (e.g. `postgres://user:pass@host:5432/identity?sslmode=disable` — same instance/database as `IDENTITY_POSTGRES_DSN` by design, not a separate one). Required at startup as of Phase 3's `wire-videojob-http-endpoints`, which wires `cmd/api/video.go`'s `setupVideo` into `main()`. |
| `REDIS_ADDR` | unset | Address (`host:port`) of the Redis instance backing upload idempotency, rate limiting, status caching, and worker leases (e.g. `redis:6379`). Required by both processes. The client is constructed at startup but establishes network connections lazily; reachability failures degrade individual Redis-backed behaviors rather than failing startup. |
| `RATE_LIMIT_MAX_REQUESTS` | `60` | Maximum requests per authenticated user within one rate-limit window before `429` responses start. Optional as of Phase 4's `add-rate-limiting-middleware` — unlike `REDIS_ADDR`, absence is not a startup failure, it just uses the default. |
| `RATE_LIMIT_WINDOW_SECONDS` | `60` | Length (seconds) of the fixed rate-limit window `RATE_LIMIT_MAX_REQUESTS` applies to. Optional, same as above. |
| `VIDEO_MINIO_ENDPOINT` | unset | Address (`host:port`) of the MinIO instance for the Video Processing context (e.g. `minio:9000`). Required at startup as of Phase 5's `migrate-result-storage-to-minio`. |
| `VIDEO_MINIO_ACCESS_KEY` | unset | MinIO access key. Required at startup. |
| `VIDEO_MINIO_SECRET_KEY` | unset | MinIO secret key. Required at startup. |
| `VIDEO_MINIO_BUCKET` | unset | Bucket holding processed ZIP results, keyed `frames_<jobID>.zip`. Required at startup; created automatically if absent. |
| `VIDEO_MINIO_USE_SSL` | `false` | Whether to connect over TLS. Optional, but a value that is *set* and not parseable as a boolean is a configuration error, never a silent `false` — a typo must not quietly downgrade an intended TLS connection to plaintext. |
| `VIDEO_MINIO_PUBLIC_ENDPOINT` | `VIDEO_MINIO_ENDPOINT` | Address (`host:port`) clients reach MinIO at, used **only** to construct presigned download URLs. Optional as of Phase 5's `add-presigned-download-urls`; defaults to the internal endpoint, so a deployment where one address serves both the server and its clients needs nothing here. The server never dials this address. |
| `RABBITMQ_URL` | unset | Full AMQP URI for the shared broker (e.g. `amqp://video:video@rabbitmq:5672/`) — a URI, not a `host:port` pair like `REDIS_ADDR`, because it carries the TLS scheme, the credentials, and the virtual host. **Required at startup by both processes** (`cmd/api` as of Phase 6's `add-videojob-source-key-and-outbox-relay`, `cmd/worker` as of `migrate-upload-to-async-processing`): an unset value stops the process with a clear error. A *reachable* broker is not required by either — the relay and the consumer each own their connection, dial it in their own goroutine, and retry with backoff, so the API serves every route and the worker stays up with the broker down. |
| `VIDEO_MINIO_PUBLIC_USE_SSL` | `VIDEO_MINIO_USE_SSL` | Scheme for the URLs issued against `VIDEO_MINIO_PUBLIC_ENDPOINT`. Optional; defaults to the *resolved* `VIDEO_MINIO_USE_SSL`, not to `false` — declaring TLS once must not silently produce `http://` links. Set it explicitly when TLS terminates in front of the public address but the server talks plaintext internally. A set-but-unparseable value is a configuration error, same as above. |

The first four `VIDEO_MINIO_*` variables are **required**; `VIDEO_MINIO_USE_SSL`, `VIDEO_MINIO_PUBLIC_ENDPOINT`, and `VIDEO_MINIO_PUBLIC_USE_SSL` are optional. `setupVideo` loads the configuration, opens the client, pings it, ensures the bucket exists, and discovers the bucket's region for the presign-only client, and **any of those steps failing stops startup**. This is deliberately fail-closed, unlike the Redis-backed features above, which degrade to a slower but correct system when Redis is down: a result that cannot be stored cannot be delivered, so there is nothing to degrade to.

#### The public endpoint, and the failure mode it exists to prevent

Since `add-presigned-download-urls`, `GET /download/:filename` returns a signed URL instead of the ZIP, and the client fetches the object from MinIO itself. Two operational consequences follow.

**MinIO must now be reachable from clients, not only from the API.** Before this change only `cmd/api` needed a route to the bucket; a browser or script that can reach the API but not the storage service now cannot download a result at all. That is a deployment-topology change, not a tuning knob: plan for the storage endpoint (or a proxy/CDN in front of it) to be publicly resolvable and reachable by whoever downloads.

**A wrong or unset public endpoint produces URLs that are correctly signed and unreachable, and the API cannot detect it.** SigV4 covers the `Host` header, so the host is fixed when the URL is signed and cannot be corrected afterwards — the presigning client must be built against the browser-facing address from the start. The API never follows a URL it issues, so from its side everything looks healthy:

| Where it shows up | What you see |
|---|---|
| The API's logs | Nothing. `GET /download/:filename` returns `200` with a well-formed URL. |
| The API's metrics/health | Nothing. No error, no elevated latency, no failed request. |
| The browser | A connection or DNS error on the storage host — often `ERR_NAME_NOT_RESOLVED` or a connection timeout — after a `200` from the API. |
| `curl` against the issued URL | The same failure to connect, before any HTTP status is returned. |

If downloads fail in the browser while the API reports success, read the `url` field of a `GET /download/:filename` response and check whether that host resolves and is reachable *from the client*. The commonest cause is an internal service name leaking into the public endpoint — `docker-compose.yml` sets `VIDEO_MINIO_ENDPOINT: minio:9000` for the server and `VIDEO_MINIO_PUBLIC_ENDPOINT: 127.0.0.1:9000` for the browser precisely because those two audiences reach the same instance by different addresses.

An issued URL also **cannot be revoked**: deleting the job, changing its owner, or changing its status leaves an outstanding URL working until its five-minute lifetime elapses. The lifetime is a compile-time constant with no environment variable, deliberately — issuance happens when a user clicks, so the interval that has to be survived is the gap before the browser navigates.

> **Deployment ordering:** MinIO must be reachable and these variables set *before* the new image starts, or the container exits immediately. Rolling back to a pre-Phase-5 image reverts to the local `outputs/` directory and ignores these variables entirely — results produced while the newer image was live then become unreachable, since they are in the bucket and the older code only looks at local disk.

Their `VIDEO_` prefix marks them as the Video Processing context's own configuration, matching `VIDEO_POSTGRES_DSN` and distinguishing them from `internal/platform/`'s unprefixed `REDIS_ADDR`.

`IDENTITY_POSTGRES_DSN`, `IDENTITY_JWT_SIGNING_KEY`, `VIDEO_POSTGRES_DSN`, `REDIS_ADDR`, and the four `VIDEO_MINIO_*` variables are all required to be *set*: the process exits at startup with a clear configuration error if any is empty, rather than running with unsafe defaults or an unauthenticated fallback (see [openspec/specs/identity-authentication/spec.md](../openspec/specs/identity-authentication/spec.md), [openspec/specs/videojob-http-api/spec.md](../openspec/specs/videojob-http-api/spec.md), and [openspec/specs/upload-idempotency/spec.md](../openspec/specs/upload-idempotency/spec.md)). Startup validation depth differs by dependency, though: both PostgreSQL DSNs are also *connectivity*-checked at startup (`db.PingContext`), so an unreachable or malformed database fails fast. `REDIS_ADDR` is not — `platformredis.Open` only constructs the client, and a malformed address or unreachable Redis surfaces later, at the first `POST /upload` request that needs it, not at startup. MinIO sits at the strict end: it is connectivity-checked *and* its bucket is provisioned at startup, so a wrong endpoint or bad credentials stop the process rather than surfacing on the first upload. `RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS` and `VIDEO_MINIO_USE_SSL`, unlike the required variables above, are optional — startup only fails if either is *set* to something malformed (non-integer or non-positive), never for being unset (see `openspec/specs/rate-limiting/spec.md`). `RABBITMQ_URL` is required to be set by both processes but is not connectivity-checked at startup by either (see the RabbitMQ section below).

`cmd/worker` applies the same rules to the subset it reads: `RABBITMQ_URL` is loaded first, before any I/O, then `VIDEO_POSTGRES_DSN` (opened, pinged, migrated), `REDIS_ADDR`, and the four `VIDEO_MINIO_*` variables (opened, pinged, bucket ensured). It also creates `temp/` at startup and **exits if it cannot** — every delivery downloads its source there, so a worker without it would claim jobs and fail each one for a reason unrelated to the job, deleting the source on the way out. Being unavailable is the honest outcome.

## Runtime Directory Structure

**`cmd/worker`** creates and uses **one** directory relative to its working directory. `cmd/api` creates none — it stopped touching the filesystem when extraction moved to the worker:

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
| Bucket, `uploads/` prefix | `POST /upload` per request | `uploads/<uploadID>_<filename>` source videos | The request before enqueue; the worker after an applied terminal result; or the sweeper after an applied abandonment failure. **Not guaranteed**, see below |
| Bucket, flat keys | `ProcessVideoJob` on success | `frames_<jobID>.zip` results | Never (manual cleanup required) |

**Source cleanup is best effort, and since the async cutover it is also not exhaustive.** A source object is owned by the `POST /upload` request until its job commits as `queued`; afterwards only a worker that applied a terminal outcome, or the sweeper that applied terminal abandonment, may delete it. Fenced and already-present outcomes delete nothing. Each cleanup uses one `RemoveObject` call with no retry, so a MinIO hiccup can leave residue logged by key.

**A job that is enqueued but never dispatched can leak its source permanently.** The request has given up ownership and no worker ever took it; this occurs when the relay never publishes or a message is dead-lettered before any claim. A worker crash after claim is now recovered by the sweeper, but a crash after a terminal commit and before best-effort cleanup can still leave the source behind.

The bucket **expiration lifecycle rule scoped to the `uploads/` prefix** is therefore no longer a recommended backstop — it is the **only** guarantee those objects are ever reclaimed. Configure it: expire objects under `uploads/` after one day (comfortably longer than any extraction). It must be scoped to that prefix, because result objects live in the same bucket under flat `frames_*.zip` keys and must never expire. No code path assumes the rule exists, which is exactly why its absence is silent.

Results accumulate indefinitely. There is no expiry, no lifecycle rule, no cleanup job, and no size limit for them — growth must be monitored manually in the current deployment. Moving artifacts off local disk removed the container-disk pressure, not the retention gap.

## CI / CD

Three required checks run on every push and pull request:

| Check | Tool | What it does |
|---|---|---|
| `Build & Test` | `go vet` + `go test ./... -v` | Compiles the application and runs integration tests (with `ffmpeg` installed on the runner) |
| `SAST (gosec)` | [`gosec`](https://github.com/securego/gosec) | Static security analysis; fails the build on any finding |
| `Vulnerability Scan (govulncheck)` | [`govulncheck`](https://go.dev/security/vuln) | Fails only when a known vulnerability is reachable from code actually called by this project |

Releases are automated via `release-please`. On every push to `main`, it maintains a "Release PR" showing the next version computed from Conventional Commits. Merging that PR creates the git tag, publishes a GitHub Release, and updates `CHANGELOG.md`.

### Post-deploy step: retire the superseded dispatch generation

The async cutover moved job dispatch to a new generation of the topology — `video.jobs.v2` / `video.jobs.queued.v2`, routing key and outbox `event_type` `video_job.queued.v2`. The previous generation's entities are **not** deleted by the application, and after every replica is running the new build they should be deleted by hand:

```bash
# from a shell that can reach the broker's management API / CLI
rabbitmqctl delete_queue video.jobs.queued.v1
rabbitmqctl delete_exchange video.jobs.v1
```

Nothing publishes to or consumes from them once the rollout completes, so this is housekeeping rather than a correctness step — an unretired generation is a bounded, idle queue, and the system is correct whether or not the deletion has happened.

**It cannot be automated, and the reason is the rollout itself.** A deletion executed at startup would race a not-yet-redeployed replica that is still publishing into the old generation, destroying dispatches for jobs that are legitimately `queued`. The safe moment is *after* every replica is on the new build, which is a fact about the deployment that no process can observe from inside. `video.jobs.dlx` and `video.jobs.dead` carry no generation suffix and must **not** be deleted — both generations share that sink deliberately, so there is one place to look at dead-lettered messages.

Those queues do not drain on their own either: the job queue carries no message TTL by design (see the RabbitMQ section below), so a superseded generation's backlog persists until it is deleted.

---

## Implemented Infrastructure

### PostgreSQL — Implemented (Phase 2 for identity, Phase 3 for video), required

Authoritative state store for users (`User` aggregate) and `VideoJob`s, configured via `IDENTITY_POSTGRES_DSN` and `VIDEO_POSTGRES_DSN` respectively — by design the same PostgreSQL instance and database, not two separate ones. Schema/migrations for both are applied automatically at startup (`postgres.Migrate`). The video processing schema (`video_jobs` and the transactional-outbox `video_job_outbox` table) was added by Phase 3's `add-videojob-infrastructure`; `cmd/api/video.go`'s `setupVideo` (added by `wire-videojob-http-endpoints`) is what actually instantiates and migrates it at startup — `VIDEO_POSTGRES_DSN` is required exactly like `IDENTITY_POSTGRES_DSN`.

- **Local/CI service:** `docker-compose.yml` at the repo root starts a matching `postgres:16-alpine` instance (`docker compose up -d postgres`) for running identity-dependent tests locally; CI provisions the same image as a service container. See [docs/development.md](development.md).
- **Local/CI credentials** (`identity`/`identity`) are fixed, non-secret defaults — never used outside a developer's machine or CI.

### Redis — Idempotency, rate limiting, status cache, and worker leases implemented

`internal/platform/redis` provides connection plumbing — `Config`/`LoadConfigFromEnv`, `Open`, `Ping`, `Close`. Both processes require `REDIS_ADDR`. Redis remains additive to PostgreSQL, not a replacement. Four responsibilities are implemented:

1. **Idempotency keys** — **Implemented.** `internal/video/infrastructure/idempotency.RedisStore` deduplicates `POST /upload` requests by content hash + `UserID`: a `Reserve`/`Finalize`/`Clear`/`Lookup` protocol backs the "prevent duplicate job creation from client retries" goal. See [docs/architecture.md](architecture.md)'s Request pipeline section and `openspec/specs/upload-idempotency/spec.md`.
2. **Rate limiting** — **Implemented.** `internal/platform/ratelimit.Limiter` enforces a per-user, fixed-window request cap (`RATE_LIMIT_MAX_REQUESTS`/`RATE_LIMIT_WINDOW_SECONDS`, both optional with defaults) on every authenticated route, mounted via `cmd/api/ratelimit.go`'s `rateLimitMiddleware`. Denied requests get `429` + `Retry-After`; a limiter failure (or an internal bounded timeout) fails open. See [docs/architecture.md](architecture.md)'s Request pipeline section and `openspec/specs/rate-limiting/spec.md`.
3. **Status cache** — **Implemented.** `CachedVideoJobRepository` provides cache-aside polling reads and atomic epoch/status-ordered write-through. Ownership decisions bypass the cache; Redis errors fall back to PostgreSQL correctness. No separate environment variable; the TTL is fixed at five minutes.
4. **Worker leases** — **Implemented.** `internal/video/infrastructure/lease.RedisStore` stores `videojob:lease:<jobID> = <lease_epoch>` with a fixed 90-second TTL. A holder renews every 30 seconds and reacquires an absent equal-epoch lease; the recovery sweeper uses successful absence at the observed epoch as its liveness signal. Lease errors fail open for execution but fail closed for takeover.

During a Redis outage, rate limiting, idempotency, status caching, and lease maintenance fail open for request/execution availability, while the sweeper fails closed: it logs `lease store unreachable ... taking over none`, clears prior confirmations, and requeues nothing. PostgreSQL claims and fence predicates continue to prevent state corruption. Crashed jobs remain `processing` until Redis answers and two fresh successful absence observations occur.

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

- **Local/CI service:** `docker-compose.yml` starts a pinned `minio/minio` instance; CI starts the same image with a `docker run` step (a GitHub Actions service container cannot pass the `server /data` arguments the image requires). The adapter's own tests use `VIDEO_MINIO_TEST_*` against a separate bucket, since they create and delete buckets; `cmd/api`'s tests use the runtime variables.
- **Local/CI credentials** (`minioadmin`/`minioadmin`) are fixed, non-secret defaults — never used outside a developer's machine or CI.

---

### RabbitMQ — Topology declared, published to by the outbox relay, consumed by `cmd/worker` (Phase 6)

`internal/platform/rabbitmq` opens, health-checks, and closes an AMQP connection and declares a topology; `internal/video/infrastructure/messaging` defines the one this context uses. Both shipped with `add-rabbitmq-infrastructure`.

**Both processes open a connection.** `cmd/api` runs the outbox relay as a goroutine, publishing `video_job.queued.v2` events (`add-videojob-source-key-and-outbox-relay`); `cmd/worker` runs the consumer that reads them (`migrate-upload-to-async-processing`). Each declares the topology after every successful dial, so neither depends on the other having started first, and a broker recreated while one was disconnected gets its entities back. See "The outbox relay" and "The worker" below.

- **`RABBITMQ_URL`** holds a full AMQP URI (`amqp://user:pass@host:5672/vhost`), not a `host:port` pair like `REDIS_ADDR`: the URI carries the scheme that selects TLS, the credentials, and the virtual host. `cmd/api`'s `setupVideo` and `cmd/worker`'s `setupWorker` each load it through `LoadConfigFromEnv` as their **first** step, before any I/O, so a missing variable fails fast and clearly instead of after PostgreSQL and MinIO have already been opened.
- **`Open` connects**, unlike the Redis and MinIO adapters, which construct a client without touching the network. AMQP has no lazy client, so an unreachable broker or wrong credentials surface immediately rather than on first use.
- **The health check is a real round trip** — it opens a channel and closes it. The client's own `IsClosed()` predicate reports only what the process has already observed, which is stale for a broker that stopped answering without the connection being torn down.

The declared topology, and the two operational policies in it that are decisions rather than defaults:

| Entity | Name | Arguments |
|---|---|---|
| Job exchange | `video.jobs.v2` | `direct`, durable |
| Routing key | `video_job.queued.v2` | equal to the outbox `event_type` string |
| Job queue | `video.jobs.queued.v2` | `x-max-length` 10 000, `x-overflow` `reject-publish`, dead-letters to `video.jobs.dlx` |
| Dead-letter exchange | `video.jobs.dlx` | `fanout`, durable |
| Dead-letter queue | `video.jobs.dead` | `x-message-ttl` 24 h, `x-max-length` 10 000, `x-overflow` `drop-head`, forwards nowhere |

- **A full job queue refuses new publishes; it does not drop old ones.** `reject-publish` means the broker nacks the publisher rather than evicting the oldest queued job, so a full queue becomes back-pressure: the publisher leaves its outbox row unstamped, retries, and the system resumes when the queue drains. Nothing is lost. Expect a stalled relay and a growing count of unpublished outbox rows as the symptom, not missing jobs.
- **Job messages never expire**, and that takes two things, not one. The job queue deliberately carries no `x-message-ttl`; publishers must also leave the per-message `expiration` property unset, since RabbitMQ honours it independently of any queue setting. Either one would dead-letter a message without any update to its `video_jobs` row, and the state machine has no transition out of `queued` except to `processing` — so the job would report `queued` to its owner forever. A backlog therefore persists until it is consumed rather than aging out, which is the intended trade.
- **The generation suffix is on the exchange, the queue, *and* the routing key** — which is also the outbox `event_type` string. Versioning the exchange alone was the original plan and it does not work: every `cmd/api` replica's relay claims from the one shared `video_job_outbox` table, filtered on `event_type`, so with a single shared string a redeployed replica's relay would claim a not-yet-redeployed replica's row and publish it into the new generation. The exchange bump is kept alongside it because the two close different holes — the event type stops a relay *claiming* the wrong generation's row, the exchange stops the broker *delivering* to the wrong generation's queue.
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

#### The outbox relay

`cmd/api` starts one relay goroutine (`internal/video/infrastructure/messaging.Relay`) and stops it on `SIGINT`/`SIGTERM`. It exists because `POST /upload` must not depend on the broker: `Repository.Enqueue` commits the `pending → queued` update and a `video_job.queued.v2` outbox row in one transaction, and the relay carries that row to RabbitMQ afterwards.

Each cycle it opens a transaction, claims a bounded batch of unpublished rows with `SELECT … FOR UPDATE SKIP LOCKED` (so several `cmd/api` replicas can each run one without dispatching the same row twice), publishes them **mandatory** on a confirm-mode channel, stamps `published_at` only for the messages the broker both acknowledged and did not return, and commits. The poll interval (2 s), the batch size (100 rows), the confirmation timeout (15 s), and the dial backoff are compile-time constants — there is no environment variable to tune them, matching the status cache's fixed TTL.

Operationally, three things are worth knowing before they surprise you:

- **A full job queue stalls the relay, by design.** `reject-publish` nacks the publish instead of evicting a queued job, so the row stays unstamped and the next poll retries it. Nothing is lost, and uploads keep being *accepted* — but they stop being *processed*, since the dispatch never reaches the queue. A queue at its 10 000-message limit now means workers are not keeping up (or are not running); add workers rather than raising the limit.
- **Unpublished outbox rows are the symptom to look at**, not a missing-message count on the broker:

  ```sql
  SELECT event_type, count(*), min(occurred_at)
    FROM video_job_outbox
   WHERE published_at IS NULL
   GROUP BY event_type;
  ```

  A growing `video_job.queued.v2` count with an ageing `min(occurred_at)` means the relay is not publishing — a broker that is down, a full queue, or an unroutable exchange. A large and **steadily growing** `video_job.created` count is normal: one row is written per job created and none is ever marked published, so that number only ever goes up. Those rows are internal events, are never dispatched, and are excluded from the claim by the `event_type` filter and its partial index (`video_job_outbox_unpublished_idx`) — which is exactly why an unbounded backlog there is harmless rather than a leak to chase.
- **Delivery is at-least-once.** The relay commits only after the broker acknowledges, so a crash in between republishes rather than loses. A consumer must tolerate a duplicate regardless, since a nack or a consumer crash produces one too.

Its lifecycle transitions are logged — started, connection lost, reconnected, stopped — because a healthy relay is otherwise invisible. Repeated dial failures back off from 1 s to a 30 s ceiling, and the topology is redeclared after every successful dial, so a broker that was recreated while the relay was disconnected gets its exchange and queues back before the next publish.

#### The worker

`cmd/worker` consumes `video.jobs.queued.v2` with a **prefetch of one**: one unacknowledged delivery at a time, because the unit of work is a full `ffmpeg` run and buffering a second delivery would hide it from every other consumer for the duration. Scale out by running more worker processes; there is no concurrency setting to raise.

A delivery is acknowledged only after a terminal outcome is confirmed. Cleanup depends on whether this actor applied it; everything without a terminal outcome is rejected without requeue and reaches `video.jobs.dlx`:

| Situation | Disposition | Job left as | Source / lease cleanup |
|---|---|---|---|
| Body will not decode, names no source key, or names an unknown job | Reject → DLQ | untouched / n/a | untouched |
| Claim lost (duplicate or stale dispatch) | Reject → DLQ | untouched | kept; this run acquired no lease |
| Run broke before any terminal state committed | Reject → DLQ | usually `processing` | kept; lease left to expire so recovery can act |
| This run applied `failed` | **Ack** | `failed` | source deleted, idempotency key cleared conditionally, held lease released |
| An identical `failed` outcome was already present | **Ack** | `failed` | no cleanup; this actor did not apply the write |
| Result stored but completion still errors after 4 retries | Reject → DLQ | usually `processing` | source and lease kept; result key logged |
| Terminal write returns `ErrJobFenced` | Reject → DLQ | authoritative winner's state | source/idempotency untouched and no lease released; held epoch and result key logged. The current log says `taken over` for both a newer epoch and a same-epoch terminal winner |
| Completion succeeds, including a retry that finds its identical outcome already present | **Ack** | `completed` | source deleted and held lease released |

The AMQP consumer requeues only a delivery pulled off the channel after shutdown, before handling began. Crash recovery does not broker-requeue a `processing` delivery: the sweeper first commits a new `queued` row state and outbox event, and the ordinary relay publishes a fresh dispatch.

**Operator symptom: a job remains `processing`.** A claimed job holds Redis key `videojob:lease:<jobID>` with its PostgreSQL `lease_epoch` as the value and a 90-second TTL. The worker renews every 30 seconds. The sweeper runs every 60 seconds, scans at most 50 rows with a rotating keyset cursor, and acts only after two consecutive successful "not held at this epoch" observations. A Redis query error clears the first observation and takes over nothing.

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
- `lease store unreachable ... taking over none` means Redis recovery failed closed and all pending confirmations for affected jobs were reset;
- `requeued job ... at epoch N` means the row advanced and a new outbox dispatch committed;
- `failed after abandonment` means the row exhausted three requeues, or had no source key, and the sweep applied the terminal write.

Normal recovery latency includes the remaining lease TTL plus up to two sweep intervals. A backlog may add cycles because one cycle examines 50 rows. Restarting a worker also discards its in-memory first-observation marks, deliberately requiring two fresh observations.

Do **not** manually change `status` or `lease_epoch`, publish a dispatch, or delete a lease. The requeue and outbox insert must commit together, and the epoch increment is what fences a prior worker. If recovery does not happen, preserve the row and artifacts, inspect the logs above, verify `source_key` still exists, and check for `frames_<jobID>.zip` from a fenced run. Escalate rather than bypassing repository predicates.

Shutdown is `SIGINT`/`SIGTERM`: cancellation tells the consumer to stop taking deliveries and the recovery sweeper to stop concurrently. `run` joins the sweeper first, then waits up to **5 minutes** for the consumer's job in hand. The handler is detached from the shutdown signal, so a normal shutdown does not kill `ffmpeg` or abort its terminal write. If the deadline expires and the process is terminated, the delivery may be redelivered and lose its old claim; after the abandoned lease expires, the sweeper advances the row's epoch and emits a fresh dispatch. Give worker containers at least five minutes of stop grace to avoid duplicated extraction even though the fence protects state.

---

## Planned Infrastructure (Not Yet Implemented)

> The components below are planned for future phases and do not exist in the current deployment. Each is labeled with the phase that introduces it.

### Email / Webhook delivery — Planned (Phase 7)

Notification infrastructure for `VideoJobCompleted` and `VideoJobFailed` events. Owned by the Notification bounded context. Delivery methods and preferences are per-user. Webhook delivery includes retry logic and HMAC signature verification.

### Observability — Planned (Phase 8)

Structured logging (zerolog or slog), Prometheus metrics at `/metrics`, health endpoint at `/health`, readiness endpoint at `/ready`. Also in Phase 8: `docker-compose.yml` for the full local development stack (API, worker, PostgreSQL, Redis, RabbitMQ, MinIO).
