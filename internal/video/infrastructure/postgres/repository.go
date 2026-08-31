package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/video/domain"
)

var _ domain.VideoJobRepository = (*Repository)(nil)

// Outbox event types. videoJobQueuedEventType is also the AMQP routing key
// the relay publishes under — it must stay equal to
// internal/video/infrastructure/messaging.RoutingKeyJobQueued, which is what
// keeps the database and the broker naming the same event identically. That
// package's tests assert the equality; this package does not import it,
// because infrastructure adapters do not depend on one another.
//
// The .v2 suffix is the dispatch generation, and it is carried here — on the
// event type string — rather than only on the broker's exchange name.
// Versioning the exchange alone does not isolate generations: every replica's
// relay claims from this one video_job_outbox table filtered on event_type
// and nothing else (see OutboxRepository.Claim), so during a rolling deploy
// an old replica's relay would happily claim a new replica's row and publish
// it into the old exchange, and vice versa. An already-deployed relay cannot
// be taught a new predicate, but it can be handed a literal it will never
// match. That is the only property that works, and this constant is where it
// lives.
//
// The generation exists to close the deploy window in which an old
// synchronous replica processes a job inline and then deletes the source
// object out from under a new worker's running extraction — not to quarantine
// stale rows, which ClaimForProcessing already renders harmless.
const (
	videoJobCreatedEventType = "video_job.created"
	videoJobQueuedEventType  = "video_job.queued.v2"
)

// Repository implements domain.VideoJobRepository against PostgreSQL using
// parameterized queries.
type Repository struct {
	db       *sql.DB
	idParser domain.VideoJobIDParser
}

// NewRepository wires a Repository to an already-open database handle and a
// VideoJobIDParser used to reconstruct VideoJobIDs read back from storage.
func NewRepository(db *sql.DB, idParser domain.VideoJobIDParser) *Repository {
	return &Repository{db: db, idParser: idParser}
}

type videoJobCreatedPayload struct {
	Type             string    `json:"type"`
	JobID            string    `json:"job_id"`
	UserID           string    `json:"user_id"`
	OriginalFilename string    `json:"original_filename"`
	OccurredAt       time.Time `json:"occurred_at"`
}

