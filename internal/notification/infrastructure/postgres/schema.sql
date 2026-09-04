CREATE TABLE IF NOT EXISTS notification_preferences (
    user_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    channel TEXT NOT NULL,
    enabled BOOLEAN NOT NULL,
    destination TEXT NOT NULL,
    -- No DEFAULT '' on purpose. The constraint is what makes "a stored
    -- preference always carries a usable secret" true of the table rather
    -- than of the one package that writes it, so it survives a second
    -- writer or a manual INSERT during an incident. A default would let an
    -- INSERT that names no secret succeed and swallow exactly that.
    --
    -- No adapter path depends on catching a violation: the insert statement
    -- always carries a non-empty secret and the update statement never names
    -- this column, so a violation would be a genuine bug and a 500 is the
    -- right outcome for one.
    secret TEXT NOT NULL CONSTRAINT notification_preferences_secret_not_empty CHECK (secret <> ''),
    -- TIMESTAMPTZ is microsecond-resolution, one order of magnitude coarser
    -- than Go's time.Time, so a timestamp with a sub-microsecond component
    -- does not round-trip exactly through these columns — the same latent
    -- constraint identity's and video's created_at columns already carry.
    created_at TIMESTAMPTZ NOT NULL,
    updated_at TIMESTAMPTZ NOT NULL,
    -- The triple is the whole identity of a preference; there is no
    -- surrogate id, because nothing references one by an id. Declaring it as
    -- the primary key both enforces the uniqueness the consumer depends on
    -- and gives the upsert its conflict target for free.
    PRIMARY KEY (user_id, event_type, channel)
);
