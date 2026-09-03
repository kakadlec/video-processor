# redis-infrastructure Specification

## Purpose

Define `internal/platform/redis`, the shared, low-level Redis connection adapter used across bounded contexts: configuration, connection lifecycle, and health check. This package remains bare connection plumbing: context-owned behaviors such as upload idempotency, status caching, and the Video Processing worker lease live in their own adapters, while the cross-cutting rate limiter lives beside the connection package under `internal/platform/`.

## Requirements

### Requirement: Redis Connection Is Configured From The Environment

`internal/platform/redis.LoadConfigFromEnv` SHALL read the Redis address from the `REDIS_ADDR` environment variable and SHALL fail with a clear, distinguishable error when it is unset, rather than defaulting to a hardcoded address.

#### Scenario: Missing REDIS_ADDR returns a clear error

- **GIVEN** the `REDIS_ADDR` environment variable is unset or empty
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns `ErrAddrRequired` and no `Config`

#### Scenario: REDIS_ADDR present is loaded into Config

- **GIVEN** the `REDIS_ADDR` environment variable is set to a `host:port` value
- **WHEN** `LoadConfigFromEnv` is called
- **THEN** it returns a `Config` whose `Addr` field equals that value, and no error

### Requirement: Open Constructs A Client Without Blocking On Connectivity

`internal/platform/redis.Open` SHALL construct and return a Redis client from a `Config` without itself verifying connectivity — matching `internal/identity/infrastructure/postgres.Open`'s lazy-connection behavior. Because constructing the underlying client from an unparsed `Addr` string cannot fail, `Open` SHALL return only the client, with no `error` result. Callers are responsible for verifying connectivity (e.g. via the health check below) before relying on the client.

#### Scenario: Open succeeds even when Redis is unreachable

- **GIVEN** a `Config` whose `Addr` does not correspond to a running Redis instance
- **WHEN** `Open` is called with it
- **THEN** it returns a non-nil client — the unreachability only surfaces on a subsequent command

### Requirement: Health Check Confirms Connectivity

`internal/platform/redis` SHALL expose a context-aware health check (`Ping`) that issues a real round-trip to the Redis server and reports failure distinguishably from success.

#### Scenario: Ping succeeds against a reachable Redis instance

- **GIVEN** a client opened against a running, reachable Redis instance
- **WHEN** `Ping` is called with a live context
- **THEN** it returns no error

#### Scenario: Ping fails against an unreachable Redis instance

- **GIVEN** a client opened against an address with no running Redis instance
- **WHEN** `Ping` is called
- **THEN** it returns a non-nil error wrapping the underlying connection failure

### Requirement: Close Releases The Client's Connection Resources

`internal/platform/redis` SHALL expose a `Close` function that releases the client's underlying connection pool.

#### Scenario: Close succeeds on an open client

- **GIVEN** a client returned by `Open`
- **WHEN** `Close` is called on it
- **THEN** it returns no error, and subsequent commands on that client fail rather than silently reusing a stale connection
