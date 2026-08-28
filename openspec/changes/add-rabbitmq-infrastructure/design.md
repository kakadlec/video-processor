## Context

This repository has three infrastructure adapters already, and they do not agree with each other on shape — for reasons worth restating before adding a fourth.

| | Package | `Open` connects? | Health check | Teardown |
|---|---|---|---|---|
| PostgreSQL | per-context `infrastructure/postgres` | no (`sql.Open` is lazy) | `PingContext` | `db.Close()` |
| Redis | `internal/platform/redis` | no, and returns no `error` | `Ping` round trip | `Close` |
| MinIO | `internal/video/infrastructure/storage` | no, but returns an `error` (endpoint parsing) | `BucketExists`, not the client's cached `IsOnline` | none — `*minio.Client` exposes no teardown |

Each difference is forced by its client library rather than chosen. RabbitMQ diverges again, and further: AMQP is a stateful, connection-oriented protocol with a handshake, so there is no meaningful object to return before connecting. The adapter also carries something none of the three above has — a **topology**, which is broker-side state that later changes must agree with exactly, because AMQP fails a redeclaration whose arguments differ.

`docs/roadmap.md`'s Phase 6 section constrains this change from the other end: the next change relays into this topology a full merge before any consumer exists, and the cutover after it consumes from a **separately named** queue rather than purging this one. Both facts have to be designed for here, not discovered there.

## Goals / Non-Goals

**Goals:**

- Connection plumbing — configure, open, health-check, close — for an AMQP broker, usable by both bounded contexts.
- An idempotent topology declaration driven by an exported descriptor, so the relay (Phase 6.2), the worker (6.3), and the Notification subscriber (Phase 7) bind to one definition rather than to literals repeated at each call site.
- A topology whose storage footprint is finite even with no consumer running for an extended period.
- Local and CI brokers to test against, wired the same way Redis and MinIO already are.

**Non-Goals:**

- Publishing, consuming, acknowledgement, or retry policy. Those belong to the changes that actually move messages.
- The outbox relay. It is `add-videojob-source-key-and-outbox-relay`'s, along with the `video_job.queued` event it publishes.
- Any `cmd/api` or `cmd/worker` wiring. Nothing in the running application opens an AMQP connection after this change, and no startup configuration becomes required.
- Publisher-confirm *policy*, consumer prefetch, and connection recovery. Those are per-caller choices, configured on the caller's own channel. The spec does record that confirm mode is required to observe an overflow rejection at all — a bare `basic.publish` is asynchronous and returns nothing — because a relay that treated a nil return as acceptance would stamp `published_at` for a message the broker refused.
- The cutover's second job queue. This change declares the topology 6.2 publishes into; 6.3 declares its own queue beside it and retires this one.

## Decisions

### Decision 1: `github.com/rabbitmq/amqp091-go`, not `streadway/amqp` or a higher-level wrapper

`amqp091-go` is the RabbitMQ team's own continuation of `streadway/amqp`, which has been unmaintained since 2021 and is what most Go/RabbitMQ material still shows. Pinning to `v1.14.0`.

Higher-level wrappers (`wagslane/go-rabbitmq` and similar) bundle reconnection, publisher confirms, and consumer supervision. They were rejected for the same reason `minio-go` was preferred over an S3 SDK abstraction layer: this package's whole job is the thin part, and the policy those wrappers impose belongs to the publishing and consuming changes, which will want to make those choices explicitly rather than inherit them.

### Decision 2: `Open` connects, and the divergence is documented rather than smoothed over

`amqp091.Dial` performs the TCP connect, the protocol handshake, and authentication. There is no client object that is meaningful before that completes, so `Open` returns `(*amqp091.Connection, error)` and a failure means the broker is genuinely unreachable or the credentials are wrong.

The alternative considered was a deferred-connect shim — a struct holding the URL that dials on first use — purely to make this adapter's signature match `redis.Open`'s. Rejected: it would move a real, diagnosable startup failure to an arbitrary later point, and the consistency it buys is cosmetic. The spec states this explicitly so a future consistency pass does not "fix" it back.

Errors are wrapped without echoing the URI, which carries the password in its userinfo component. The wrapped message names the failure, not the credential.

### Decision 3: `Ping` opens and closes a channel

`(*Connection).IsClosed()` is available and cheap, and is the wrong primitive: it reports whether *this process* has already observed a close. A broker that has stopped answering has not necessarily closed the connection from this side yet, which is precisely the case a health check exists to detect — the same trap `minio-infrastructure` documents for `minio-go`'s cached `IsOnline`.

