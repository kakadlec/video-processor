package postgres_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"os"
	"sort"
	"sync"
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
	return newTestJobWithSourceKey(t, ids, userID, filename, "uploads/"+filename, createdAt)
}

// newTestJobWithSourceKey builds a job whose source key is set explicitly,
// including to "" for a job with no stored source (POST /api/video-jobs).
func newTestJobWithSourceKey(t *testing.T, ids domain.VideoJobIDGenerator, userID, filename, sourceKey string, createdAt time.Time) *domain.VideoJob {
	t.Helper()

	uid, err := domain.NewUserID(userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	fn, err := domain.NewOriginalFilename(filename)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	var key domain.StorageKey
	if sourceKey != "" {
		key, err = domain.NewStorageKey(sourceKey)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	job, err := domain.NewVideoJob(ids, uid, fn, key, "", createdAt)
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
	dup, err := domain.RestoreVideoJob(first.ID(), first.UserID(), first.OriginalFilename(), first.SourceKey(), first.ContentHash(), first.StorageKey(), 0, "", domain.JobStatusPending, time.Now().UTC(), 0)
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

func TestRepository_Update_PersistsTransitionedState(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job := newTestJob(t, ids, "user-1", "video.mp4", time.Now().UTC().Truncate(time.Microsecond))
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	epoch := driveCreatedJobToProcessing(t, repo, job)
	storageKey, err := domain.NewStorageKey("outputs/frames_" + job.ID().String() + ".zip")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if err := job.Complete(storageKey, 7); err != nil {
		t.Fatalf("unexpected error completing: %v", err)
	}

	applied, err := repo.Update(ctx, job, epoch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !applied {
		t.Fatal("applied = false, want true")
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.Status() != domain.JobStatusCompleted {
		t.Fatalf("Status = %v, want %v", found.Status(), domain.JobStatusCompleted)
	}
	if found.FrameCount() != 7 {
		t.Fatalf("FrameCount = %d, want 7", found.FrameCount())
	}
	if found.StorageKey() != storageKey {
		t.Fatalf("StorageKey = %v, want %v", found.StorageKey(), storageKey)
	}
}

func TestRepository_Update_DoesNotWriteOutboxRow(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job := newTestJob(t, ids, "user-1", "video.mp4", time.Now().UTC().Truncate(time.Microsecond))
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Counted after the job has reached processing, so the outbox row
	// Enqueue legitimately writes is part of the baseline and only Update's
	// own effect is under test.
	epoch := driveCreatedJobToProcessing(t, repo, job)

	var before int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM video_job_outbox").Scan(&before); err != nil {
		t.Fatalf("unexpected error counting outbox rows: %v", err)
	}

	if err := job.Fail("boom"); err != nil {
		t.Fatalf("unexpected error failing: %v", err)
	}
	if _, err := repo.Update(ctx, job, epoch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var after int
	if err := db.QueryRowContext(ctx, "SELECT count(*) FROM video_job_outbox").Scan(&after); err != nil {
		t.Fatalf("unexpected error counting outbox rows: %v", err)
	}
	if after != before {
		t.Fatalf("outbox row count changed from %d to %d; Update must not write an outbox row", before, after)
	}
}

// TestRepository_Update_CanceledContext_FailsButFreshContextSucceeds
// demonstrates the exact risk application.NewFinalizationContext exists
// for: reusing a request context that's already canceled by the time a
// terminal state transition needs to be persisted makes that persistence
// write itself fail, leaving the job stuck wherever it was — a fresh,
// independent context succeeds where the canceled one fails.
func TestRepository_Update_CanceledContext_FailsButFreshContextSucceeds(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)

	job := newTestJob(t, ids, "user-1", "video.mp4", time.Now().UTC().Truncate(time.Microsecond))
	if err := repo.Create(context.Background(), job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	epoch := driveCreatedJobToProcessing(t, repo, job)
	if err := job.Fail("boom"); err != nil {
		t.Fatalf("unexpected error failing: %v", err)
	}

	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := repo.Update(canceledCtx, job, epoch); err == nil {
		t.Fatalf("expected an error updating with an already-canceled context, got nil")
	}

	applied, err := repo.Update(context.Background(), job, epoch)
	if err != nil {
		t.Fatalf("unexpected error updating with a fresh context: %v", err)
	}
	if !applied {
		t.Fatal("applied = false, want true")
	}

	found, err := repo.FindByID(context.Background(), job.ID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.Status() != domain.JobStatusFailed {
		t.Fatalf("Status = %v, want %v", found.Status(), domain.JobStatusFailed)
	}
}

// completeTestJob drives a fresh job through its full transition sequence
// and persists the result, so a test can seed genuinely completed rows
// rather than hand-crafting invalid aggregate state.
func completeTestJob(t *testing.T, repo *postgres.Repository, ids domain.VideoJobIDGenerator, userID, filename string, createdAt time.Time) *domain.VideoJob {
	t.Helper()
	ctx := context.Background()

	job := newTestJob(t, ids, userID, filename, createdAt)
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	epoch := driveCreatedJobToProcessing(t, repo, job)
	if err := job.Complete(domain.ResultStorageKey(job.ID()), 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.Update(ctx, job, epoch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return job
}

func TestRepository_FindCompletedByUserID_ReturnsOnlyCompletedJobsForThatUser(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	completed := completeTestJob(t, repo, ids, "user-1", "done.mp4", time.Now().UTC())
	// A pending job for the same user, and a completed job for another one.
	if err := repo.Create(ctx, newTestJob(t, ids, "user-1", "pending.mp4", time.Now().UTC())); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	completeTestJob(t, repo, ids, "user-2", "someone-else.mp4", time.Now().UTC())

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobs, err := repo.FindCompletedByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 {
		t.Fatalf("len(jobs) = %d, want 1", len(jobs))
	}
	if !jobs[0].ID().Equal(completed.ID()) {
		t.Fatalf("jobs[0].ID() = %v, want %v", jobs[0].ID(), completed.ID())
	}
	if jobs[0].StorageKey().IsZero() {
		t.Fatal("expected the completed job's StorageKey to be populated")
	}
}

// TestRepository_FindCompletedByUserID_NonCompletedJobsDoNotHideCompletedOnes
// is the case that motivates a dedicated query: filtering a page of
// FindByUserID results would let a run of recent non-completed jobs push a
// user's completed results out of the listing entirely.
func TestRepository_FindCompletedByUserID_NonCompletedJobsDoNotHideCompletedOnes(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	base := time.Now().UTC()
	completed := completeTestJob(t, repo, ids, "user-1", "done.mp4", base.Add(-time.Hour))
	for i := range 5 {
		newer := newTestJob(t, ids, "user-1", "pending.mp4", base.Add(time.Duration(i)*time.Minute))
		if err := repo.Create(ctx, newer); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	}

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobs, err := repo.FindCompletedByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 1 || !jobs[0].ID().Equal(completed.ID()) {
		t.Fatalf("expected the older completed job to be returned despite 5 newer non-completed jobs, got %d jobs", len(jobs))
	}
}

// TestRepository_FindCompletedByUserID_HasNoImplicitLimit guards the
// no-pagination decision: GET /api/status takes no pagination parameters, so
// a bound here would silently make a heavy user's older results unreachable.
func TestRepository_FindCompletedByUserID_HasNoImplicitLimit(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	// More than ListUserJobs' maximum page size of 100.
	const total = 101
	base := time.Now().UTC()
	for i := range total {
		completeTestJob(t, repo, ids, "user-1", "done.mp4", base.Add(time.Duration(i)*time.Second))
	}

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobs, err := repo.FindCompletedByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != total {
		t.Fatalf("len(jobs) = %d, want %d — the query must apply no limit of its own", len(jobs), total)
	}
}

func TestRepository_FindCompletedByUserID_OrdersNewestFirst(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	older := completeTestJob(t, repo, ids, "user-1", "older.mp4", time.Now().UTC().Add(-time.Hour))
	newer := completeTestJob(t, repo, ids, "user-1", "newer.mp4", time.Now().UTC())

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	jobs, err := repo.FindCompletedByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(jobs) != 2 {
		t.Fatalf("len(jobs) = %d, want 2", len(jobs))
	}
	if !jobs[0].ID().Equal(newer.ID()) || !jobs[1].ID().Equal(older.ID()) {
		t.Fatalf("jobs = [%v, %v], want [newer, older]", jobs[0].ID(), jobs[1].ID())
	}
}

// TestRepository_SourceKeyRoundTripsThroughEveryReadMethod covers all four
// read paths at once, because they share one scan helper and a mistake in it
// reaches all of them. Both keys are asserted, not just the new one: they are
// the same type and adjacent in RestoreVideoJob's signature, so transposing
// them compiles and — for a completed job — passes the aggregate's own
// validation, surfacing only as GET /download/:filename rejecting every
// result.
func TestRepository_SourceKeyRoundTripsThroughEveryReadMethod(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job := newTestJobWithSourceKey(t, ids, "user-1", "movie.mp4", "uploads/upload-1_movie.mp4", time.Now().UTC())
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	resultKey := domain.ResultStorageKey(job.ID())
	epoch := driveCreatedJobToProcessing(t, repo, job)
	if err := job.Complete(resultKey, 3); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := repo.Update(ctx, job, epoch); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	assertKeys := func(t *testing.T, method string, found *domain.VideoJob) {
		t.Helper()
		if !found.SourceKey().Equal(job.SourceKey()) {
			t.Fatalf("%s: SourceKey = %v, want %v", method, found.SourceKey(), job.SourceKey())
		}
		if !found.StorageKey().Equal(resultKey) {
			t.Fatalf("%s: StorageKey = %v, want %v", method, found.StorageKey(), resultKey)
		}
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	assertKeys(t, "FindByID", found)

	page, err := repo.FindByUserID(ctx, job.UserID(), 0, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(page) != 1 {
		t.Fatalf("len(FindByUserID) = %d, want 1", len(page))
	}
	assertKeys(t, "FindByUserID", page[0])

	completed, err := repo.FindCompletedByUserID(ctx, job.UserID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("len(FindCompletedByUserID) = %d, want 1", len(completed))
	}
	assertKeys(t, "FindCompletedByUserID", completed[0])
}

// TestRepository_FindByID_PreMigrationRowLoadsWithAnEmptySourceKey is the
// migration hazard this change turns on. source_key ships with an empty
// default and cannot be backfilled, and a row can legitimately already be
// sitting in queued — POST /upload drives the whole sequence inside one
// request, so a crash or a client disconnect strands one. Such a row must
// load, not error: pairing the field with status at reconstitution would
// turn every one of them into a FindByID failure at deploy time.
func TestRepository_FindByID_PreMigrationRowLoadsWithAnEmptySourceKey(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	// Inserted directly, without source_key, exactly as a row written before
	// the column existed reads back today.
	id := ids.NewVideoJobID()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO video_jobs (id, user_id, original_filename, status, frame_count, error_reason, storage_key, created_at)
		VALUES ($1, $2, $3, $4, 0, '', '', $5)
	`, id.String(), "user-1", "legacy.mp4", string(domain.JobStatusQueued), time.Now().UTC()); err != nil {
		t.Fatalf("unexpected error inserting a pre-migration row: %v", err)
	}

	found, err := repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("unexpected error loading a pre-migration row: %v", err)
	}
	if !found.SourceKey().IsZero() {
		t.Fatalf("SourceKey = %v, want unset", found.SourceKey())
	}
	if found.Status() != domain.JobStatusQueued {
		t.Fatalf("Status = %v, want %v", found.Status(), domain.JobStatusQueued)
	}
}

// seedJobInStatus persists a job already driven to status, returning it.
func seedJobInStatus(t *testing.T, repo *postgres.Repository, ids domain.VideoJobIDGenerator, status domain.JobStatus) *domain.VideoJob {
	t.Helper()
	job, _ := seedJobInStatusAtEpoch(t, repo, ids, status)
	return job
}

// seedJobInStatusAtEpoch is seedJobInStatus plus the epoch the claim won,
// which a terminal write has to carry.
//
// Every state is reached through the aggregate's own transitions and through
// the repository method that owns that edge — Enqueue for queued,
// ClaimForProcessing for processing — rather than by writing the end state
// straight over the created row. That is not ceremony: Update is conditional
// on the row already being processing at the caller's epoch, so the short
// cut no longer persists anything.
func seedJobInStatusAtEpoch(t *testing.T, repo *postgres.Repository, ids domain.VideoJobIDGenerator, status domain.JobStatus) (*domain.VideoJob, int64) {
	t.Helper()
	ctx := context.Background()

	job := newTestJob(t, ids, "user-1", "video.mp4", time.Now().UTC().Truncate(time.Microsecond))
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if status == domain.JobStatusPending {
		return job, 0
	}

	if err := job.Enqueue(); err != nil {
		t.Fatalf("unexpected error enqueuing: %v", err)
	}
	if err := repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("unexpected error persisting the enqueue: %v", err)
	}
	if status == domain.JobStatusQueued {
		return job, 0
	}

	epoch := claimSeededJob(t, repo, job)

	if status == domain.JobStatusProcessing {
		return job, epoch
	}

	switch status {
	case domain.JobStatusCompleted:
		storageKey, err := domain.NewStorageKey("frames_" + job.ID().String() + ".zip")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if err := job.Complete(storageKey, 7); err != nil {
			t.Fatalf("unexpected error completing: %v", err)
		}
	case domain.JobStatusFailed:
		if err := job.Fail("boom"); err != nil {
			t.Fatalf("unexpected error failing: %v", err)
		}
	}

	applied, err := repo.Update(ctx, job, epoch)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !applied {
		t.Fatal("Update reported applied = false while seeding a terminal job")
	}
	return job, epoch
}

// driveCreatedJobToProcessing takes a job whose row exists in pending
// through the two persisted edges that reach processing, returning the epoch
// the claim won. Every terminal write in these tests goes through it,
// because Update refuses anything else.
func driveCreatedJobToProcessing(t *testing.T, repo *postgres.Repository, job *domain.VideoJob) int64 {
	t.Helper()

	if err := job.Enqueue(); err != nil {
		t.Fatalf("unexpected error enqueuing: %v", err)
	}
	if err := repo.Enqueue(context.Background(), job); err != nil {
		t.Fatalf("unexpected error persisting the enqueue: %v", err)
	}
	return claimSeededJob(t, repo, job)
}

// claimSeededJob takes a queued job through the conditional claim, leaving
// both the aggregate and the row in processing, and returns the epoch the
// claim won.
func claimSeededJob(t *testing.T, repo *postgres.Repository, job *domain.VideoJob) int64 {
	t.Helper()

	if err := job.StartProcessing(); err != nil {
		t.Fatalf("unexpected error starting processing: %v", err)
	}
	claimed, epoch, err := repo.ClaimForProcessing(context.Background(), job)
	if err != nil {
		t.Fatalf("unexpected error claiming: %v", err)
	}
	if !claimed {
		t.Fatal("ClaimForProcessing reported claimed = false while seeding a processing job")
	}
	return epoch
}

// TestRepository_ClaimForProcessing_ClaimsAQueuedRow is the happy path of the
// primitive that makes at-least-once delivery safe. Reporting claimed=true is
// only half of it: the row itself has to be in processing afterwards, since
// the UPDATE never reads the aggregate handed to it and an implementation
// that wrote nothing at all could still return true.
func TestRepository_ClaimForProcessing_ClaimsAQueuedRow(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job := seedJobInStatus(t, repo, ids, domain.JobStatusQueued)

	claimed, epoch, err := repo.ClaimForProcessing(ctx, job)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !claimed {
		t.Fatal("claimed = false, want true for a queued row")
	}
	if epoch != 0 {
		t.Fatalf("epoch = %d, want 0 for a job that has never been requeued", epoch)
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found.Status() != domain.JobStatusProcessing {
		t.Fatalf("Status = %v, want %v", found.Status(), domain.JobStatusProcessing)
	}
}

// TestRepository_ClaimForProcessing_RefusesEveryOtherStatus covers the
// predicate's whole complement. completed and failed are the cases with
// teeth: a claim that ignored the current status would overwrite a terminal
// row, throwing away a result that was already delivered.
func TestRepository_ClaimForProcessing_RefusesEveryOtherStatus(t *testing.T) {
	for _, status := range []domain.JobStatus{
		domain.JobStatusPending,
		domain.JobStatusProcessing,
		domain.JobStatusCompleted,
		domain.JobStatusFailed,
	} {
		t.Run(string(status), func(t *testing.T) {
			db := testDB(t)
			ids := idgen.New()
			repo := postgres.NewRepository(db, ids)
			ctx := context.Background()

			job := seedJobInStatus(t, repo, ids, status)
			before, err := repo.FindByID(ctx, job.ID())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			claimed, _, err := repo.ClaimForProcessing(ctx, job)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if claimed {
				t.Fatalf("claimed = true, want false for a %v row", status)
			}

			after, err := repo.FindByID(ctx, job.ID())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if after.Status() != before.Status() {
				t.Fatalf("Status = %v, want the row left in %v", after.Status(), before.Status())
			}
			if after.StorageKey() != before.StorageKey() {
				t.Fatalf("StorageKey = %v, want %v", after.StorageKey(), before.StorageKey())
			}
			if after.FrameCount() != before.FrameCount() {
				t.Fatalf("FrameCount = %d, want %d", after.FrameCount(), before.FrameCount())
			}
			if after.ErrorReason() != before.ErrorReason() {
				t.Fatalf("ErrorReason = %q, want %q", after.ErrorReason(), before.ErrorReason())
			}
		})
	}
}

// TestRepository_ClaimForProcessing_ConcurrentClaimsProduceExactlyOneWinner
// is the reason the claim is a single conditional UPDATE rather than a read
// followed by a write. Two consumers handed the same message run this
// against one row; under READ COMMITTED the loser blocks on the row lock,
// re-evaluates status = 'queued' after the winner commits, and matches
// nothing.
func TestRepository_ClaimForProcessing_ConcurrentClaimsProduceExactlyOneWinner(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job := seedJobInStatus(t, repo, ids, domain.JobStatusQueued)

	const consumers = 8
	start := make(chan struct{})
	results := make(chan bool, consumers)
	errs := make(chan error, consumers)
	var wg sync.WaitGroup
	for i := 0; i < consumers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			claimed, _, err := repo.ClaimForProcessing(ctx, job)
			if err != nil {
				errs <- err
				return
			}
			results <- claimed
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)

	for err := range errs {
		t.Fatalf("unexpected error: %v", err)
	}
	winners := 0
	for claimed := range results {
		if claimed {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
}

// TestRepository_ClaimForProcessing_UnknownIDIsDistinctFromALostClaim pins
// the distinction the follow-up SELECT exists for. Both outcomes report
// claimed=false, and the worker treats them very differently: a lost claim
// is a duplicate delivery to ack quietly, while a job that does not exist is
// a message to dead-letter.
func TestRepository_ClaimForProcessing_UnknownIDIsDistinctFromALostClaim(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	unknown := newTestJob(t, ids, "user-1", "video.mp4", time.Now().UTC())
	claimed, _, err := repo.ClaimForProcessing(ctx, unknown)
	if !errors.Is(err, domain.ErrVideoJobNotFound) {
		t.Fatalf("error = %v, want %v", err, domain.ErrVideoJobNotFound)
	}
	if claimed {
		t.Fatal("claimed = true, want false for an unknown id")
	}

	// The contrast: a row that exists but is no longer queued reports the
	// same claimed=false with no error at all.
	taken := seedJobInStatus(t, repo, ids, domain.JobStatusProcessing)
	claimed, _, err = repo.ClaimForProcessing(ctx, taken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if claimed {
		t.Fatal("claimed = true, want false for a row already taken")
	}
}
