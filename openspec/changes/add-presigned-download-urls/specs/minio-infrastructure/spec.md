## MODIFIED Requirements

### Requirement: MinIO Connection Is Configured From The Environment

`internal/video/infrastructure/storage.LoadConfigFromEnv` SHALL read `VIDEO_MINIO_ENDPOINT`, `VIDEO_MINIO_ACCESS_KEY`, `VIDEO_MINIO_SECRET_KEY`, and `VIDEO_MINIO_BUCKET` from the environment, and SHALL fail with a clear, distinguishable error identifying the missing variable when any of them is unset or empty, rather than defaulting to a hardcoded endpoint, credential, or bucket name. `VIDEO_MINIO_USE_SSL` SHALL be optional: unset or empty SHALL mean `false`. When it is present but not parseable as a boolean, `LoadConfigFromEnv` SHALL return a clear error naming the variable and the offending value, and SHALL NOT fall back to `false` — silently treating an unrecognized value as "no TLS" would turn a deployment typo into a plaintext connection to an endpoint the operator believed was TLS-protected. This matches `internal/platform/ratelimit`'s existing treatment of optional variables, where absent and present-but-invalid are distinct outcomes.

`LoadConfigFromEnv` SHALL additionally read two optional variables describing how clients outside the server's own network reach the same storage service: `VIDEO_MINIO_PUBLIC_ENDPOINT`, defaulting to `VIDEO_MINIO_ENDPOINT` when unset or empty, and `VIDEO_MINIO_PUBLIC_USE_SSL`, defaulting to the resolved value of `VIDEO_MINIO_USE_SSL` when unset or empty. Neither SHALL be a startup precondition: a deployment where one address serves both the server and its clients SHALL require no new configuration. `VIDEO_MINIO_PUBLIC_USE_SSL` SHALL be rejected when present but unparseable, on the same reasoning as its internal counterpart.

The two exist because a signed URL's host is covered by the signature and therefore cannot be rewritten after issuance, while the internal endpoint is routinely a name only the server resolves. The public value is used solely to construct URLs handed to clients; it is never dialed by the server.

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

#### Scenario: The public endpoint defaults to the internal one

- **GIVEN** every required variable is set and `VIDEO_MINIO_PUBLIC_ENDPOINT` is unset
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns a `Config` whose public endpoint equals `VIDEO_MINIO_ENDPOINT`, and no error

#### Scenario: The public endpoint is honored when set

- **GIVEN** every required variable is set and `VIDEO_MINIO_PUBLIC_ENDPOINT` names an address different from `VIDEO_MINIO_ENDPOINT`
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns a `Config` carrying both addresses separately, and no error

#### Scenario: The public TLS setting defaults to the internal one

- **GIVEN** every required variable is set, `VIDEO_MINIO_USE_SSL` is set to a recognized true value, and `VIDEO_MINIO_PUBLIC_USE_SSL` is unset
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** the resolved public TLS setting is `true`, following its internal counterpart rather than the `false` default

#### Scenario: The public TLS setting is independent when set

- **GIVEN** every required variable is set, `VIDEO_MINIO_USE_SSL` is unset, and `VIDEO_MINIO_PUBLIC_USE_SSL` is set to a recognized true value
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** the resolved internal TLS setting is `false` and the resolved public TLS setting is `true`, so a plaintext internal hop and a TLS-terminated public address can coexist

#### Scenario: A malformed VIDEO_MINIO_PUBLIC_USE_SSL is rejected rather than defaulted

- **GIVEN** every required variable is set and `VIDEO_MINIO_PUBLIC_USE_SSL` is set to a non-empty value that is not a recognized boolean
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns an error naming the variable and the offending value, and no `Config`

## ADDED Requirements

### Requirement: A Presign-Only Client Is Constructed Against The Public Endpoint

The package SHALL expose construction of a second client whose sole purpose is issuing signed URLs, built from the resolved public endpoint and public TLS setting rather than the internal ones. That client SHALL NOT be pinged, SHALL NOT be used for any bucket or object operation, and SHALL NOT be expected to reach the address it is configured with — in the deployment shape this exists for, the server cannot resolve that address at all.

This is a use of the existing contract, not an exception to it: this package already specifies that client construction performs no connectivity check. The code SHALL nonetheless state the intent explicitly, because a deliberately unreachable client is otherwise indistinguishable from a wiring mistake.

#### Scenario: The presign-only client is constructed without connectivity

- **GIVEN** a public endpoint that does not resolve from the constructing process
- **WHEN** the presign-only client is constructed
- **THEN** construction succeeds and no network call is made

#### Scenario: The presign-only client is never used for storage operations

- **WHEN** the composition root's wiring is inspected
- **THEN** the presign-only client is passed only where signed URLs are issued, and every store, stat, retrieve, and delete operation uses the internal client

### Requirement: The Bucket Region Is Discovered At Startup And Supplied To The Presign-Only Client

The composition root SHALL determine the configured bucket's region using the internal, reachable client, and SHALL supply that region when constructing the presign-only client. No environment variable SHALL configure the region.

The reason is behavioral, not stylistic: signing an object URL on a client with no configured region first issues a bucket-location request to that client's endpoint. On the presign-only client that endpoint is, by design, unreachable from the server, so signing would fail. With the region supplied, signing performs no network call at all.

Discovery SHALL occur on the existing fail-closed startup path, alongside the connectivity check and bucket provisioning, and a failure SHALL prevent startup like every other step there. A region variable was rejected as one more value an operator can set wrong for something the storage service will report on request; a hardcoded default was rejected because it is correct for a local MinIO and silently wrong for storage in any other region.

#### Scenario: The region is discovered during startup

- **GIVEN** a reachable storage service and a provisioned bucket
- **WHEN** the application starts
- **THEN** it obtains the bucket's region through the internal client and constructs the presign-only client with it

#### Scenario: Signing performs no network call once the region is supplied

- **GIVEN** a presign-only client constructed with a region and an endpoint that does not resolve
- **WHEN** a URL is signed for a stored object
- **THEN** signing succeeds without contacting any host

#### Scenario: A failure to discover the region prevents startup

- **GIVEN** the region cannot be determined
- **WHEN** the application starts
- **THEN** it exits with an error rather than serving requests with a presign-only client that cannot sign

#### Scenario: No region variable is introduced

- **WHEN** the configuration surface is inspected
- **THEN** no environment variable sets the storage region
