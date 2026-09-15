package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	videodomain "video-processor/internal/video/domain"
	videoidgen "video-processor/internal/video/infrastructure/idgen"
	videomessaging "video-processor/internal/video/infrastructure/messaging"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
)

// countingSources counts source-object reads, so a test can assert that a
// path which must not touch the input did not.
type countingSources struct {
	videodomain.SourceStorage
	gets atomic.Int32
}

func (s *countingSources) Get(ctx context.Context, key videodomain.StorageKey, localPath string) error {
	s.gets.Add(1)
	return s.SourceStorage.Get(ctx, key, localPath)
}

// countingLeases counts lease acquisitions for the same reason.
type countingLeases struct {
	videodomain.JobLeaseStore
	acquires atomic.Int32
}

func (l *countingLeases) Acquire(ctx context.Context, jobID videodomain.VideoJobID, epoch int64) (bool, error) {
	l.acquires.Add(1)
	return l.JobLeaseStore.Acquire(ctx, jobID, epoch)
}

// claimThrough sends the claim statement, and nothing else, to claimer. Every
// other method — the authoritative load included, since StartProcessing reads
// through the undecorated repository — still reaches the real database.
type claimThrough struct {
	videodomain.VideoJobRepository
	claimer videodomain.VideoJobRepository

	mu    sync.Mutex
	calls []time.Time
}

func (r *claimThrough) ClaimForProcessing(ctx context.Context, job *videodomain.VideoJob) (bool, int64, error) {
	r.mu.Lock()
	r.calls = append(r.calls, time.Now())
	r.mu.Unlock()
	return r.claimer.ClaimForProcessing(ctx, job)
}

func (r *claimThrough) callTimes() []time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]time.Time(nil), r.calls...)
}

// loadThrough sends the authoritative load, and nothing else, to loader. It
// has to wrap the reader StartProcessing loads through — the undecorated
// repository, as in setupWorker — because a decorator on the caching writer
// would leave the load on the real database.
type loadThrough struct {
	videodomain.VideoJobRepository
	loader videodomain.VideoJobRepository
}

func (r loadThrough) FindByID(ctx context.Context, id videodomain.VideoJobID) (*videodomain.VideoJob, error) {
	return r.loader.FindByID(ctx, id)
}

// finalizedIdempotencyKey maps the job's content hash to the job, as a
// completed upload leaves it, so a test can assert the key survives.
func finalizedIdempotencyKey(t *testing.T, env *workerTestEnv, job *videodomain.VideoJob) videodomain.IdempotencyKey {
	t.Helper()
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
	return key
}

