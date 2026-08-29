## Context

This repository has three infrastructure adapters already, and they do not agree with each other on shape — for reasons worth restating before adding a fourth.

| | Package | `Open` connects? | Health check | Teardown |
|---|---|---|---|---|
| PostgreSQL | per-context `infrastructure/postgres` | no (`sql.Open` is lazy) | `PingContext` | `db.Close()` |
| Redis | `internal/platform/redis` | no, and returns no `error` | `Ping` round trip | `Close` |
| MinIO | `internal/video/infrastructure/storage` | no, but returns an `error` (endpoint parsing) | `BucketExists`, not the client's cached `IsOnline` | none — `*minio.Client` exposes no teardown |

Each difference is forced by its client library rather than chosen. RabbitMQ diverges again, and further: AMQP is a stateful, connection-oriented protocol with a handshake, so there is no meaningful object to return before connecting. The adapter also carries something none of the three above has — a **topology**, which is broker-side state that later changes must agree with exactly, because AMQP fails a redeclaration whose arguments differ.

`docs/roadmap.md`'s Phase 6 section constrains this change from the other end: the next change relays into this topology a full merge before any consumer exists, and the cutover after it must not let a not-yet-redeployed replica's messages reach the new consumer. Both facts have to be designed for here, not discovered there.

## Goals / Non-Goals

**Goals:**

- Connection plumbing — configure, open, health-check, close — for an AMQP broker, usable by both bounded contexts.
- A generic, idempotent topology declaration driven by a caller-supplied descriptor, so the Video Processing context and later the Notification context declare their own shapes through one implementation.
- The Video Processing context's own job-dispatch topology, defined in that context, with a generation scheme the cutover can extend without a rename.
- A topology whose storage footprint is finite with no consumer running, and which strands no job when it fills.
- Local and CI brokers to test against, wired the same way Redis and MinIO already are.

**Non-Goals:**

- Publishing, consuming, acknowledgement, or retry policy. Those belong to the changes that actually move messages.
- The outbox relay. It is `add-videojob-source-key-and-outbox-relay`'s, along with the `video_job.queued` event it publishes.
- Any `cmd/api` or `cmd/worker` wiring. Nothing in the running application opens an AMQP connection after this change, and no startup configuration becomes required.
- Publisher-confirm *policy*, consumer prefetch, and connection recovery. Those are per-caller choices, configured on the caller's own channel. The spec does record that confirm mode is required to observe an overflow rejection at all — a bare `basic.publish` is asynchronous and returns nothing — because a publisher that treated a nil return as acceptance would record work as dispatched that the broker refused.
- The cutover's second generation. This change declares the topology the relay publishes into and fixes the scheme the successor follows; declaring it is the cutover's.
- Phase 7's notification topology. It reuses the connection adapter and `DeclareTopology`, not this topology.

## Decisions

### Decision 1: `github.com/rabbitmq/amqp091-go`, not `streadway/amqp` or a higher-level wrapper

`amqp091-go` is the RabbitMQ team's own continuation of `streadway/amqp`, which has been unmaintained since 2021 and is what most Go/RabbitMQ material still shows. Pinning to `v1.14.0`.

Higher-level wrappers (`wagslane/go-rabbitmq` and similar) bundle reconnection, publisher confirms, and consumer supervision. They were rejected for the same reason `minio-go` was preferred over an S3 SDK abstraction layer: this package's whole job is the thin part, and the policy those wrappers impose belongs to the publishing and consuming changes, which will want to make those choices explicitly rather than inherit them.

### Decision 2: `Open` connects, and the divergence is documented rather than smoothed over

`amqp091.Dial` performs the TCP connect, the protocol handshake, and authentication. There is no client object that is meaningful before that completes, so `Open` returns `(*amqp091.Connection, error)` and a failure means the broker is genuinely unreachable or the credentials are wrong.

The alternative considered was a deferred-connect shim — a struct holding the URL that dials on first use — purely to make this adapter's signature match `redis.Open`'s. Rejected: it would move a real, diagnosable startup failure to an arbitrary later point, and the consistency it buys is cosmetic. The spec states this explicitly so a future consistency pass does not "fix" it back.

Errors are wrapped without echoing the URI, which carries both the username and the password in its userinfo component. The wrapped message names the failure, not the credential.

### Decision 3: `Ping` opens and closes a channel

`(*Connection).IsClosed()` is available and cheap, and is the wrong primitive: it reports whether *this process* has already observed a close. A broker that has stopped answering has not necessarily closed the connection from this side yet, which is precisely the case a health check exists to detect — the same trap `minio-infrastructure` documents for `minio-go`'s cached `IsOnline`.

