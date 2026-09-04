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
//
// The two terminal event types carry a generation of their own for the same
// reason, and it is independent of the dispatch generation: they are claimed
// by a different relay, from the same table, on the same predicate.
const (
	videoJobCreatedEventType   = "video_job.created"
	videoJobQueuedEventType    = "video_job.queued.v2"
	videoJobCompletedEventType = "video_job.completed.v1"
	videoJobFailedEventType    = "video_job.failed.v1"
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

// videoJobCompletedPayload and videoJobFailedPayload are the bodies of the
// two terminal outbox rows Update writes. They are the producer halves of a
// wire contract whose consumer halves live in
// internal/video/infrastructure/messaging, pinned the same way
// videoJobQueuedPayload is: by a round-trip test, not by a shared type.
type videoJobCompletedPayload struct {
	Type       string    `json:"type"`
	JobID      string    `json:"job_id"`
	UserID     string    `json:"user_id"`
	FrameCount int       `json:"frame_count"`
	StorageKey string    `json:"storage_key"`
	OccurredAt time.Time `json:"occurred_at"`
}

type videoJobFailedPayload struct {
	Type        string    `json:"type"`
	JobID       string    `json:"job_id"`
	UserID      string    `json:"user_id"`
	ErrorReason string    `json:"error_reason"`
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
		INSERT INTO video_jobs (id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at, lease_epoch)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11)
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
		job.LeaseEpoch(),
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
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at, lease_epoch
		FROM video_jobs WHERE id = $1
	`
	return r.scanJob(r.db.QueryRowContext(ctx, query, id.String()))
}

// FindByUserID returns userID's jobs ordered by CreatedAt descending, with
// VideoJobID ascending as a tie-breaker, bounded by offset and limit.
func (r *Repository) FindByUserID(ctx context.Context, userID domain.UserID, offset, limit int) ([]*domain.VideoJob, error) {
	const query = `
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at, lease_epoch
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
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at, lease_epoch
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

// enqueueJobStatement is Enqueue's write. Its predicate is the id alone:
// the pending -> queued transition is guarded by the aggregate, which
// refuses it from any other status, and by the transaction the outbox insert
// commits with.
//
// It was once shared verbatim with Update. The split is load-bearing rather
// than cosmetic: Update's predicate now names lease_epoch and a processing
// status, and an enqueue running through that statement would affect zero
// rows while still inserting and committing its outbox row — leaving a job
// stuck at pending with a live dispatch naming it.
//
// Enqueue writing frame_count, error_reason, and storage_key is a stated
// precondition rather than an accident: it only ever runs on a job the
// aggregate has just moved from pending to queued, where all three are still
// their zero values. It must not be called on a job carrying a result.
// source_key is absent from both statements because it is written once, by
// Create, and never changes afterwards.
const enqueueJobStatement = `
	UPDATE video_jobs
	SET status = $1, frame_count = $2, error_reason = $3, storage_key = $4
	WHERE id = $5
`

// updateTerminalJobStatement is Update's write, and it is fenced. It used to
// be unconditional, on the reasoning that only the one consumer holding the
// claim could ever reach it. Recovery makes that false: a job whose worker
// died is requeued and claimed again, so two actors can hold the same job's
// aggregate in memory at once.
//
// Both added conjuncts are required and neither is redundant. lease_epoch
// rejects a worker superseded by a requeue — its epoch is behind the row's,
// so its terminal write for a run that has already been re-dispatched cannot
// land. status = processing makes the write exclusive between two actors
// legitimately holding the *same* epoch: a leaseless worker still running and
// the sweep abandoning it. Whoever writes first leaves the row terminal, and
// every other write affects no row.
//
// The aggregate's own transition check does not substitute for the status
// conjunct. That check runs against the copy in this process's memory, which
// was loaded before the other actor wrote; the predicate is the only thing
// that reads the row as it stands at the moment of the write.
//
// Fencing here is safe because Update writes only processing -> completed and
// processing -> failed. Enqueue, ClaimForProcessing, and Requeue own the
// other three edges, and none of them goes through this statement.
const updateTerminalJobStatement = `
	UPDATE video_jobs
	SET status = $1, frame_count = $2, error_reason = $3, storage_key = $4
	WHERE id = $5 AND lease_epoch = $6 AND status = $7
`

// Update persists job's terminal outcome to its existing row, conditional on
// epoch — the lease epoch the caller holds — and on the row still being
// processing. Like Create and Enqueue, it writes a video_job_outbox row in
// the same transaction: the outcome and the event announcing it commit
// together or not at all.
//
// The event write is gated by the statement's own row count, not by a second
// predicate evaluated separately. That is the whole reason emission lives
// here rather than a layer up: the actor whose write applied and the actor
// who announces the outcome cannot then be different actors.
//
// Zero rows affected has three readings, told apart by a follow-up lookup the
// way ClaimForProcessing already tells a lost claim from an unknown job.
func (r *Repository) Update(ctx context.Context, job *domain.VideoJob, epoch int64) (bool, error) {
	// Truncated to the precision PostgreSQL stores, so the instant in the
	// payload and the instant in the row's occurred_at column are the same
	// value rather than one rounded copy of the other. A consumer correlating
	// the two would otherwise see them disagree in the last three digits.
	occurredAt := time.Now().UTC().Truncate(time.Microsecond)
	eventType, payload, err := marshalTerminalOutboxPayload(job, occurredAt)
	if err != nil {
		return false, err
	}

	applied, err := r.writeTerminalOutcome(ctx, job, epoch, eventType, payload, occurredAt)
	if err != nil {
		return false, err
	}
	if applied {
		return true, nil
	}
	// The transaction is already resolved by the time this runs, and the
	// lookup deliberately reads the pool rather than a transaction that
	// wrote nothing: it must see the row as every other reader does.
	return false, r.classifyRefusedUpdate(ctx, job, epoch)
}

// writeTerminalOutcome runs the fenced statement and, only when it affected a
// row, the outbox insert alongside it. Affecting no row rolls the whole thing
// back and reports that to Update, which classifies the refusal.
func (r *Repository) writeTerminalOutcome(ctx context.Context, job *domain.VideoJob, epoch int64, eventType string, payload []byte, occurredAt time.Time) (bool, error) {
	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("video: begin terminal update transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, updateTerminalJobStatement,
		string(job.Status()),
		job.FrameCount(),
		job.ErrorReason(),
		job.StorageKey().String(),
		job.ID().String(),
		epoch,
		string(domain.JobStatusProcessing),
	)
	if err != nil {
		return false, fmt.Errorf("video: update video job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("video: update video job: %w", err)
	}
	if affected == 0 {
		return false, nil
	}

	if err := insertOutboxEvent(ctx, tx, eventType, payload, occurredAt); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("video: commit terminal update transaction: %w", err)
	}
	return true, nil
}

// marshalTerminalOutboxPayload selects the event type and payload shape from
// the job's own status, generating occurred_at once so the payload's field
// and the row's column carry the same instant.
//
// A status that is neither completed nor failed is refused here, before any
// statement runs. The fenced statement's status = processing conjunct would
// stop such a write anyway, but it would stop it as a fence — reporting a
// lost race for what is a caller defect, and doing so only after a round
// trip.
func marshalTerminalOutboxPayload(job *domain.VideoJob, occurredAt time.Time) (string, []byte, error) {
	var (
		eventType string
		body      any
	)
	switch job.Status() {
	case domain.JobStatusCompleted:
		eventType = videoJobCompletedEventType
		body = videoJobCompletedPayload{
			Type:       videoJobCompletedEventType,
			JobID:      job.ID().String(),
			UserID:     job.UserID().String(),
			FrameCount: job.FrameCount(),
			StorageKey: job.StorageKey().String(),
			OccurredAt: occurredAt,
		}
	case domain.JobStatusFailed:
		eventType = videoJobFailedEventType
		body = videoJobFailedPayload{
			Type:        videoJobFailedEventType,
			JobID:       job.ID().String(),
			UserID:      job.UserID().String(),
			ErrorReason: job.ErrorReason(),
			OccurredAt:  occurredAt,
		}
	default:
		return "", nil, fmt.Errorf("video: update video job: %q is not a terminal status", job.Status())
	}

	payload, err := json.Marshal(body)
	if err != nil {
		return "", nil, fmt.Errorf("video: marshal outbox payload: %w", err)
	}
	return eventType, payload, nil
}

// classifyRefusedUpdate reads the row Update could not write and decides what
// its refusal meant.
//
// It classifies from the stored row alone, so every refusal that is not this
// caller's own recorded outcome is reported as a fence — including a row that
// is no longer processing at the caller's epoch. The finer reading of that
// case (an equal-epoch queued row is a stale cache entry, and a pending job
// is a defect rather than a fence) needs the aggregate and belongs to
// application.classifyRefusedTransition, which loads authoritatively and
// therefore intercepts both before any statement runs. The two are not in
// conflict; they answer at different levels of information.
func (r *Repository) classifyRefusedUpdate(ctx context.Context, job *domain.VideoJob, epoch int64) error {
	const lookup = `
		SELECT status, frame_count, error_reason, storage_key, lease_epoch
		FROM video_jobs WHERE id = $1
	`
	var (
		statusValue   string
		frameCount    int
		errorReason   string
		storageKeyVal string
		storedEpoch   int64
	)
	if err := r.db.QueryRowContext(ctx, lookup, job.ID().String()).Scan(&statusValue, &frameCount, &errorReason, &storageKeyVal, &storedEpoch); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return domain.ErrVideoJobNotFound
		}
		return fmt.Errorf("video: update video job: %w", err)
	}

	if storedEpoch == epoch && isTerminalStatus(domain.JobStatus(statusValue)) {
		// This caller's own earlier commit, re-attempted after a lost
		// response. All four outcome fields are compared because the
		// sweep's abandonment and a genuine failure differ only in the
		// reason.
		if statusValue == string(job.Status()) &&
			frameCount == job.FrameCount() &&
			errorReason == job.ErrorReason() &&
			storageKeyVal == job.StorageKey().String() {
			return nil
		}
		// Another actor holding the same epoch finished first. Wrapped
		// distinctly from a takeover: an abandonment race is not a
		// supersession, and a log that conflates them hides which one
		// is happening.
		return fmt.Errorf("video: update video job: another actor recorded %s at epoch %d: %w", statusValue, epoch, domain.ErrJobFenced)
	}
	// A strictly greater stored epoch is a takeover; anything else that
	// refused the predicate means the caller does not hold a processing row
	// at its epoch, which is the same loss by a different route.
	return fmt.Errorf("video: update video job: job is at epoch %d, caller holds %d: %w", storedEpoch, epoch, domain.ErrJobFenced)
}

