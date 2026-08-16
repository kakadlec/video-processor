CREATE TABLE IF NOT EXISTS video_jobs (
    id UUID PRIMARY KEY,
    user_id TEXT NOT NULL,
    original_filename TEXT NOT NULL,
    status TEXT NOT NULL,
    frame_count INTEGER NOT NULL DEFAULT 0,
    error_reason TEXT NOT NULL DEFAULT '',
    storage_key TEXT NOT NULL DEFAULT '',
    created_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX IF NOT EXISTS video_jobs_user_id_created_at_id_idx
    ON video_jobs (user_id, created_at DESC, id ASC);

-- Transactional outbox: Repository.Create writes a row here in the same
-- transaction as the video_jobs insert, so a future publisher (Phase 6)
-- can relay it without ever observing a job without its creation event or
-- vice versa. published_at stays NULL until that relay exists.
CREATE TABLE IF NOT EXISTS video_job_outbox (
    id UUID PRIMARY KEY,
    event_type TEXT NOT NULL,
    payload JSONB NOT NULL,
    occurred_at TIMESTAMPTZ NOT NULL,
    published_at TIMESTAMPTZ
);