`Channel()` followed by `Close()` is a synchronous `channel.open`/`channel.close` exchange the broker must answer, cheap enough to call on a health endpoint, and it fails fast on a dead connection. Publishing a message to a throwaway queue would also work and was rejected as needlessly destructive for a read-only check.

### Decision 4: The topology's *shape* is platform plumbing; its *names* belong to the Video context

Two packages, split on a line `ddd-architecture` already draws. Its "Shared infrastructure with no owning context lives under internal/platform" scenario permits that namespace "only connection/lifecycle plumbing — never domain or application logic for a specific context's use case".

- `internal/platform/rabbitmq` — `Config`, `LoadConfigFromEnv`, `Open`, `Ping`, `Close`, a generic `Topology` descriptor, and `DeclareTopology(conn, topo)`. No exchange, queue, or routing-key name appears here, and there is no default descriptor. Phase 7's Notification context declares its own topology through the same function.
- `internal/video/infrastructure/messaging` — `JobDispatchTopology()`, returning the descriptor with `video.jobs.v1`, `video_job.queued`, and the rest.

The first shape considered put `DefaultTopology()` in `internal/platform/rabbitmq`, and it was wrong on exactly the canonical rule above: an exchange named `video.jobs` carrying a routing key named after a `VideoJob` event is a specific context's use case, however generic the surrounding code is. Keeping it there would have required a MODIFIED delta relaxing that scenario — a real architectural change, made incidentally, to avoid moving one function. The split is smaller than the delta.

It also makes Phase 7 cheaper rather than more expensive: a Notification topology extending a video-named default would have been the awkward case, and now there is nothing to extend.

### Decision 5: The generation lives in the exchange name, not the queue name or the routing key

```
job exchange (direct)   video.jobs.v1
routing key             video_job.queued
job queue               video.jobs.queued.v1
dead-letter exchange    video.jobs.dlx     (fanout, unversioned)
dead-letter queue       video.jobs.dead    (unversioned)
```

`docs/roadmap.md`'s cutover row commits to a separately named queue so that a rolling deploy cannot let a replica still processing in-request feed the new worker. **A separately named queue does not achieve that**, and this is worth stating plainly because it is a design correction, not a refinement: a `direct` exchange delivers each publish to *every* queue bound with the matching routing key, so binding `video.jobs.queued.v2` to the same exchange with the same key would hand it the old replicas' messages too — reproducing precisely the double-processing window the separate queue was meant to close.

Versioning the routing key would work and costs more: the key is deliberately equal to the outbox `event_type` string, which is what gives the database and the broker one vocabulary for the event. Versioning the **exchange** gives the two generations genuinely separate delivery paths and leaves that equality intact. The queue name carries the same suffix for legibility, not for isolation.

The dead-letter exchange and queue are unversioned on purpose. A fanout DLX delivers to every bound queue, so both generations' dead-lettered messages land in the same `video.jobs.dead` — which is what is wanted: a dead-lettered message exists to be looked at, and one place to look beats one per generation.

`direct` rather than `topic` for the job exchange: there is one routing key, and Phase 7's notification events get their own exchange with their own semantics. `direct` states that intent; `topic` would invite pattern bindings this design has no use for.

The cutover change inherits this: its own proposal must version the exchange, and `docs/roadmap.md`'s cutover row still describes the queue-only scheme. Correcting that row belongs to the change that owns it, not to this change's finalization PR.

### Decision 6: `DeclareTopology` takes a descriptor and owns its own channel

The signature is `DeclareTopology(conn *amqp091.Connection, topo Topology) error`. A `DeclareTopology(conn)` reading package constants internally was the first shape considered and is untestable in the way that matters: the only way to exercise it would be to declare the production names on a shared broker, which leaves a queue behind under a production name — with test-sized arguments, in the overflow test's case — for a later run to collide with as `PRECONDITION_FAILED`. Isolating tests in per-test vhosts was the alternative; it needs the management HTTP API to create them, which means the `-management` image variant and a second protocol in the test suite, to buy isolation the descriptor gives for free.

The two overflow policies and the dead-letter queue's lack of its own dead-letter exchange are deliberately **not** descriptor fields. They are what make the topology bounded, and a caller able to vary them could declare an unbounded chain through this same function. Names and the bound values are fields; the invariants are not.

It opens its own channel, declares everything, binds, and closes it. Callers pass a connection, not a channel, so the function cannot leave a caller's long-lived publishing or consuming channel in a closed state — a failed declaration closes the channel it happened on, and AMQP gives no way to reopen the same one.

