CREATE TABLE IF NOT EXISTS video_jobs (
    id UUID PRIMARY KEY,
    user_id TEXT NOT NULL,
    original_filename TEXT NOT NULL,
    status TEXT NOT NULL,
    frame_count INTEGER NOT NULL DEFAULT 0,
    error_reason TEXT NOT NULL DEFAULT '',
    storage_key TEXT NOT NULL DEFAULT '',
    source_key TEXT NOT NULL DEFAULT '',
    content_hash TEXT NOT NULL DEFAULT '',
    lease_epoch BIGINT NOT NULL DEFAULT 0,
    -- Minted by PostgreSQL, not by the application: Repository.Create no
    -- longer accepts a caller-supplied instant (see
    -- docs/roadmap.md's mint-videojob-timestamps-in-database entry), which
    -- is also what retires the earlier microsecond-truncation concern this
    -- comment used to raise — nothing outside this database ever proposes a
    -- value for the column to round-trip. The DEFAULT is a safety net rather
    -- than the mechanism in ordinary use: every INSERT this package issues
    -- names created_at explicitly, reading it back from the same
    -- transaction's own now() (see transactionNow in repository.go) so the
    -- row and its video_job.created outbox payload agree bit for bit. Only a
    -- statement outside this package's control — a hand-run INSERT, a future
    -- caller that forgets the column — would ever fall back to it.
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- CREATE TABLE IF NOT EXISTS above is a no-op against a database that
-- already has this table, so it never applies a DEFAULT added after the
-- table's first creation. This ALTER is what reaches such a database; it is
-- idempotent (setting an identical default twice is a no-op) and touches no
-- existing row, since a DEFAULT only ever applies to a future INSERT that
-- omits the column.
ALTER TABLE video_jobs ALTER COLUMN created_at SET DEFAULT now();

-- source_key is also declared above, for a database created from scratch.
-- This ALTER is what reaches a database that already exists, where the
-- CREATE TABLE IF NOT EXISTS above is a no-op and would otherwise leave the
-- column missing. Purely additive: no backfill is possible, because the key
-- embeds a generated uploadID that exists in no other column, so every
-- pre-existing row keeps the empty default. VideoJob tolerates that by
-- design — see RestoreVideoJob's comment on why source_key is not paired
-- with status. Column order differs between the two paths (appended here,
-- inline there); every query in this package names its columns explicitly,
-- so nothing depends on the ordinal.
ALTER TABLE video_jobs ADD COLUMN IF NOT EXISTS source_key TEXT NOT NULL DEFAULT '';

-- content_hash follows source_key's pattern exactly, and for the same
-- reasons: declared inline above for a fresh database, added here for one
-- that already exists, purely additive, no backfill. The value is the
-- SHA-256 of the uploaded bytes, which only the request that streamed them
-- ever saw, so no pre-existing row can be reconstructed and every one keeps
-- the empty default. It is persisted so the worker can rebuild the
-- IdempotencyKey the submitting request derived, having only the job.
ALTER TABLE video_jobs ADD COLUMN IF NOT EXISTS content_hash TEXT NOT NULL DEFAULT '';

-- lease_epoch follows the same two-path pattern: declared inline above for a
-- fresh database, added here for one that already exists, purely additive.
--
-- 0 is the correct value for a pre-existing row rather than a placeholder
-- standing in for an unknown one. The epoch counts how many times a job has
-- been requeued after being abandoned, and a row written before recovery
-- existed has been abandoned exactly zero times. Every such row therefore
-- enters the fence at the same epoch the first claim of a fresh job holds,
-- which is what lets the first sweep recover the jobs stranded in processing
-- by earlier builds.
ALTER TABLE video_jobs ADD COLUMN IF NOT EXISTS lease_epoch BIGINT NOT NULL DEFAULT 0;

CREATE INDEX IF NOT EXISTS video_jobs_user_id_created_at_id_idx
    ON video_jobs (user_id, created_at DESC, id ASC);

-- The recovery sweep's scan: every processing job, ordered by id, resumed
-- from a keyset cursor. Partial for the same reason the outbox's index is
-- partial, and the stake is the same one. The sweep runs on a timer for the
-- life of the deployment, so an unindexed scan would re-read the entire job
-- history — which only ever grows — every interval, to find the handful of
-- rows that are currently processing. Ordering by id alone matches the
-- cursor, so the index answers the filter, the order, and the resume point.
CREATE INDEX IF NOT EXISTS video_jobs_processing_id_idx
    ON video_jobs (id)
    WHERE status = 'processing';

-- The in-flight aggregates' two lookups, one partial index per state, shaped
-- like the sweep's above and justified by the same sentence: these statements
-- run on every scrape for the life of the deployment, and a scan whose cost
-- grows with total job history is what a partial index over a transient
-- predicate exists to avoid.
--
-- Keyed by created_at rather than by id, because the question is the age of
-- the oldest row and not which rows match. The processing one is therefore
-- NOT redundant with video_jobs_processing_id_idx above: that index is keyed
-- by id to match the sweep's keyset cursor, so it finds the processing rows
-- and cannot order them by created_at — an oldest-age lookup over it reads
-- every matching row instead of one. A processing row consequently carries
-- two partial index entries rather than one, which is the price of two
-- questions no single key order answers.
--
-- The set is the in-flight states and no other. pending is excluded on a
-- stronger footing than cost: a job created through the job-lifecycle API has
-- no processing trigger and stays pending permanently by design, so a count
-- of them climbs monotonically and describes nothing. The terminal states are
-- excluded because the interesting quantity for a state a job enters once is
-- a rate, which is owed by the process that writes the transition.
CREATE INDEX IF NOT EXISTS video_jobs_queued_created_at_idx
    ON video_jobs (created_at)
    WHERE status = 'queued';

CREATE INDEX IF NOT EXISTS video_jobs_processing_created_at_idx
    ON video_jobs (created_at)
    WHERE status = 'processing';

-- Transactional outbox: Repository.Create and Repository.Enqueue each write
-- a row here in the same transaction as their video_jobs write, so a reader
-- can never observe a job without the event describing that write, or vice
-- versa.
--
-- Only video_job.queued rows are relayed. The relay filters on event_type
-- and never publishes video_job.created, so those rows keep published_at
-- NULL permanently and by design — they are a record, not a pending
-- dispatch. Whichever change first publishes a creation event owns
-- deciding what to do with the accumulated backlog.
CREATE TABLE IF NOT EXISTS video_job_outbox (
    id UUID PRIMARY KEY,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    -- Minted by PostgreSQL, like video_jobs.created_at above and for the
    -- same reason: every INSERT this package issues names occurred_at
    -- explicitly, read from the writing transaction's own now() so the
    -- column and the instant embedded in payload agree exactly. The DEFAULT
    -- is the same safety net, not the mechanism in ordinary use.
    occurred_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    published_at TIMESTAMPTZ
);

-- Mirrors the created_at ALTER above, for the same reason: CREATE TABLE IF
-- NOT EXISTS does not apply a DEFAULT added after a table's first creation.
ALTER TABLE video_job_outbox ALTER COLUMN occurred_at SET DEFAULT now();

-- The relay's claim query, ordered by occurred_at within a single
-- event_type. event_type leads on purpose: with occurred_at first it is
-- only a filter, so an idle poll would scan the permanent (never shrinking)
-- video_job.created backlog before concluding there is nothing to dispatch,
-- and that scan grows with every job ever created.
CREATE INDEX IF NOT EXISTS video_job_outbox_unpublished_idx
    ON video_job_outbox (event_type, occurred_at)
    WHERE published_at IS NULL;

-- Generation cutoff. Rows written under the previous dispatch generation's
-- event type can no longer be delivered: nothing declares that generation's
-- exchange or queue any more, so publishing one would either be returned
-- unroutable or land in a topology no consumer reads. Stamping them
-- published_at retires them instead of leaving them to be re-attempted
-- forever.
--
-- The hardcoded literal IS the guard, and it is why this statement is safe
-- to re-execute on every startup (Migrate runs the whole file). It names the
-- previous generation and nothing else, so it can never match a row the
-- current build writes — those carry the current event type. Do not replace
-- it with the videoJobQueuedEventType constant, which moves with each
-- generation and would then stamp live dispatches.
--
-- It can still match a row written after this ran, by a replica of the
-- previous build not yet redeployed. That is harmless and intended: such a
-- row is undeliverable for the same reason as the rest.
UPDATE video_job_outbox
    SET published_at = now()
    WHERE event_type = 'video_job.queued' AND published_at IS NULL;