// unreachableRepository is the real PostgreSQL adapter over a pool whose DSN
// names a port nothing listens on, so its statements never reach a server
// and fail through the adapter's own availability classifier.
func unreachableRepository(t *testing.T) videodomain.VideoJobRepository {
	t.Helper()
	parsed, err := url.Parse(withDatabase(t, os.Getenv("VIDEO_POSTGRES_TEST_DSN"), workerTestDatabase))
	if err != nil {
		t.Fatalf("parse dsn: %v", err)
	}
	parsed.Host = "127.0.0.1:1"
	db, err := videopostgres.Open(videopostgres.Config{DSN: parsed.String()})
	if err != nil {
		t.Fatalf("open unreachable pool: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return videopostgres.NewRepository(db, videoidgen.New())
}

// failTerminalWrite makes the write recording one terminal status report
// that the database could not be reached, after the claim has been won.
type failTerminalWrite struct {
	videodomain.VideoJobRepository
	status videodomain.JobStatus
}

func (r failTerminalWrite) Update(ctx context.Context, job *videodomain.VideoJob, epoch int64) (bool, error) {
	if job.Status() == r.status {
		return false, fmt.Errorf("simulated terminal write outage: %w", videodomain.ErrRepositoryUnavailable)
	}
	return r.VideoJobRepository.Update(ctx, job, epoch)
}

// takeOverBeforeCompletion advances the row's epoch immediately before the
// completion is written, which is what a sweep and a successor claim leave
// behind. The completion is then refused by the real fence.
type takeOverBeforeCompletion struct {
	videodomain.VideoJobRepository
	env *workerTestEnv
}

func (r *takeOverBeforeCompletion) Update(ctx context.Context, job *videodomain.VideoJob, epoch int64) (bool, error) {
	if job.Status() == videodomain.JobStatusCompleted {
		if _, err := r.env.db.ExecContext(ctx, `UPDATE video_jobs SET lease_epoch = lease_epoch + 1 WHERE id = $1`, job.ID().String()); err != nil {
			return false, err
		}
	}
	return r.VideoJobRepository.Update(ctx, job, epoch)
}

// refusedFilename marks the one row the refusing trigger acts on.
const refusedFilename = "refused-by-trigger.mp4"

// refuseClaimsOnMarkedRows installs a trigger that makes the server refuse
// any UPDATE of a row carrying refusedFilename, so the claim's own statement
// fails with an error PostgreSQL answered — not one a test fabricated.
func refuseClaimsOnMarkedRows(t *testing.T, env *workerTestEnv) {
	t.Helper()
	ctx := context.Background()
	for _, statement := range []string{
		`DROP TRIGGER IF EXISTS worker_test_refuse_update ON video_jobs`,
		`CREATE OR REPLACE FUNCTION worker_test_refuse_update() RETURNS trigger LANGUAGE plpgsql AS $$
		 BEGIN
		   RAISE EXCEPTION 'refused by the test trigger' USING ERRCODE = 'insufficient_privilege';
		 END
		 $$`,
		`CREATE TRIGGER worker_test_refuse_update BEFORE UPDATE ON video_jobs
		 FOR EACH ROW WHEN (OLD.original_filename = 'refused-by-trigger.mp4')
		 EXECUTE FUNCTION worker_test_refuse_update()`,
	} {
		if _, err := env.db.ExecContext(ctx, statement); err != nil {
			t.Fatalf("install the refusing trigger: %v", err)
		}
	}
	t.Cleanup(func() {
		_, _ = env.db.ExecContext(context.Background(), `DROP TRIGGER IF EXISTS worker_test_refuse_update ON video_jobs`)
		_, _ = env.db.ExecContext(context.Background(), `DROP FUNCTION IF EXISTS worker_test_refuse_update()`)
	})
}

func dispatchBody(t *testing.T, jobID, sourceKey string) []byte {
	t.Helper()
	body, err := json.Marshal(videomessaging.JobQueuedMessage{
		Type:        videopostgres.VideoJobQueuedEventType,
		JobID:       jobID,
		UserID:      uuid.NewString(),
		SourceKey:   sourceKey,
		ContentHash: testContentHash,
		OccurredAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}
	return body
}

// TestHandle_DispositionTable asserts the disposition table as a whole, in
// both directions. The requeue rows alone would pass with the default branch
// broken; the rows after them are the decision. Three of them separate the
// rule from a call-site one: a claim the database itself refused, a row read
// but not understood, and a dependency outage on the failure write after the
// claim was won all still dead-letter. The completion-write outage looks like
// a duplicate of that last one and is not: it is caught before the switch, so
// only the failure write pins that the unavailability marker alone never
// requeues.
func TestHandle_DispositionTable(t *testing.T) {
	type row struct {
		name  string
		want  videomessaging.Disposition
		build func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T))
	}
	noCheck := func(*testing.T) {}

	rows := []row{
		{
			name: "claim outcome unknown",
			want: videomessaging.Requeue,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				env := newWorkerTestEnv(t, envOptions{
					decorate: func(inner videodomain.VideoJobRepository) videodomain.VideoJobRepository {
						return &claimThrough{VideoJobRepository: inner, claimer: unreachableRepository(t)}
					},
				})
				job, body := seedQueuedJob(t, env, []byte("never read"))
				return env, body, func(t *testing.T) {
					if status := statusOf(t, env, job); status != videodomain.JobStatusQueued {
						t.Fatalf("status = %q, want %q", status, videodomain.JobStatusQueued)
					}
				}
			},
		},
		{
			name: "authoritative load unavailable before any claim",
			want: videomessaging.Requeue,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				sources := &countingSources{}
				leases := &countingLeases{}
				claims := &claimThrough{}
				env := newWorkerTestEnv(t, envOptions{
					wrapReader: func(inner videodomain.VideoJobRepository) videodomain.VideoJobRepository {
						return loadThrough{VideoJobRepository: inner, loader: unreachableRepository(t)}
					},
					// Passes the claim through to the real writer; it is
					// here only to count that none was attempted.
					decorate: func(inner videodomain.VideoJobRepository) videodomain.VideoJobRepository {
						claims.VideoJobRepository = inner
						claims.claimer = inner
						return claims
					},
					wrapSources: func(inner videodomain.SourceStorage) videodomain.SourceStorage {
						sources.SourceStorage = inner
						return sources
					},
					wrapLeases: func(inner videodomain.JobLeaseStore) videodomain.JobLeaseStore {
						leases.JobLeaseStore = inner
						return leases
					},
				})
				job, body := seedQueuedJob(t, env, []byte("never read"))
				key := finalizedIdempotencyKey(t, env, job)
				return env, body, func(t *testing.T) {
					ctx := context.Background()
					if n := len(claims.callTimes()); n != 0 {
						t.Fatalf("claim statements = %d, want 0 — the load failed before any claim", n)
					}
					stored, err := env.repo.FindByID(ctx, job.ID())
					if err != nil {
						t.Fatalf("reload job: %v", err)
					}
					if stored.Status() != videodomain.JobStatusQueued || stored.LeaseEpoch() != job.LeaseEpoch() {
						t.Fatalf("job = %q at epoch %d, want %q at epoch %d", stored.Status(), stored.LeaseEpoch(), videodomain.JobStatusQueued, job.LeaseEpoch())
					}
					if n := leases.acquires.Load(); n != 0 {
						t.Fatalf("lease acquisitions = %d, want 0", n)
					}
					if n := sources.gets.Load(); n != 0 {
						t.Fatalf("source object reads = %d, want 0", n)
					}
					if len(env.extractor.started) != 0 {
						t.Fatal("an extraction started for a job whose load was never answered")
					}
					if !objectExists(t, env, job.SourceKey().String()) {
						t.Fatal("the source object was deleted")
					}
					if mapped, found, err := env.keys.Lookup(ctx, key); err != nil || !found || mapped != job.ID() {
						t.Fatalf("idempotency key: mapped=%v found=%v err=%v, want it to still name %v", mapped, found, err, job.ID())
					}
				}
			},
		},
		{
			name: "lost claim",
			want: videomessaging.Reject,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				env := newWorkerTestEnv(t, envOptions{})
				job, body := seedQueuedJob(t, env, []byte("never read"))
				claimSeededJob(t, env, job)
				return env, body, noCheck
			},
		},
		{
			name: "unknown job",
			want: videomessaging.Reject,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				env := newWorkerTestEnv(t, envOptions{})
				return env, dispatchBody(t, uuid.NewString(), videodomain.SourceStorageKey(uuid.NewString(), "input.mp4").String()), noCheck
			},
		},
		{
			name: "undecodable body",
			want: videomessaging.Reject,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				return newWorkerTestEnv(t, envOptions{}), []byte("{ this is not a dispatch"), noCheck
			},
		},
		{
			name: "missing source key",
			want: videomessaging.Reject,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				env := newWorkerTestEnv(t, envOptions{})
				job, _ := seedQueuedJob(t, env, []byte("never read"))
				return env, dispatchBody(t, job.ID().String(), ""), func(t *testing.T) {
					if status := statusOf(t, env, job); status != videodomain.JobStatusQueued {
						t.Fatalf("status = %q, want %q — a dispatch naming no source must not claim", status, videodomain.JobStatusQueued)
					}
				}
			},
		},
		{
			name: "fenced write",
			want: videomessaging.Reject,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				takeOver := &takeOverBeforeCompletion{}
				env := newWorkerTestEnv(t, envOptions{
					decorate: func(inner videodomain.VideoJobRepository) videodomain.VideoJobRepository {
						takeOver.VideoJobRepository = inner
						return takeOver
					},
				})
				takeOver.env = env
				job, body := seedQueuedJob(t, env, generateTestVideo(t, 1))
				return env, body, func(t *testing.T) {
					if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
						t.Fatalf("status = %q, want %q — the fence must have refused the completion", status, videodomain.JobStatusProcessing)
					}
				}
			},
		},
		{
			name: "completion write unavailable after the claim",
			want: videomessaging.Reject,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				env := newWorkerTestEnv(t, envOptions{
					decorate: func(inner videodomain.VideoJobRepository) videodomain.VideoJobRepository {
						return failTerminalWrite{VideoJobRepository: inner, status: videodomain.JobStatusCompleted}
					},
				})
				job, body := seedQueuedJob(t, env, generateTestVideo(t, 1))
				return env, body, func(t *testing.T) {
					if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
						t.Fatalf("status = %q, want %q", status, videodomain.JobStatusProcessing)
					}
				}
			},
		},
		{
			name: "failure write unavailable after the claim",
			want: videomessaging.Reject,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				env := newWorkerTestEnv(t, envOptions{
					decorate: func(inner videodomain.VideoJobRepository) videodomain.VideoJobRepository {
						return failTerminalWrite{VideoJobRepository: inner, status: videodomain.JobStatusFailed}
					},
				})
				job, body := seedQueuedJob(t, env, []byte("this is not a video"))
				return env, body, func(t *testing.T) {
					if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
						t.Fatalf("status = %q, want %q — the run broke after its claim was won", status, videodomain.JobStatusProcessing)
					}
				}
			},
		},
		{
			name: "claim refused by the database",
			want: videomessaging.Reject,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				env := newWorkerTestEnv(t, envOptions{})
				job, body := seedQueuedJob(t, env, []byte("never read"))
				if _, err := env.db.ExecContext(context.Background(),
					`UPDATE video_jobs SET original_filename = $1 WHERE id = $2`, refusedFilename, job.ID().String(),
				); err != nil {
					t.Fatalf("mark the row for refusal: %v", err)
				}
				refuseClaimsOnMarkedRows(t, env)
				logs := captureLogs(t)
				return env, body, func(t *testing.T) {
					if !strings.Contains(logs.String(), "refused by the test trigger") {
						t.Fatalf("the claim did not fail on the server's refusal; got:\n%s", logs.String())
					}
					if status := statusOf(t, env, job); status != videodomain.JobStatusQueued {
						t.Fatalf("status = %q, want %q", status, videodomain.JobStatusQueued)
					}
				}
			},
		},
		{
			name: "row that will not reconstruct",
			want: videomessaging.Reject,
			build: func(t *testing.T) (*workerTestEnv, []byte, func(t *testing.T)) {
				env := newWorkerTestEnv(t, envOptions{})
				id := env.ids.NewVideoJobID()
				sourceKey := videodomain.SourceStorageKey(uuid.NewString(), "input.mp4").String()
				if _, err := env.db.ExecContext(context.Background(), `
					INSERT INTO video_jobs (id, user_id, original_filename, status, source_key, content_hash, created_at)
					VALUES ($1, $2, $3, $4, $5, $6, $7)
				`, id.String(), uuid.NewString(), "input.mp4", "not-a-status", sourceKey, testContentHash, time.Now().UTC()); err != nil {
					t.Fatalf("insert an unreadable row: %v", err)
				}
				return env, dispatchBody(t, id.String(), sourceKey), noCheck
			},
		},
	}

	for _, r := range rows {
		t.Run(r.name, func(t *testing.T) {
			env, body, check := r.build(t)

			var inFlight atomic.Pointer[string]
			if got := env.deps.handle(context.Background(), body, &inFlight); got != r.want {
				t.Fatalf("disposition = %v, want %v", got, r.want)
			}
			check(t)
		})
	}
}

