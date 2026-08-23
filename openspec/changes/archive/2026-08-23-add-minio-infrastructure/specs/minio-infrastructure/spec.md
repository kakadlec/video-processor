## ADDED Requirements

### Requirement: The MinIO Adapter Lives In The Video Processing Context

The MinIO connection adapter SHALL live at `internal/video/infrastructure/storage`, inside the bounded context that owns every one of its consumers, and SHALL NOT live under `internal/platform/`. `internal/platform/` is reserved for infrastructure no single bounded context owns; uploads, results, and presigned download URLs are all Video Processing concerns. The package MAY hold both connection plumbing and, in later changes, the context's storage port adapter, mirroring how `internal/video/infrastructure/postgres` holds connection plumbing beside its repository implementation.

#### Scenario: The adapter is placed inside the owning context

- **GIVEN** every consumer of MinIO belongs to the Video Processing context
- **WHEN** the connection adapter is added to the codebase
- **THEN** it lives under `internal/video/infrastructure/`, not under `internal/platform/`

#### Scenario: Domain and application layers do not import the adapter

- **GIVEN** the adapter exists under `internal/video/infrastructure/storage`
- **WHEN** `internal/video/dependency_rules_test.go` runs
- **THEN** it still passes, no package under `internal/video/domain` or `internal/video/application` importing the adapter

### Requirement: MinIO Connection Is Configured From The Environment

`internal/video/infrastructure/storage.LoadConfigFromEnv` SHALL read `VIDEO_MINIO_ENDPOINT`, `VIDEO_MINIO_ACCESS_KEY`, `VIDEO_MINIO_SECRET_KEY`, and `VIDEO_MINIO_BUCKET` from the environment, and SHALL fail with a clear, distinguishable error identifying the missing variable when any of them is unset or empty, rather than defaulting to a hardcoded endpoint, credential, or bucket name. `VIDEO_MINIO_USE_SSL` SHALL be optional: unset or empty SHALL mean `false`. When it is present but not parseable as a boolean, `LoadConfigFromEnv` SHALL return a clear error naming the variable and the offending value, and SHALL NOT fall back to `false` — silently treating an unrecognized value as "no TLS" would turn a deployment typo into a plaintext connection to an endpoint the operator believed was TLS-protected. This matches `internal/platform/ratelimit`'s existing treatment of optional variables, where absent and present-but-invalid are distinct outcomes.

The variables SHALL carry the owning context's prefix, matching `VIDEO_POSTGRES_DSN` and distinguishing them from the unprefixed variables used by `internal/platform/` packages.

#### Scenario: A missing required variable returns a clear error

- **GIVEN** `VIDEO_MINIO_ENDPOINT`, `VIDEO_MINIO_ACCESS_KEY`, `VIDEO_MINIO_SECRET_KEY`, or `VIDEO_MINIO_BUCKET` is unset or empty
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns an error identifying which variable is missing, and no `Config`

#### Scenario: All required variables present are loaded into Config

- **GIVEN** `VIDEO_MINIO_ENDPOINT`, `VIDEO_MINIO_ACCESS_KEY`, `VIDEO_MINIO_SECRET_KEY`, and `VIDEO_MINIO_BUCKET` are all set
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns a `Config` whose fields equal those values, and no error

#### Scenario: VIDEO_MINIO_USE_SSL is optional and defaults to false

- **GIVEN** every required variable is set and `VIDEO_MINIO_USE_SSL` is unset
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns a `Config` whose `UseSSL` field is `false`, and no error

#### Scenario: VIDEO_MINIO_USE_SSL is honored when set

- **GIVEN** every required variable is set and `VIDEO_MINIO_USE_SSL` is set to a recognized true value
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns a `Config` whose `UseSSL` field is `true`, and no error

#### Scenario: A malformed VIDEO_MINIO_USE_SSL is rejected rather than defaulted

- **GIVEN** every required variable is set and `VIDEO_MINIO_USE_SSL` is set to a non-empty value that is not a recognized boolean (for example a typo such as `ture`)
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns an error naming the variable and the offending value, and no `Config` — TLS is never silently disabled by an unrecognized value

