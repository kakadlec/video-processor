## MODIFIED Requirements

### Requirement: Pre-Existing Unpublished Rows Are Bounded by an Explicit Cutoff

Unpublished outbox rows that pre-date the switch to the current job-dispatch generation SHALL be excluded from dispatch by an explicit cutoff applied before the relay first publishes into that generation, and SHALL NOT be delivered to a worker.

The cutoff SHALL be structural: because a generation bump changes the `event_type` string (see `videojob-messaging`) and the claim filters on it, the current generation's relay cannot match a row written by the previous one. That is what makes the exclusion airtight rather than best-effort — it holds for rows a not-yet-redeployed replica writes *after* the switch as well as before it, which no timestamp could.

It SHALL additionally be recorded: a schema migration SHALL stamp `published_at` on the previous generation's rows that are still unpublished, so exclusion is a fact about each row rather than an inference a future reader has to reconstruct from two string literals. `video_job_outbox.occurred_at` bounds which rows the migration touches. The migration is the record; the event-type scoping is the mechanism, and this requirement asks for both.

The rows in question are not merely stale, they are undeliverable. Each was written by a `POST /upload` request that went on to process the job in-request, drive it to `completed` or `failed`, and delete its source object; dispatching one would hand a worker a job whose input no longer exists.

The conditional claim in `videojob-lifecycle` would refuse most of them anyway, and that SHALL NOT be relied upon as the boundary. It is an accidental consequence of a predicate written for a different purpose, it does not cover a row whose job is still `queued` (a request that crashed between the enqueue and the processing), and it would fill the dead-letter queue with a batch of expected garbage, degrading the one place operators look for anomalies.

A later change that switches the job-dispatch generation again SHALL apply the same treatment to rows unpublished at that point, and SHALL modify this requirement rather than leave it describing only the first cutover.

#### Scenario: A stale unpublished row is not delivered as a live dispatch

- **GIVEN** an unpublished outbox row written before the cutover, naming a job already `completed` with its source object deleted
- **WHEN** the migration has run and the relay polls for unpublished rows
- **THEN** the row is not claimed — its `event_type` does not match the current generation's, and it carries a `published_at` stamp besides — and no message naming that job is published to the current generation

#### Scenario: A previous generation's row written after the migration is still excluded

- **GIVEN** a not-yet-redeployed replica that enqueues a job after the migration has already run, writing an unstamped row under the previous generation's `event_type`
- **WHEN** the current generation's relay polls
- **THEN** it does not claim that row, demonstrating that the exclusion is not a timestamp boundary a late writer could fall outside of

#### Scenario: The cutoff does not stamp rows written after it

- **GIVEN** the migration has run
- **WHEN** a new `POST /upload` request enqueues a job, writing a fresh outbox row under the current generation's `event_type`
- **THEN** that row is unpublished, the relay claims and publishes it, and the worker receives it

#### Scenario: The dead-letter queue is not used as the cutoff

- **WHEN** the deployment completes and the worker has been consuming for a full poll cycle
- **THEN** no dead-lettered message corresponds to a pre-cutover outbox row, because none was ever published

### Requirement: The Claim Is Scoped to One Event Type

The relay's claim SHALL filter on the `event_type` of the job-dispatch generation it publishes into, and a supporting index SHALL exist for that predicate.

This is not an optimization, and it now serves two purposes rather than one.

It keeps internal events off the job queue. `video_job_outbox` has accumulated an unpublished `video_job.created` row for every job created since Phase 3, and those rows are internal events that must never reach the job queue. An unfiltered claim would re-read that backlog on every poll and, with a bounded batch, could starve the rows the relay exists to deliver — publishing nothing while appearing to work.

**It is also what isolates dispatch generations, and that is load-bearing during a rolling deploy.** Every replica's relay reads the same `video_job_outbox` table, so a filter shared across generations would let a new replica's relay claim an old replica's row and publish it into the new generation, and an old replica's relay claim a new replica's row and publish it into the old one — where nothing consumes it and the job waits in `queued` forever. Isolating at the exchange cannot help, because the crossing happens before anything is published, and an already-deployed relay cannot be given a new predicate. The current generation's `event_type` SHALL therefore differ from every previous generation's, and a relay SHALL NOT be given a predicate that matches more than its own.

The event-type string SHALL be a single constant shared between the insert, the claim, and the routing key, so the writer, the reader, and the broker cannot drift apart into a relay that silently matches nothing.

#### Scenario: Creation events are never dispatched

- **GIVEN** unpublished `video_job.created` rows in the outbox
- **WHEN** the relay runs
- **THEN** none of them is published, and none of their `published_at` values changes

#### Scenario: A backlog of other event types does not starve dispatch

- **GIVEN** more unpublished `video_job.created` rows than the relay's batch size, and one unpublished dispatch row older or newer than them
- **WHEN** the relay runs
- **THEN** the dispatch row is published

#### Scenario: A relay never claims another generation's dispatch row

- **GIVEN** unpublished dispatch rows written under a previous generation's `event_type`
- **WHEN** the current generation's relay runs
- **THEN** none of them is claimed or published, and none of their `published_at` values changes as a result of that relay
