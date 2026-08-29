## MODIFIED Requirements

### Requirement: The Adapter Is Tested Against A Real RabbitMQ Instance

`internal/platform/rabbitmq`'s tests SHALL exercise `Open`, `Ping`, `Close`, and `DeclareTopology` against a running broker rather than a fake, reached through a `RABBITMQ_TEST_URL` environment variable. When that variable is unset, **this package's** tests SHALL skip with a clear message rather than fail, matching `internal/platform/redis` and `internal/video/infrastructure/storage` exactly.

A fake proves nothing about the two behaviors this package exists to get right: that a handshake against a real broker succeeds or fails as reported, and that a redeclaration with the arguments it declares is accepted while a conflicting one is not. Both are broker-enforced, and a test double would assert only that the package calls the functions the test double was written to expect.

The skip is scoped to this package. `cmd/api` now opens an AMQP connection, so its own `TestMain` requires `RABBITMQ_URL` to be **set**, alongside `ffmpeg` and the `VIDEO_MINIO_*` variables — but it does not require a reachable broker, because `cmd/api` does not either: the connection belongs to the outbox relay, which dials it in its own goroutine and retries rather than blocking startup. A `TestMain` demanding a live broker would assert a stronger contract than the code has.

This requirement's "No composition root opens a connection yet" scenario is replaced below rather than dropped. It was written as a current-state requirement with a scheduled expiry, obliging the change that first wires the broker into a composition root to modify it in the same change; this is that change, and the replacement scenario states the narrower guarantee that survives — the variable is required, a reachable broker is not. `cmd/worker` still does not exist and still opens nothing.

The broker SHALL be reached through a dedicated account rather than the built-in `guest`. RabbitMQ confines `guest` to loopback as the broker itself sees it, and every connection in this project's local and CI environments arrives over a Docker network from another address — so a `guest` URI fails with `ACCESS_REFUSED` in both, presenting as every test in the package failing at `Open` and reading like an absent broker.

Tests SHALL exercise the exported `DeclareTopology` itself, passing descriptors whose names are scoped to the individual test, and SHALL delete the exchanges and queues they declared when they finish, including on failure.

#### Scenario: Tests skip with a clear message when no broker is configured

- **GIVEN** `RABBITMQ_TEST_URL` is unset
- **WHEN** this package's tests run
- **THEN** they skip with a message naming the variable, and `go test ./...` still passes on a machine with no broker available

#### Scenario: A test leaves no topology behind

- **GIVEN** a test that calls `DeclareTopology` with a test-scoped descriptor
- **WHEN** it finishes, whether it passed or failed
- **THEN** none of the entities it declared remains on the broker

#### Scenario: cmd/api requires the variable but not a reachable broker

- **GIVEN** `RABBITMQ_URL` is set to an address with no broker listening
- **WHEN** `cmd/api` starts, or its test suite runs
- **THEN** it starts and serves every route, and the suite runs — the relay retries in the background and no request or test depends on the broker being up
