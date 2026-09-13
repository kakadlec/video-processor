## Context

`proposal.md` states that one failure mode strands a job where nothing can reach it. This document settles what distinguishes *"this message can never succeed"* from *"this message cannot succeed right now"*, and how the handler can tell them apart without reintroducing the hazard the blanket no-requeue rule exists to prevent.

Eight properties of the tree constrain the answer. Each was read out of the code for this change rather than carried over from the backlog row.

- **The disposition set is two-valued and the rule is deliberate.** `internal/video/infrastructure/messaging/consumer.go:53-67`: `Ack`, `Reject`, and a doc comment on `Reject` saying requeueing "would loop rather than recover". `cmd/worker/main.go:368-376` says the same from the handler's side. Neither is an oversight to be corrected; both are correct about the cases they enumerate.
- **`StartProcessing` propagates its repository's errors unchanged.** `internal/video/application/start_processing.go:44-47` (`FindByID`) and `:73-76` (`ClaimForProcessing`). Only two conditions are converted to sentinels, and both are *decisions*: the aggregate refusing the transition for a non-`pending` job, and a claim affecting no row. A transport failure is neither.
- **`ProcessVideoJob.Execute` propagates the claim step's error at exactly one site.** `internal/video/application/process_video_job.go:266-270`. There is one place to mark the pre-claim region, and it is three lines long.
- **`FindByID` can fail permanently on a healthy database.** `scanJobRow` (`internal/video/infrastructure/postgres/repository.go`) parses the id, the user id, the filename, both storage keys and then calls `RestoreVideoJob`. A stored status outside the closed set, or a legacy row the aggregate refuses, yields an error that is neither `ErrVideoJobNotFound` nor `ErrJobClaimLost`. **This is the fact that kills the obvious rule** — see decision 2.
- **`sql.ErrNoRows` is mapped to `ErrVideoJobNotFound` before anything else happens** (`scanJob`), so a not-found row can never be confused with an unreachable database.
- **The cache decorator passes `ClaimForProcessing`'s error through verbatim** after invalidating (`internal/video/infrastructure/cache/repository.go:415-428`), and `StartProcessing` reads through the *undecorated* repository by construction. Neither hides a sentinel today.
- **`cmd/notifier` already has the three-valued shape, and its `Requeue` pauses.** `internal/notification/infrastructure/messaging/consumer.go:52-63` (`DefaultRequeuePause = 5 * time.Second`), `:297-315` (nack, then `sleepCtx(ctx, c.requeuePause)` — after the nack and before returning to the select, "what makes it a pause before the *next* delivery"). The constructor takes the pause as a parameter deliberately, because "a `Requeue`'s observable behaviour" is otherwise untestable.
- **There are three worker replicas** (`docker-compose.yml:400`, `deploy.replicas: 3`) and prefetch is 1. Whatever pacing this change chooses is per consumer, and the deployment-wide rate is three times it.

One more fact frames every rejection below: **the work queue is a classic durable queue** (`internal/platform/rabbitmq/topology.go:117-121`, `QueueDeclare` with `x-max-length`, `x-overflow` and `x-dead-letter-exchange`, and no `x-queue-type`). Classic queues expose a `redelivered` *flag* and no count. `x-delivery-limit` is a quorum-queue feature.

## Goals / Non-Goals

**Goals:**

- A rule that separates "never" from "not now" on a fact the handler can establish structurally — a sentinel, never an error type or an error's text — with the one judgement that does require looking at an error made once, in the adapter that produced it.
- A rule whose *default* is today's behaviour, so an unclassified failure mode behaves exactly as it does now and only a positively-identified condition retries.
- The two consumers in this system holding one reconcilable rule, with the place they still differ named and justified.
- Pacing whose arithmetic across replicas is written down, and whose interaction with shutdown is stated.
- Every rejected option rejected for a reason that would survive someone proposing it again.

**Non-Goals:**

- Recovering a job stranded in `queued` *by some other mechanism* — the sweeper, an age heuristic, a reconciliation job. Decision 6 rejects these; this change prevents the stranding instead.
- Making a transient MinIO outage retryable. Structurally impossible at this layer (decision 8) and a `videojob-lifecycle` change.
- A delivery count, a quorum-queue migration, a delayed-retry queue, or a generation bump of any kind.
- Any change to the four existing sentinels, to the fence, to the lease, or to the sweeper's own behaviour.
- A `Requeue` anywhere in the *post*-claim region, including the `!result.Success` branch and the `completeWithRetry` failure branch.

