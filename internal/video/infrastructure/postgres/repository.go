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
const (
	videoJobCreatedEventType = "video_job.created"
	videoJobQueuedEventType  = "video_job.queued"
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
type videoJobQueuedPayload struct {
	Type       string    `json:"type"`
	JobID      string    `json:"job_id"`
	UserID     string    `json:"user_id"`
	SourceKey  string    `json:"source_key"`
	OccurredAt time.Time `json:"occurred_at"`
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
		INSERT INTO video_jobs (id, user_id, original_filename, status, frame_count, error_reason, source_key, storage_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9)
	`
	if _, err := tx.ExecContext(ctx, insertJob,
		job.ID().String(),
		job.UserID().String(),
		job.OriginalFilename().String(),
		string(job.Status()),
		job.FrameCount(),
		job.ErrorReason(),
		job.SourceKey().String(),
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
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, storage_key, created_at
		FROM video_jobs WHERE id = $1
	`
	return r.scanJob(r.db.QueryRowContext(ctx, query, id.String()))
}

// FindByUserID returns userID's jobs ordered by CreatedAt descending, with
// VideoJobID ascending as a tie-breaker, bounded by offset and limit.
func (r *Repository) FindByUserID(ctx context.Context, userID domain.UserID, offset, limit int) ([]*domain.VideoJob, error) {
	const query = `
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, storage_key, created_at
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
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, storage_key, created_at
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
		Type:       videoJobQueuedEventType,
		JobID:      job.ID().String(),
		UserID:     job.UserID().String(),
		SourceKey:  job.SourceKey().String(),
		OccurredAt: occurredAt,
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
		storageKeyVal string
		createdAt     time.Time
	)
	// Scan order follows the SELECT column list above, and source_key sits
	// before storage_key in both. The two are the same type, so transposing
	// them compiles and passes RestoreVideoJob's validation for a completed
	// job — it surfaces only as GET /download/:filename rejecting every
	// result. Keep the SELECT list, this Scan, and the RestoreVideoJob call
	// below in one order.
	if err := row.Scan(&idValue, &userIDValue, &filenameValue, &statusValue, &frameCount, &errorReason, &sourceKeyVal, &storageKeyVal, &createdAt); err != nil {
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

	return domain.RestoreVideoJob(id, userID, filename, sourceKey, storageKey, frameCount, errorReason, domain.JobStatus(statusValue), createdAt)
}