### Requirement: MinIO Configuration Is Not Required At Application Startup

Loading MinIO configuration SHALL NOT be a precondition for starting `cmd/api`. No composition root SHALL call `LoadConfigFromEnv` or `Open` as part of this capability, so an environment with none of the `VIDEO_MINIO_*` variables set SHALL start and serve every existing route exactly as it did before this capability existed.

#### Scenario: The server starts with no VIDEO_MINIO_* variables set

- **GIVEN** none of the `VIDEO_MINIO_*` environment variables are set
- **WHEN** `cmd/api` starts
- **THEN** it starts successfully and every existing route behaves as it did before this capability was added

### Requirement: Open Constructs A Client Without Blocking On Connectivity

`internal/video/infrastructure/storage.Open` SHALL construct and return a MinIO client from a `Config` without itself verifying connectivity — matching `internal/video/infrastructure/postgres.Open`'s and `internal/platform/redis.Open`'s lazy-connection behavior. Because the underlying client constructor parses the endpoint and builds the HTTP transport, either of which can fail, `Open` SHALL return an `error` alongside the client and SHALL propagate that failure rather than discarding it. `Open` SHALL NOT be specified to validate credentials: the static credential provider performs no key-shape validation, so invalid or absent credentials surface only on a later server operation. Callers are responsible for verifying reachability via the health check below before relying on the client.

#### Scenario: Open succeeds even when MinIO is unreachable

- **GIVEN** a `Config` whose endpoint does not correspond to a running MinIO instance
- **WHEN** `Open` is called with it
- **THEN** it returns a non-nil client and no error — the unreachability only surfaces on a subsequent operation

#### Scenario: Open reports an endpoint the client constructor rejects

- **GIVEN** a `Config` whose endpoint the underlying client constructor rejects — for example one carrying a path (`host:port/path`) or an invalid character in the host
- **WHEN** `Open` is called with it
- **THEN** it returns a non-nil error and no usable client

#### Scenario: Endpoint validation is the client library's, not this adapter's

- **GIVEN** a `Config` whose endpoint carries a URL scheme (`http://host:port`) rather than being a bare `host:port` value
- **WHEN** `Open` is called with it
- **THEN** it returns a client and no error, because the pinned client tolerates the scheme — this adapter SHALL NOT add endpoint validation of its own, so `Open` is not a guard against a mis-shaped endpoint the library accepts

### Requirement: Health Check Confirms Connectivity With A Real Round Trip

`internal/video/infrastructure/storage` SHALL expose a context-aware health check (`Ping`) that issues a genuine round trip to the MinIO server and reports failure distinguishably from success. It SHALL NOT report health from a cached or background-refreshed observation, and it SHALL honor cancellation and deadlines on the supplied context.

#### Scenario: Ping succeeds against a reachable MinIO instance

- **GIVEN** a client opened against a running, reachable MinIO instance
- **WHEN** `Ping` is called with a live context
- **THEN** it returns no error

#### Scenario: Ping fails against an unreachable MinIO instance

- **GIVEN** a client opened against an endpoint with no running MinIO instance
- **WHEN** `Ping` is called
- **THEN** it returns a non-nil error wrapping the underlying connection failure

#### Scenario: Ping honors a canceled context

- **GIVEN** a client opened against any endpoint
- **WHEN** `Ping` is called with an already-canceled context
- **THEN** it returns a non-nil error rather than blocking or reporting success

### Requirement: Bucket Provisioning Is Idempotent And Concurrency-Safe

`internal/video/infrastructure/storage` SHALL expose `EnsureBucket`, which creates the configured bucket only if it does not already exist and SHALL succeed when the bucket is already present. It SHALL treat a concurrent-creation outcome — another caller **using the same credentials** having created the same bucket between the existence check and the creation attempt — as success rather than as an error, so that multiple instances starting simultaneously do not fail startup.