func isTerminalStatus(status domain.JobStatus) bool {
	return status == domain.JobStatusCompleted || status == domain.JobStatusFailed
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
	payload, err := marshalQueuedOutboxPayload(job, occurredAt)
	if err != nil {
		return err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("video: begin enqueue transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, enqueueJobStatement,
		string(job.Status()),
		job.FrameCount(),
		job.ErrorReason(),
		job.StorageKey().String(),
		job.ID().String(),
	); err != nil {
		return fmt.Errorf("video: enqueue video job: %w", err)
	}

	if err := insertOutboxEvent(ctx, tx, videoJobQueuedEventType, payload, occurredAt); err != nil {
		return err
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("video: commit enqueue transaction: %w", err)
	}
	return nil
}

// marshalQueuedOutboxPayload is shared by Enqueue and Requeue rather than
// copied into each. The consumer cannot tell a first dispatch from a
// re-dispatch and must not have to, so the two write the same event type and
// the same payload; a drifted second copy would be a message the consumer
// decodes to zero values. The event type is named here, once, for both — the
// insert below takes it as an argument precisely so that choice stays with
// the payload it belongs to.
func marshalQueuedOutboxPayload(job *domain.VideoJob, occurredAt time.Time) ([]byte, error) {
	payload, err := json.Marshal(videoJobQueuedPayload{
		Type:        videoJobQueuedEventType,
		JobID:       job.ID().String(),
		UserID:      job.UserID().String(),
		SourceKey:   job.SourceKey().String(),
		ContentHash: job.ContentHash(),
		OccurredAt:  occurredAt,
	})
	if err != nil {
		return nil, fmt.Errorf("video: marshal outbox payload: %w", err)
	}
	return payload, nil
}

// insertOutboxEvent writes one outbox row inside the caller's transaction.
// It is the single insert every emitting operation goes through, so the row
// shape is written in one place; which event type a row carries is the
// caller's decision, not this function's.
func insertOutboxEvent(ctx context.Context, tx *sql.Tx, eventType string, payload []byte, occurredAt time.Time) error {
	const insertOutbox = `
		INSERT INTO video_job_outbox (id, event_type, payload, occurred_at)
		VALUES ($1, $2, $3, $4)
	`
	if _, err := tx.ExecContext(ctx, insertOutbox,
		uuid.NewString(),
		eventType,
		payload,
		occurredAt,
	); err != nil {
		return fmt.Errorf("video: record outbox event: %w", err)
	}
	return nil
}

// Requeue returns an abandoned job from processing to queued and, in the same
// transaction, writes the outbox row re-dispatching it. The two commit
// together for the same reason Enqueue's do: a job back in queued with no
// dispatch naming it is stranded exactly as badly as it was before.
//
// The UPDATE is conditional on observedEpoch and on the row still being
// processing, and it advances the epoch by one. That advance is the fence:
// the previous holder's terminal write names the old epoch and can no longer
// apply.
func (r *Repository) Requeue(ctx context.Context, job *domain.VideoJob, observedEpoch int64) (bool, error) {
	occurredAt := time.Now().UTC()
	payload, err := marshalQueuedOutboxPayload(job, occurredAt)
	if err != nil {
		return false, err
	}

	tx, err := r.db.BeginTx(ctx, nil)
	if err != nil {
		return false, fmt.Errorf("video: begin requeue transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	const requeueStatement = `
		UPDATE video_jobs
		SET status = $1, lease_epoch = lease_epoch + 1
		WHERE id = $2 AND status = $3 AND lease_epoch = $4
	`
	result, err := tx.ExecContext(ctx, requeueStatement,
		string(domain.JobStatusQueued),
		job.ID().String(),
		string(domain.JobStatusProcessing),
		observedEpoch,
	)
	if err != nil {
		return false, fmt.Errorf("video: requeue video job: %w", err)
	}
	affected, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("video: requeue video job: %w", err)
	}
	if affected == 0 {
		// Another sweep won, or the job left processing between the scan
		// and this write. Rolling back rather than committing is what
		// keeps the outbox row from announcing a dispatch that did not
		// happen.
		return false, nil
	}

	if err := insertOutboxEvent(ctx, tx, videoJobQueuedEventType, payload, occurredAt); err != nil {
		return false, err
	}

	if err := tx.Commit(); err != nil {
		return false, fmt.Errorf("video: commit requeue transaction: %w", err)
	}
	return true, nil
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
//
// The winner is handed the row's lease_epoch by the claiming statement
// itself, through RETURNING. Not a second query: one read before the claim
// can be stale by the time the claim lands — a sweep can requeue in between,
// and the winner would then carry an epoch that fences its own terminal
// write — and one read after is a statement another writer can interleave
// with. The claim does not advance the epoch; only a requeue does.
func (r *Repository) ClaimForProcessing(ctx context.Context, job *domain.VideoJob) (bool, int64, error) {
	const claim = `
		UPDATE video_jobs SET status = $1 WHERE id = $2 AND status = $3
		RETURNING lease_epoch
	`
	var epoch int64
	err := r.db.QueryRowContext(ctx, claim,
		string(domain.JobStatusProcessing),
		job.ID().String(),
		string(domain.JobStatusQueued),
	).Scan(&epoch)
	if err == nil {
		return true, epoch, nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return false, 0, fmt.Errorf("video: claim video job for processing: %w", err)
	}

	const exists = `SELECT 1 FROM video_jobs WHERE id = $1`
	var one int
	if err := r.db.QueryRowContext(ctx, exists, job.ID().String()).Scan(&one); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return false, 0, domain.ErrVideoJobNotFound
		}
		return false, 0, fmt.Errorf("video: claim video job for processing: %w", err)
	}
	return false, 0, nil
}

// FindProcessing returns up to limit processing jobs ordered by id, resuming
// strictly after the cursor. The recovery sweep carries that cursor between
// cycles and wraps on a short read.
//
// The keyset is not a refinement of ORDER BY id LIMIT n; it is what keeps the
// sweep from starving. One batch's worth of healthy long-running extractions
// sorts first every cycle, and an abandoned job behind them would never be
// examined until they finished.
//
// A zero cursor means the start of the scan and selects the statement with no
// keyset predicate at all. It cannot be bound instead: it serializes to the
// empty string, and id is a uuid, so id > ” errors before a row is examined.
// Two constant statements rather than one assembled from fragments, so no
// query in this package is ever built by concatenation.
func (r *Repository) FindProcessing(ctx context.Context, after domain.VideoJobID, limit int) ([]*domain.VideoJob, error) {
	const fromStart = `
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at, lease_epoch
		FROM video_jobs
		WHERE status = $1
		ORDER BY id ASC
		LIMIT $2
	`
	const afterCursor = `
		SELECT id, user_id, original_filename, status, frame_count, error_reason, source_key, content_hash, storage_key, created_at, lease_epoch
		FROM video_jobs
		WHERE status = $1 AND id > $2
		ORDER BY id ASC
		LIMIT $3
	`

	var (
		rows *sql.Rows
		err  error
	)
	if after.IsZero() {
		rows, err = r.db.QueryContext(ctx, fromStart, string(domain.JobStatusProcessing), limit)
	} else {
		rows, err = r.db.QueryContext(ctx, afterCursor, string(domain.JobStatusProcessing), after.String(), limit)
	}
	if err != nil {
		return nil, fmt.Errorf("video: list processing video jobs: %w", err)
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
		return nil, fmt.Errorf("video: list processing video jobs: %w", err)
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
		sourceKeyVal  string
		contentHash   string
		storageKeyVal string
		createdAt     time.Time
		leaseEpoch    int64
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
	if err := row.Scan(&idValue, &userIDValue, &filenameValue, &statusValue, &frameCount, &errorReason, &sourceKeyVal, &contentHash, &storageKeyVal, &createdAt, &leaseEpoch); err != nil {
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

	return domain.RestoreVideoJob(id, userID, filename, sourceKey, contentHash, storageKey, frameCount, errorReason, domain.JobStatus(statusValue), createdAt, leaseEpoch)
}
