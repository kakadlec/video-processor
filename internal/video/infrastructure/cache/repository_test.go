package cache_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/cache"
)

func testAddr(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; skipping Redis integration test")
	}
	return addr
}

func newTestClient(t *testing.T) *redis.Client {
	t.Helper()

	client := redis.NewClient(&redis.Options{Addr: testAddr(t)})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// idParser is a real (non-fake) parser so these tests exercise the same
// re-validation path production code does — this package cares that
// FindByID re-validates through whatever parser it's given, not which
// concrete ID scheme is used.
type idParser struct{}

func (idParser) ParseVideoJobID(value string) (domain.VideoJobID, error) {
	return domain.NewVideoJobID(value)
}

// fakeRepository is an in-memory domain.VideoJobRepository standing in for
// PostgreSQL, with call counters and injectable errors so tests can prove a
// cache hit genuinely skipped it, rather than merely "would have worked
// either way".
type fakeRepository struct {
	mu   sync.Mutex
	byID map[string]*domain.VideoJob

	findByIDCalls              int
	findByUserIDCalls          int
	findCompletedByUserIDCalls int
	createCalls                int
	updateCalls                int

	enqueueCalls             int
	findByIDErr              error
	updateErr                error
	enqueueErr               error
	findByUserIDErr          error
	findCompletedByUserIDErr error
}

func newFakeRepository() *fakeRepository {
	return &fakeRepository{byID: make(map[string]*domain.VideoJob)}
}

// cloneVideoJob returns an independent copy of job, mirroring
// internal/video/application/fakes_test.go's own fake repository: without
// this, a test that mutates the *domain.VideoJob it holds (e.g. via
// job.Enqueue()) would see that mutation leak into whatever this fake
// already stored or already returned to another caller through pointer
// aliasing — exactly the kind of false-positive that would make a race
// test like TestCachedVideoJobRepository_MissRepopulation_* pass whether or
// not the production code actually handles the race correctly.
func cloneVideoJob(job *domain.VideoJob) *domain.VideoJob {
	clone, err := domain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.SourceKey(), job.StorageKey(), job.FrameCount(), job.ErrorReason(), job.Status(), job.CreatedAt())
	if err != nil {
		panic("fakeRepository: failed to clone video job: " + err.Error())
	}
	return clone
}

func (r *fakeRepository) Create(_ context.Context, job *domain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.createCalls++
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return nil
}

func (r *fakeRepository) FindByID(_ context.Context, id domain.VideoJobID) (*domain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findByIDCalls++
	if r.findByIDErr != nil {
		return nil, r.findByIDErr
	}
	job, ok := r.byID[id.String()]
	if !ok {
		return nil, domain.ErrVideoJobNotFound
	}
	return cloneVideoJob(job), nil
}

func (r *fakeRepository) FindByUserID(_ context.Context, _ domain.UserID, _, _ int) ([]*domain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findByUserIDCalls++
	if r.findByUserIDErr != nil {
		return nil, r.findByUserIDErr
	}
	return nil, nil
}

func (r *fakeRepository) FindCompletedByUserID(_ context.Context, _ domain.UserID) ([]*domain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.findCompletedByUserIDCalls++
	if r.findCompletedByUserIDErr != nil {
		return nil, r.findCompletedByUserIDErr
	}
	return nil, nil
}