`Channel()` followed by `Close()` is a synchronous `channel.open`/`channel.close` exchange the broker must answer, cheap enough to call on a health endpoint, and it fails fast on a dead connection. Publishing a message to a throwaway queue would also work and was rejected as needlessly destructive for a read-only check.

### Decision 4: Topology names carry a version suffix from the start

```
exchange (direct)    video.jobs
routing key          video_job.queued
job queue            video.jobs.queued.v1
dead-letter exchange video.jobs.dlx  (fanout)
dead-letter queue    video.jobs.dead
```

The `.v1` suffix on the job queue is not speculative generality: `docs/roadmap.md`'s cutover row commits to consuming from a separately named queue rather than purging this one, because a purge would have to be timed against a deployment in which an old replica can still publish after it runs. Naming the first queue `.v1` makes that successor a convention (`.v2`) instead of an ad-hoc rename someone has to justify later. The exchange, the dead-letter exchange, and the dead-letter queue are unversioned — the cutover adds a queue and a binding, not a new event or a new failure sink.

The routing key matches the outbox `event_type` string (`video_job.queued`) that 6.2 persists, itself following the existing `video_job.created` constant. One vocabulary for the event across the database and the broker.

A `direct` exchange rather than `topic`: there is exactly one routing key today and Phase 7's notification events will warrant their own exchange with their own fanout semantics. `direct` states that intent; `topic` would invite pattern bindings this design has no use for.

### Decision 5: Both queues bounded, and the job queue rejects rather than drops

| Queue | `x-message-ttl` | `x-max-length` | `x-overflow` | `x-dead-letter-exchange` |
|---|---|---|---|---|
| `video.jobs.queued.v1` | 1 h | 10 000 | `reject-publish-dlx` | `video.jobs.dlx` |
| `video.jobs.dead` | 24 h | 10 000 | `drop-head` | *(none)* |

Two bounds, not one. Bounding only the job queue relocates growth rather than capping it, because everything the bound sheds lands in the dead-letter queue — and during the window 6.2 opens, with nothing consuming the job queue, that is where every message ends up. The dead-letter queue forwards nowhere and drops its own head, so the chain terminates.

Both queues are declared durable, and durability is only one of three conditions. A durable queue survives a broker restart; the messages in it do not unless each was published with delivery mode 2, and a transient message in a durable queue is dropped on restart silently. The spec states the persistent-publish obligation as a requirement of this capability rather than leaving it to each publisher, because the argument in decision 8 — that a stamped outbox row makes the message the only record of a waiting job — fails just as completely if the message was transient as if the volume were missing.

`reject-publish-dlx` over the default `drop-head` on the job queue: overflow should refuse the newest publish, not silently evict the oldest queued job. The relay can leave the corresponding outbox row unpublished and retry a refused publish; a dropped head is a job that vanishes with no record that it was ever queued. `reject-publish-dlx` also dead-letters what it refuses, so the refusal is inspectable rather than merely counted.

One hour on the job queue is a policy statement, not a tuning parameter: a job nobody has picked up in an hour is a failure to surface, not work to keep waiting. Twenty-four hours on the dead-letter queue is long enough to look at the morning after an incident. Both are pinned defaults returned by `DefaultTopology()` rather than separately exported constants — the descriptor is the single public surface, so there is one place to read the values from and one to change them. Changing either requires deleting and redeclaring the queue, since AMQP rejects a redeclaration with different arguments; that friction is intended, not an obstacle to work around.

### Decision 6: `DeclareTopology` takes a descriptor and owns its own channel

The signature is `DeclareTopology(conn *amqp091.Connection, topo Topology) error`, with `DefaultTopology()` returning the production values. A `DeclareTopology(conn)` that read package constants internally was the first shape considered and is untestable in the way that matters: the only way to exercise it would be to declare the production names on a shared broker, which leaves a queue behind under a production name — with test-sized arguments, in the overflow test's case — for a later run to collide with as `PRECONDITION_FAILED`. Isolating tests in per-test vhosts was the alternative; it needs the management HTTP API to create them, which means the `-management` image variant and a second protocol in the test suite, to buy isolation the descriptor gives for free.

The two overflow policies and the dead-letter queue's lack of its own dead-letter exchange are deliberately **not** descriptor fields. They are what make the topology bounded, and a caller able to vary them could declare an unbounded chain through this same function. Names and the four TTL/length values are fields; the invariants are not.

It opens its own channel, declares everything, binds, and closes it. Callers pass a connection, not a channel, so the function cannot leave a caller's long-lived publishing or consuming channel in a closed state — a failed declaration closes the channel it happened on, and AMQP gives no way to reopen the same one.

