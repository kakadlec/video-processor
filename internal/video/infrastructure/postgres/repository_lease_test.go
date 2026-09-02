package postgres_test

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idgen"
	"video-processor/internal/video/infrastructure/postgres"
)

// queuedEventType is the current dispatch generation's event type, spelled
// out rather than imported: these tests assert what a consumer of the outbox
// would see, and a constant they shared with the code under test would move
// with it silently.
const queuedEventType = "video_job.queued.v2"

func countQueuedOutboxRows(t *testing.T, db *sql.DB, job *domain.VideoJob) int {
	t.Helper()

	var count int
	err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM video_job_outbox WHERE event_type = $1 AND payload->>'job_id' = $2`,
		queuedEventType, job.ID().String(),
	).Scan(&count)
	if err != nil {
		t.Fatalf("counting queued outbox rows: %v", err)
	}
	return count
}

func storedEpoch(t *testing.T, db *sql.DB, job *domain.VideoJob) int64 {
	t.Helper()

	var epoch int64
	if err := db.QueryRowContext(context.Background(), `SELECT lease_epoch FROM video_jobs WHERE id = $1`, job.ID().String()).Scan(&epoch); err != nil {
		t.Fatalf("reading lease_epoch: %v", err)
	}
	return epoch
}

// TestRepository_Enqueue_StillDispatchesAfterTheStatementSplit is the
// regression the statement split invites and a fenced-Update test would
// never catch: Enqueue and Update stopped sharing one UPDATE, and an Enqueue
// left on the fenced statement would affect no row — its job would stay
// pending while its outbox row announced a dispatch for it.
func TestRepository_Enqueue_StillDispatchesAfterTheStatementSplit(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job := newTestJob(t, ids, "user-1", "video.mp4", time.Now().UTC())
	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	if err := repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.Status() != domain.JobStatusQueued {
		t.Fatalf("Status = %v, want %v", found.Status(), domain.JobStatusQueued)
	}
	if got := countQueuedOutboxRows(t, db, job); got != 1 {
		t.Fatalf("queued outbox rows = %d, want 1", got)
	}
	if got := storedEpoch(t, db, job); got != 0 {
		t.Fatalf("lease_epoch = %d, want 0 — enqueuing is not a recovery", got)
	}
}

// TestRepository_ClaimForProcessing_ReportsTheStoredEpochWithoutAdvancingIt
// pins the claim's relationship to the fence: it reports the epoch so the
// winner can carry it, and it is emphatically not the thing that advances
// one. Only a requeue does that, and a claim that also advanced would fence
// out the holder it had just created.
func TestRepository_ClaimForProcessing_ReportsTheStoredEpochWithoutAdvancingIt(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, firstEpoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)

	// One recovery, so the epoch under test is not the zero every fresh row
	// carries — a claim that returned a hardcoded 0 would pass otherwise.
	if err := job.Requeue(); err != nil {
		t.Fatalf("job.Requeue: %v", err)
	}
	requeued, err := repo.Requeue(ctx, job, firstEpoch)
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if !requeued {
		t.Fatal("requeued = false, want true")
	}

	if err := job.StartProcessing(); err != nil {
		t.Fatalf("job.StartProcessing: %v", err)
	}
	claimed, epoch, err := repo.ClaimForProcessing(ctx, job)
	if err != nil {
		t.Fatalf("ClaimForProcessing: %v", err)
	}
	if !claimed {
		t.Fatal("claimed = false, want true")
	}
	if epoch != firstEpoch+1 {
		t.Fatalf("epoch = %d, want %d (the stored epoch after one requeue)", epoch, firstEpoch+1)
	}
	if got := storedEpoch(t, db, job); got != firstEpoch+1 {
		t.Fatalf("lease_epoch = %d, want %d — a claim must not advance it", got, firstEpoch+1)
	}
}

// TestRepository_Update_FencedAtASupersededEpochChangesNothing is the fence
// itself. Every outcome column is asserted, not only the status: a
// superseded holder's completion carries a result key and a frame count, and
// a predicate that let those through would overwrite the successor's record
// of the artifact that was actually delivered.
func TestRepository_Update_FencedAtASupersededEpochChangesNothing(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)

	// The successor: requeued and re-claimed, leaving the row at a newer
	// epoch while this caller still holds the old one.
	superseded := job
	if err := superseded.Requeue(); err != nil {
		t.Fatalf("job.Requeue: %v", err)
	}
	if _, err := repo.Requeue(ctx, superseded, epoch); err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if err := superseded.StartProcessing(); err != nil {
		t.Fatalf("job.StartProcessing: %v", err)
	}
	if _, _, err := repo.ClaimForProcessing(ctx, superseded); err != nil {
		t.Fatalf("ClaimForProcessing: %v", err)
	}

	before, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}

	stale, err := domain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.SourceKey(), job.ContentHash(), domain.StorageKey{}, 0, "", domain.JobStatusProcessing, job.CreatedAt(), epoch)
	if err != nil {
		t.Fatalf("RestoreVideoJob: %v", err)
	}
	if err := stale.Complete(domain.ResultStorageKey(job.ID()), 9); err != nil {
		t.Fatalf("stale.Complete: %v", err)
	}

	applied, err := repo.Update(ctx, stale, epoch)
	if !errors.Is(err, domain.ErrJobFenced) {
		t.Fatalf("error = %v, want %v", err, domain.ErrJobFenced)
	}
	if applied {
		t.Fatal("applied = true, want false")
	}

	after, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if after.Status() != before.Status() || after.FrameCount() != before.FrameCount() ||
		after.StorageKey() != before.StorageKey() || after.ErrorReason() != before.ErrorReason() ||
		after.LeaseEpoch() != before.LeaseEpoch() {
		t.Fatalf("row changed under a fenced write: before = %+v, after = %+v", before, after)
	}
}

// TestRepository_Update_TwoTerminalWritesAtOneEpochProduceOneWinner is the
// test that pins the status = 'processing' conjunct. An epoch-only predicate
// passes every other test in this file and fails this one: the second write
// would find the epoch unchanged (a terminal write does not advance it) and
// overwrite a delivered result with an abandonment, or the reverse.
func TestRepository_Update_TwoTerminalWritesAtOneEpochProduceOneWinner(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)

	completed, err := domain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.SourceKey(), job.ContentHash(), domain.StorageKey{}, 0, "", domain.JobStatusProcessing, job.CreatedAt(), epoch)
	if err != nil {
		t.Fatalf("RestoreVideoJob: %v", err)
	}
	if err := completed.Complete(domain.ResultStorageKey(job.ID()), 4); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	failed, err := domain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.SourceKey(), job.ContentHash(), domain.StorageKey{}, 0, "", domain.JobStatusProcessing, job.CreatedAt(), epoch)
	if err != nil {
		t.Fatalf("RestoreVideoJob: %v", err)
	}
	if err := failed.Fail("video processing was interrupted and could not be recovered"); err != nil {
		t.Fatalf("Fail: %v", err)
	}

	firstApplied, err := repo.Update(ctx, completed, epoch)
	if err != nil {
		t.Fatalf("first Update: %v", err)
	}
	if !firstApplied {
		t.Fatal("first Update applied = false, want true")
	}

	secondApplied, err := repo.Update(ctx, failed, epoch)
	if !errors.Is(err, domain.ErrJobFenced) {
		t.Fatalf("second Update error = %v, want %v", err, domain.ErrJobFenced)
	}
	if secondApplied {
		t.Fatal("second Update applied = true, want false — exactly one terminal write may land")
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.Status() != domain.JobStatusCompleted || found.FrameCount() != 4 {
		t.Fatalf("job = %v/%d frames, want the first writer's completion", found.Status(), found.FrameCount())
	}
}

// TestRepository_Update_ARetryOfItsOwnOutcomeAppliesNothingAndDoesNotFence
// is what makes the worker's terminal write retryable: the response to a
// commit can be lost, and the retry must reach the same disposition rather
// than dead-lettering a job that finished.
func TestRepository_Update_ARetryOfItsOwnOutcomeAppliesNothingAndDoesNotFence(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)
	if err := job.Complete(domain.ResultStorageKey(job.ID()), 4); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if _, err := repo.Update(ctx, job, epoch); err != nil {
		t.Fatalf("first Update: %v", err)
	}

	applied, err := repo.Update(ctx, job, epoch)
	if err != nil {
		t.Fatalf("retry of the same outcome: %v", err)
	}
	if applied {
		t.Fatal("applied = true on a retry, want false")
	}
}

func TestRepository_Requeue_AdvancesTheEpochByOneAndDispatchesOnce(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)
	before := countQueuedOutboxRows(t, db, job)

	if err := job.Requeue(); err != nil {
		t.Fatalf("job.Requeue: %v", err)
	}
	requeued, err := repo.Requeue(ctx, job, epoch)
	if err != nil {
		t.Fatalf("Requeue: %v", err)
	}
	if !requeued {
		t.Fatal("requeued = false, want true")
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.Status() != domain.JobStatusQueued {
		t.Fatalf("Status = %v, want %v", found.Status(), domain.JobStatusQueued)
	}
	if found.LeaseEpoch() != epoch+1 {
		t.Fatalf("LeaseEpoch = %d, want %d", found.LeaseEpoch(), epoch+1)
	}
	if got := countQueuedOutboxRows(t, db, job); got != before+1 {
		t.Fatalf("queued outbox rows = %d, want %d", got, before+1)
	}
}

// TestRepository_Requeue_AtAStaleEpochWritesNothing covers the rollback. The
// outbox row is inserted after the conditional UPDATE and inside the same
// transaction, so a requeue that moved no row must leave no dispatch behind
// — otherwise a job in processing would be handed to a second consumer on
// the strength of a sweep that lost.
func TestRepository_Requeue_AtAStaleEpochWritesNothing(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)
	before := countQueuedOutboxRows(t, db, job)

	if err := job.Requeue(); err != nil {
		t.Fatalf("job.Requeue: %v", err)
	}
	requeued, err := repo.Requeue(ctx, job, epoch+7)
	if err != nil {
		t.Fatalf("Requeue at a stale epoch must not error: %v", err)
	}
	if requeued {
		t.Fatal("requeued = true at a stale epoch, want false")
	}

	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if found.Status() != domain.JobStatusProcessing {
		t.Fatalf("Status = %v, want the row left in %v", found.Status(), domain.JobStatusProcessing)
	}
	if found.LeaseEpoch() != epoch {
		t.Fatalf("LeaseEpoch = %d, want the epoch left at %d", found.LeaseEpoch(), epoch)
	}
	if got := countQueuedOutboxRows(t, db, job); got != before {
		t.Fatalf("queued outbox rows = %d, want %d — the transaction must have rolled back", got, before)
	}
}

// TestRepository_Requeue_ConcurrentSweepsProduceOneWinnerAndOneDispatch is
// the sweeper's own at-most-once guarantee. Two workers sweep on independent
// timers and will overlap; a second dispatch for one recovery would put the
// same job on the queue twice at the same epoch, and both deliveries would
// race the same claim.
func TestRepository_Requeue_ConcurrentSweepsProduceOneWinnerAndOneDispatch(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	job, epoch := seedJobInStatusAtEpoch(t, repo, ids, domain.JobStatusProcessing)
	before := countQueuedOutboxRows(t, db, job)

	const sweeps = 4
	start := make(chan struct{})
	results := make(chan bool, sweeps)
	errs := make(chan error, sweeps)
	var wg sync.WaitGroup
	for i := 0; i < sweeps; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Each sweep loads and transitions its own copy, exactly as
			// two worker processes would.
			mine, err := repo.FindByID(ctx, job.ID())
			if err != nil {
				errs <- err
				return
			}
			if err := mine.Requeue(); err != nil {
				errs <- err
				return
			}
			<-start
			requeued, err := repo.Requeue(ctx, mine, epoch)
			if err != nil {
				errs <- err
				return
			}
			results <- requeued
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
	for requeued := range results {
		if requeued {
			winners++
		}
	}
	if winners != 1 {
		t.Fatalf("winners = %d, want exactly 1", winners)
	}
	if got := countQueuedOutboxRows(t, db, job); got != before+1 {
		t.Fatalf("queued outbox rows = %d, want %d", got, before+1)
	}
}

// TestRepository_ProcessingScanIsIndexed asserts the index from PostgreSQL's
// own catalog rather than only that FindProcessing returns the right rows.
// The functional test passes with no index at all: the sweep runs on a timer
// for the life of the deployment, and without this the regression is a full
// scan of a table that only grows, every interval, forever.
func TestRepository_ProcessingScanIsIndexed(t *testing.T) {
	db := testDB(t)

	var indexDef string
	err := db.QueryRowContext(context.Background(),
		`SELECT indexdef FROM pg_indexes WHERE tablename = 'video_jobs' AND indexname = 'video_jobs_processing_id_idx'`,
	).Scan(&indexDef)
	if errors.Is(err, sql.ErrNoRows) {
		t.Fatal("video_jobs_processing_id_idx is missing; the recovery sweep would scan the whole table every cycle")
	}
	if err != nil {
		t.Fatalf("reading pg_indexes: %v", err)
	}
	for _, want := range []string{"(id)", "processing"} {
		if !strings.Contains(indexDef, want) {
			t.Fatalf("indexdef = %q, want it to contain %q", indexDef, want)
		}
	}
}

// TestRepository_FindProcessing_ResumesStrictlyAfterTheCursor is the keyset
// half of the sweep's starvation guarantee. A fixed ORDER BY id LIMIT n
// passes every other test here: it only fails when a batch's worth of jobs
// sorts ahead of the one that needs recovering, which is exactly the shape a
// busy worker fleet produces.
func TestRepository_FindProcessing_ResumesStrictlyAfterTheCursor(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	const rows = 5
	for i := 0; i < rows; i++ {
		seedJobInStatus(t, repo, ids, domain.JobStatusProcessing)
	}

	// Walk the whole set one row at a time, exactly as the sweep does with
	// a batch smaller than the number of processing jobs.
	var (
		cursor domain.VideoJobID
		seen   []string
	)
	for i := 0; i < rows; i++ {
		batch, err := repo.FindProcessing(ctx, cursor, 1)
		if err != nil {
			t.Fatalf("FindProcessing: %v", err)
		}
		if len(batch) != 1 {
			t.Fatalf("batch %d returned %d rows, want 1", i, len(batch))
		}
		seen = append(seen, batch[0].ID().String())
		cursor = batch[0].ID()
	}

	last, err := repo.FindProcessing(ctx, cursor, 1)
	if err != nil {
		t.Fatalf("FindProcessing past the end: %v", err)
	}
	if len(last) != 0 {
		t.Fatalf("FindProcessing past the last row returned %d rows, want 0", len(last))
	}

	distinct := make(map[string]struct{}, len(seen))
	for _, id := range seen {
		if _, dup := distinct[id]; dup {
			t.Fatalf("job %s was returned twice; the cursor is not strict", id)
		}
		distinct[id] = struct{}{}
	}
	if len(distinct) != rows {
		t.Fatalf("saw %d distinct jobs, want %d", len(distinct), rows)
	}
	for i := 1; i < len(seen); i++ {
		if seen[i-1] >= seen[i] {
			t.Fatalf("ids not ascending: %q then %q", seen[i-1], seen[i])
		}
	}
}

// TestRepository_PreMigrationRowLoadsAtEpochZero covers the deploy itself: a
// row written before the column existed takes the default, and zero is the
// right value rather than a placeholder — such a job has been abandoned
// exactly zero times, so it enters the fence at the same epoch a fresh
// claim holds and the first sweep can recover it.
func TestRepository_PreMigrationRowLoadsAtEpochZero(t *testing.T) {
	db := testDB(t)
	ids := idgen.New()
	repo := postgres.NewRepository(db, ids)
	ctx := context.Background()

	id := ids.NewVideoJobID()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO video_jobs (id, user_id, original_filename, status, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, id.String(), "user-1", "legacy.mp4", string(domain.JobStatusProcessing), time.Now().UTC()); err != nil {
		t.Fatalf("inserting a pre-migration row: %v", err)
	}

	byID, err := repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if byID.LeaseEpoch() != 0 {
		t.Fatalf("FindByID: LeaseEpoch = %d, want 0", byID.LeaseEpoch())
	}

	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	byUser, err := repo.FindByUserID(ctx, userID, 0, 10)
	if err != nil {
		t.Fatalf("FindByUserID: %v", err)
	}
	assertAllAtEpochZero(t, "FindByUserID", byUser)

	processing, err := repo.FindProcessing(ctx, domain.VideoJobID{}, 10)
	if err != nil {
		t.Fatalf("FindProcessing: %v", err)
	}
	if len(processing) != 1 {
		t.Fatalf("FindProcessing returned %d rows, want 1", len(processing))
	}
	assertAllAtEpochZero(t, "FindProcessing", processing)

	// The completed-only read path needs a completed row, which the same
	// pre-migration shape can supply.
	completedID := ids.NewVideoJobID()
	if _, err := db.ExecContext(ctx, `
		INSERT INTO video_jobs (id, user_id, original_filename, status, frame_count, storage_key, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
	`, completedID.String(), "user-1", "legacy-done.mp4", string(domain.JobStatusCompleted), 3, domain.ResultStorageKey(completedID).String(), time.Now().UTC()); err != nil {
		t.Fatalf("inserting a pre-migration completed row: %v", err)
	}
	completed, err := repo.FindCompletedByUserID(ctx, userID)
	if err != nil {
		t.Fatalf("FindCompletedByUserID: %v", err)
	}
	if len(completed) != 1 {
		t.Fatalf("FindCompletedByUserID returned %d rows, want 1", len(completed))
	}
	assertAllAtEpochZero(t, "FindCompletedByUserID", completed)
}

func assertAllAtEpochZero(t *testing.T, method string, jobs []*domain.VideoJob) {
	t.Helper()

	for _, job := range jobs {
		if job.LeaseEpoch() != 0 {
			t.Fatalf("%s: job %s LeaseEpoch = %d, want 0", method, job.ID(), job.LeaseEpoch())
		}
	}
}
