package postgres

import (
	"context"
	"database/sql"
	"fmt"
)

// VideoJobQueuedEventType is the event_type Repository.Enqueue writes and
// the relay claims. It is exported because it is also the AMQP routing key
// the relay publishes under: internal/video/infrastructure/messaging's
// RoutingKeyJobQueued must equal it, and that package's tests assert the
// equality rather than leaving two literals to drift.
const VideoJobQueuedEventType = videoJobQueuedEventType

// OutboxRepository reads the transactional outbox Repository writes to.
//
// It is deliberately separate from Repository: that type persists the
// VideoJob aggregate, while this one serves a background relay that knows
// nothing about jobs and only moves opaque payloads to a broker.
type OutboxRepository struct {
	db *sql.DB
}

// NewOutboxRepository wires an OutboxRepository to an already-open handle.
func NewOutboxRepository(db *sql.DB) *OutboxRepository {
	return &OutboxRepository{db: db}
}

// OutboxMessage is one claimed row: the id used to stamp it published, and
// the payload to deliver. Both are opaque to the relay.
type OutboxMessage struct {
	ID      string
	Payload []byte
}

// OutboxBatch owns the transaction holding a claim's row locks. The caller
// drives the cycle — read Messages, publish them, MarkPublished the ones the
// broker accepted, then Commit — and must always finish with Commit or
// Rollback, so the locks are released.
//
// The *sql.Tx stays behind this type on purpose. The relay lives in
// internal/video/infrastructure/messaging, which owns the AMQP client and
// has no business naming a database driver; handing it a transaction handle
// would put both drivers in one package, which is the split design.md
// decision 3 exists to keep.
type OutboxBatch struct {
	tx       *sql.Tx
	messages []OutboxMessage
}

// Messages returns the claimed rows, oldest first.
func (b *OutboxBatch) Messages() []OutboxMessage {
	return b.messages
}

// Claim opens a transaction and locks up to limit unpublished rows of
// eventType, oldest first, skipping rows another replica already holds. The
// returned batch keeps those locks until it is committed or rolled back, so
// the caller must always finish it.
//
// The event_type predicate is not tuning. The table holds an unpublished
// video_job.created row for every job created since Phase 3 and always
// will — nothing publishes those — so an unfiltered claim would re-read that
// permanent backlog on every poll and, with a bounded batch, never reach the
// rows the relay exists to deliver.
//
// FOR UPDATE SKIP LOCKED is what makes concurrent cmd/api replicas safe: a
// replica steps over rows another has claimed instead of blocking behind
// them, so the same row is never published twice concurrently and no replica
// waits on another's broker round trip.
func (r *OutboxRepository) Claim(ctx context.Context, eventType string, limit int) (*OutboxBatch, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("video: begin outbox claim transaction: %w", err)
	}

	const query = `
		SELECT id, payload
		FROM video_job_outbox
		WHERE event_type = $1 AND published_at IS NULL
		ORDER BY occurred_at
		LIMIT $2
		FOR UPDATE SKIP LOCKED
	`
	rows, err := tx.QueryContext(ctx, query, eventType, limit)
	if err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("video: claim outbox rows: %w", err)
	}
	defer rows.Close()

	var messages []OutboxMessage
	for rows.Next() {
		var msg OutboxMessage
		if err := rows.Scan(&msg.ID, &msg.Payload); err != nil {
			_ = tx.Rollback()
			return nil, fmt.Errorf("video: scan outbox row: %w", err)
		}
		messages = append(messages, msg)
	}
	if err := rows.Err(); err != nil {
		_ = tx.Rollback()
		return nil, fmt.Errorf("video: claim outbox rows: %w", err)
	}

	return &OutboxBatch{tx: tx, messages: messages}, nil
}

// MarkPublished stamps published_at on ids, which must be a subset of the
// batch's own claimed rows. Calling it with none is a no-op, which is the
// normal outcome when every publish was refused.
func (b *OutboxBatch) MarkPublished(ctx context.Context, ids []string) error {
	if len(ids) == 0 {
		return nil
	}

	const query = `
		UPDATE video_job_outbox
		SET published_at = now()
		WHERE id = ANY($1::uuid[])
	`
	if _, err := b.tx.ExecContext(ctx, query, ids); err != nil {
		return fmt.Errorf("video: mark outbox rows published: %w", err)
	}
	return nil
}

// Commit releases the claim's locks and makes its stamps durable.
func (b *OutboxBatch) Commit() error {
	if err := b.tx.Commit(); err != nil {
		return fmt.Errorf("video: commit outbox claim transaction: %w", err)
	}
	return nil
}

// Rollback abandons the claim, leaving every row unpublished for a later
// poll. Safe to call after Commit, where it is a no-op — which is what makes
// a deferred Rollback the correct way to guarantee the locks are released.
func (b *OutboxBatch) Rollback() {
	_ = b.tx.Rollback()
}
