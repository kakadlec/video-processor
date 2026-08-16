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

const videoJobCreatedEventType = "video_job.created"

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
		INSERT INTO video_jobs (id, user_id, original_filename, status, frame_count, error_reason, storage_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
	`
	if _, err := tx.ExecContext(ctx, insertJob,
		job.ID().String(),
		job.UserID().String(),
		job.OriginalFilename().String(),
		string(job.Status()),
		job.FrameCount(),
		job.ErrorReason(),
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
		SELECT id, user_id, original_filename, status, frame_count, error_reason, storage_key, created_at
		FROM video_jobs WHERE id = $1
	`
	return r.scanJob(r.db.QueryRowContext(ctx, query, id.String()))
}

// FindByUserID returns userID's jobs ordered by CreatedAt descending, with
// VideoJobID ascending as a tie-breaker, bounded by offset and limit.
func (r *Repository) FindByUserID(ctx context.Context, userID domain.UserID, offset, limit int) ([]*domain.VideoJob, error) {
	const query = `
		SELECT id, user_id, original_filename, status, frame_count, error_reason, storage_key, created_at
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
		storageKeyVal string
		createdAt     time.Time
	)
	if err := row.Scan(&idValue, &userIDValue, &filenameValue, &statusValue, &frameCount, &errorReason, &storageKeyVal, &createdAt); err != nil {
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
	var storageKey domain.StorageKey
	if storageKeyVal != "" {
		storageKey, err = domain.NewStorageKey(storageKeyVal)
		if err != nil {
			return nil, fmt.Errorf("video: stored storage key is invalid: %w", err)
		}
	}

	return domain.RestoreVideoJob(id, userID, filename, storageKey, frameCount, errorReason, domain.JobStatus(statusValue), createdAt)
}