func (r *fakeRepository) Update(_ context.Context, job *domain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.updateCalls++
	if r.updateErr != nil {
		return r.updateErr
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return nil
}

func (r *fakeRepository) Enqueue(_ context.Context, job *domain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enqueueCalls++
	if r.enqueueErr != nil {
		return r.enqueueErr
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return nil
}

var jobIDCounter int

func newTestJob(t *testing.T) *domain.VideoJob {
	t.Helper()

	jobIDCounter++
	id, err := domain.NewVideoJobID(fmt.Sprintf("videojob-status-cache-test-%d-%d", time.Now().UnixNano(), jobIDCounter))
	if err != nil {
		t.Fatalf("NewVideoJobID: %v", err)
	}
	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}
	filename, err := domain.NewOriginalFilename("video.mp4")
	if err != nil {
		t.Fatalf("NewOriginalFilename: %v", err)
	}
	// A source key is set because Enqueue rejects a job without one, and
	// its value is chosen to be unmistakable next to a result key so a
	// transposition between the two adjacent StorageKey fields shows up as a
	// wrong value rather than as a passing test.
	sourceKey, err := domain.NewStorageKey("uploads/source-video.mp4")
	if err != nil {
		t.Fatalf("NewStorageKey: %v", err)
	}
	job, err := domain.RestoreVideoJob(id, userID, filename, sourceKey, domain.StorageKey{}, 0, "", domain.JobStatusPending, time.Now())
	if err != nil {
		t.Fatalf("RestoreVideoJob: %v", err)
	}
	return job
}

func TestCachedVideoJobRepository_FindByID_CacheMissFallsThroughAndRepopulates(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()

	first, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("first FindByID: %v", err)
	}
	if first.Status() != domain.JobStatusPending {
		t.Fatalf("first FindByID status = %q, want pending", first.Status())
	}
	if fake.findByIDCalls != 1 {
		t.Fatalf("findByIDCalls after first call = %d, want 1", fake.findByIDCalls)
	}

	// Make the inner repository fail on any further call — a second
	// lookup only succeeds if it's genuinely served from cache.
	fake.mu.Lock()
	fake.findByIDErr = errors.New("inner repository should not be called again")
	fake.mu.Unlock()

	second, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("second FindByID (expected cache hit): %v", err)
	}
	if second.Status() != domain.JobStatusPending {
		t.Fatalf("second FindByID status = %q, want pending", second.Status())
	}
	if fake.findByIDCalls != 1 {
		t.Fatalf("findByIDCalls after second call = %d, want still 1 (cache hit)", fake.findByIDCalls)
	}
}

func TestCachedVideoJobRepository_Update_WritesThroughForImmediateCacheHit(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()

	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	if err := repo.Update(ctx, job); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Force any FindByID that isn't a cache hit to fail, proving the
	// following read is served from the write-through entry.
	fake.mu.Lock()
	fake.findByIDErr = errors.New("inner repository should not be called")
	fake.mu.Unlock()

	got, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID after Update (expected cache hit): %v", err)
	}
	if got.Status() != domain.JobStatusQueued {
		t.Fatalf("status = %q, want queued (the just-written state)", got.Status())
	}
}

func TestCachedVideoJobRepository_Update_InnerFailureLeavesCacheUntouched(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()

	// Populate the cache with the pending state via a normal FindByID.
	if _, err := repo.FindByID(ctx, job.ID()); err != nil {
		t.Fatalf("FindByID (populate cache): %v", err)
	}

	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	fake.mu.Lock()
	fake.updateErr = errors.New("simulated postgres failure")
	fake.mu.Unlock()

	if err := repo.Update(ctx, job); err == nil {
		t.Fatal("Update succeeded despite inner repository failure, want an error")
	}

	// The cache must still hold the pre-transition value. Force a
	// non-cache FindByID to fail so a successful read here proves it came
	// from the (untouched) cache entry.
	fake.mu.Lock()
	fake.updateErr = nil
	fake.findByIDErr = errors.New("inner repository should not be called")
	fake.mu.Unlock()

	got, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID after failed Update (expected cache hit): %v", err)
	}
	if got.Status() != domain.JobStatusPending {
		t.Fatalf("status = %q, want pending (cache must not reflect the failed write)", got.Status())
	}
}

func TestCachedVideoJobRepository_FindByUserID_PassesThroughUncached(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()
	userID, err := domain.NewUserID("user-1")
	if err != nil {
		t.Fatalf("NewUserID: %v", err)
	}

	if _, err := repo.FindByUserID(ctx, userID, 0, 10); err != nil {
		t.Fatalf("first FindByUserID: %v", err)
	}
	if _, err := repo.FindByUserID(ctx, userID, 0, 10); err != nil {
		t.Fatalf("second FindByUserID: %v", err)
	}

	if fake.findByUserIDCalls != 2 {
		t.Fatalf("findByUserIDCalls = %d, want 2 (no caching)", fake.findByUserIDCalls)
	}
}

// blockingFindByID wraps a fakeRepository so its FindByID pauses, after
// actually reading the underlying value, until the test signals it to
// continue — modeling a slow miss-repopulation caller that has already
// read a (possibly soon-to-be-stale) value from the inner repository but
// hasn't yet reached its own cache write.
type blockingFindByID struct {
	fake    *fakeRepository
	started chan struct{}
	proceed chan struct{}
}

func (b *blockingFindByID) Create(ctx context.Context, job *domain.VideoJob) error {
	return b.fake.Create(ctx, job)
}