## Decisions

### 1. The boundary is the claim, and it is the only boundary that is both sharp and free

The handler already knows, structurally, whether `ProcessVideoJob` got past `StartProcessing` — it is the first thing `Execute` does, and its failure returns before anything else can happen. That boundary has a property no error classification has: **on the near side the set of possible states is closed, and every member of it already has an owner.**

`ClaimForProcessing` is a single autocommit `UPDATE ... RETURNING` whose result is read with `QueryRowContext(...).Scan(&epoch)` over the connection that carried it (`internal/video/infrastructure/postgres/repository.go:636-652`). There is no client-visible boundary between the commit and the delivery of the result, so a failure the caller sees admits exactly two states and no third:

- **The claim did not commit.** The row is `queued`, no lease exists, no source object has been read, no `ffmpeg` has run and no outbox row has been written. A redelivery is not a *retry* — it is the original delivery, arriving later, and the very same conditional claim will decide it.
- **The claim committed and its result was lost** — a connection dropped between `COMMIT` and the client reading the returned epoch. The row is `processing` and holds **no lease**, because `ProcessVideoJob` acquires the lease only at the moment `StartProcessing` reports a *won* claim and here it reported an error. That is exactly `videojob-lease-recovery`'s definition of an abandoned job: the redelivery loses the claim and dead-letters under the existing rule, two unleased observations at the same epoch requeue the row at a fresh epoch, and the ordinary path resumes.

Two outcomes, two mechanisms that already exist, and the disposition is correct under both. **That is what makes the boundary sharp — not that nothing happened, but that everything that could have happened is already owned.** An earlier draft of this document argued the near side was "bit-for-bit unchanged". It is not, the source disproves it, and the argument never needed it.

On the far side, every one of those statements is false, and the existing rule is correct for exactly that reason: the row is `processing`, the claim predicate admits `queued` alone, and a redelivery can only lose the claim and be dead-lettered — which `videojob-lease-recovery` already states as its reason for not building recovery on broker redelivery.

So the new disposition applies to one condition: **the claim step could not learn whether the claim was won**, because the persistence layer could not answer. Not "an error occurred before the claim" — decision 2 explains why that is not the same sentence, and not "the claim provably did not happen", which decision 1 has just shown the statement cannot provide.

*Alternative considered — the notifier's rule verbatim, "has this handler attempted anything yet".* It is the same rule at a different resolution, and the worker's version is sharper because the worker has an atomic, persisted boundary the notifier does not: the notifier's "attempt" is an outbound HTTP or SMTP request whose having-happened is not recorded anywhere the handler can consult, which is why it has to reason about it by enumerating error positions. The worker's boundary is a row in a table. Adopting the notifier's phrasing would import the weaker construct along with the right conclusion.

### 2. The rule is "the server could not answer", classified by the error's own evidence — **not** "an error occurred before the claim", and **not** "which driver call failed"

This is the decision the change turns on, and the obvious formulation is wrong.

`FindByID` runs inside the claim step, and it can fail *permanently on a healthy database*: `scanJobRow` parses five stored values and then calls `RestoreVideoJob`, so a row carrying a status outside the closed set, or a legacy row the aggregate refuses to reconstruct, returns an error that is neither `ErrVideoJobNotFound` nor `ErrJobClaimLost`. Under "requeue anything that fails before the claim", one such row requeues forever at the pause interval — and because prefetch is 1 and a nacked message returns toward the *front* of the queue, it would block **all three replicas** against every healthy job behind it. That is strictly worse than today, where the row is silently dead-lettered.

It is also the case that destroys the head-of-line argument. Blocking the head is free *only* because the requeue path is entered only when the shared dependency every message needs is unavailable, so there is no work to do instead. One permanently-unreadable row breaks that premise completely.