Declaration order matters and is fixed: dead-letter exchange, dead-letter queue, and its binding first, then the job exchange and the job queue that references the dead-letter exchange by name. RabbitMQ does not validate that a queue's `x-dead-letter-exchange` exists at declare time — it silently drops dead-lettered messages if it does not — so declaring the sink first makes a partial failure leave a topology that is incomplete rather than one that is complete-looking and lossy.

### Decision 7: CI uses a `services:` container; MinIO's `docker run` workaround does not generalize

`.github/workflows/ci.yml` starts MinIO with `docker run` in a step, carrying a comment warning not to convert it back to a service container, because that image's `CMD` lacks `server /data` and a service container has no field to supply command arguments. That reasoning is specific to MinIO. The `rabbitmq` image's default command is `rabbitmq-server`, which is exactly what is wanted, so this goes in `services:` alongside `postgres` and `redis`, with `rabbitmq-diagnostics -q ping` as the health command.

Image pinned to `rabbitmq:4-alpine`, matching the major-version pinning convention `postgres:16-alpine` and `redis:7-alpine` already use, and the same tag in `docker-compose.yml` so "works in CI" and "works locally" mean the same broker.

### Decision 8: A named volume in `docker-compose.yml`, following `postgres` and `minio` rather than `redis`

The tempting argument is that PostgreSQL is authoritative and a lost message is replayable from an outbox row whose `published_at` was never stamped. That argument is wrong, and the failure it misses is the whole point of the outbox pattern: once the relay has a broker acknowledgement and stamps `published_at`, the row is done and the *message* is the only record that the job is waiting to be processed. Wiping an unpersisted broker at that moment loses the job with PostgreSQL showing nothing left to replay — the same dangling state `minio_data` exists to prevent, where a `completed` row points at an object that no longer exists.

So the broker gets `rabbitmq_data`. `redis` is the correct counter-example rather than the model: its contents are explicitly non-authoritative (idempotency keys, rate-limit counters, a status cache), and losing them is a correctness non-event by design. A queued job is not.

This does mean a `docker compose down -v` is now the way to clear the residue the relay change accumulates, rather than an ordinary `down`. That is the right trade: the residue is bounded by the queue's own TTL and the cutover reaches it through a differently named queue anyway, so persistence costs a little stale storage and buys not losing real work.

### Decision 9: Tests scope their topology per test

Each test declares queues and exchanges under names derived from the test — not the production constants — and tears them down. Two reasons: a test that declared the real topology with test-sized arguments would leave a queue behind that a later run with the production constants collides with (`PRECONDITION_FAILED`, at a distance, on someone else's branch); and the overflow behavior in the spec needs a `max-length` of a few messages to be testable at all, which the production value cannot provide.

The idempotency and conflict scenarios are the exception in spirit: they assert that a redeclaration with the package's own arguments succeeds and that one with different arguments fails. Both run against test-scoped names using the same argument set the constants define, so they pin the arguments without pinning the broker's global state.

## Risks / Trade-offs

- **A future consistency pass "fixes" `Open` to be lazy like the other three adapters** → The spec forbids it in normative text with the reason attached, and this document records the alternative that was considered and why it was rejected.
- **The TTL/max-length values are guesses at this workload's scale, and AMQP makes them expensive to change** (a redeclaration with different arguments fails, so changing one means deleting the queue) → They are exported constants with the policy intent written down, and the `.v1` naming already establishes that a queue with new arguments ships as a new queue. The friction is real and deliberate rather than an oversight.
- **A dead-letter exchange named in `x-dead-letter-exchange` that does not exist causes silent message loss, and RabbitMQ will not warn** → Declaration order puts the sink first, and the topology is declared in one function so no caller can create half of it.
- **`Ping` opening a channel is heavier than a cached predicate, and a health endpoint calling it under load adds broker work** → Nothing calls `Ping` in this change; the composition roots that will are free to rate-limit or cache their own health responses. The alternative fails the check it exists to perform.
- **This package's tests skip when no broker is configured, so a local `go test ./...` can pass having exercised none of it** → The skip matches both sibling adapters and is the right local behavior; the risk it carries is silently missing integration coverage, not a failing suite. Mitigated by CI, which always provides the broker, and by task 5.3, which requires confirming the section 3 tests reported `PASS` rather than `SKIP` in the verification run. `docker compose run --build --rm app-test go test ./... -v` supplies all four services locally.

## Migration Plan

None. New package, new dependency, new compose and CI services. No existing Go source is modified, no startup configuration becomes required, and no endpoint changes behavior — reverting is deleting the package and the two service definitions.

## Open Questions

None blocking. Two are deliberately deferred to the changes that own them: the relay's poll interval and batch size (6.2), and the consumer's prefetch and acknowledgement policy (6.3). Neither is a property of the connection or the topology.
