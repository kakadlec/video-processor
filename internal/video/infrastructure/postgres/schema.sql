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
    -- PostgreSQL's TIMESTAMPTZ has microsecond resolution, one order of
    -- magnitude coarser than Go's time.Time (nanosecond). A CreatedAt with a
    -- non-zero sub-microsecond component will not round-trip exactly through
    -- this column — the same latent constraint identity's created_at column
    -- already has. No code currently depends on sub-microsecond CreatedAt
    -- equality; whichever future change wires a real (non-fake) Clock for
    -- this context should truncate to microsecond precision at that source,
    -- not here, so the in-memory aggregate and the persisted row agree from
    -- the moment the timestamp is minted.
    created_at TIMESTAMPTZ NOT NULL
);

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

CREATE INDEX IF NOT EXISTS video_jobs_user_id_created_at_id_idx
    ON video_jobs (user_id, created_at DESC, id ASC);

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
    occurred_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ
);

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
