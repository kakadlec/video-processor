CREATE TABLE IF NOT EXISTS notification_preferences (
    user_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    channel TEXT NOT NULL,
    enabled BOOLEAN NOT NULL,
    destination TEXT NOT NULL,
    -- No DEFAULT '' on purpose. The constraint below is what makes "a stored
    -- preference on a signing channel always carries a usable secret" true
    -- of the table rather than of the one package that writes it, so it
    -- survives a second writer or a manual INSERT during an incident. A
    -- default would let an INSERT that names no secret succeed and swallow
    -- exactly that: the statement for a non-signing channel therefore names
    -- this column and writes the empty string, deliberately and visibly,
    -- rather than leaving it out.
    --
    -- No adapter path depends on catching a violation: every insert carries
    -- a value the constraint permits for the channel it names, so a
    -- violation would be a genuine bug and a 500 is the right outcome.
    secret TEXT NOT NULL,
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
    PRIMARY KEY (user_id, event_type, channel),
    -- Conditional on the channel, because only a channel whose delivery is
    -- signed needs a secret to sign with. An e-mail preference carries none
    -- and must still be storable; a webhook preference with none describes
    -- an endpoint that could never be signed, which is what this refuses.
    --
    -- The channel is spelled out rather than parameterized because this file
    -- is executed argument-free (see Migrate's comment on the simple query
    -- protocol). TestTheSchemaConstraintNamesTheSigningChannel is what keeps
    -- the literal and domain.ChannelWebhook from drifting apart.
    --
    -- A table constraint rather than a column one: a column constraint that
    -- references another column is accepted by PostgreSQL and silently
    -- recorded as a table constraint anyway, so writing it here says what it
    -- actually is.
    CONSTRAINT notification_preferences_signing_secret_present
        CHECK (channel <> 'webhook' OR secret <> '')
);

-- The constraint above reaches a database created from scratch. This block
-- reaches one that already exists, where the CREATE TABLE IF NOT EXISTS is a
-- no-op and would otherwise leave the previous, unconditional constraint in
-- place — under which no preference on a non-signing channel could ever be
-- stored.
--
-- Guarded on pg_constraint rather than written as an unconditional
-- DROP ... IF EXISTS followed by ADD: ADD CONSTRAINT has no IF NOT EXISTS,
-- so the unguarded form would re-add and therefore re-validate the whole
-- table under an ACCESS EXCLUSIVE lock on every process start, forever. The
-- guard makes both halves no-ops once applied, which is what lets this run
-- on every startup like the rest of the file.
--
-- Purely widening: every row written before it is on the signing channel and
-- carries a secret, so it satisfies the new constraint unchanged and no
-- backfill is possible or needed.
DO $$
BEGIN
    IF EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'notification_preferences'::regclass
           AND conname = 'notification_preferences_secret_not_empty'
    ) THEN
        ALTER TABLE notification_preferences
            DROP CONSTRAINT notification_preferences_secret_not_empty;
    END IF;

    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
         WHERE conrelid = 'notification_preferences'::regclass
           AND conname = 'notification_preferences_signing_secret_present'
    ) THEN
        ALTER TABLE notification_preferences
            ADD CONSTRAINT notification_preferences_signing_secret_present
            CHECK (channel <> 'webhook' OR secret <> '');
    END IF;
END
$$;

CREATE TABLE IF NOT EXISTS notification_deliveries (
    user_id TEXT NOT NULL,
    event_type TEXT NOT NULL,
    channel TEXT NOT NULL,
    job_id TEXT NOT NULL,
    -- Two identifiers rather than one, because they answer different
    -- questions. delivery_id is what the receiver deduplicates on, so a
    -- reclaim preserves it; claim_token names which grant is current and is
    -- reissued on every grant, so a superseded claimant's resolve is fenced
    -- out by it. Collapsing them would let a reclaim look like a second
    -- logical delivery to the receiver.
    delivery_id UUID NOT NULL,
    claim_token UUID NOT NULL,
    status TEXT NOT NULL,
    attempts INT NOT NULL,
    claimed_at TIMESTAMPTZ NOT NULL,
    -- Both nullable, and only for a pending row: a resolved row always
    -- carries a resolution time, and a reason only when it failed.
    resolved_at TIMESTAMPTZ,
    reason TEXT,
    -- No secret, no destination, no request body. The record exists to say
    -- whether this job's notification was delivered on this channel; the
    -- material needed to send it is read from notification_preferences at
    -- delivery time and is not copied here.
    --
    -- The quadruple is the whole identity, as the triple is for a
    -- preference. Declaring it the primary key is what makes "at most one
    -- delivery per preference and job" a property of the table rather than
    -- of a read-then-write two consumers would both pass, and it gives the
    -- claim statement its conflict target for free.
    PRIMARY KEY (user_id, event_type, channel, job_id)
);

-- ResolveDelivery finds its row by delivery_id and claim_token, and the
-- primary key above starts with user_id, so it cannot serve that lookup:
-- without this index every resolution scans a table that grows by one row
-- per notification delivered.
--
-- UNIQUE rather than plain, because the identifier is minted once per record
-- and is what a receiver deduplicates on. A second row carrying the same one
-- would make both the receiver's deduplication and the fence ambiguous, so
-- the constraint states an invariant as well as serving the lookup.
--
-- A separate statement rather than a column constraint, so it also reaches a
-- database whose table already exists — CREATE TABLE IF NOT EXISTS would
-- skip the whole declaration there and silently leave the index out.
CREATE UNIQUE INDEX IF NOT EXISTS notification_deliveries_delivery_id_key
    ON notification_deliveries (delivery_id);