It SHALL NOT extend that tolerance to a bucket owned by a different account. In the globally shared bucket namespace, "the name is already taken by someone else" (`BucketAlreadyExists`) means the configured bucket is unusable by this client, and reporting successful provisioning would defer the failure to the first object write. Only the owned-by-you outcome (`BucketAlreadyOwnedByYou`) is benign, and it is what same-credential replicas racing each other actually produce; every other failure SHALL propagate.

`EnsureBucket` SHALL NOT apply any versioning, retention, lifecycle, or access policy to the bucket.

#### Scenario: EnsureBucket creates a bucket that does not exist

- **GIVEN** a reachable MinIO instance with no bucket of the configured name
- **WHEN** `EnsureBucket` is called
- **THEN** it returns no error and the bucket exists afterwards

#### Scenario: EnsureBucket is a no-op when the bucket already exists

- **GIVEN** a reachable MinIO instance where the configured bucket already exists
- **WHEN** `EnsureBucket` is called
- **THEN** it returns no error and the bucket's existing contents are unchanged

#### Scenario: Concurrent EnsureBucket calls both succeed

- **GIVEN** a reachable MinIO instance with no bucket of the configured name
- **WHEN** two callers using the same credentials invoke `EnsureBucket` concurrently for that same bucket
- **THEN** both return no error and exactly one bucket exists afterwards

#### Scenario: A bucket owned by another account is not reported as provisioned

- **GIVEN** the configured bucket name is already taken in the shared namespace by a different account
- **WHEN** `EnsureBucket` is called
- **THEN** it returns a non-nil error rather than reporting success, so the unusable configuration surfaces at provisioning time instead of at the first object write

#### Scenario: EnsureBucket fails against an unreachable instance

- **GIVEN** a client opened against an endpoint with no running MinIO instance
- **WHEN** `EnsureBucket` is called
- **THEN** it returns a non-nil error rather than reporting success

### Requirement: The Adapter Exposes No Client Teardown Function

`internal/video/infrastructure/storage` SHALL NOT expose a `Close` or other teardown function for the MinIO client, breaking symmetry with `internal/platform/redis.Close` and with `postgres.Open`'s `*sql.DB` deliberately. The underlying client library exposes no teardown method and keeps its HTTP transport unexported, so such a function could only report success while releasing nothing — an API promising a resource-management contract it cannot honor. Callers of this capability SHALL therefore have no teardown obligation for a client returned by `Open`.

#### Scenario: No teardown function is offered

- **GIVEN** a caller has obtained a client from `Open`
- **WHEN** it looks for a teardown function in this package
- **THEN** none exists, and the caller is not required to release the client

#### Scenario: A client remains usable for the process's lifetime

- **GIVEN** a client returned by `Open` against a reachable instance
- **WHEN** it is used for repeated operations over time without any teardown call
- **THEN** those operations continue to succeed, connection reuse being the library's own concern

### Requirement: The Adapter Is Tested Against A Real MinIO Instance

This capability's tests SHALL exercise `Open`, `Ping`, and `EnsureBucket` against a real MinIO instance rather than a mock, configured through `VIDEO_MINIO_TEST_ENDPOINT`, `VIDEO_MINIO_TEST_ACCESS_KEY`, `VIDEO_MINIO_TEST_SECRET_KEY`, and `VIDEO_MINIO_TEST_BUCKET`. The test bucket SHALL be distinct from the runtime `VIDEO_MINIO_BUCKET`, so a test that creates or deletes buckets cannot disturb a locally running application's artifacts. When those variables are unset the tests SHALL skip with a clear message rather than fail, so `go test ./...` still passes on a machine with no MinIO available.

#### Scenario: Tests run against the configured instance

- **GIVEN** the `VIDEO_MINIO_TEST_*` variables point at a running MinIO instance
- **WHEN** the package's tests run
- **THEN** they exercise `Open`, `Ping`, and `EnsureBucket` against that instance and pass

#### Scenario: Tests skip when no test instance is configured

- **GIVEN** the `VIDEO_MINIO_TEST_*` variables are unset
- **WHEN** the package's tests run
- **THEN** they skip with a message naming the missing configuration, and `go test ./...` still succeeds