// TestWorker_RequeuesADispatchWhoseClaimCannotReachTheDatabase is the
// definite pre-statement branch of the claim, through the running process and
// a real broker. The claim statement never reaches a server, so the job is
// provably untouched, and the test asserts it.
func TestWorker_RequeuesADispatchWhoseClaimCannotReachTheDatabase(t *testing.T) {
	conn := openTestConn(t)
	sources := &countingSources{}
	leases := &countingLeases{}
	claims := &claimThrough{}
	env := newWorkerTestEnv(t, envOptions{
		decorate: func(inner videodomain.VideoJobRepository) videodomain.VideoJobRepository {
			claims.VideoJobRepository = inner
			claims.claimer = unreachableRepository(t)
			return claims
		},
		wrapSources: func(inner videodomain.SourceStorage) videodomain.SourceStorage {
			sources.SourceStorage = inner
			return sources
		},
		wrapLeases: func(inner videodomain.JobLeaseStore) videodomain.JobLeaseStore {
			leases.JobLeaseStore = inner
			return leases
		},
	})
	topo := testTopology(t, conn)
	publisher := declaredPublisher(t, conn, topo)
	ctx := context.Background()

	job, body := seedQueuedJob(t, env, []byte("never read"))
	key := finalizedIdempotencyKey(t, env, job)

	logs := captureLogs(t)
	cancel, done := startWorker(t, env, topo, time.Second)
	publishDispatch(t, publisher, body)

	// A second claim attempt is the redelivery: a rejected message would have
	// gone to the dead-letter queue and an acknowledged one would be gone.
	waitFor(t, 60*time.Second, "the dispatch to be redelivered", func() bool {
		return len(claims.callTimes()) >= 2
	})
	if depth := queueDepth(t, conn, topo.DeadQueue); depth != 0 {
		t.Fatalf("dead-letter queue depth = %d, want 0 while the database is unreachable", depth)
	}
	// The composition root's pause, observed from outside: the redelivery was
	// not taken before it had elapsed.
	times := claims.callTimes()
	if gap := times[1].Sub(times[0]); gap < videomessaging.DefaultRequeuePause {
		t.Fatalf("redelivery taken %s after the first attempt, want at least the %s requeue pause", gap, videomessaging.DefaultRequeuePause)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return; the requeue pause must not hold up shutdown")
	}

	waitFor(t, 30*time.Second, "the dispatch to be back on the work queue", func() bool {
		return queueDepth(t, conn, topo.WorkQueue) == 1
	})
	if depth := queueDepth(t, conn, topo.DeadQueue); depth != 0 {
		t.Fatalf("dead-letter queue depth = %d, want 0", depth)
	}

	stored, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusQueued || stored.LeaseEpoch() != job.LeaseEpoch() {
		t.Fatalf("job = %q at epoch %d, want %q at epoch %d — no statement reached the server", stored.Status(), stored.LeaseEpoch(), videodomain.JobStatusQueued, job.LeaseEpoch())
	}
	if n := leases.acquires.Load(); n != 0 {
		t.Fatalf("lease acquisitions = %d, want 0", n)
	}
	if exists, err := env.deps.redis.Exists(ctx, "videojob:lease:"+job.ID().String()).Result(); err != nil || exists != 0 {
		t.Fatalf("a lease exists for the job: exists=%d err=%v", exists, err)
	}
	if n := sources.gets.Load(); n != 0 {
		t.Fatalf("source object reads = %d, want 0", n)
	}
	if len(env.extractor.started) != 0 {
		t.Fatal("an extraction started for a job whose claim was never learned")
	}
	if !objectExists(t, env, job.SourceKey().String()) {
		t.Fatal("the source object was deleted")
	}
	if _, found, err := env.keys.Lookup(ctx, key); err != nil || !found {
		t.Fatalf("the idempotency key was cleared: found=%v err=%v", found, err)
	}
	if !hasRecord(t, logs.String(), map[string]any{
		"component": componentJobDispatch,
		"job_id":    job.ID().String(),
		"level":     "WARN",
		"msg":       "the claim outcome could not be learned; requeueing",
	}) {
		t.Fatalf("no warn record reports the requeue; got:\n%s", logs.String())
	}
}

