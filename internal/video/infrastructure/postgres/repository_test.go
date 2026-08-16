package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"testing"
	"time"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idgen"
	"video-processor/internal/video/infrastructure/postgres"
)

// testDB skips the test unless VIDEO_POSTGRES_TEST_DSN is explicitly set,
// per design.md: the default unit-test path must not require a live external
// service. Set the env var and provision a real PostgreSQL instance to
// exercise this adapter end-to-end.
func testDB(t *testing.T) *sql.DB {
	t.Helper()

	dsn := os.Getenv("VIDEO_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("VIDEO_POSTGRES_TEST_DSN not set; skipping PostgreSQL integration test")
	}

	db, err := postgres.Open(postgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("unexpected error opening database: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	ctx := context.Background()
	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("unexpected error migrating schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE video_jobs, video_job_outbox"); err != nil {
		t.Fatalf("unexpected error truncating tables: %v", err)
	}

	return db
}

func newTestJob(t *testing.T, ids domain.VideoJobIDGenerator, userID, filename string, createdAt time.Time) *domain.VideoJob {
	t.Helper()

	uid, err := domain.NewUserID(userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fn, err := domain.NewOriginalFilename(filename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	job, err := domain.NewVideoJob(ids, uid, fn, createdAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return job
}

func TestRepository_CreateAndFindByID(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	// See schema.sql's comment on video_jobs.created_at: TIMESTAMPTZ is
	// microsecond-precision, so an untruncated time.Now() would not
	// round-trip exactly and this test's own CreatedAt assertion below
	// would fail on a real (not fake) precision boundary, not a bug.
	now := time.Now().UTC().Truncate(time.Microsecond)
	job := newTestJob(t, ids, "user-1", "video.mp4", now)

	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found.ID().Equal(job.ID()) {
		t.Fatalf("ID = %v, want %v", found.ID(), job.ID())
	}
	if !found.UserID().Equal(job.UserID()) {
		t.Fatalf("UserID = %v, want %v", found.UserID(), job.UserID())
	}
	if found.OriginalFilename() != job.OriginalFilename() {
		t.Fatalf("OriginalFilename = %v, want %v", found.OriginalFilename(), job.OriginalFilename())
	}
	if found.Status() != job.Status() {
		t.Fatalf("Status = %v, want %v", found.Status(), job.Status())
	}
	if found.FrameCount() != job.FrameCount() {
		t.Fatalf("FrameCount = %v, want %v", found.FrameCount(), job.FrameCount())
	}
	if found.ErrorReason() != job.ErrorReason() {
		t.Fatalf("ErrorReason = %v, want %v", found.ErrorReason(), job.ErrorReason())
	}
	if !found.StorageKey().IsZero() {
		t.Fatalf("StorageKey = %v, want zero value", found.StorageKey())
	}
	if !found.CreatedAt().Equal(now) {
		t.Fatalf("CreatedAt = %v, want %v", found.CreatedAt(), now)
	}
}

func TestRepository_FindByID_NotFound(t *testing.T) {
	db := testDB(t)
	repo := postgres.NewRepository(db, idgen.New())

	id, err := domain.NewVideoJobID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = repo.FindByID(context.Background(), id)
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
}

func TestRepository_FindByUserID_OrdersByCreatedAtDescending(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	older := newTestJob(t, ids, "user-1", "older.mp4", time.Now().UTC().Add(-time.Hour))
	newer := newTestJob(t, ids, "user-1", "newer.mp4", time.Now().UTC())
	if err := repo.Create(ctx, older); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := repo.Create(ctx, newer); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobs, err := repo.FindByUserID(ctx, userID, 0, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2", len(jobs))
	}
	if !jobs[0].ID().Equal(newer.ID()) || !jobs[1].ID().Equal(older.ID()) {
		t.Fatalf("jobs = [%v, %v], want [newer, older] (CreatedAt descending)", jobs[0].ID(), jobs[1].ID())
	}
}

func TestRepository_FindByUserID_TieBreaksByAscendingIDAndPaginates(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	other := newTestJob(t, ids, "other-user", "other.mp4", time.Now().UTC())
	if err := repo.Create(ctx, other); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	tied := time.Now().UTC().Truncate(time.Microsecond)
	var mine []*domain.VideoJob
	for range 3 {
		job := newTestJob(t, ids, "user-1", "video.mp4", tied)
		if err := repo.Create(ctx, job); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		mine = append(mine, job)
	}

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	page, err := repo.FindByUserID(ctx, userID, 0, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page) != 2 {
		t.Fatalf("len(page) = %d, want 2", len(page))
	}

	sortedWant := []string{mine[0].ID().String(), mine[1].ID().String(), mine[2].ID().String()}
	// All three jobs share CreatedAt, so ascending VideoJobID breaks the tie.
	sort.Strings(sortedWant)

	got := []string{page[0].ID().String(), page[1].ID().String()}
	want := sortedWant[:2]
	if got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("page IDs = %v, want %v (ascending VideoJobID tie-break)", got, want)
	}

	rest, err := repo.FindByUserID(ctx, userID, 2, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(rest) != 1 {
		t.Fatalf("len(rest) = %d, want 1", len(rest))
	}
	if rest[0].ID().String() != sortedWant[2] {
		t.Fatalf("rest[0].ID() = %v, want %v", rest[0].ID().String(), sortedWant[2])
	}
}

func TestRepository_Create_RecordsOutboxEvent(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	now := time.Now().UTC().Truncate(time.Microsecond)
	job := newTestJob(t, ids, "user-1", "video.mp4", now)

	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var (
		eventType    string
		payloadBytes []byte
		publishedAt  sql.NullTime
	)
	row := db.QueryRowContext(ctx, `SELECT event_type, payload, published_at FROM video_job_outbox WHERE payload->>'job_id' = $1`, job.ID().String())
	if err := row.Scan(&eventType, &payloadBytes, &publishedAt); err != nil {
		t.Fatalf("unexpected error querying outbox: %v", err)
	}
	if eventType != "video_job.created" {
		t.Fatalf("event_type = %q, want %q", eventType, "video_job.created")
	}
	if publishedAt.Valid {
		t.Fatalf("published_at = %v, want NULL", publishedAt)
	}

	var payload struct {
		Type             string    `json:"type"`
		JobID            string    `json:"job_id"`
		UserID           string    `json:"user_id"`
		OriginalFilename string    `json:"original_filename"`
		OccurredAt       time.Time `json:"occurred_at"`
	}
	if err := json.Unmarshal(payloadBytes, &payload); err != nil {
		t.Fatalf("unexpected error unmarshaling payload: %v", err)
	}
	if payload.Type != "video_job.created" {
		t.Fatalf("payload.Type = %q, want %q", payload.Type, "video_job.created")
	}
	if payload.JobID != job.ID().String() {
		t.Fatalf("payload.JobID = %q, want %q", payload.JobID, job.ID().String())
	}
	if payload.UserID != job.UserID().String() {
		t.Fatalf("payload.UserID = %q, want %q", payload.UserID, job.UserID().String())
	}
	if payload.OriginalFilename != job.OriginalFilename().String() {
		t.Fatalf("payload.OriginalFilename = %q, want %q", payload.OriginalFilename, job.OriginalFilename().String())
	}
	if !payload.OccurredAt.Equal(now) {
		t.Fatalf("payload.OccurredAt = %v, want %v", payload.OccurredAt, now)
	}
}

func TestRepository_Create_DuplicateID_LeavesNoOutboxRow(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	first := newTestJob(t, ids, "user-1", "video.mp4", time.Now().UTC())
	if err := repo.Create(ctx, first); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Reuse the same ID to force a primary-key violation on the second Create.
	dup, err := domain.RestoreVideoJob(first.ID(), first.UserID(), first.OriginalFilename(), first.StorageKey(), 0, "", domain.JobStatusPending, time.Now().UTC())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := repo.Create(ctx, dup); err == nil {
		t.Fatal("expected an error creating a job with a duplicate ID, got nil")
	}

	var count int
	row := db.QueryRowContext(ctx, `SELECT count(*) FROM video_job_outbox WHERE payload->>'job_id' = $1`, first.ID().String())
	if err := row.Scan(&count); err != nil {
		t.Fatalf("unexpected error counting outbox rows: %v", err)
	}
	if count != 1 {
		t.Fatalf("outbox row count = %d, want 1 (only the first, successful Create)", count)
	}
}

// TestRepository_Create_OutboxInsertFailure_RollsBackJobRow exercises the
// direction the duplicate-ID test above cannot: it lets the video_jobs
// insert succeed and forces the video_job_outbox insert to fail, so only a
// genuinely transactional Create passes. A non-transactional implementation
// (two independent inserts) would leave an orphaned video_jobs row here.
func TestRepository_Create_OutboxInsertFailure_RollsBackJobRow(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	if _, err := db.ExecContext(ctx, "DROP TABLE video_job_outbox"); err != nil {
		t.Fatalf("unexpected error dropping outbox table: %v", err)
	}
	t.Cleanup(func() {
		if err := postgres.Migrate(context.Background(), db); err != nil {
			t.Fatalf("unexpected error re-migrating schema: %v", err)
		}
	})

	job := newTestJob(t, ids, "user-1", "video.mp4", time.Now().UTC())
	if err := repo.Create(ctx, job); err == nil {
		t.Fatal("expected an error when the outbox insert fails, got nil")
	}

	var count int
	row := db.QueryRowContext(ctx, `SELECT count(*) FROM video_jobs WHERE id = $1`, job.ID().String())
	if err := row.Scan(&count); err != nil {
		t.Fatalf("unexpected error counting video_jobs rows: %v", err)
	}
	if count != 0 {
		t.Fatalf("video_jobs row count = %d, want 0 (the transaction must roll back the job insert too)", count)
	}
}