**And the same fact kills the second-most-obvious formulation, which an earlier draft of this document chose: "wrap whatever `Scan`, `Query` or `Exec` returned".** That is a *call-site* classification, not an availability one. Every one of those calls returns permanent and transient failures through the same return value. `row.Scan` yields a lost connection and a `database/sql` conversion failure alike — and a destination count that does not match the `SELECT` list, which is reachable in this tree by exactly the mistake `scanJobRow`'s own comment warns about: add a column to the `SELECT` list without adding it to the `Scan` and every row on a perfectly healthy database returns `sql: expected N destination arguments in Scan, not M`. `Query` and `Exec` yield a lost connection and a server-answered refusal alike — `42P01` undefined table, `42703` undefined column, `42501` insufficient privilege — the first two of which are what a half-applied migration produces, marking *every* call unavailable at once and spinning all three replicas forever.

So the sentinel is raised by the adapter that made the call, and the adapter classifies **the error**, never the call site:

- An error is marked `domain.ErrRepositoryUnavailable` when its own evidence establishes that **the server did not, or could not, answer this statement**: the connection could not be established, was lost while the statement was in flight, or was already closed; or the server itself replied that it cannot serve right now.
- It is **not** marked when the server answered and *refused*. Those are permanent on a running database and no retry changes them.
- It is **not** marked when `database/sql` itself failed interpreting a row it *received* — a value conversion, or a destination count mismatch.
- The value-object and `RestoreVideoJob` failures *below* that `Scan` are not marked either, for the original reason: the row was read.
- `ClaimForProcessing`'s `!claimed` answer is a decision and stays `ErrJobClaimLost`; `sql.ErrNoRows` is still mapped to `ErrVideoJobNotFound` before anything is marked.

**A permission list, not a deny-list** — the same shape `notification-webhook-delivery`'s destination policy already uses in this system, and for the same reason: an error carrying none of the enumerated evidence is refused the marker and dead-letters, so a driver error nobody has seen yet cannot silently widen the retryable set.

The candidate evidence, to be *confirmed* against the pinned `github.com/jackc/pgx/v5 v5.10.0` and against `database/sql`'s own pooling at implementation time rather than pinned normatively in the spec: `*pgconn.ConnectError` (the connection attempt failed — and note it does **not** satisfy `pgconn.SafeToRetry`, which has no method on it and unwraps to a plain net error, so that helper is not the classifier), `pgconn.ErrConnClosed`, `driver.ErrBadConn`, `sql.ErrConnDone`, any `net.Error` reached by `errors.As`, `io.EOF`/`io.ErrUnexpectedEOF`, and a `*pgconn.PgError` whose `SQLState()` falls in class `08` (connection exception), `53` (insufficient resources), `57` (operator intervention — `57P01 admin_shutdown`, `57P03 cannot_connect_now`) or `58` (system error). Every other SQLSTATE a `*pgconn.PgError` can carry is the server answering and refusing.

**Note what decision 1's closed disjunction buys here, because it is what makes this classification tractable rather than a second hazard.** The classifier does **not** have to prove the statement never reached the server. Several of the entries above are ambiguous about that by construction — a connection lost in flight, a `57014 query_canceled` — and under a "the state is unchanged" argument each would have to be excluded, or handled specially, or defended. Under the two-outcome argument each is simply safe, because both outcomes are owned. Conceding that the state is not guaranteed unchanged is what lets the permission list be drawn on availability rather than on provable non-delivery.

**The default is today's behaviour.** An error out of the claim step carrying none of that evidence dead-letters, exactly as now. Only a positively-identified unavailable server retries. A failure mode nobody has thought of behaves as it does today rather than as a new spin loop — which is the property that makes this change safe to ship without enumerating every error the driver can produce.

*Alternative considered — mark the permanent errors instead and let everything else retry.* The inverse assignment: the adapter raises `ErrJobUnreadable` for a row it could not interpret, and the handler requeues anything else. Symmetric on paper, and rejected because its default is the dangerous one. An unclassified new failure would spin and block three replicas, and the argument for it — "retrying is the safe default before the claim" — is only true for failures that are transient, which is the thing being classified.

*Alternative considered — classify at the application layer with `errors.As` against driver error types.* Classifying by the error's own evidence is exactly right; doing it in `internal/video/application` is not. It puts knowledge of a database driver in a layer `ddd-architecture` forbids it in, and it duplicates a judgement the adapter is in the best position to make — the adapter chose the driver, imports it already, and is the only file that has to change when it is swapped. The classification therefore stays inside `internal/video/infrastructure/postgres`, in one unexported helper; the application layer sees one domain sentinel and never an error type. This is not the rejected option arriving by the back door: what was rejected is the *location*, not the technique.

