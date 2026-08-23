## Context

Phase 5 moves both durable artifact paths — the uploaded video and the extracted-frames zip — off the local filesystem and into MinIO, then hands downloads out as presigned URLs. None of that is buildable today: no package in the repository can open an object-store connection.

The established precedent for this shape of slice is `add-videojob-infrastructure` (Phase 3: PostgreSQL adapter and schema, zero `cmd/api` wiring, consumed by a later change) and `add-redis-infrastructure` (Phase 4: connection plumbing consumed only by its own tests until the first feature change). This change is the Phase 5 equivalent, and deliberately copies that shape rather than inventing a new one.

What makes MinIO different from Redis, and what several decisions below turn on: **MinIO becomes authoritative storage.** Redis backs idempotency keys, rate-limit counters, and a status cache — all explicitly non-authoritative, all safe to lose. From `migrate-result-storage-to-minio` onward, the bucket holds the *only* durable copy of a completed job's zip. Design choices that were correct for Redis (no volume, fail-open consumers) are wrong here, and are called out explicitly so the next change doesn't inherit them by pattern-matching.

## Goals / Non-Goals

**Goals:**
- A minimal, connectable MinIO client adapter configured from the environment, with an on-demand health check and idempotent bucket provisioning.
- `docker-compose.yml` and CI gain a real MinIO instance so the adapter's own tests run against the genuine service, not a mock — matching how the PostgreSQL and Redis adapters are tested.
- Place the package where the repository's existing conventions already put context-owned infrastructure, so no canonical spec has to move to accommodate it.

**Non-Goals:**
- No `StoragePort` interface, no upload/download/presign helpers, no `StorageKey` translation. Those are use-case concerns and land in `migrate-result-storage-to-minio` — in this same package, alongside this change's plumbing.
- No `cmd/api` wiring, and no new **required** startup configuration. A developer with no MinIO running must be able to build, run, and use the application exactly as before this change.
- No production credential/TLS/bucket-policy design. Local dev and CI use fixed, non-secret, loopback-only credentials, matching the existing PostgreSQL and Redis services' posture. Production configuration is `docs/operations.md`'s concern when a change actually deploys this.
- No retention, lifecycle, or versioning policy on the bucket. Nothing writes objects yet; policy without a write path is speculative.
- No change to `internal/platform/`'s charter. An earlier draft of this change proposed widening it; decision 1 records why that was withdrawn.

## Decisions

**1. Package location: `internal/video/infrastructure/storage`, not `internal/platform/minio`.**

Object storage feels like textbook platform infrastructure, and an earlier draft of this change placed it there, justified by "`cmd/api` and Phase 6's `cmd/worker` each need their own client" — with a `ddd-architecture` delta widening `internal/platform/`'s charter from "shared across contexts" to "shared across entrypoints."

That justification does not survive the repository's own PostgreSQL precedent. `internal/identity/infrastructure/postgres` and `internal/video/infrastructure/postgres` are **two separate packages with duplicated `Config`/`LoadConfigFromEnv`/`Open`/`migrate` plumbing**, differing only in environment-variable name and error prefix — and `docker-compose.yml` points both at the *same physical database*, with a comment stating that identity is intentional. The most platform-like resource in the system was deliberately not put in `internal/platform/`. And on Phase 6 specifically: both entrypoints will import `internal/video/infrastructure/postgres` without difficulty, so "shared across entrypoints" is already satisfied by a context-owned package. The proposed criterion was contradicted by code that already exists.

What `internal/platform/` actually contains confirms the rule from the other side: `redis` (consumed by Video's idempotency store and status cache **and** by transport-level rate-limiting middleware that belongs to no context) and `ratelimit` (pure transport-level). Both genuinely satisfy the spec's stated criterion — infrastructure no single bounded context owns. MinIO does not: uploads, results, and presigned URLs are all Video Processing.

So placement follows consumer ownership, not the technology's perceived altitude. The package sits beside `postgres`, `idempotency`, and `cache` in `internal/video/infrastructure/`, and holds this change's plumbing (`config.go`, `client.go`) plus the next change's `StoragePort` adapter — structurally identical to `internal/video/infrastructure/postgres`'s `config.go`/`db.go`/`repository.go`.

Two consequences worth stating rather than discovering later. First, **no `ddd-architecture` delta**: this change modifies no canonical spec, and the "one change equals one coherent spec delta" rule is satisfied by a single new capability. Second, promotion to `internal/platform/` stays available if a genuine second consumer ever appears — Phase 6's RabbitMQ is the real candidate there (Video publishes, Notification subscribes), and that is the moment to make the call, with a concrete second context rather than a speculative one.

**2. Client library: `github.com/minio/minio-go/v7`.**

Pinned to **v7.3.0** at `go get` time, not floated to whatever v7 release is newest when implementation starts: decisions 4 and 7 below rest on behavior verified against exactly that version (`minio.New`'s failure modes, `credentials.NewStaticV4`'s absence of validation, the absence of any `Close` method). A later release could invalidate either without anyone noticing, so an upgrade should re-check those two decisions rather than assume they carry.

It is MinIO's own client, is the de facto Go S3 client outside the AWS SDK, and is context-aware throughout (matching this codebase's consistent `context.Context` threading through `pgx`, `go-redis`, and `exec.CommandContext`). The considered alternative, `aws-sdk-go-v2/service/s3`, is heavier, drags in the AWS credential/region machinery for a service that needs none of it, and would make the code read as if it targeted S3 rather than the MinIO the roadmap actually names. Presigned URLs — Phase 5's fourth change — are first-class in `minio-go` (`PresignedGetObject`), so nothing later forces a library switch.

