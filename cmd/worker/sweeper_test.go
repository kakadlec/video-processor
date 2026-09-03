package main

import (
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	videoapplication "video-processor/internal/video/application"
	videodomain "video-processor/internal/video/domain"
	videomessaging "video-processor/internal/video/infrastructure/messaging"
)

// claimSeededJob puts a queued job into processing the way a worker does,
// returning the epoch that claim won. Tests about recovery all start here:
// the state the sweep examines is one only a real claim produces.
func claimSeededJob(t *testing.T, env *workerTestEnv, job *videodomain.VideoJob) int64 {
	t.Helper()
	ctx := context.Background()

	claim, err := videoapplication.NewStartProcessing(env.repo, env.repo, env.ids).Execute(ctx, job.ID().String())
	if err != nil {
		t.Fatalf("claim job %s: %v", job.ID().String(), err)
	}
	return claim.LeaseEpoch
}

// dropLease removes a job's lease the way a worker's death does: the process
// stops renewing and the key lapses. Deleting it outright is the same
// observable state without the 90-second wait.
func dropLease(t *testing.T, env *workerTestEnv, job *videodomain.VideoJob) {
	t.Helper()
	if err := env.deps.redis.Del(context.Background(), "videojob:lease:"+job.ID().String()).Err(); err != nil {
		t.Fatalf("drop lease for job %s: %v", job.ID().String(), err)
	}
}

// takeLease holds a job's lease at an epoch, standing in for a healthy
// worker that is still renewing it.
func takeLease(t *testing.T, env *workerTestEnv, job *videodomain.VideoJob, epoch int64) {
	t.Helper()
	acquired, err := env.deps.leases.Acquire(context.Background(), job.ID(), epoch)
	if err != nil {
		t.Fatalf("acquire lease for job %s: %v", job.ID().String(), err)
	}
	if !acquired {
		t.Fatalf("lease for job %s was refused", job.ID().String())
	}
}

// ownJobsOnly hides every processing row but the ones a test seeded.
//
// The worker package's test database is deliberately never truncated — a
// drain-deadline test parks a goroutine inside an extraction, and a truncate
// would race it — so the video_jobs table accumulates processing rows across
// runs. A sweep over all of them would both bury the job under test behind
// earlier batches and act on rows belonging to other tests.
//
// The real FindProcessing still does the work: this pages through it with
// its own cursor and limit, so the keyset query, its index, and the
// sweeper's own cursor arithmetic are all still exercised.
type ownJobsOnly struct {
	videodomain.VideoJobRepository
	mine map[string]bool
}

func (r *ownJobsOnly) FindProcessing(ctx context.Context, after videodomain.VideoJobID, limit int) ([]*videodomain.VideoJob, error) {
	var out []*videodomain.VideoJob
	cursor := after
	for len(out) < limit {
		batch, err := r.VideoJobRepository.FindProcessing(ctx, cursor, limit)
		if err != nil {
			return nil, err
		}
		if len(batch) == 0 {
			break
		}
		for _, job := range batch {
			if r.mine[job.ID().String()] && len(out) < limit {
				out = append(out, job)
			}
		}
		cursor = batch[len(batch)-1].ID()
		if len(batch) < limit {
			break
		}
	}
	return out, nil
}

// sweeperFor builds a sweeper that sees only the given jobs.
func sweeperFor(t *testing.T, env *workerTestEnv, jobs ...*videodomain.VideoJob) *sweeper {
	t.Helper()

	mine := make(map[string]bool, len(jobs))
	for _, job := range jobs {
		mine[job.ID().String()] = true
	}
	scoped := *env.deps
	scoped.jobReader = &ownJobsOnly{VideoJobRepository: env.deps.jobReader, mine: mine}
	return newSweeper(&scoped)
}

// sweepTwice runs the confirmation the sweep requires before acting. One
// observation is never enough: a claim commits in PostgreSQL and its lease
// is set in Redis immediately afterwards, so every healthy run is briefly
// unleased.
func sweepTwice(ctx context.Context, s *sweeper) {
	s.sweep(ctx)
	s.sweep(ctx)
}