// videoJobQueuedPayload is the body of a video_job.queued outbox row, and
// therefore of the AMQP message the relay publishes from it. SourceKey is
// the field that makes the message actionable: a consumer needs the pair
// (job_id, source_key) to fetch the video it is being asked to process.
//
// It is the producer half of a wire contract whose consumer half is
// messaging.JobQueuedMessage. The two are separate types in separate
// packages, with no compiler enforcement between them, because infrastructure
// adapters do not import one another; a round-trip test is what pins them.
type videoJobQueuedPayload struct {
	Type        string    `json:"type"`
	JobID       string    `json:"job_id"`
	UserID      string    `json:"user_id"`
	SourceKey   string    `json:"source_key"`
	ContentHash string    `json:"content_hash"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// Create persists a new VideoJob and, in the same transaction, an outbox row
// describing its creation — the two are never observably inconsistent.
func (r *Repository) Create(ctx context.Context, job *domain.VideoJob) error {
	payload, err := json.Marshal(videoJobCreatedPayload{
		Type:             videoJobCreatedEventType,
		JobID:            job.ID().String(),
		UserID:           job.UserID().String(),
		OriginalFilename: job.OriginalFilename().String(),
		OccurredAt:       job.CreatedAt(),
	})
	if err != nil {
		return fmt.Errorf("video: marshal outbox payload: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("video: begin create transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const insertJob = `
		INSERT INTO video_jobs (id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	if _, err := tx.ExecContext(ctx, insertJob,
		job.ID().String(),
		job.UserID().String(),
		job.OriginalFilename().String(),
		string(job.Status()),
		job.FrameCount(),
		job.ErrorReason(),
		job.SourceKey().String(),
		job.ContentHash(),
		job.StorageKey().String(),
		job.CreatedAt(),
	); err != nil {
		return fmt.Errorf("video: create video job: %w", err)
	}

	const insertOutbox = `
		INSERT INTO video_job_outbox (id, event_type, payload, occurred_at)
		VALUES ($1, $2, $3, $4)
	`
	if _, err := tx.ExecContext(ctx, insertOutbox,
		uuid.NewString(),
		videoJobCreatedEventType,
		payload,
		job.CreatedAt(),
	); err != nil {
		return fmt.Errorf("video: record outbox event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("video: commit create transaction: %w", err)
	}
	return nil
}

// FindByID looks up a job by ID, returning domain.ErrVideoJobNotFound if none exists.
func (r *Repository) FindByID(ctx context.Context, id domain.VideoJobID) (*domain.VideoJob, error) {
	const query = `
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at
		FROM video_jobs WHERE id = $1
	`
	return r.scanJob(r.db.QueryRowContext(ctx, query, id.String()))
}

// FindByUserID returns userID's jobs ordered by CreatedAt descending, with
// VideoJobID ascending as a tie-breaker, bounded by offset and limit.
func (r *Repository) FindByUserID(ctx context.Context, userID domain.UserID, offset, limit int) ([]*domain.VideoJob, error) {
	const query = `
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at
		FROM video_jobs
		WHERE user_id = $1
		ORDER BY created_at DESC, id ASC
		OFFSET $2 LIMIT $3
	`
	rows, err := r.db.QueryContext(ctx, query, userID.String(), offset, limit)
	if err != nil {
		return nil, fmt.Errorf("video: list video jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*domain.VideoJob
	for rows.Next() {
		job, err := r.scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("video: list video jobs: %w", err)
	}
	return jobs, nil
}

// FindCompletedByUserID returns all of userID's completed jobs, ordered
// like FindByUserID. The status predicate is in the query rather than
// applied to a retrieved page: filtering afterwards would let a run of
// recent pending/failed jobs hide a user's completed results entirely.
func (r *Repository) FindCompletedByUserID(ctx context.Context, userID domain.UserID) ([]*domain.VideoJob, error) {
	const query = `
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at
		FROM video_jobs
		WHERE user_id = $1 AND status = $2
		ORDER BY created_at DESC, id ASC
	`
	rows, err := r.db.QueryContext(ctx, query, userID.String(), string(domain.JobStatusCompleted))
	if err != nil {
		return nil, fmt.Errorf("video: list completed video jobs: %w", err)
	}
	defer rows.Close()

	var jobs []*domain.VideoJob
	for rows.Next() {
		job, err := r.scanJobRow(rows)
		if err != nil {
			return nil, err
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("video: list completed video jobs: %w", err)
	}
	return jobs, nil
}

// updateJobStatement is shared by Update and Enqueue, which persist exactly
// the same columns — the only difference between them is the outbox row
// Enqueue writes alongside. source_key is absent because it is written once,
// by Create, and never changes afterwards.
//
// Enqueue writing frame_count, error_reason, and storage_key is a stated
// precondition rather than an accident: it only ever runs on a job the
// aggregate has just moved from pending to queued, where all three are still
// their zero values. It must not be called on a job carrying a result.
const updateJobStatement = `
	UPDATE video_jobs
	SET status = $1, frame_count = $2, error_reason = $3, storage_key = $4
	WHERE id = $5
`

// Update persists job's current status, frame count, error reason, and
// storage key to its existing row, identified by its unchanging id. Unlike
// Create and Enqueue, it writes no video_job_outbox row.
func (r *Repository) Update(ctx context.Context, job *domain.VideoJob) error {
	if _, err := r.db.ExecContext(ctx, updateJobStatement,
		string(job.Status()),
		job.FrameCount(),
		job.ErrorReason(),
		job.StorageKey().String(),
		job.ID().String(),
	); err != nil {
		return fmt.Errorf("video: update video job: %w", err)
	}
	return nil
}

// Enqueue persists job's queued state and, in the same transaction, the
// video_job.queued outbox row the relay publishes from — mirroring what
// Create does for video_job.created, so the row and the dispatch announcing
// it commit together or not at all.
//
// occurred_at is the moment of the enqueue, not job.CreatedAt(): the outbox
// is ordered by it, and reusing the creation timestamp would place a job
// enqueued long after it was created behind rows that were queued first.
func (r *Repository) Enqueue(ctx context.Context, job *domain.VideoJob) error {
	occurredAt := time.Now().UTC()
	payload, err := json.Marshal(videoJobQueuedPayload{
		Type:        videoJobQueuedEventType,
		JobID:       job.ID().String(),
		UserID:      job.UserID().String(),
		SourceKey:   job.SourceKey().String(),
		ContentHash: job.ContentHash(),
		OccurredAt:  occurredAt,
	})
	if err != nil {
		return fmt.Errorf("video: marshal outbox payload: %w", err)
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("video: begin enqueue transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, updateJobStatement,
		string(job.Status()),
		job.FrameCount(),
		job.ErrorReason(),
		job.StorageKey().String(),
		job.ID().String(),
	); err != nil {
		return fmt.Errorf("video: enqueue video job: %w", err)
	}

	const insertOutbox = `
		INSERT INTO video_job_outbox (id, event_type, payload, occurred_at)
		VALUES ($1, $2, $3, $4)
	`
	if _, err := tx.ExecContext(ctx, insertOutbox,
		uuid.NewString(),
		videoJobQueuedEventType,
		payload,
		occurredAt,
	); err != nil {
		return fmt.Errorf("video: record outbox event: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("video: commit enqueue transaction: %w", err)
	}
	return nil
}

// ClaimForProcessing moves the job from queued to processing with a single
// conditional UPDATE, reporting whether the row was still queued when it ran.
//
// One statement is the whole mechanism. It is not a transaction, not a
// SELECT followed by an UPDATE, and it holds no lock past its own execution:
// PostgreSQL serializes concurrent updates of the same row, so of two
// consumers handed the same dispatch exactly one sees a non-zero rows
// affected. Anything longer-lived would hold a lock for the duration of an
// ffmpeg run.
//
// job's status is deliberately not read for the predicate — the literal
// 'queued' is — because the caller has already transitioned the in-memory
// aggregate to processing before getting here.
//
// On zero rows affected it distinguishes a job that exists but is no longer
// queued from an id that names no row at all, so a consumer can tell a lost
// claim from an unknown job when classifying a message for dead-lettering.
func (r *Repository) ClaimForProcessing(ctx context.Context, job *domain.VideoJob) (bool, error) {
	const claim = `
		UPDATE video_jobs SET status = $1 WHERE id = $2 AND status = $3
	`
	result, err := r.db.ExecContext(ctx, claim,
		string(domain.JobStatusProcessing),
		job.ID().String(),
		string(domain.JobStatusQueued),
	)
	if err != nil {
		return false, fmt.Errorf("video: claim video job for processing: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("video: claim video job for processing: %w", err)
	}
	if affected > 0 {
		return true, nil
	}

	const exists = `SELECT 1 FROM video_jobs WHERE id = $1`
	var one int
	if err := r.db.QueryRowContext(ctx, exists, job.ID().String()).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, domain.ErrVideoJobNotFound
		}
		return false, fmt.Errorf("video: claim video job for processing: %w", err)
	}
	return false, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (r *Repository) scanJob(row *sql.Row) (*domain.VideoJob, error) {
	job, err := r.scanJobRow(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrVideoJobNotFound
	}
	return job, err
}

func (r *Repository) scanJobRow(row rowScanner) (*domain.VideoJob, error) {
	var (
		idValue       string
		userIDValue   string
		filenameValue string
		statusValue   string
		frameCount    int
		errorReason   string
		sourceKeyVal  string
		contentHash   string
		storageKeyVal string
		createdAt     time.Time
	)
	// Scan order follows the SELECT column list above: source_key, then
	// content_hash, then storage_key, in both. All three are strings on the
	// wire, so transposing any two compiles and passes RestoreVideoJob's
	// validation for a completed job — a swapped source_key/storage_key
	// surfaces only as GET /download/:filename rejecting every result, and a
	// swapped content_hash only as the worker failing to clear a failed job's
	// idempotency key. Keep the SELECT list, this Scan, and the
	// RestoreVideoJob call below in one order. content_hash sitting between
	// the two keys is deliberate: it makes that particular transposition the
	// less likely one to write.
	if err := row.Scan(&idValue, &userIDValue, &filenameValue, &statusValue, &frameCount, &errorReason, &sourceKeyVal, &contentHash, &storageKeyVal, &createdAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return nil, fmt.Errorf("video: scan video job: %w", err)
	}

	id, err := r.idParser.ParseVideoJobID(idValue)
	if err != nil {
		return nil, fmt.Errorf("video: stored video job id is invalid: %w", err)
	}
	userID, err := domain.NewUserID(userIDValue)
	if err != nil {
		return nil, fmt.Errorf("video: stored user id is invalid: %w", err)
	}
	filename, err := domain.NewOriginalFilename(filenameValue)
	if err != nil {
		return nil, fmt.Errorf("video: stored original filename is invalid: %w", err)
	}
	// Both keys are optional in the column and rejected as empty by
	// NewStorageKey, so each is parsed only when present. For source_key
	// that is not merely symmetry: the column ships with an empty default
	// and every row predating it carries one, so an unconditional parse
	// here would make those rows unloadable.
	var sourceKey domain.StorageKey
	if sourceKeyVal != "" {
		sourceKey, err = domain.NewStorageKey(sourceKeyVal)
		if err != nil {
			return nil, fmt.Errorf("video: stored source key is invalid: %w", err)
		}
	}
	var storageKey domain.StorageKey
	if storageKeyVal != "" {
		storageKey, err = domain.NewStorageKey(storageKeyVal)
		if err != nil {
			return nil, fmt.Errorf("video: stored storage key is invalid: %w", err)
		}
	}

	return domain.RestoreVideoJob(id, userID, filename, sourceKey, contentHash, storageKey, frameCount, errorReason, domain.JobStatus(statusValue), createdAt)
}