// claimCommitsThenLosesTheResult performs the real claim and then reports
// that the database could not be reached — the connection lost between the
// commit and the result read, which no real database can be made to do on
// cue. Only the first successful claim is lost; there is no second, because
// the redelivery is refused before the claim statement runs.
type claimCommitsThenLosesTheResult struct {
	videodomain.VideoJobRepository
	calls atomic.Int32
	lost  atomic.Bool
}

func (r *claimCommitsThenLosesTheResult) ClaimForProcessing(ctx context.Context, job *videodomain.VideoJob) (bool, int64, error) {
	r.calls.Add(1)
	claimed, epoch, err := r.VideoJobRepository.ClaimForProcessing(ctx, job)
	if err != nil || !claimed {
		return claimed, epoch, err
	}
	if r.lost.CompareAndSwap(false, true) {
		return false, 0, fmt.Errorf("simulated: the claim committed and its result was lost: %w", videodomain.ErrRepositoryUnavailable)
	}
	return claimed, epoch, nil
}

// TestHandle_AnAmbiguousClaimCommitIsRequeuedAndThenRecoveredBySweep is the
// other branch the sentinel admits. The row is not unchanged here and the
// test does not claim it is: it asserts that the state left behind is the
// one the sweeper owns. Dispositions are asserted directly, as the other
// handler tests are; the broker's carrying-out of a requeue is covered above.
func TestHandle_AnAmbiguousClaimCommitIsRequeuedAndThenRecoveredBySweep(t *testing.T) {
	sources := &countingSources{}
	leases := &countingLeases{}
	claims := &claimCommitsThenLosesTheResult{}
	env := newWorkerTestEnv(t, envOptions{
		decorate: func(inner videodomain.VideoJobRepository) videodomain.VideoJobRepository {
			claims.VideoJobRepository = inner
			return claims
		},
		wrapSources: func(inner videodomain.SourceStorage) videodomain.SourceStorage {
			sources.SourceStorage = inner
			return sources
		},
		wrapLeases: func(inner videodomain.JobLeaseStore) videodomain.JobLeaseStore {
			leases.JobLeaseStore = inner
			return leases
		},
	})
	logs := captureLogs(t)
	job, body := seedQueuedJob(t, env, []byte("never read"))
	ctx := context.Background()
	outboxBefore := queuedOutboxRows(t, env, job)

	var inFlight atomic.Pointer[string]
	if got := env.deps.handle(ctx, body, &inFlight); got != videomessaging.Requeue {
		t.Fatalf("first delivery: disposition = %v, want Requeue", got)
	}
	if !claims.lost.Load() {
		t.Fatal("the claim never committed; this test exercises nothing")
	}
	if !hasRecord(t, logs.String(), map[string]any{
		"job_id": job.ID().String(),
		"msg":    "the claim outcome could not be learned; requeueing",
	}) {
		t.Fatalf("the requeue was not reported on the unknown-outcome path; got:\n%s", logs.String())
	}

	// The redelivery. StartProcessing's authoritative load reads the row as
	// processing and the aggregate refuses the transition, so the claim step
	// reports a lost claim without its statement running again.
	if got := env.deps.handle(ctx, body, &inFlight); got != videomessaging.Reject {
		t.Fatalf("redelivery: disposition = %v, want Reject", got)
	}
	if n := claims.calls.Load(); n != 1 {
		t.Fatalf("claim statements = %d, want 1", n)
	}

	stored, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if stored.Status() != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q, want %q — the claim committed", stored.Status(), videodomain.JobStatusProcessing)
	}
	epoch := stored.LeaseEpoch()
	if n := leases.acquires.Load(); n != 0 {
		t.Fatalf("lease acquisitions = %d, want 0", n)
	}
	if held, err := env.deps.leases.Held(ctx, job.ID(), epoch); err != nil || held {
		t.Fatalf("the job is leased: held=%v err=%v", held, err)
	}
	if n := sources.gets.Load(); n != 0 {
		t.Fatalf("source object reads = %d, want 0", n)
	}
	if len(env.extractor.started) != 0 {
		t.Fatal("an extraction started")
	}

	// Recovery on the ordinary path: one observation marks, the second acts.
	s := sweeperFor(t, env, job)
	s.sweep(ctx)
	if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q after one sweep, want %q — one observation is not a confirmation", status, videodomain.JobStatusProcessing)
	}
	s.sweep(ctx)

	recovered, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if recovered.Status() != videodomain.JobStatusQueued {
		t.Fatalf("status = %q after two sweeps, want %q", recovered.Status(), videodomain.JobStatusQueued)
	}
	if recovered.LeaseEpoch() != epoch+1 {
		t.Fatalf("lease epoch = %d, want %d — the recovery spends one requeue", recovered.LeaseEpoch(), epoch+1)
	}
	if got := queuedOutboxRows(t, env, job); got != outboxBefore+1 {
		t.Fatalf("outbox rows = %d, want %d", got, outboxBefore+1)
	}
	if !objectExists(t, env, job.SourceKey().String()) {
		t.Fatal("the source object was deleted for a job about to be dispatched again")
	}
}
