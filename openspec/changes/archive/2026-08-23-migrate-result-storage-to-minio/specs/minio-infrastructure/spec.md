## MODIFIED Requirements

### Requirement: The Adapter Is Tested Against A Real MinIO Instance

This capability's tests SHALL exercise `Open`, `Ping`, and `EnsureBucket` against a real MinIO instance rather than a mock, configured through `VIDEO_MINIO_TEST_ENDPOINT`, `VIDEO_MINIO_TEST_ACCESS_KEY`, `VIDEO_MINIO_TEST_SECRET_KEY`, and `VIDEO_MINIO_TEST_BUCKET`. The test bucket SHALL be distinct from the runtime `VIDEO_MINIO_BUCKET`, so a test that creates or deletes buckets cannot disturb a locally running application's artifacts. When those variables are unset **this package's** tests SHALL skip with a clear message rather than fail.

That skip is scoped to this package. It previously carried the further claim that `go test ./...` therefore still passes on a machine with no MinIO available, which is no longer true and is corrected here rather than left standing: `cmd/api`'s `TestMain` requires MinIO the same way it already requires `ffmpeg`, exiting non-zero with a clear message when the configuration is absent. That is deliberate — `POST /upload` stores its result in a bucket and `GET /download/:filename` and `GET /api/status` read it back from one, so a suite that skipped those silently would report green while covering none of its own core behavior.

Any test that creates a bucket SHALL remove it, and its objects, when it finishes — including when it fails. The local MinIO service keeps its data in a named volume, so a bucket left behind accumulates across every later run.

#### Scenario: Tests run against the configured instance

- **GIVEN** the `VIDEO_MINIO_TEST_*` variables point at a running MinIO instance
- **WHEN** the package's tests run
- **THEN** they exercise `Open`, `Ping`, and `EnsureBucket` against that instance and pass

#### Scenario: This package's tests skip when no test instance is configured

- **GIVEN** the `VIDEO_MINIO_TEST_*` variables are unset
- **WHEN** this package's tests run
- **THEN** they skip with a message naming the missing configuration

#### Scenario: The application's own test suite requires MinIO rather than skipping

- **GIVEN** the runtime `VIDEO_MINIO_*` variables are unset
- **WHEN** `cmd/api`'s test suite starts
- **THEN** it exits non-zero with a message naming what is missing and pointing at the Docker fallback, rather than skipping the tests that exercise result storage

#### Scenario: A test that creates a bucket leaves nothing behind

- **WHEN** a test provisions a bucket, whether it then passes or fails
- **THEN** that bucket's objects and the bucket itself are removed before the suite ends

## REMOVED Requirements

### Requirement: MinIO Configuration Is Not Required At Application Startup

**Reason**: This requirement described a deliberately transitional state. `add-minio-infrastructure` shipped the connection adapter without wiring it into any composition root, and stated that property normatively so a reviewer could tell "not yet wired" apart from "wired and broken". That state ends here: `cmd/api` now loads the configuration, opens the client, pings it, and ensures the bucket at startup, so the variables this capability already marks as required become startup preconditions.

**Migration**: The opposite property is now specified by `videojob-result-storage`'s "MinIO Configuration Is Required At Application Startup" requirement, which also records why this capability's fail-closed posture differs from every Redis-backed feature's fail-open one. Nothing else in `minio-infrastructure` changes — `Open` still constructs a client without blocking on connectivity, `Ping` still performs a real round trip, and `EnsureBucket` is still idempotent and concurrency-safe; only the claim that no composition root calls them is retired.

This capability's own configuration contract is untouched: `VIDEO_MINIO_ENDPOINT`/`ACCESS_KEY`/`SECRET_KEY`/`BUCKET` stay required and `VIDEO_MINIO_USE_SSL` stays optional, with `LoadConfigFromEnv` unchanged. Only the claim that nothing calls the loader is retired.

Operationally, an existing deployment that set none of the `VIDEO_MINIO_*` variables started and served every route before this change and will refuse to start after it. That is the intended breaking change, declared in this change's proposal.
