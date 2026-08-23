## Why

Phase 5 migrates upload and result storage off the local filesystem and onto MinIO, but nothing in the codebase can talk to an object store yet. The three migration changes that follow this one (`migrate-result-storage-to-minio`, `migrate-upload-storage-to-minio`, `add-presigned-download-urls`) all need a connection to build against, so the connectivity plumbing lands first and alone — the same sequencing `add-videojob-infrastructure` used before any use case could persist a `VideoJob`, and `add-redis-infrastructure` used before any Phase 4 feature could reach Redis.

Landing plumbing separately also keeps the migration changes honest: each one is then a behavior change to a specific storage path, reviewable on its own, rather than a behavior change bundled with a new external dependency and its CI wiring.

## What Changes

- New `internal/video/infrastructure/storage/` package: `Config`/`LoadConfigFromEnv` (reads `VIDEO_MINIO_ENDPOINT`, `VIDEO_MINIO_ACCESS_KEY`, `VIDEO_MINIO_SECRET_KEY`, `VIDEO_MINIO_BUCKET`, and the optional `VIDEO_MINIO_USE_SSL`), `Open` (returns a configured client), `Ping` (a real round-trip health check), and `EnsureBucket` (idempotently creates the configured bucket if it does not exist). Connection plumbing only in this change — no `StoragePort`, no upload/download/presign helpers, no `StorageKey` handling. Those land in `migrate-result-storage-to-minio`, in this same package, exactly as `internal/video/infrastructure/postgres` holds `config.go`/`db.go` alongside `repository.go`.
- The package lives in the Video Processing context, **not** in `internal/platform/`. Every planned MinIO consumer (uploads, results, presigned URLs) belongs to Video Processing, and the repository's own precedent for context-owned infrastructure is unambiguous: `internal/identity/infrastructure/postgres` and `internal/video/infrastructure/postgres` are two separate packages with duplicated `Config`/`Open` plumbing pointing at the same physical database. Placement here follows consumer ownership, not how infrastructural the technology feels. See `design.md` decision 1.
- **No `Close`**, deliberately, unlike `internal/platform/redis`. Verified against `minio-go` v7.3.0: `*minio.Client` exposes no teardown method and keeps its HTTP transport private, so a `Close` wrapper could only return `nil` while releasing nothing — a promise of resource cleanup the package cannot keep. Callers therefore have no teardown obligation for a MinIO client, which `docs/operations.md` states explicitly at finalization.
- `Open` returns an `error` alongside the client, unlike `internal/platform/redis.Open`: the underlying constructor parses the endpoint and builds the transport, either of which can fail, so construction is genuinely fallible here where Redis's is not. It does **not** validate credentials — `credentials.NewStaticV4` constructs a static provider without checking key shape, so bad credentials surface only on a later server operation. The lazy-connection posture is otherwise identical to Redis's and Postgres's: `Open` does not verify reachability, and callers use `Ping` for that.
- New dependency: `github.com/minio/minio-go/v7`.
- `docker-compose.yml` gains a `minio` service (mirroring the existing `postgres`/`redis` services' shape: pinned image, healthcheck, loopback-only published ports, development-only credentials). `.github/workflows/ci.yml`'s `test` job starts the **same pinned image** with a `docker run` step rather than a `services:` entry — the image requires `server /data` arguments that a GitHub Actions service container has no field to supply (see `design.md` decision 9, verified against the image). Both are consumed only by this package's own tests via a `VIDEO_MINIO_TEST_*` env var set, mirroring how the `redis` service is consumed today.
- No `cmd/api` composition-root wiring. The new environment variables are **not** required at startup in this change — nothing outside this package's tests opens a MinIO connection until the first migration change does, so a developer without MinIO running can still build, run, and use the application exactly as today.
- No change to `uploads/`, `temp/`, `outputs/`, `StorageKey`, or any HTTP route. `createDirs` is untouched.

## Capabilities

### New Capabilities
- `minio-infrastructure`: the Video Processing context's MinIO connection adapter (`internal/video/infrastructure/storage`) — configuration loading, client construction, connectivity health check, and bucket provisioning. No feature-specific storage behavior, and no teardown (see above).

### Modified Capabilities

None. Placing this package inside the owning bounded context is what the existing architecture already prescribes, so no canonical spec needs amending. In particular, `openspec/specs/ddd-architecture/spec.md`'s "Monorepo Package Topology Is the Target Structure" requirement is untouched: its `internal/platform/` clause covers infrastructure "that no single bounded context owns," which is not this — and an earlier draft of this proposal that widened that clause has been withdrawn (see `design.md` decision 1 for why the justification did not survive contact with the PostgreSQL precedent).

The storage-related requirements this phase will eventually touch (`videojob-execution`'s `FrameExtractor` contract, `videojob-http-api`'s download route, `video-processing-access`'s ownership rules) are all deliberately out of scope here, since this change adds no consumer of the new package.

## Impact

- New files only, under `internal/video/infrastructure/storage/`, plus `docker-compose.yml`/`.github/workflows/ci.yml` service and environment-variable additions. No existing Go source is modified.
- New third-party library dependency: `github.com/minio/minio-go/v7`, in `go.mod`/`go.sum`. Not yet an application runtime dependency — `cmd/api` never opens a MinIO connection in this change, so nothing in the running application depends on MinIO being reachable. A MinIO instance (local dev via `docker-compose.yml`, CI via a `docker run` step) is only a dependency of this package's own test suite until `migrate-result-storage-to-minio` wires the client in.
- `internal/video/dependency_rules_test.go` is unaffected: it forbids `domain` and `application` from importing `infrastructure`, and says nothing about what an `infrastructure` package may import. The new package is on the permitted side of that rule and nothing in `domain`/`application` references it.
- No behavior change to any existing endpoint, and no new required startup configuration.
- `docs/operations.md` gains the new environment variables documented as **not yet required**, so a reader can stand the service up without MinIO until the first migration change ships.