### 3. `ProcessVideoJob` converts that error into a statement about control flow, at one site

`domain.ErrRepositoryUnavailable` alone is insufficient at the handler, and the reason is exact: `failWith`'s own `FailJob` write can also fail with it, *after* the claim, and reach the same default branch. Requeueing there would redeliver a `processing` row into `ErrJobClaimLost` and a dead-letter — harmless, because the sweeper still owns that row, but it is one wasted pass per replica and precisely the loop this design is trying not to build.

So at `process_video_job.go:266-270`, the single place `Execute` propagates the claim step's error, a `domain.ErrRepositoryUnavailable` becomes `domain.ErrJobClaimOutcomeUnknown` (wrapping the original, so both remain reachable by `errors.Is`). The name is a sibling to `ErrJobClaimLost` and says the thing that matters: `ErrJobClaimLost` means **another actor owns this job**; `ErrJobClaimOutcomeUnknown` means **this worker cannot say who owns it, including whether it does itself** — and, either way, has run no extraction, acquired no lease, read no source object and written no event.

It is named for the *outcome* rather than the claim deliberately. "Undecided", the earlier name, asserts that the transition did not happen, which decision 1 shows the statement cannot guarantee; what is missing is this caller's knowledge, and the disposition is licensed by what the caller did not do rather than by what the row does not say.

The handler then branches on `ErrJobClaimOutcomeUnknown` and on nothing else. It never sees `ErrRepositoryUnavailable` directly, and post-claim occurrences of it keep falling to the default branch and dead-lettering, where the sweeper recovers them.

*Alternative considered — a `Claimed bool` on `ProcessVideoJobResult`.* The error paths return a zero-valued result today (`return ProcessVideoJobResult{}, err`), so this means populating the result on failure paths that currently do not, in a struct whose fields are documented as meaningful only under specific conditions. A sentinel composes with `errors.Is` the way every other condition in this pipeline already does.

### 4. A third `Disposition`, named and shaped like the notifier's

`videomessaging.Disposition` gains `Requeue`: nack with requeue, then pause before taking the next delivery. Three values, in the same order and with the same meanings as `notificationmessaging.Disposition`.

The symmetry is the point, not a coincidence to be tidied later. Two consumers on one broker held two different rules for one class of failure, and nothing said so. After this change they hold one rule — *a handler that has changed nothing may ask for the message again* — expressed in two type declarations that cannot be shared, because `ddd-architecture` forbids the Notification context from importing the Video Processing one. The place they still differ is stated rather than smoothed over: the notifier requeues a `ClaimHeldByAnother` and the worker never requeues a lost claim, because the notifier's claim expires under a reclaim bound and can be picked up again, while the worker's claim is won by a conditional transition that no later delivery can re-open.

The existing `Reject` doc comment keeps its text; a sentence is added distinguishing the condition it covers from the one `Requeue` covers, so the two are read together.

### 5. Pacing: five seconds, per consumer, on the cancellable context — and the arithmetic is stated

`Nack(requeue=true)` on a classic queue returns the message toward the front, and the broker offers it to the next available consumer immediately. Without a pause the worker takes it straight back and spins at the speed of a failed connection attempt.

The consumer therefore pauses after the nack and before returning to its select loop, matching `notification/.../consumer.go:297-315` line for line, including that the pause uses `sleepCtx(ctx, …)` on the **consumer's cancellable context** rather than the handler's detached one. Two consequences, both wanted: shutdown skips the pause, and the pause cannot eat into `cmd/worker`'s five-minute drain — by the time it runs the handler has already returned, and nothing is in flight to drain.

The value is `5s`, the notifier's `DefaultRequeuePause`, passed to `NewConsumer` by the composition root for the reason the notifier's constructor takes it: a constant read from inside the package can only be exercised at its production value.