func (b *blockingFindByID) FindByUserID(ctx context.Context, userID domain.UserID, offset, limit int) ([]*domain.VideoJob, error) {
	return b.fake.FindByUserID(ctx, userID, offset, limit)
}

func (b *blockingFindByID) FindCompletedByUserID(ctx context.Context, userID domain.UserID) ([]*domain.VideoJob, error) {
	return b.fake.FindCompletedByUserID(ctx, userID)
}

func (b *blockingFindByID) Update(ctx context.Context, job *domain.VideoJob) error {
	return b.fake.Update(ctx, job)
}

func (b *blockingFindByID) Enqueue(ctx context.Context, job *domain.VideoJob) error {
	return b.fake.Enqueue(ctx, job)
}

func (b *blockingFindByID) FindByID(ctx context.Context, id domain.VideoJobID) (*domain.VideoJob, error) {
	job, err := b.fake.FindByID(ctx, id)
	close(b.started)
	<-b.proceed
	return job, err
}

func TestCachedVideoJobRepository_MissRepopulation_DoesNotClobberConcurrentWriteThrough(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	ctx := context.Background()

	slow := &blockingFindByID{fake: fake, started: make(chan struct{}), proceed: make(chan struct{})}
	slowRepo := cache.NewCachedVideoJobRepository(slow, client, idParser{})
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})

	done := make(chan error, 1)
	go func() {
		_, err := slowRepo.FindByID(ctx, job.ID())
		done <- err
	}()

	// Wait until the slow reader has read "pending" from the inner
	// repository and is paused right before its own cache repopulation.
	<-slow.started

	// A concurrent transition commits and write-throughs "queued" while
	// the slow reader is still paused.
	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	if err := repo.Update(ctx, job); err != nil {
		t.Fatalf("Update: %v", err)
	}

	// Let the slow reader's own (now-stale) repopulation attempt run.
	close(slow.proceed)
	if err := <-done; err != nil {
		t.Fatalf("slow FindByID: %v", err)
	}

	// The cache must still reflect the write-through, not the slow
	// reader's stale repopulation attempt. Force any non-cache read to
	// fail so a successful result here proves it's a genuine cache hit.
	fake.mu.Lock()
	fake.findByIDErr = errors.New("must be served from cache")
	fake.mu.Unlock()

	got, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID after race (expected cache hit): %v", err)
	}
	if got.Status() != domain.JobStatusQueued {
		t.Fatalf("status = %q, want queued (cache was clobbered by the slow reader's stale repopulation)", got.Status())
	}
}

func TestCachedVideoJobRepository_FindByID_GenuineRedisErrorFallsBackLikeAMiss(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()
	key := "videojob:status:" + job.ID().String()

	// Force a real (non-Nil) Redis error on GET: a WRONGTYPE error from a
	// key holding a list instead of a string.
	if err := client.RPush(ctx, key, "not-a-cached-record").Err(); err != nil {
		t.Fatalf("RPush (setup): %v", err)
	}
	t.Cleanup(func() { _ = client.Del(context.Background(), key).Err() })

	got, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID (expected fallback to inner despite Redis error): %v", err)
	}
	if got.Status() != domain.JobStatusPending {
		t.Fatalf("status = %q, want pending", got.Status())
	}
	if fake.findByIDCalls != 1 {
		t.Fatalf("findByIDCalls = %d, want 1 (fell back to inner)", fake.findByIDCalls)
	}

	// The WRONGTYPE key must have self-healed: a second call should be a
	// genuine cache hit now that SET NX had a genuinely empty slot to fill.
	fake.mu.Lock()
	fake.findByIDErr = errors.New("must be served from the now-repaired cache")
	fake.mu.Unlock()

	if _, err := repo.FindByID(ctx, job.ID()); err != nil {
		t.Fatalf("second FindByID (expected cache hit after wrong-type self-heal): %v", err)
	}
}