**3. Configuration: five discrete, `VIDEO_`-prefixed environment variables, not a DSN.**

`VIDEO_MINIO_ENDPOINT` (`host:port`), `VIDEO_MINIO_ACCESS_KEY`, `VIDEO_MINIO_SECRET_KEY`, `VIDEO_MINIO_BUCKET`, and the optional `VIDEO_MINIO_USE_SSL` (default `false`).

The context prefix follows directly from decision 1 and from the precedent it rests on: context-owned adapters use context-prefixed variables (`IDENTITY_POSTGRES_DSN`, `VIDEO_POSTGRES_DSN`) with context-prefixed error text (`video: ... is required`), while `internal/platform/` packages use bare ones (`REDIS_ADDR`, `RATE_LIMIT_*`). A bare `MINIO_*` set would have quietly implied the platform placement this change rejects, and would leave no room for a second context to configure its own bucket the way `IDENTITY_`/`VIDEO_` do for PostgreSQL today.

Discrete variables rather than a packed DSN because `minio.New` takes exactly this shape natively — endpoint plus a credentials provider plus a `Secure` bool — so a DSN would only be parsed straight back apart, the same reasoning that gave Redis a bare `REDIS_ADDR` instead of a connection string. The first four are required and `LoadConfigFromEnv` fails with a distinguishable error naming the missing one; `VIDEO_MINIO_USE_SSL` is optional because local dev and CI are plaintext loopback and every other value is a deployment concern.

"Optional" here means **unset falls back; malformed fails**, not "anything that isn't `true` means false." `VIDEO_MINIO_USE_SSL` is parsed with `strconv.ParseBool`, and a non-empty value it cannot parse returns an error rather than `false`. The failure mode this closes is specific and silent: with a naive `raw == "true"` check, a deployment typo (`ture`, `yes`, a trailing space) would leave `UseSSL` false and the service would happily speak plaintext to an endpoint the operator configured believing it was TLS-protected — no error, no log, nothing to notice. `internal/platform/ratelimit`'s `positiveIntFromEnv` already draws exactly this distinction for its own optional variables (empty → default, unparseable → error), so this is the codebase's existing posture rather than a new rule.

Critically, **none of these are required at `cmd/api` startup in this change.** `LoadConfigFromEnv` has exactly one caller — this package's own tests — until `migrate-result-storage-to-minio` wires the composition root. This mirrors `add-redis-infrastructure`, where `REDIS_ADDR` only became a startup requirement when `add-upload-idempotency-keys` actually opened a client.

**4. `Open` returns `(*minio.Client, error)`, diverging from `redis.Open`'s error-free signature.**

`redis.Open` returns no error because `redis.NewClient(&redis.Options{Addr: ...})` genuinely cannot fail — the address is an unparsed string. `minio.New` is different: it parses the endpoint, rejects a malformed one, and constructs the HTTP transport, so it returns a real error that a caller must handle. Copying Redis's signature would mean swallowing a genuine failure to preserve a cosmetic symmetry. `postgres.Open`, the sibling in this same package's directory, already has the fallible shape.

What `Open` explicitly does **not** do is validate credentials. `credentials.NewStaticV4(id, secret, token)` builds a static provider without checking key shape — empty keys are accepted and behave as anonymous credentials rather than as an error — so a wrong or missing access key surfaces on the first server operation, not at construction. The error return is justified by endpoint parsing and transport setup alone, and neither this design nor the delta spec claims otherwise. The lazy-connection posture is otherwise identical to both `redis.Open` and `postgres.Open`: `Open` performs no network round trip, and unreachability surfaces only on `Ping` or a later operation.

**5. `Ping` issues a real round trip via `BucketExists`, not `minio-go`'s `IsOnline`.**