**The arithmetic, because a reviewer should not have to derive it.** The pause bounds *each consumer's* rate, not the message's. A consumer sitting in its post-nack pause holds nothing, so the other two replicas are free to take the requeued message at once. At three replicas the effective deployment-wide retry interval is roughly `pause / 3` — about 1.7 seconds, some 35 redeliveries a minute — not one every five seconds. That is the number to check against, and it is acceptable: each pass costs one failed connection attempt against a database that is down, and when the database returns the first pass after that succeeds, so recovery latency is at most one pause.

*Alternative considered — a longer, worker-specific pause.* The notifier's 5s is sized against its reclaim bound, because a held claim blocks a real delivery; the worker has no equivalent urgency, since the job is `queued` and nothing is racing it. A longer pause would be defensible. It is rejected because the symmetry is worth more than the tuning: two consumers, one constant, one reason, and the recovery latency after the outage ends is bounded by the pause either way.

*Alternative considered — exponential backoff on the pause.* Requires per-message state, and there is none: the consumer holds no map, a requeued message can move between replicas on every pass, and a classic queue carries no counter to hang the state on. A per-consumer backoff would grow on unrelated messages; a per-message one cannot be built without the broker's help. A fixed pause with published arithmetic is honest; a backoff that silently keys on the wrong thing is not.

### 6. Recovery stays where it is: the sweeper does **not** learn to scan `queued`

The tempting alternative is to leave the disposition alone and teach the recovery side to reach a `queued` row. It is rejected, and the reason is not squeamishness about scope.

**The sweeper's authority comes from the lease, and a `queued` job holds no lease by design.** That is what makes a `processing` row without one *evidence* of abandonment. A `queued` row has no such signal, because `queued` is the ordinary state of a job waiting for a free worker: there is no observable difference between "queued, dispatch en route, worker busy" and "queued, dispatch dead-lettered, nothing coming". The sweeper would have to act on an *age heuristic*, and the work queue is bounded at 10 000 with `reject-publish`, so a backlog deep enough to make a job old is an expected operating condition rather than an anomaly.

It is worse than merely ambiguous. Re-dispatching means writing a **new** outbox row — the original is already stamped `published_at` — so the sweeper would be manufacturing duplicate dispatches for healthy jobs during exactly the backlog that made them look old. The conditional claim absorbs duplicates correctly, but paying for a permanent duplicate-dispatch source to cover a failure the disposition can prevent outright is the wrong trade.

The existing scenario *"Jobs stranded in queued are not the sweeper's concern"* is therefore kept, not deleted. What narrows is the clause inside it that says such a job's dispatch "was never published **or was dead-lettered**": after this change a dead-lettered dispatch is no longer one of the ways a job reaches that state through a dependency outage, and the delta says so.

*Alternative considered — a separate reconciliation job over `queued` rows older than N.* The same age heuristic in a different process, with the added cost of a second recovery mechanism whose interaction with the sweeper nobody has reasoned about.

### 7. A documented dead-letter replay procedure is worth writing and is **not** the fix

The cheapest-looking option is to leave everything alone and write a runbook. It is rejected as the fix by two numbers already in the tree: `deadLetterTTL = 24h` and `deadLetterMaxLength = 10000` with `x-overflow: drop-head`. The dead-letter queue is not an archive — it is a 24-hour, 10 000-message, shared, unversioned buffer that both topologies and every generation write into. The dead-lettered dispatch — the only remaining artefact naming a `queued` job that still needs dispatching, the row and its source object and its stamped outbox row all being durable and intact — can expire, or be pushed out by newer messages, before anyone reads the runbook. A procedure that requires a human to notice within a window the system itself may close is not a recovery guarantee.

It is also, separately, worth having. The dead-letter queue still receives the four permanent sentinels, and an operator who finds messages there today has no documented procedure at all. This change adds a short one to `docs/operations.md` at finalization, framed as what it is: a way to inspect and, where appropriate, replay *permanently* dead-lettered messages — not the answer to a dependency outage, which after this change produces none.

### 8. The MinIO half is out of scope because it is unfixable at this layer, not because it is small

By the time `handle` reads `!result.Success`, `failWith` has already committed the job `failed` and returned. A `Requeue` there would be redelivered into a `ClaimForProcessing` that admits `queued` alone, refused as `ErrJobClaimLost`, and dead-lettered. **There is no disposition that makes a transient storage failure retryable**, at any pause, with any sentinel.