Declaration order is fixed: dead-letter exchange, dead-letter queue, and its binding first, then the job exchange and the job queue that references the dead-letter exchange by name. RabbitMQ does not validate that a queue's `x-dead-letter-exchange` exists at declare time — it silently drops dead-lettered messages if it does not — so declaring the sink first makes a partial failure leave a topology that is incomplete rather than complete-looking and lossy.

All four declaration flags are pinned, not just durability. `autoDelete`, `exclusive`, and `noWait` are independent of `durable`, and each defeats what durability is here for: an exclusive queue is visible only to its declaring connection, an auto-delete queue vanishes with its last consumer, and `noWait: true` returns before the broker answers, so a rejected declaration surfaces later as a channel exception rather than as this function's error.

### Decision 7: Bounded by length, not by time, and overflow refuses rather than discards

| Queue | `x-max-length` | `x-message-ttl` | `x-overflow` | `x-dead-letter-exchange` |
|---|---|---|---|---|
| `video.jobs.queued.v1` | 10 000 | *(none)* | `reject-publish` | `video.jobs.dlx` |
| `video.jobs.dead` | 10 000 | 24 h | `drop-head` | *(none)* |

**No TTL on the job queue.** An earlier draft gave it one hour and called that "a job nobody has picked up in an hour is a failure to surface". It surfaces nowhere: RabbitMQ moves the expired message to the dead-letter queue and touches no database row, and `internal/video/domain/job_status.go`'s transition table has no edge out of `queued` except to `processing`. The job would report `queued` to its owner forever, with the message that could have advanced it sitting in a queue nothing reads. Making a TTL honest requires a dead-letter reconciler and a new `queued → failed` edge — a real design, owned by a change that has a worker to reconcile against. Bounding by length instead avoids creating the inconsistency rather than scheduling its repair.

**`reject-publish` rather than `drop-head`.** A full queue refuses the newest publish instead of evicting the oldest. The relay can leave the corresponding outbox row unstamped and retry, which turns a full queue into back-pressure: nothing is lost, the job stays `queued` with its outbox row unpublished, and the system resumes the moment the queue drains. A dropped head is a job that vanishes with no record it was ever queued. Note what this means during the relay change's window — if `.v1` fills with residue before the cutover, the relay stalls and outbox rows accumulate unpublished. That is the correct outcome: nothing should be consuming those messages anyway, uploads are unaffected because they still process in-request, and the cutover retires `.v1` entirely.

**`reject-publish` rather than `reject-publish-dlx`.** The `-dlx` variant also dead-letters what it refuses, which sounds like free observability and is not: a publisher that retries deposits one dead-lettered copy per attempt, so a stalled relay would fill the dead-letter queue with duplicates of work that was never lost. The nack the publisher already receives is the authoritative signal, and it is the one that can name the outbox row.

**24 h and 10 000 on the dead-letter queue.** Long enough to look at the morning after an incident, bounded so the chain terminates. A TTL is safe here in a way it is not on the job queue: a message reaches the dead-letter queue only after a consumer has rejected it or a bound has shed it, so it is already something no consumer will act on.

Both are pinned defaults returned by `JobDispatchTopology()` rather than separately exported constants — the descriptor is the single public surface, so there is one place to read the values from and one to change them. Changing either requires deleting and redeclaring the queue, since AMQP rejects a redeclaration with different arguments; that friction is intended, not an obstacle to work around.

### Decision 8: A named volume and a pinned hostname in `docker-compose.yml`

The tempting argument is that PostgreSQL is authoritative and a lost message is replayable from an outbox row whose `published_at` was never stamped. That argument is wrong, and the failure it misses is the whole point of the outbox pattern: once the relay has a broker acknowledgement and stamps `published_at`, the row is done and the *message* is the only record that the job is waiting to be processed. Wiping an unpersisted broker at that moment loses the job with PostgreSQL showing nothing left to replay — the same dangling state `minio_data` exists to prevent, where a `completed` row points at an object that no longer exists.

So the broker gets `rabbitmq_data`. `redis` is the correct counter-example rather than the model: its contents are explicitly non-authoritative (idempotency keys, rate-limit counters, a status cache), and losing them is a correctness non-event by design. A queued job is not.

The volume alone does not work. RabbitMQ stores its Mnesia database under `mnesia/rabbit@<hostname>`, and Compose gives a recreated container a freshly generated hostname — so the persisted volume would be reopened by a node with a different name, which starts against a new empty directory beside the old one rather than resuming it. `hostname: rabbitmq` is pinned on the service for that reason.