`minio-go` exposes `IsOnline()`, but it reports the state of a *background* health-check goroutine that must be started separately and reflects a cached observation, not the caller's context or the caller's moment. `Ping(ctx, client, bucket)` calls `BucketExists` instead: a genuine, context-aware round trip whose failure is indistinguishable from any other connectivity failure, matching what `redis.Ping` and the Postgres adapter's health check already mean by "ping." A caller passing a canceled or deadline-bounded context gets that honored.

**6. `EnsureBucket` belongs in this change; it is lifecycle plumbing, not use-case logic.**

Redis needed no analogue — keys spring into existence on write. An object store does not: every future consumer needs the bucket to already exist, and none of them should each carry its own "create if missing" branch. The closest sibling precedent is `internal/video/infrastructure/postgres`'s `migrate.go`, which provisions schema in the same package as the connection. `EnsureBucket(ctx, client, bucket)` is idempotent — `BucketExists`, then `MakeBucket` only if absent — and treats `BucketAlreadyOwnedByYou` as success rather than a failure, since two replicas starting simultaneously is the normal case this system is being prepared for. It sets no policy, versioning, or lifecycle rule; those need a write path to be meaningful.

**Corrected during implementation review (PR #174):** this decision originally named `BucketAlreadyExists` as benign alongside `BucketAlreadyOwnedByYou`. That was wrong, and wrong in the direction this phase's fail-closed posture exists to prevent. The two codes mean opposite things: `BucketAlreadyOwnedByYou` is the same-account race this branch exists for, while `BucketAlreadyExists` means the name is taken by a *different* account in the globally shared bucket namespace — the configured bucket is unusable, and swallowing it would report successful provisioning for a bucket whose every write is later denied. Verified against `minio/minio:RELEASE.2025-04-22T22-12-26Z` that a duplicate `MakeBucket` with the same credentials returns `BucketAlreadyOwnedByYou` (409), so restricting the benign branch to that one code loses nothing the concurrency guarantee needs.

**7. No `Close` — the asymmetry with `internal/platform/redis` is deliberate.**

`redis.Close` and `postgres`'s `*sql.DB` teardown both wrap a real resource release. `minio-go` has no equivalent: verified against v7.3.0, `*minio.Client` exposes no `Close` (or any other teardown) method, and its underlying HTTP client and transport are unexported, so a caller cannot reach them either. A `Close(client) error` wrapper in this package could therefore only return `nil` while releasing nothing — an API that documents a resource-management contract it does not honor, and that a later reader would reasonably trust. Better to have no such function and say why: connections are pooled and reclaimed by the shared transport's own idle handling, so callers of this package have no teardown obligation. `docs/operations.md` records that at finalization, so the absence reads as a decision rather than an oversight.

**8. Fail-closed, not fail-open — stated here so the next change cannot inherit the wrong default.**

The three most recent Redis-backed changes (`add-upload-idempotency-keys` as corrected by `fail-open-upload-idempotency`, `add-rate-limiting-middleware`, `add-videojob-status-cache`) all fail open, because each degrades an optimization. MinIO is the critical path: with no bucket reachable there is no artifact, and a job that reports `completed` while its object was never stored is a correctness failure, not a slow path. Nothing in this change can fail either way — it has no consumer — but the posture is recorded now, because the pattern in this repository points the other way and the next change will be read against it.

**9. CI starts MinIO with a `docker run` step, not a service container — verified, not assumed.**

GitHub Actions service containers cannot supply command arguments, and the MinIO image needs them. Confirmed directly against `minio/minio:RELEASE.2025-04-22T22-12-26Z` — the exact immutable tag this change pins in both `docker-compose.yml` and CI, so the verification is reproducible rather than a claim about "the MinIO image" in general: `ENTRYPOINT=[/usr/bin/docker-entrypoint.sh]`, `CMD=[minio]`, and running it with no arguments prints the usage banner and exits instead of serving. `server /data` therefore has to come from somewhere a service container has no field for. So `.github/workflows/ci.yml` gains a step that runs that same pinned tag via `docker run -d ... server /data`, followed by a bounded readiness wait, rather than a `services:` entry — preserving the "same image locally and in CI" property the `postgres` and `redis` services were pinned for, which is the property that actually matters.

The `docker run` invocation must also carry what a `services:` block would otherwise have provided: `-p 127.0.0.1:9000:9000` so the endpoint the tests use is actually reachable on the runner, and `MINIO_ROOT_USER`/`MINIO_ROOT_PASSWORD` set to the same values as `VIDEO_MINIO_TEST_ACCESS_KEY`/`VIDEO_MINIO_TEST_SECRET_KEY`, or the server rejects the tests' credentials. (Those two are the *server's* own variables, read by the MinIO process, so they keep their upstream names — they are not this package's configuration.) Both `mc` and `curl` are present in the image (also verified against that tag), which is what lets `docker-compose.yml`'s healthcheck — which runs *inside* the container — probe `/minio/health/live` without installing anything. CI's readiness wait is a different host: it runs on the GitHub runner, against the published port, and relies on `curl` being present on `ubuntu-latest` rather than in the image. (Corrected at finalization: this paragraph originally justified both probes with the same in-image fact.)

**10. `docker-compose.yml` gets a named `minio_data` volume — unlike `redis`, deliberately.**

The `redis` service carries a comment explaining why it has no volume: everything in it is non-authoritative and losing it on restart is a correctness non-event. That reasoning does not transfer. Once `migrate-result-storage-to-minio` lands, the bucket holds the only durable copy of every completed job's zip, and wiping it on `docker compose down` would leave `completed` `VideoJob` rows in PostgreSQL pointing at objects that no longer exist — the same class of dangling state `postgres_data` exists to prevent. The volume is added now, with the service, so the migration change never has to introduce persistence retroactively to data that was already being lost.

**11. Test configuration: `VIDEO_MINIO_TEST_*` variables and a separate test bucket.**

This package's tests read `VIDEO_MINIO_TEST_ENDPOINT`, `VIDEO_MINIO_TEST_ACCESS_KEY`, `VIDEO_MINIO_TEST_SECRET_KEY`, and `VIDEO_MINIO_TEST_BUCKET`, following `VIDEO_POSTGRES_TEST_DSN` and `REDIS_TEST_ADDR`. The test bucket is distinct from `VIDEO_MINIO_BUCKET` for the same reason the `identity_test` database is distinct from `identity`: a test that creates and deletes buckets must not be able to touch what a locally running `app` is serving. Tests skip with a clear message when the variables are unset, so `go test ./...` still passes on a machine with no MinIO — matching how the existing Postgres- and Redis-backed tests behave.

## Risks / Trade-offs

- **[Risk]** Connection plumbing and the future `StoragePort` adapter share one package, so `internal/video/infrastructure/storage` will not be purely a connection package the way `internal/platform/redis` is. → **Mitigation:** that is exactly the shape of `internal/video/infrastructure/postgres` (`config.go`/`db.go`/`migrate.go` beside `repository.go`), which has been reviewed and shipped; a reader of one already knows how to read the other.
- **[Risk]** If Phase 7's Notification context ever needs its own MinIO access, this package becomes a cross-context import or has to be duplicated. → **Mitigation:** duplication is precisely what the PostgreSQL precedent already chose, and either way that is the moment a promotion to `internal/platform/` has a real second consumer to justify it. Deciding now, with no such consumer, is what this change declines to do.
- **[Risk]** The package is inert on merge — nothing outside its own tests calls it until the next change. → **Mitigation:** identical to `add-videojob-infrastructure` and `add-redis-infrastructure`, both of which shipped this way and were consumed within the same phase. Sequenced, not speculative.
- **[Risk]** A `docker run` step is less idiomatic than a `services:` block and sits outside the health-gating the runner gives service containers for free. → **Mitigation:** the necessity was verified against the image rather than assumed, the readiness wait is explicit and bounded (so a failure to start surfaces as a clear timeout, not a confusing test failure), and the workflow carries a comment recording why, so nobody "fixes" it back into a service container that cannot work.
- **[Risk]** The MinIO image is ~183MB against `redis:7-alpine`'s ~39MB, so CI pays a longer pull on every run. → **Mitigation:** accepted. It is a one-time pull per job on a service the phase structurally requires, and the alternative — mocking the object store in the adapter's own tests — would leave the one package whose entire job is talking to MinIO never actually talking to MinIO.
- **[Risk]** Fixed non-secret credentials in `docker-compose.yml`, CI, and test files may trip `gosec`'s hardcoded-credential rule (G101). → **Mitigation:** same posture already accepted for the Postgres credentials; if G101 fires it is handled the way this repo handles other justified findings — a narrowly scoped annotation with a comment, not a rule disable.
- **[Risk]** Deferring TLS/production credentials means a later deployment change revisits `Config`'s shape. → **Mitigation:** `VIDEO_MINIO_USE_SSL` is in the struct from day one precisely so the TLS switch is a value change rather than a signature change, and `Config` is small enough to extend without breaking `LoadConfigFromEnv`'s callers.

## Migration Plan

No data migration — this is new infrastructure with nothing depending on it. Rollout is: merge → `docker compose up` picks up the new `minio` service and volume automatically → CI's `test` job starts the container in its new step. Rollback is a plain revert: no schema, no persisted application data, no `cmd/api` behavior to unwind, and no environment variable that any running deployment has started depending on.

## Open Questions

None blocking. Production bucket naming, retention/lifecycle policy, credential rotation, and TLS termination are all deferred until a change actually deploys this — the same treatment PostgreSQL and Redis already have in `docs/operations.md`.