Fixing it means a storage failure must not commit a terminal state at all — which means `ProcessVideoJob` returning the claimed job to `queued`, a worker-driven `processing → queued` edge that today only the sweeper may write, fenced by the same epoch, interacting with the lease it must then release and with the `Applied` gate that licenses every cleanup. That is a `videojob-lifecycle` and `videojob-execution` change of its own size, and folding it in here would make one change carry two unrelated correctness arguments.

The existing mitigation is real and was verified rather than assumed: a committed failure clears the job's idempotency key (`cmd/worker/main.go:445-451`, gated on `result.Applied`), so the user's re-upload of the same bytes is not deduplicated onto the failed job. The outcome is recorded, visible through `GET /api/video-jobs/:id`, and recoverable by the user without an operator. A backlog row is proposed at finalization so the finding is not lost.

### 9. A bounded redelivery count is rejected, and the topology is the reason

Three ways to bound the retry, all rejected:

- **`x-delivery-limit`.** A quorum-queue feature. `video.jobs.queued.v2` is a classic queue, and RabbitMQ refuses to redeclare a queue whose type differs — so adopting it means a new queue name, which means a `.v3` generation bump, which means the rolling-deploy reasoning in `videojob-messaging` all over again. And what the limit buys is that the message is dead-lettered after N passes, which is the outcome this change exists to stop.
- **An in-memory counter in the worker.** Worthless across three replicas and a restart: the message moves between consumers on every pass, so each one counts to one.
- **A counter in a message header, incremented on republish.** Requires publishing a modified copy rather than nacking, which means the worker becomes a publisher on the dispatch exchange — a second producer for a stream `videojob-outbox-relay` owns exclusively, and an at-least-once duplicate on every crash between nack and publish.

The pacing in decision 5 is the bound, and it bounds the right quantity. The retry is unbounded in *count* and that is correct: the message is the only record that a `queued` job needs dispatching, and discarding it is the defect. As long as it is cheap, putting it back is right for as long as the condition lasts.

## Risks / Trade-offs

- **The ambiguous commit is designed *for*, not merely survived.** `ClaimForProcessing` can commit in PostgreSQL and then fail to report it — a connection dropped between `COMMIT` and the client reading the returned epoch. The handler then knows only that the outcome is unknown, requeues, and the redelivery finds the row `processing`, loses the claim, and is dead-lettered. **That is the mode the sweeper already recovers**: `processing` with no lease, two confirmations, a requeue at a fresh epoch. Decision 1 is built on this branch rather than around it, which is why the sentinel names an unknown *outcome* and why no artifact in this change asserts the row is untouched. Cost: one wasted pass, one dead-letter, and one of three requeues spent.
- **A database that flaps can convert a job into a committed `failed`.** Each pass through the committed branch spends one of the sweeper's bounded requeues, so a server available enough to commit and unavailable enough to lose the result can, on one job, reach `maxRequeues` and have the sweeper commit `failed`. Accepted, and in the right direction: a `failed` job is visible through `GET /api/video-jobs/:id`, its idempotency key is cleared, and the user's re-upload of the same bytes runs again. That is strictly better than the `queued` row nothing can reach, which is the outcome this change exists to remove. Named here because it is an outcome the change *introduces*, not one it inherits.
- **A `Requeue` that the broker rejects.** `Nack` can itself fail on a closing channel. The notifier logs and moves on; this consumer does the same. The message is then unacknowledged on a dying channel and the broker requeues it when the connection closes — which is the intended outcome reached by a different route.
- **A reviewer reading the lease-recovery clause as contradicted.** *"Recovery SHALL NOT be built on broker redelivery"* remains true and is about a different subject: a job already `processing`, whose redelivery cannot be claimed. This change redelivers a dispatch that was **never acted on**, for a row still `queued`. The delta states the distinction in the requirement itself rather than leaving it to be inferred, because the sentence is otherwise the first thing someone will quote against this design.
- **The one-line change nobody will notice is wrong.** Wrapping the driver error must happen *after* `sql.ErrNoRows` is mapped to `ErrVideoJobNotFound`, or a not-found job requeues forever instead of dead-lettering. The mapping is in `scanJob` and the wrap belongs in `scanJobRow`'s `Scan` branch, which already special-cases `ErrNoRows` and returns it unwrapped. A test that only stops the database passes without ever touching this ordering.