Durability has a third condition beyond the volume and the durable queue: messages are transient unless published with delivery mode 2, and a transient message in a durable queue is dropped on restart silently. The `videojob-messaging` spec carries that as a requirement of the topology rather than leaving it to each publisher, because the argument above fails just as completely against a transient publish as against a missing volume.

### Decision 9: CI uses a `services:` container; MinIO's `docker run` workaround does not generalize

`.github/workflows/ci.yml` starts MinIO with `docker run` in a step, carrying a comment warning not to convert it back to a service container, because that image's `CMD` lacks `server /data` and a service container has no field to supply command arguments. That reasoning is specific to MinIO. The `rabbitmq` image's default command is `rabbitmq-server`, which is exactly what is wanted, so this goes in `services:` alongside `postgres` and `redis`, with `rabbitmq-diagnostics -q ping` as the health command.

Image pinned to `rabbitmq:4-alpine`, matching the major-version pinning convention `postgres:16-alpine` and `redis:7-alpine` already use, and the same tag in `docker-compose.yml` so "works in CI" and "works locally" mean the same broker.

A dedicated `video`/`video` account is provisioned in both environments through `RABBITMQ_DEFAULT_USER`/`RABBITMQ_DEFAULT_PASS`, in the same fixed non-secret local-only posture as `minioadmin` and `identity`. This is not a stylistic preference: RabbitMQ confines the built-in `guest` account to loopback as the broker itself sees it, and every connection here arrives over a Docker network from another address — `app-test` across the compose network, and CI's published port presenting the runner as the bridge gateway. A `guest` URI fails with `ACCESS_REFUSED` in both, which presents as every test in the package failing at `Open` and reads like an absent broker.

### Decision 10: Tests scope their topology per test

Each test builds a descriptor whose names derive from the test and tears it down afterwards. Two reasons: a test declaring the production names with test-sized arguments would leave a queue behind that a later run with `JobDispatchTopology()`'s values collides with (`PRECONDITION_FAILED`, at a distance, on someone else's branch); and the overflow behavior in the spec needs a `max-length` of a few messages to be testable at all, which the production value cannot provide.

`JobDispatchTopology()`'s own values are asserted directly by a separate test. Without it, every topology test runs under test-scoped names and would pass against any defaults at all.

## Risks / Trade-offs

- **A future consistency pass "fixes" `Open` to be lazy like the other three adapters** → The spec forbids it in normative text with the reason attached, and this document records the alternative that was considered and why it was rejected.
- **The max-length and dead-letter TTL are guesses at this workload's scale, and AMQP makes them expensive to change** (a redeclaration with different arguments fails, so changing one means deleting the queue) → They are pinned defaults on `JobDispatchTopology()` with the policy intent written down, and the `.v1` naming already establishes that a topology with new arguments ships as a new generation. The friction is real and deliberate rather than an oversight.
- **A stalled relay is a quiet failure mode**: if `.v1` fills, publishes are nacked and outbox rows accumulate unpublished, with no user-visible symptom during the relay change's window because uploads still complete in-request → Accepted, and preferable to both alternatives (a TTL strands jobs; `drop-head` loses them). The relay change owns logging the nack against the outbox row, and queue depth at max length is directly observable.
- **A dead-letter exchange named in `x-dead-letter-exchange` that does not exist causes silent message loss, and RabbitMQ will not warn** → Declaration order puts the sink first, and the topology is declared in one function so no caller can create half of it.
- **`Ping` opening a channel is heavier than a cached predicate, and a health endpoint calling it under load adds broker work** → Nothing calls `Ping` in this change; the composition roots that will are free to rate-limit or cache their own health responses. The alternative fails the check it exists to perform.
- **This package's tests skip when no broker is configured, so a local `go test ./...` can pass having exercised none of it** → The skip matches both sibling adapters and is the right local behavior; the risk it carries is silently missing integration coverage, not a failing suite. Mitigated by CI, which always provides the broker, and by task 5.3, which requires confirming the section 3 tests reported `PASS` rather than `SKIP`. `docker compose run --build --rm app-test go test ./... -v` supplies all four services locally.

## Migration Plan

None. Two new packages, a new dependency, new compose and CI services. No existing Go source is modified, no startup configuration becomes required, and no endpoint changes behavior — reverting is deleting the packages and the two service definitions.

## Open Questions

None blocking. Three are deliberately deferred to the changes that own them: the relay's poll interval and batch size, and its handling of a nacked publish (6.2); the consumer's prefetch and acknowledgement policy (6.3); and whether a dead-letter reconciler is ever warranted, which only becomes answerable once something consumes.