func TestCachedVideoJobRepository_FindByID_IDMismatchTreatedAsMalformed(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	jobA := newTestJob(t)
	jobB := newTestJob(t)
	if err := fake.Create(context.Background(), jobA); err != nil {
		t.Fatalf("fake.Create jobA: %v", err)
	}
	if err := fake.Create(context.Background(), jobB); err != nil {
		t.Fatalf("fake.Create jobB: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()

	// Store a structurally valid record for jobB under jobA's key —
	// simulating corruption/a key collision, not a normal code path.
	keyA := "videojob:status:" + jobA.ID().String()
	data, err := json.Marshal(map[string]any{
		"id":                jobB.ID().String(),
		"user_id":           jobB.UserID().String(),
		"original_filename": jobB.OriginalFilename().String(),
		"frame_count":       0,
		"status":            string(domain.JobStatusPending),
		"created_at":        jobB.CreatedAt(),
	})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := client.Set(ctx, keyA, data, 0).Err(); err != nil {
		t.Fatalf("Set (setup): %v", err)
	}

	got, err := repo.FindByID(ctx, jobA.ID())
	if err != nil {
		t.Fatalf("FindByID (expected fallback past the ID-mismatched entry): %v", err)
	}
	if !got.ID().Equal(jobA.ID()) {
		t.Fatalf("FindByID(jobA) returned job %q, want %q — a mismatched cache entry was returned as a hit", got.ID().String(), jobA.ID().String())
	}
	if fake.findByIDCalls != 1 {
		t.Fatalf("findByIDCalls = %d, want 1 (fell back to inner)", fake.findByIDCalls)
	}

	// The mismatched entry must have been replaced by a valid one for jobA.
	fake.mu.Lock()
	fake.findByIDErr = errors.New("must be served from the now-repaired cache")
	fake.mu.Unlock()

	if _, err := repo.FindByID(ctx, jobA.ID()); err != nil {
		t.Fatalf("second FindByID (expected cache hit after repair): %v", err)
	}
}

func TestCachedVideoJobRepository_Update_WriteThroughSurvivesACanceledCallerContext(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})

	// Populate the cache with the pending state via a normal, live context.
	if _, err := repo.FindByID(context.Background(), job.ID()); err != nil {
		t.Fatalf("FindByID (populate cache): %v", err)
	}

	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}

	// The caller's own context is already canceled by the time Update
	// runs — modeling a request/finalization deadline expiring right as
	// PostgreSQL's write commits. The fake's inner.Update ignores ctx (as
	// the real postgres.Repository would already have committed via its
	// own transaction by this point), so this isolates the cache layer's
	// own handling of a dead incoming context.
	canceledCtx, cancel := context.WithCancel(context.Background())
	cancel()

	if err := repo.Update(canceledCtx, job); err != nil {
		t.Fatalf("Update with a canceled caller context: %v", err)
	}

	// The write-through must still have landed via its own detached
	// context — a fresh, live context observes the new state.
	got, err := repo.FindByID(context.Background(), job.ID())
	if err != nil {
		t.Fatalf("FindByID after Update: %v", err)
	}
	if got.Status() != domain.JobStatusQueued {
		t.Fatalf("status = %q, want queued (write-through must survive a canceled caller context)", got.Status())
	}
}

func TestCachedVideoJobRepository_FindByID_MalformedEntryFallsBackAndIsReplaced(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()
	key := "videojob:status:" + job.ID().String()

	if err := client.Set(ctx, key, "this is not json", 0).Err(); err != nil {
		t.Fatalf("Set (setup): %v", err)
	}

	got, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID (expected fallback past the malformed entry): %v", err)
	}
	if got.Status() != domain.JobStatusPending {
		t.Fatalf("status = %q, want pending", got.Status())
	}
	if fake.findByIDCalls != 1 {
		t.Fatalf("findByIDCalls = %d, want 1", fake.findByIDCalls)
	}

	// The malformed entry must have been replaced by a valid one.
	fake.mu.Lock()
	fake.findByIDErr = errors.New("must be served from the now-repaired cache")
	fake.mu.Unlock()

	if _, err := repo.FindByID(ctx, job.ID()); err != nil {
		t.Fatalf("second FindByID (expected cache hit after repair): %v", err)
	}
}

func TestCachedVideoJobRepository_CacheWriteFailure_DoesNotSurface(t *testing.T) {
	// An unreachable Redis address (nothing listens on port 1) makes every
	// command fail fast without needing a live server misbehaving.
	unreachable := redis.NewClient(&redis.Options{Addr: "127.0.0.1:1", MaxRetries: -1, DialTimeout: 200 * time.Millisecond})
	t.Cleanup(func() { _ = unreachable.Close() })

	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, unreachable, idParser{})
	ctx := context.Background()

	if _, err := repo.FindByID(ctx, job.ID()); err != nil {
		t.Fatalf("FindByID with an unreachable cache: %v", err)
	}

	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	if err := repo.Update(ctx, job); err != nil {
		t.Fatalf("Update with an unreachable cache: %v", err)
	}
}