func queuedOutboxRows(t *testing.T, env *workerTestEnv, job *videodomain.VideoJob) int {
	t.Helper()

	var count int
	if err := env.db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM video_job_outbox WHERE payload->>'job_id' = $1`,
		job.ID().String(),
	).Scan(&count); err != nil {
		t.Fatalf("count outbox rows for job %s: %v", job.ID().String(), err)
	}
	return count
}

// TestSweep_RequeuesAnAbandonedJobAndDispatchesItAgain is the sweep's whole
// purpose. Before this existed a worker that died mid-extraction left its
// job in processing with no lease, no timeout, and no edge back to queued —
// nothing redelivered it and nothing reclaimed it.
func TestSweep_RequeuesAnAbandonedJobAndDispatchesItAgain(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, _ := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	epoch := claimSeededJob(t, env, job)
	dropLease(t, env, job)
	before := queuedOutboxRows(t, env, job)

	sweepTwice(ctx, sweeperFor(t, env, job))

	stored, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusQueued {
		t.Fatalf("status = %q, want %q", stored.Status(), videodomain.JobStatusQueued)
	}
	if stored.LeaseEpoch() != epoch+1 {
		t.Fatalf("lease epoch = %d, want %d", stored.LeaseEpoch(), epoch+1)
	}
	if got := queuedOutboxRows(t, env, job); got != before+1 {
		t.Fatalf("outbox rows = %d, want %d — a requeue with no dispatch is a job nothing will pick up", got, before+1)
	}
	if !objectExists(t, env, job.SourceKey().String()) {
		t.Fatal("the source object was deleted for a job that is about to be processed again")
	}
}

// TestSweep_LeavesALiveJobAlone is the other half, and the more dangerous
// one to get wrong: taking a job away from a worker that is still extracting
// it produces two runs of the same job, which is exactly what the claim was
// built to prevent.
func TestSweep_LeavesALiveJobAlone(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, _ := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	epoch := claimSeededJob(t, env, job)
	takeLease(t, env, job, epoch)

	sweepTwice(ctx, sweeperFor(t, env, job))

	stored, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q, want %q — a leased job is being worked on", stored.Status(), videodomain.JobStatusProcessing)
	}
	if stored.LeaseEpoch() != epoch {
		t.Fatalf("lease epoch = %d, want it left at %d", stored.LeaseEpoch(), epoch)
	}
}

// TestSweep_ActsOnlyOnASecondConfirmation pins the two-cycle rule. Between a
// claim committing and its lease being set, a job is legitimately unleased;
// acting on that single observation would requeue a run that had just
// started, and at the bound it would fail one and delete its source.
func TestSweep_ActsOnlyOnASecondConfirmation(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, _ := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	epoch := claimSeededJob(t, env, job)
	dropLease(t, env, job)

	s := sweeperFor(t, env, job)
	s.sweep(ctx)

	stored, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q after one cycle, want %q — one observation is not a confirmation", stored.Status(), videodomain.JobStatusProcessing)
	}

	// The lease coming back before the second cycle is a worker that was
	// only briefly unobserved, and it must clear the mark rather than be
	// acted on.
	takeLease(t, env, job, epoch)
	s.sweep(ctx)
	if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q, want %q — a lease observed on the second cycle must clear the mark", status, videodomain.JobStatusProcessing)
	}
}

// TestSweep_FailsAJobAtTheRequeueBound covers the exit from the loop. An
// input that reliably kills its worker would otherwise be re-dispatched
// forever, taking down each replica in turn.
func TestSweep_FailsAJobAtTheRequeueBound(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, _ := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	// Drive the job to the bound through the real edges: claim, abandon,
	// requeue, repeat.
	for i := 0; i < maxRequeues; i++ {
		epoch := claimSeededJob(t, env, job)
		dropLease(t, env, job)
		sweepTwice(ctx, sweeperFor(t, env, job))

		stored, err := env.repo.FindByID(ctx, job.ID())
		if err != nil {
			t.Fatalf("reload job: %v", err)
		}
		if stored.LeaseEpoch() != epoch+1 {
			t.Fatalf("after requeue %d: lease epoch = %d, want %d", i+1, stored.LeaseEpoch(), epoch+1)
		}
		job = stored
	}

	// One more abandonment, this time at the bound.
	epoch := claimSeededJob(t, env, job)
	if epoch != maxRequeues {
		t.Fatalf("epoch at the bound = %d, want %d", epoch, maxRequeues)
	}
	dropLease(t, env, job)

	key, err := videodomain.NewIdempotencyKey(job.UserID().String(), testContentHash)
	if err != nil {
		t.Fatalf("build idempotency key: %v", err)
	}
	token, reserved, err := env.keys.Reserve(ctx, key)
	if err != nil || !reserved {
		t.Fatalf("reserve idempotency key: reserved=%v err=%v", reserved, err)
	}
	if finalized, err := env.keys.Finalize(ctx, key, token, job.ID()); err != nil || !finalized {
		t.Fatalf("finalize idempotency key: finalized=%v err=%v", finalized, err)
	}

	sweepTwice(ctx, sweeperFor(t, env, job))

	stored, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusFailed {
		t.Fatalf("status = %q, want %q at the requeue bound", stored.Status(), videodomain.JobStatusFailed)
	}
	if stored.ErrorReason() == "" {
		t.Fatal("a failed job carries no reason")
	}
	for _, leak := range []string{"redis", "minio", "amqp", "postgres", "9000", env.bucket} {
		if strings.Contains(strings.ToLower(stored.ErrorReason()), strings.ToLower(leak)) {
			t.Fatalf("error reason %q names infrastructure (%q)", stored.ErrorReason(), leak)
		}
	}
	if objectExists(t, env, job.SourceKey().String()) {
		t.Fatal("the source object survived a job the sweep gave up on")
	}
	if _, found, err := env.keys.Lookup(ctx, key); err != nil || found {
		t.Fatalf("the idempotency key survived an abandoned job: found=%v err=%v", found, err)
	}
}

// TestSweep_FailsAProcessingJobWithNoSourceOnTheFirstRecovery covers the
// pre-migration rows. Requeueing one is a loop with no exit — the aggregate
// rejects a requeue with no source, so the epoch never advances and the
// bound is never reached — and filtering them out of the scan would leave
// them stranded, which is the condition this sweep exists to end.
func TestSweep_FailsAProcessingJobWithNoSourceOnTheFirstRecovery(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	ctx := context.Background()

	id := env.ids.NewVideoJobID()
	if _, err := env.db.ExecContext(ctx, `
		INSERT INTO video_jobs (id, user_id, original_filename, status, created_at)
		VALUES ($1, $2, $3, $4, $5)
	`, id.String(), "user-1", "legacy.mp4", string(videodomain.JobStatusProcessing), time.Now().UTC()); err != nil {
		t.Fatalf("insert a pre-migration processing row: %v", err)
	}

	legacy, err := env.repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("reload the pre-migration row: %v", err)
	}

	sweepTwice(ctx, sweeperFor(t, env, legacy))

	stored, err := env.repo.FindByID(ctx, id)
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusFailed {
		t.Fatalf("status = %q, want %q — a job with no source can never be re-dispatched", stored.Status(), videodomain.JobStatusFailed)
	}
	if stored.LeaseEpoch() != 0 {
		t.Fatalf("lease epoch = %d, want 0 — the job was failed, never requeued", stored.LeaseEpoch())
	}
}

// TestSweep_TakesOverNothingWhenTheLeaseStoreIsUnreachable is the fail-closed
// half of the posture, and the one with the worst failure mode: "cannot
// reach Redis" reported as "not held" would hand every running job to a
// second worker at the same instant.
func TestSweep_TakesOverNothingWhenTheLeaseStoreIsUnreachable(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, _ := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	epoch := claimSeededJob(t, env, job)
	logs := captureLogs(t)

	if err := env.deps.redis.Close(); err != nil {
		t.Fatalf("close redis: %v", err)
	}

	sweepTwice(ctx, sweeperFor(t, env, job))

	stored, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q, want %q — an unreachable lease store is no evidence a lease lapsed", stored.Status(), videodomain.JobStatusProcessing)
	}
	if stored.LeaseEpoch() != epoch {
		t.Fatalf("lease epoch = %d, want it left at %d", stored.LeaseEpoch(), epoch)
	}
	if !strings.Contains(logs.String(), "lease store unreachable") {
		t.Fatalf("logs do not report the outage:\n%s", logs.String())
	}
}

type leaseStoreFailingSecondHeld struct {
	videodomain.JobLeaseStore
	calls int
}

func (s *leaseStoreFailingSecondHeld) Held(context.Context, videodomain.VideoJobID, int64) (bool, error) {
	s.calls++
	if s.calls == 2 {
		return false, errors.New("simulated lease-store outage")
	}
	return false, nil
}

// TestSweep_DiscardsPreOutageConfirmation requires two fresh successful
// not-held observations after the lease store recovers. A mark retained from
// before an outage could otherwise combine with the first post-recovery read
// and requeue a live worker before its heartbeat recreates the expired key.
func TestSweep_DiscardsPreOutageConfirmation(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, _ := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	claimSeededJob(t, env, job)
	dropLease(t, env, job)

	s := sweeperFor(t, env, job)
	s.deps.leases = &leaseStoreFailingSecondHeld{JobLeaseStore: env.deps.leases}

	s.sweep(ctx) // First successful not-held observation marks the job.
	s.sweep(ctx) // The outage must discard that mark.
	s.sweep(ctx) // First post-recovery observation only marks it again.

	if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q, want %q — a pre-outage mark confirmed a post-outage observation", status, videodomain.JobStatusProcessing)
	}

	s.sweep(ctx) // The second fresh observation may now recover it.
	if status := statusOf(t, env, job); status != videodomain.JobStatusQueued {
		t.Fatalf("status = %q, want %q after two fresh post-outage observations", status, videodomain.JobStatusQueued)
	}
}

// TestSweep_ReachesAJobBehindAFullBatchOfLeasedOnes is the keyset cursor's
// reason for existing. A fixed ORDER BY id LIMIT n passes every other test
// here and fails this one: a batch's worth of healthy long-running
// extractions sorts first every cycle, and the abandoned job behind them
// would never be examined.
func TestSweep_ReachesAJobBehindAFullBatchOfLeasedOnes(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	ctx := context.Background()

	// More leased processing jobs than one batch holds, then the abandoned
	// one — which sorts last, because ids are ordered and this one is
	// created last only if that happens to hold. It does not, so the search
	// below finds whichever job actually sorts last and abandons that one.
	jobs := make([]*videodomain.VideoJob, 0, sweepBatchSize+1)
	epochs := make(map[string]int64, sweepBatchSize+1)
	for i := 0; i < sweepBatchSize+1; i++ {
		job, _ := seedQueuedJob(t, env, generateTestVideo(t, 1))
		epochs[job.ID().String()] = claimSeededJob(t, env, job)
		jobs = append(jobs, job)
	}

	last := jobs[0]
	for _, job := range jobs {
		if job.ID().String() > last.ID().String() {
			last = job
		}
	}
	for _, job := range jobs {
		if job.ID().Equal(last.ID()) {
			continue
		}
		takeLease(t, env, job, epochs[job.ID().String()])
	}
	dropLease(t, env, last)

	// Enough cycles to wrap the scan and confirm the mark: the first
	// rotation is two full batches, and the second is what confirms.
	s := sweeperFor(t, env, jobs...)
	for i := 0; i < 6; i++ {
		s.sweep(ctx)
	}

	stored, err := env.repo.FindByID(ctx, last.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusQueued {
		t.Fatalf("status = %q, want %q — the scan never reached the job behind a full batch", stored.Status(), videodomain.JobStatusQueued)
	}
}

// TestRecovery_FencesTheOriginalClaimantAfterASweep is the change's whole
// reason for existing, as one test. Recovery that returned a job to the
// queue without fencing its previous holder would be worse than no recovery
// at all: the successor extracts, and the original claimant — still alive,
// just slow — overwrites its outcome.
func TestRecovery_FencesTheOriginalClaimantAfterASweep(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, _ := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	originalEpoch := claimSeededJob(t, env, job)
	dropLease(t, env, job)

	sweepTwice(ctx, sweeperFor(t, env, job))

	requeued, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if requeued.Status() != videodomain.JobStatusQueued {
		t.Fatalf("status = %q, want %q", requeued.Status(), videodomain.JobStatusQueued)
	}
	if queuedOutboxRows(t, env, job) < 2 {
		t.Fatal("the recovery wrote no new dispatch row")
	}

	// The successor claims it; the original claimant, still running, then
	// tries to record its own completion at the epoch it has held all along.
	successorEpoch := claimSeededJob(t, env, requeued)
	if successorEpoch != originalEpoch+1 {
		t.Fatalf("successor epoch = %d, want %d", successorEpoch, originalEpoch+1)
	}

	_, err = videoapplication.NewCompleteJob(env.repo, env.repo, env.ids).Execute(ctx, videoapplication.CompleteJobInput{
		JobID:      job.ID().String(),
		StorageKey: videodomain.ResultStorageKey(job.ID()).String(),
		FrameCount: 1,
		LeaseEpoch: originalEpoch,
	})
	if err == nil {
		t.Fatal("the original claimant's completion was accepted after its job was recovered")
	}
	if !errors.Is(err, videodomain.ErrJobFenced) {
		t.Fatalf("error = %v, want a fence", err)
	}
	if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q, want %q — the successor still owns the job", status, videodomain.JobStatusProcessing)
	}
}

// TestHandle_AFencedRunKeepsItsSourceAndClearsNothing is task 7.8's
// disposition row. A run taken over mid-extraction must not clean up after
// itself: the source object is the successor's input, and the idempotency
// key names a job that is running again.
func TestHandle_AFencedRunKeepsItsSourceAndClearsNothing(t *testing.T) {
	release := make(chan struct{})
	env := newWorkerTestEnv(t, envOptions{release: release})
	job, body := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	key, err := videodomain.NewIdempotencyKey(job.UserID().String(), testContentHash)
	if err != nil {
		t.Fatalf("build idempotency key: %v", err)
	}
	token, reserved, err := env.keys.Reserve(ctx, key)
	if err != nil || !reserved {
		t.Fatalf("reserve idempotency key: reserved=%v err=%v", reserved, err)
	}
	if finalized, err := env.keys.Finalize(ctx, key, token, job.ID()); err != nil || !finalized {
		t.Fatalf("finalize idempotency key: finalized=%v err=%v", finalized, err)
	}

	disposition := make(chan videomessaging.Disposition, 1)
	go func() {
		var inFlight atomic.Pointer[string]
		disposition <- env.deps.handle(ctx, body, &inFlight)
	}()

	// Wait until this worker holds the claim and is inside ffmpeg, then take
	// the job away from it exactly as a sweep plus a successor would.
	waitFor(t, 30*time.Second, "the extraction to start", func() bool {
		return len(env.extractor.started) > 0
	})
	taken, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if err := taken.Requeue(); err != nil {
		t.Fatalf("requeue job: %v", err)
	}
	if requeued, err := env.repo.Requeue(ctx, taken, 0); err != nil || !requeued {
		t.Fatalf("persist the requeue: requeued=%v err=%v", requeued, err)
	}
	successorEpoch := claimSeededJob(t, env, taken)

	close(release)

	if got := <-disposition; got != videomessaging.Reject {
		t.Fatalf("disposition = %v, want Reject — a fenced outcome must not be acknowledged", got)
	}
	if !objectExists(t, env, job.SourceKey().String()) {
		t.Fatal("a fenced run deleted the source object its successor is about to read")
	}
	if _, found, err := env.keys.Lookup(ctx, key); err != nil || !found {
		t.Fatalf("a fenced run cleared the idempotency key of a job that is running again: found=%v err=%v", found, err)
	}
	stored, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusProcessing || stored.LeaseEpoch() != successorEpoch {
		t.Fatalf("job = %q at epoch %d, want processing at epoch %d — the successor's row was overwritten", stored.Status(), stored.LeaseEpoch(), successorEpoch)
	}
}

// blockingProcessingScan holds a sweep inside its scan until it is released,
// which is the only moment a shutdown can be observed racing one.
type blockingProcessingScan struct {
	videodomain.VideoJobRepository
	started chan struct{}
	release chan struct{}
	once    sync.Once
}

func (b *blockingProcessingScan) FindProcessing(ctx context.Context, after videodomain.VideoJobID, limit int) ([]*videodomain.VideoJob, error) {
	b.once.Do(func() { close(b.started) })
	<-b.release
	return b.VideoJobRepository.FindProcessing(ctx, after, limit)
}

// TestRun_ShutdownWaitsForAnInFlightSweep pins task 6.5's ordering. main
// closes PostgreSQL and Redis the moment run returns, and a sweep holds a
// transaction while it runs — returning underneath one would abort a claim
// instead of resolving it. Every functional sweeper test passes without this
// join, which is exactly why it is asserted separately.
func TestRun_ShutdownWaitsForAnInFlightSweep(t *testing.T) {
	conn := openTestConn(t)
	env := newWorkerTestEnv(t, envOptions{})
	topo := testTopology(t, conn)

	blocking := &blockingProcessingScan{
		VideoJobRepository: env.deps.jobReader,
		started:            make(chan struct{}),
		release:            make(chan struct{}),
	}
	env.deps.jobReader = blocking

	cancel, done := startWorkerSweeping(t, env, topo, time.Second, 20*time.Millisecond)

	select {
	case <-blocking.started:
	case <-time.After(30 * time.Second):
		t.Fatal("the sweep never started")
	}

	cancel()

	select {
	case <-done:
		t.Fatal("run returned while a sweep was still in flight; main would have closed PostgreSQL underneath it")
	case <-time.After(300 * time.Millisecond):
	}

	// The dependencies the sweep is using are still open, which is the
	// property the join exists to preserve.
	if err := env.db.PingContext(context.Background()); err != nil {
		t.Fatalf("PostgreSQL was closed under a live sweep: %v", err)
	}

	close(blocking.release)

	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after the sweep finished")
	}
}
