## MODIFIED Requirements

### Requirement: Pre-Existing Unpublished Rows Are Bounded by an Explicit Cutoff

Unpublished `video_job.queued` rows that pre-date the switch to the current job-dispatch generation SHALL be excluded from dispatch by an explicit cutoff applied before the relay first publishes into that generation, and SHALL NOT be delivered to a worker. The cutoff SHALL be expressed against `video_job_outbox.occurred_at` and SHALL be applied by a schema migration that stamps those rows `published_at`, so exclusion is a recorded fact about each row rather than a predicate a future reader has to remember.

The rows in question are not merely stale, they are undeliverable. Each was written by a `POST /upload` request that went on to process the job in-request, drive it to `completed` or `failed`, and delete its source object; dispatching one would hand a worker a job whose input no longer exists.

The conditional claim in `videojob-lifecycle` would refuse most of them anyway, and that SHALL NOT be relied upon as the boundary. It is an accidental consequence of a predicate written for a different purpose, it does not cover a row whose job is still `queued` (a request that crashed between the enqueue and the processing), and it would fill the dead-letter queue with a batch of expected garbage, degrading the one place operators look for anomalies.

A later change that switches the job-dispatch generation again SHALL apply the same treatment to rows unpublished at that point, and SHALL modify this requirement rather than leave it describing only the first cutover.

#### Scenario: A stale unpublished row is not delivered as a live dispatch

- **GIVEN** an unpublished `video_job.queued` row written before the cutover, naming a job already `completed` with its source object deleted
- **WHEN** the migration has run and the relay polls for unpublished rows
- **THEN** the row is not claimed, because it carries a `published_at` stamp, and no message naming that job is published to the current generation

#### Scenario: The cutoff does not stamp rows written after it

- **GIVEN** the migration has run
- **WHEN** a new `POST /upload` request enqueues a job, writing a fresh `video_job.queued` outbox row
- **THEN** that row is unpublished, the relay claims and publishes it, and the worker receives it

#### Scenario: The dead-letter queue is not used as the cutoff

- **WHEN** the deployment completes and the worker has been consuming for a full poll cycle
- **THEN** no dead-lettered message corresponds to a pre-cutover outbox row, because none was ever published