func TestCachedVideoJobRepository_CacheWrites_CarryTheFixedTTL(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()
	key := "videojob:status:" + job.ID().String()

	assertBoundedTTL := func(t *testing.T, label string) {
		t.Helper()
		ttl, err := client.TTL(ctx, key).Result()
		if err != nil {
			t.Fatalf("%s: TTL: %v", label, err)
		}
		if ttl <= 0 || ttl > 5*time.Minute {
			t.Fatalf("%s: TTL = %v, want in (0, 5m]", label, ttl)
		}
	}

	if _, err := repo.FindByID(ctx, job.ID()); err != nil {
		t.Fatalf("FindByID (miss-repopulation): %v", err)
	}
	assertBoundedTTL(t, "after miss-repopulation")

	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	if err := repo.Update(ctx, job); err != nil {
		t.Fatalf("Update (write-through): %v", err)
	}
	assertBoundedTTL(t, "after write-through")
}

func TestCachedVideoJobRepository_Create_PassesThroughUncachedAndDoesNotPopulateCache(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()
	job := newTestJob(t)

	if err := repo.Create(ctx, job); err != nil {
		t.Fatalf("Create: %v", err)
	}
	if fake.createCalls != 1 {
		t.Fatalf("createCalls = %d, want 1", fake.createCalls)
	}

	fake.mu.Lock()
	fake.findByIDErr = errors.New("inner repository should be called, since Create must not have populated the cache")
	fake.mu.Unlock()

	if _, err := repo.FindByID(ctx, job.ID()); err == nil {
		t.Fatal("FindByID succeeded from cache after Create, want a miss falling through to the (erroring) inner repository")
	}
}

// TestCachedVideoJobRepository_Enqueue_WritesThrough covers the decorator's
// second write path. Passing Enqueue through uncached, the way Create is
// passed through, would leave the cache reporting pending for a job
// PostgreSQL already has as queued — contradicting the very row the relay is
// about to publish a dispatch for.
func TestCachedVideoJobRepository_Enqueue_WritesThrough(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	job := newTestJob(t)
	if err := fake.Create(context.Background(), job); err != nil {
		t.Fatalf("fake.Create: %v", err)
	}
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})
	ctx := context.Background()

	// Populate the cache with the pending state first, so the assertion
	// below can only pass if Enqueue actually overwrote it.
	if _, err := repo.FindByID(ctx, job.ID()); err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	if err := repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue: %v", err)
	}
	if fake.enqueueCalls != 1 {
		t.Fatalf("fake.enqueueCalls = %d, want 1", fake.enqueueCalls)
	}

	before := fake.findByIDCalls
	found, err := repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("FindByID: %v", err)
	}
	if fake.findByIDCalls != before {
		t.Fatalf("fake.findByIDCalls = %d, want %d — the read should have been served from the cache", fake.findByIDCalls, before)
	}
	if found.Status() != domain.JobStatusQueued {
		t.Fatalf("Status = %v, want %v", found.Status(), domain.JobStatusQueued)
	}
	// The source key must survive the round trip through Redis, or the
	// relay publishes a dispatch naming no object.
	if !found.SourceKey().Equal(job.SourceKey()) {
		t.Fatalf("SourceKey = %v, want %v", found.SourceKey(), job.SourceKey())
	}
}

// TestCachedVideoJobRepository_Enqueue_InnerFailureIsNotCached keeps the
// authority where it belongs: PostgreSQL must accept the write before the
// cache reflects it.
func TestCachedVideoJobRepository_Enqueue_InnerFailureIsNotCached(t *testing.T) {
	client := newTestClient(t)
	fake := newFakeRepository()
	fake.enqueueErr = errors.New("postgres unavailable")
	job := newTestJob(t)
	repo := cache.NewCachedVideoJobRepository(fake, client, idParser{})

	if err := job.Enqueue(); err != nil {
		t.Fatalf("job.Enqueue: %v", err)
	}
	if err := repo.Enqueue(context.Background(), job); err == nil {
		t.Fatal("expected the inner repository's error to propagate")
	}
	if _, err := client.Get(context.Background(), "videojob:status:"+job.ID().String()).Result(); !errors.Is(err, redis.Nil) {
		t.Fatalf("expected no cache entry, got err = %v", err)
	}
}
