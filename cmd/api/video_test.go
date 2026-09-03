package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"

	"video-processor/internal/identity/infrastructure/jwtauth"
	platformrabbitmq "video-processor/internal/platform/rabbitmq"
	videoapplication "video-processor/internal/video/application"
	videodomain "video-processor/internal/video/domain"
	videoidgen "video-processor/internal/video/infrastructure/idgen"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
	videostorage "video-processor/internal/video/infrastructure/storage"
)

// fakeIdempotencyStore is an in-memory videodomain.IdempotencyStore so HTTP
// tests don't need a live Redis instance, mirroring
// inMemoryVideoJobRepository. It implements the same token-owned
// reserve/finalize/lookup/clear semantics as the real Redis-backed adapter.
type fakeIdempotencyStore struct {
	mu      sync.Mutex
	entries map[string]fakeIdempotencyEntry
	// finalizeGate, if non-nil, blocks Finalize until closed — lets a test
	// hold a reservation open (unfinalized) while a concurrent duplicate
	// polls Lookup, so the bounded wait-and-retry path is genuinely
	// exercised rather than always seeing an already-finalized key.
	finalizeGate chan struct{}
	// clearGate, if non-nil, blocks Clear until closed — lets a test hold
	// a finalized-but-failed key in place while a concurrent duplicate
	// observes it, so the narrow pre-Clear window is genuinely exercised
	// rather than racing a Clear that usually wins.
	clearGate chan struct{}
	// reserveErr, if non-nil, makes Reserve return it instead of ever
	// touching entries — simulates a Redis-layer failure, for
	// fail-open-upload-idempotency's tests.
	reserveErr error
	// finalizeCalls/clearCalls count invocations, so a test can assert
	// they were never called (e.g. after a Reserve error, per
	// fail-open-upload-idempotency's design.md Decision 1).
	finalizeCalls int
	clearCalls    int
}

type fakeIdempotencyEntry struct {
	token string
	jobID videodomain.VideoJobID
	final bool
}

func newFakeIdempotencyStore() *fakeIdempotencyStore {
	return &fakeIdempotencyStore{entries: make(map[string]fakeIdempotencyEntry)}
}

func (s *fakeIdempotencyStore) Reserve(_ context.Context, key videodomain.IdempotencyKey) (string, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.reserveErr != nil {
		return "", false, s.reserveErr
	}
	if _, exists := s.entries[key.String()]; exists {
		return "", false, nil
	}
	token := uuid.NewString()
	s.entries[key.String()] = fakeIdempotencyEntry{token: token}
	return token, true, nil
}

func (s *fakeIdempotencyStore) Finalize(ctx context.Context, key videodomain.IdempotencyKey, token string, jobID videodomain.VideoJobID) (bool, error) {
	if s.finalizeGate != nil {
		select {
		case <-s.finalizeGate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.finalizeCalls++
	entry, exists := s.entries[key.String()]
	if !exists || entry.token != token {
		return false, nil
	}
	entry.jobID = jobID
	entry.final = true
	s.entries[key.String()] = entry
	return true, nil
}

func (s *fakeIdempotencyStore) Lookup(_ context.Context, key videodomain.IdempotencyKey) (videodomain.VideoJobID, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key.String()]
	if !exists || !entry.final {
		return videodomain.VideoJobID{}, false, nil
	}
	return entry.jobID, true, nil
}

func (s *fakeIdempotencyStore) Clear(ctx context.Context, key videodomain.IdempotencyKey, token string) (bool, error) {
	if s.clearGate != nil {
		select {
		case <-s.clearGate:
		case <-ctx.Done():
			return false, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearCalls++
	entry, exists := s.entries[key.String()]
	if !exists || entry.token != token {
		return false, nil
	}
	delete(s.entries, key.String())
	return true, nil
}

// ClearByJob mirrors the real store: it removes only a finalized entry that
// names this exact job, so an unfinalized reservation — someone else's
// in-flight request — is never removed.
func (s *fakeIdempotencyStore) ClearByJob(_ context.Context, key videodomain.IdempotencyKey, jobID videodomain.VideoJobID) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.clearCalls++
	entry, exists := s.entries[key.String()]
	if !exists || !entry.final || entry.jobID.String() != jobID.String() {
		return false, nil
	}
	delete(s.entries, key.String())
	return true, nil
}

// hasReservation reports whether key has any entry (reserved or finalized)
// — test-only, for synchronizing on "a reservation exists" independent of
// whether it has finalized yet.
func (s *fakeIdempotencyStore) hasReservation(key videodomain.IdempotencyKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, exists := s.entries[key.String()]
	return exists
}

// isFinalized reports whether key holds a finalized entry naming a real
// job, as opposed to a bare reservation — the distinction ClearByJob acts
// on, and therefore the one the finalize/enqueue ordering turns on.
func (s *fakeIdempotencyStore) isFinalized(key videodomain.IdempotencyKey) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	entry, exists := s.entries[key.String()]
	return exists && entry.final
}

// callCounts reports Finalize/Clear invocation counts, for asserting a
// request that proceeded without a reservation never touches either.
func (s *fakeIdempotencyStore) callCounts() (finalize, clear int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.finalizeCalls, s.clearCalls
}

// alwaysAllowRateLimiter is a fake videoRateLimiter so HTTP tests unrelated
// to rate limiting itself don't need a live Redis instance and are
// unaffected by it, mirroring fakeIdempotencyStore's role for idempotency.
type alwaysAllowRateLimiter struct{}

func (alwaysAllowRateLimiter) Allow(context.Context, string) (bool, time.Duration, error) {
	return true, 0, nil
}

// inMemoryVideoJobRepository is a fake videodomain.VideoJobRepository so
// these HTTP tests don't need a live PostgreSQL instance, mirroring
// inMemoryUserRepository in identity_test.go. Ordering matches
// videodomain.VideoJobRepository's documented contract: CreatedAt
// descending, VideoJobID ascending as a tie-breaker. Create/FindByID/Update
// all store and return independent clones, like the real PostgreSQL
// adapter, so mutating a job a caller holds never changes what's stored
// unless the caller actually calls Update.
type inMemoryVideoJobRepository struct {
	mu   sync.Mutex
	byID map[string]*videodomain.VideoJob
	// enqueueErr, when set, fails every Enqueue — the branch POST /upload
	// takes when a job cannot be queued for the relay to dispatch.
	enqueueErr   error
	enqueueCalls int
	// beforeEnqueue, when set, runs on entry to Enqueue and before its
	// lock is taken — a seam for observing what the handler had already
	// done to state outside this repository by the time it queued.
	beforeEnqueue func()
}

func newInMemoryVideoJobRepository() *inMemoryVideoJobRepository {
	return &inMemoryVideoJobRepository{byID: make(map[string]*videodomain.VideoJob)}
}

func cloneVideoJob(job *videodomain.VideoJob) *videodomain.VideoJob {
	clone, err := videodomain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.SourceKey(), job.ContentHash(), job.StorageKey(), job.FrameCount(), job.ErrorReason(), job.Status(), job.CreatedAt(), job.LeaseEpoch())
	if err != nil {
		panic("inMemoryVideoJobRepository: failed to clone video job: " + err.Error())
	}
	return clone
}

func (r *inMemoryVideoJobRepository) Create(_ context.Context, job *videodomain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return nil
}

func (r *inMemoryVideoJobRepository) FindByID(_ context.Context, id videodomain.VideoJobID) (*videodomain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.byID[id.String()]
	if !ok {
		return nil, videodomain.ErrVideoJobNotFound
	}
	return cloneVideoJob(job), nil
}

// Update mirrors the real adapter's fence: it writes only when the stored
// row is still processing at the caller's epoch, and reports whether it did.
func (r *inMemoryVideoJobRepository) Update(_ context.Context, job *videodomain.VideoJob, epoch int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[job.ID().String()]
	if !ok {
		return false, videodomain.ErrVideoJobNotFound
	}
	if stored.Status() != videodomain.JobStatusProcessing || stored.LeaseEpoch() != epoch {
		return false, videodomain.ErrJobFenced
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return true, nil
}

// Requeue mirrors the recovery edge: conditional on the stored row still
// being processing at the observed epoch.
func (r *inMemoryVideoJobRepository) Requeue(_ context.Context, job *videodomain.VideoJob, observedEpoch int64) (bool, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[job.ID().String()]
	if !ok {
		return false, videodomain.ErrVideoJobNotFound
	}
	if stored.Status() != videodomain.JobStatusProcessing || stored.LeaseEpoch() != observedEpoch {
		return false, nil
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return true, nil
}

func (r *inMemoryVideoJobRepository) FindProcessing(_ context.Context, after videodomain.VideoJobID, limit int) ([]*videodomain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matches []*videodomain.VideoJob
	for _, job := range r.byID {
		if job.Status() != videodomain.JobStatusProcessing {
			continue
		}
		if !after.IsZero() && job.ID().String() <= after.String() {
			continue
		}
		matches = append(matches, cloneVideoJob(job))
	}
	sort.Slice(matches, func(i, j int) bool {
		return matches[i].ID().String() < matches[j].ID().String()
	})
	if limit > 0 && len(matches) > limit {
		matches = matches[:limit]
	}
	return matches, nil
}

func (r *inMemoryVideoJobRepository) Enqueue(_ context.Context, job *videodomain.VideoJob) error {
	if r.beforeEnqueue != nil {
		r.beforeEnqueue()
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.enqueueCalls++
	if r.enqueueErr != nil {
		return r.enqueueErr
	}
	if _, ok := r.byID[job.ID().String()]; !ok {
		return videodomain.ErrVideoJobNotFound
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return nil
}

// ClaimForProcessing mirrors the real adapter: it writes only when the
// stored row is still queued, and reports whether it did.
func (r *inMemoryVideoJobRepository) ClaimForProcessing(_ context.Context, job *videodomain.VideoJob) (bool, int64, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	stored, ok := r.byID[job.ID().String()]
	if !ok {
		return false, 0, videodomain.ErrVideoJobNotFound
	}
	if stored.Status() != videodomain.JobStatusQueued {
		return false, 0, nil
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return true, stored.LeaseEpoch(), nil
}

func (r *inMemoryVideoJobRepository) FindByUserID(_ context.Context, userID videodomain.UserID, offset, limit int) ([]*videodomain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matches []*videodomain.VideoJob
	for _, job := range r.byID {
		if job.UserID().Equal(userID) {
			matches = append(matches, cloneVideoJob(job))
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].CreatedAt().Equal(matches[j].CreatedAt()) {
			return matches[i].CreatedAt().After(matches[j].CreatedAt())
		}
		return matches[i].ID().String() < matches[j].ID().String()
	})

	if offset >= len(matches) {
		return []*videodomain.VideoJob{}, nil
	}
	end := offset + limit
	if end > len(matches) {
		end = len(matches)
	}
	return matches[offset:end], nil
}

func (r *inMemoryVideoJobRepository) FindCompletedByUserID(_ context.Context, userID videodomain.UserID) ([]*videodomain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matches []*videodomain.VideoJob
	for _, job := range r.byID {
		if job.UserID().Equal(userID) && job.Status() == videodomain.JobStatusCompleted {
			matches = append(matches, cloneVideoJob(job))
		}
	}
	sort.Slice(matches, func(i, j int) bool {
		if !matches[i].CreatedAt().Equal(matches[j].CreatedAt()) {
			return matches[i].CreatedAt().After(matches[j].CreatedAt())
		}
		return matches[i].ID().String() < matches[j].ID().String()
	})
	return matches, nil
}

// createFailingRepository wraps inMemoryVideoJobRepository so a test can
// force Create to fail a fixed number of times before delegating to the
// wrapped repository normally — used to exercise the idempotency
// reservation-is-cleared-on-CreateVideoJob-failure path without needing a
// real PostgreSQL failure. Every other repository method is unaffected.
type createFailingRepository struct {
	*inMemoryVideoJobRepository
	mu       sync.Mutex
	failLeft int
}

func newCreateFailingRepository(failN int) *createFailingRepository {
	return &createFailingRepository{inMemoryVideoJobRepository: newInMemoryVideoJobRepository(), failLeft: failN}
}

func (r *createFailingRepository) Create(ctx context.Context, job *videodomain.VideoJob) error {
	r.mu.Lock()
	if r.failLeft > 0 {
		r.failLeft--
		r.mu.Unlock()
		return errors.New("simulated CreateVideoJob failure")
	}
	r.mu.Unlock()
	return r.inMemoryVideoJobRepository.Create(ctx, job)
}

// newTestVideoModule returns a videoModule backed by an in-memory
// repository, for callers that only need the router to have working video
// routes and don't inspect job state directly.
func newTestVideoModule(t *testing.T) *videoModule {
	t.Helper()
	module, _ := newTestVideoModuleWithRepo(t)
	return module
}

// newTestVideoModuleWithRepo wires a videoModule whose ProcessVideoJob use
// case runs a real ffmpeg extractor, not a fake: this module is reachable
// from main_test.go's POST /upload tests via startTestServer, which upload
// real generated test videos and assert on real extracted frame counts.
func newTestVideoModuleWithRepo(t *testing.T) (*videoModule, *inMemoryVideoJobRepository) {
	t.Helper()
	module, repo, _ := newTestVideoModuleWithStorage(t)
	return module, repo
}

// newTestStorages builds both storage adapters against the same real MinIO
// instance cmd/api itself reads (VIDEO_MINIO_*). TestMain has already proven
// the configuration loads, so a failure here is a genuine fault rather than
// an unconfigured machine.
//
// One bucket serves both, as in production, where sources and results are
// separated only by key prefix. Each test gets its own, drained and removed
// afterwards: sharing the configured runtime bucket would leave a
// UUID-keyed object behind on every upload test, and the local MinIO
// service keeps its data in a named volume, so that would grow without
// bound across suite runs.
func newTestStorages(t *testing.T) (videodomain.SourceStorage, videodomain.ResultStorage) {
	t.Helper()
	sources, results, _ := newTestStoragesWithInspector(t)
	return sources, results
}

// bucketInspector lists a test bucket's objects, so a test can assert on
// what a request left behind rather than only on what it can fetch by key —
// which matters for source objects, whose uploadID-derived keys the test
// never sees.
type bucketInspector struct {
	client *minio.Client
	bucket string
}

func (b bucketInspector) keysWithPrefix(t *testing.T, prefix string) []string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var keys []string
	for object := range b.client.ListObjects(ctx, b.bucket, minio.ListObjectsOptions{Prefix: prefix, Recursive: true}) {
		if object.Err != nil {
			t.Fatalf("list %q under %q: %v", b.bucket, prefix, object.Err)
		}
		keys = append(keys, object.Key)
	}
	return keys
}

// removeObject deletes one object from the test bucket, so a test can
// exercise the case where a result the VideoJob row still points at is no
// longer in storage.
func (b bucketInspector) removeObject(t *testing.T, key string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	if err := b.client.RemoveObject(ctx, b.bucket, key, minio.RemoveObjectOptions{}); err != nil {
		t.Fatalf("remove %q from %q: %v", key, b.bucket, err)
	}
}

// newTestStoragesWithInspector is newTestStorages plus a handle on the
// underlying bucket.
func newTestStoragesWithInspector(t *testing.T) (videodomain.SourceStorage, videodomain.ResultStorage, bucketInspector) {
	t.Helper()
	cfg, err := videostorage.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("load MinIO config: %v", err)
	}
	client, err := videostorage.Open(cfg)
	if err != nil {
		t.Fatalf("open MinIO client: %v", err)
	}

	bucket := uniqueTestBucket(t)
	if err := videostorage.EnsureBucket(context.Background(), client, bucket); err != nil {
		t.Fatalf("ensure bucket %q: %v", bucket, err)
	}
	t.Cleanup(func() { removeTestBucket(t, client, bucket) })

	// Built the way setupVideo builds it: region discovered on the
	// reachable client, then handed to a presign-only client. cfg's
	// PublicEndpoint defaults to Endpoint here, so the URLs these tests
	// issue point at the same MinIO the test process can reach — which is
	// what lets a test follow one.
	region, err := videostorage.BucketRegion(context.Background(), client, bucket)
	if err != nil {
		t.Fatalf("bucket region %q: %v", bucket, err)
	}
	presigner, err := videostorage.OpenPresigner(cfg, region)
	if err != nil {
		t.Fatalf("open presigning client: %v", err)
	}

	return videostorage.NewSourceStorage(client, bucket),
		videostorage.NewResultStorage(client, presigner, bucket),
		bucketInspector{client: client, bucket: bucket}
}

// uniqueTestBucket derives an S3-valid (lowercase, no underscores, <= 63
// chars) name unique to this test and run.
func uniqueTestBucket(t *testing.T) string {
	t.Helper()
	name := strings.ToLower(t.Name())
	name = strings.NewReplacer("/", "-", "_", "-", " ", "-").Replace(name)
	full := fmt.Sprintf("api-%s-%d", name, time.Now().UnixNano()%1e6)
	if len(full) > 63 {
		full = full[:63]
	}
	return strings.Trim(full, "-")
}

// removeTestBucket drains bucket and deletes it; a bucket must be empty
// before it can be removed. Failures are logged rather than fatal — cleanup
// must not turn a passing test red, and it also runs after a failed one.
func removeTestBucket(t *testing.T, client *minio.Client, bucket string) {
	t.Helper()
	ctx := context.Background()

	for object := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
		if object.Err != nil {
			t.Logf("cleanup: list %s: %v", bucket, object.Err)
			continue
		}
		if err := client.RemoveObject(ctx, bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
			t.Logf("cleanup: remove %s/%s: %v", bucket, object.Key, err)
		}
	}
	if err := client.RemoveBucket(ctx, bucket); err != nil {
		t.Logf("cleanup: remove bucket %s: %v", bucket, err)
	}
}

// newTestVideoModuleWithStorage additionally hands back the ResultStorage
// the module was wired with, so a test can assert directly against the
// bucket the upload path actually wrote to.
func newTestVideoModuleWithStorage(t *testing.T) (*videoModule, *inMemoryVideoJobRepository, videodomain.ResultStorage) {
	t.Helper()
	module, repo, _, results := newTestVideoModuleWithBothStorages(t)
	return module, repo, results
}

func newTestVideoModuleWithBothStorages(t *testing.T) (*videoModule, *inMemoryVideoJobRepository, videodomain.SourceStorage, videodomain.ResultStorage) {
	t.Helper()
	repo := newInMemoryVideoJobRepository()
	ids := videoidgen.New()
	sources, results := newTestStorages(t)
	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, systemClock{}),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewEnqueueVideoJob(repo, ids),
		videoapplication.NewListUserResults(repo, results),
		newFakeIdempotencyStore(),
		repo,
		sources,
		results,
		ids,
	)
	return module, repo, sources, results
}

// startTestVideoServer wires a real router with a fake identity module (so
// tests can mint bearer tokens for arbitrary users) and a fake video module
// backed by an in-memory repository.
func startTestVideoServer(t *testing.T) (*httptest.Server, jwtauth.Adapter) {
	t.Helper()
	identity, tokens := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	return srv, tokens
}

func TestHandleCreateVideoJob_Success(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := postJSONWithAuthorization(t, srv.URL+"/api/video-jobs", "Bearer "+token, createVideoJobRequest{OriginalFilename: "movie.mp4"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var result videoJobResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result.JobID == "" {
		t.Fatal("expected a non-empty job_id")
	}
	if result.OriginalFilename != "movie.mp4" {
		t.Fatalf("original_filename = %q, want %q", result.OriginalFilename, "movie.mp4")
	}
	if result.Status != "pending" {
		t.Fatalf("status = %q, want %q", result.Status, "pending")
	}
	if result.CreatedAt.IsZero() {
		t.Fatal("expected a non-zero created_at")
	}
}

func TestHandleCreateVideoJob_MissingAuth_ReturnsUnauthorized(t *testing.T) {
	srv, _ := startTestVideoServer(t)

	resp := postJSON(t, srv.URL+"/api/video-jobs", createVideoJobRequest{OriginalFilename: "movie.mp4"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHandleCreateVideoJob_UnsupportedExtension_ReturnsBadRequest(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := postJSONWithAuthorization(t, srv.URL+"/api/video-jobs", "Bearer "+token, createVideoJobRequest{OriginalFilename: "notes.txt"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleCreateVideoJob_EmptyFilename_ReturnsBadRequest(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := postJSONWithAuthorization(t, srv.URL+"/api/video-jobs", "Bearer "+token, createVideoJobRequest{OriginalFilename: ""})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleCreateVideoJob_IgnoresCallerSuppliedOwnerField(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	body := []byte(`{"original_filename":"movie.mp4","user_id":"someone-else"}`)
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/video-jobs", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	var created videoJobResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}

	statusResp := getWithAuthorization(t, srv.URL+"/api/video-jobs/"+created.JobID, "Bearer "+token)
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("the authenticated user (%s) could not retrieve the job it just created — owner was not the authenticated user; status = %d", userID.String(), statusResp.StatusCode)
	}
}

func TestHandleGetVideoJobStatus_Owner_Success(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	created := createTestVideoJob(t, srv.URL, token, "movie.mp4")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs/"+created.JobID, "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result videoJobStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if result.JobID != created.JobID {
		t.Fatalf("job_id = %q, want %q", result.JobID, created.JobID)
	}
	if result.Status != "pending" {
		t.Fatalf("status = %q, want %q", result.Status, "pending")
	}
}

func TestHandleGetVideoJobStatus_NonOwner_ReturnsNotFound(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, ownerToken := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, otherToken := issueTestToken(t, tokens, "9c858901-8a57-4791-81fe-4c455b099bc9")

	created := createTestVideoJob(t, srv.URL, ownerToken, "movie.mp4")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs/"+created.JobID, "Bearer "+otherToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (non-owner must get the same response as a nonexistent job)", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHandleGetVideoJobStatus_Nonexistent_ReturnsNotFound(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs/"+videoidgen.New().NewVideoJobID().String(), "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusNotFound)
	}
}

func TestHandleGetVideoJobStatus_MalformedID_ReturnsBadRequest(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs/not-a-valid-id", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleListVideoJobs_DefaultPagination(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	createTestVideoJob(t, srv.URL, token, "one.mp4")
	createTestVideoJob(t, srv.URL, token, "two.mp4")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var result videoJobListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result.Jobs) != 2 {
		t.Fatalf("expected 2 jobs with default pagination, got %d", len(result.Jobs))
	}
}

func TestHandleListVideoJobs_OutOfRangeLimit_ReturnsBadRequest(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs?limit=0", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleListVideoJobs_NegativeOffset_ReturnsBadRequest(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs?offset=-1", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleListVideoJobs_NonIntegerLimit_ReturnsBadRequest(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs?limit=abc", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (a non-integer limit must not be treated as absent/defaulted)", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleListVideoJobs_NonIntegerOffset_ReturnsBadRequest(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs?offset=1.5", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d (a non-integer offset must not be treated as absent/defaulted)", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleListVideoJobs_EmptyLimitQueryValue_UsesDefault(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	createTestVideoJob(t, srv.URL, token, "one.mp4")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs?limit=&offset=", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d (an explicitly-empty query value is indistinguishable from absent and must default, not 400)", resp.StatusCode, http.StatusOK)
	}
}

func TestHandleListVideoJobs_ScopedToCaller(t *testing.T) {
	srv, tokens := startTestVideoServer(t)
	_, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, tokenB := issueTestToken(t, tokens, "9c858901-8a57-4791-81fe-4c455b099bc9")

	createTestVideoJob(t, srv.URL, tokenA, "a.mp4")
	createTestVideoJob(t, srv.URL, tokenB, "b.mp4")

	resp := getWithAuthorization(t, srv.URL+"/api/video-jobs", "Bearer "+tokenA)
	defer resp.Body.Close()

	var result videoJobListResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(result.Jobs) != 1 || result.Jobs[0].OriginalFilename != "a.mp4" {
		t.Fatalf("expected only the caller's own job, got: %+v", result.Jobs)
	}
}

// createTestVideoJob creates a job as the given token's user and returns the
// decoded response.
func createTestVideoJob(t *testing.T, baseURL, token, originalFilename string) videoJobResponse {
	t.Helper()
	resp := postJSONWithAuthorization(t, baseURL+"/api/video-jobs", "Bearer "+token, createVideoJobRequest{OriginalFilename: originalFilename})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("failed to create test video job: status = %d", resp.StatusCode)
	}
	var result videoJobResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	return result
}

func postJSONWithAuthorization(t *testing.T, url, authorizationHeader string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error marshaling request: %v", err)
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", authorizationHeader)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	return resp
}

func TestSetupVideo_DSNMissing_ReturnsError(t *testing.T) {
	t.Setenv("VIDEO_POSTGRES_DSN", "")

	module, db, _, _, err := setupVideo(context.Background())
	if err == nil {
		t.Fatal("expected an error when VIDEO_POSTGRES_DSN is not set")
	}
	if !errors.Is(err, videopostgres.ErrDSNRequired) {
		t.Fatalf("expected error to wrap videopostgres.ErrDSNRequired, got: %v", err)
	}
	if module != nil {
		t.Fatalf("expected a nil module on error, got %+v", module)
	}
	if db != nil {
		t.Fatalf("expected a nil db on error, got %+v", db)
	}
}

func TestSetupVideo_UnreachablePostgres_ReturnsError(t *testing.T) {
	// A loopback address on a port nothing listens on fails fast (connection
	// refused) rather than hanging, so this stays a fast unit-style test.
	t.Setenv("VIDEO_POSTGRES_DSN", "postgres://user:pass@127.0.0.1:1/video?sslmode=disable&connect_timeout=1")

	_, _, _, _, err := setupVideo(context.Background())
	if err == nil {
		t.Fatal("expected an error when configured PostgreSQL is unreachable")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("expected a non-empty error message")
	}
}

// TestSetupVideo_RabbitMQURLMissing_ReturnsError asserts the startup gate
// itself, which is why it does not configure anything else: setupVideo checks
// this variable before it opens a database, so the test reaches the gate
// whatever the rest of the environment holds. An earlier version of this test
// assumed VIDEO_POSTGRES_DSN was set, which is true in Docker and false in
// CI — where it failed on the wrong error entirely.
func TestSetupVideo_RabbitMQURLMissing_ReturnsError(t *testing.T) {
	// t.Setenv registers the restore; Unsetenv is what actually produces the
	// unset case a bare t.Setenv("", ...) would not.
	t.Setenv("RABBITMQ_URL", "")
	if err := os.Unsetenv("RABBITMQ_URL"); err != nil {
		t.Fatalf("unsetenv: %v", err)
	}

	module, db, _, relay, err := setupVideo(context.Background())
	if err == nil {
		t.Fatal("expected an error when RABBITMQ_URL is not set")
	}
	if !errors.Is(err, platformrabbitmq.ErrURLRequired) {
		t.Fatalf("expected error to wrap platformrabbitmq.ErrURLRequired, got: %v", err)
	}
	if module != nil || db != nil || relay != nil {
		t.Fatalf("expected nil module, db, and relay on error, got %+v %+v %+v", module, db, relay)
	}
}

// newIdempotencyTestVideoModule wires a videoModule backed by an in-memory
// repository and idempotency store, with a caller-supplied extractor for
// deterministic control over processing timing — unlike
// newTestVideoModuleWithRepo, which always uses a real ffmpeg extractor.
func newIdempotencyTestVideoModule() (*videoModule, *fakeIdempotencyStore, *inMemoryVideoJobRepository) {
	repo := newInMemoryVideoJobRepository()
	sources := newFakeSourceStorage()
	results := newFakeResultStorage()
	ids := videoidgen.New()
	store := newFakeIdempotencyStore()
	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, systemClock{}),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewEnqueueVideoJob(repo, ids),
		videoapplication.NewListUserResults(repo, results),
		store,
		repo,
		sources,
		results,
		ids,
	)
	return module, store, repo
}

func startIdempotencyTestServer(t *testing.T) (*httptest.Server, jwtauth.Adapter, *fakeIdempotencyStore) {
	t.Helper()
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, store, _ := newIdempotencyTestVideoModule()
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	return srv, tokens, store
}

// newIdempotencyTestVideoModuleWithRepo is newIdempotencyTestVideoModule but
// with a caller-supplied repository — lets a test inject a repository whose
// Create fails on demand (createFailingRepository), which
// newIdempotencyTestVideoModule's always-succeeding in-memory repository
// can't do.
func newIdempotencyTestVideoModuleWithRepo(repo videodomain.VideoJobRepository) (*videoModule, *fakeIdempotencyStore) {
	return newIdempotencyTestVideoModuleWithRepoAndStorage(repo, newFakeResultStorage())
}

// newIdempotencyTestVideoModuleWithRepoAndStorage additionally takes the
// ResultStorage, so a test can inject one whose Put fails — the storage
// equivalent of createFailingRepository.
func newIdempotencyTestVideoModuleWithRepoAndStorage(repo videodomain.VideoJobRepository, results videodomain.ResultStorage) (*videoModule, *fakeIdempotencyStore) {
	sources := newFakeSourceStorage()
	ids := videoidgen.New()
	store := newFakeIdempotencyStore()
	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, systemClock{}),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewEnqueueVideoJob(repo, ids),
		videoapplication.NewListUserResults(repo, results),
		store,
		repo,
		sources,
		results,
		ids,
	)
	return module, store
}

func startIdempotencyTestServerWithRepo(t *testing.T, repo videodomain.VideoJobRepository) (*httptest.Server, jwtauth.Adapter, *fakeIdempotencyStore) {
	return startIdempotencyTestServerWithRepoAndStorage(t, repo, newFakeResultStorage())
}

func startIdempotencyTestServerWithRepoAndStorage(t *testing.T, repo videodomain.VideoJobRepository, results videodomain.ResultStorage) (*httptest.Server, jwtauth.Adapter, *fakeIdempotencyStore) {
	t.Helper()
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, store := newIdempotencyTestVideoModuleWithRepoAndStorage(repo, results)
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	return srv, tokens, store
}

// writeTestUploadContent writes content to a temp file so uploadVideo (from
// main_test.go) can multipart-upload it — content is caller-controlled, so
// tests can force two uploads to be byte-identical (or deliberately not).
func writeTestUploadContent(t *testing.T, content []byte) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "video.mp4")
	if err := os.WriteFile(path, content, 0644); err != nil {
		t.Fatalf("failed to write test upload content: %v", err)
	}
	return path
}

// driveJobToCompleted and driveJobToFailed move a job past the point POST
// /upload can take it. cmd/worker owns both transitions now, so a test about
// what the *handler* does with an already-finished job has to put it there
// itself — through the same use cases the worker calls, not by writing rows.
func driveJobToCompleted(t *testing.T, m *videoModule, jobID string) {
	t.Helper()
	ctx := context.Background()
	claim, err := videoapplication.NewStartProcessing(m.jobs, m.jobs, m.idsFor).Execute(ctx, jobID)
	if err != nil {
		t.Fatalf("start processing %s: %v", jobID, err)
	}
	if _, err := videoapplication.NewCompleteJob(m.jobs, m.jobs, m.idsFor).Execute(ctx, videoapplication.CompleteJobInput{
		JobID:      jobID,
		StorageKey: "frames_" + jobID + ".zip",
		FrameCount: 3,
		LeaseEpoch: claim.LeaseEpoch,
	}); err != nil {
		t.Fatalf("complete %s: %v", jobID, err)
	}
}

func driveJobToFailed(t *testing.T, m *videoModule, jobID, reason string) {
	t.Helper()
	ctx := context.Background()
	claim, err := videoapplication.NewStartProcessing(m.jobs, m.jobs, m.idsFor).Execute(ctx, jobID)
	if err != nil {
		t.Fatalf("start processing %s: %v", jobID, err)
	}
	if _, err := videoapplication.NewFailJob(m.jobs, m.jobs, m.idsFor).Execute(ctx, videoapplication.FailJobInput{
		JobID:      jobID,
		Reason:     reason,
		LeaseEpoch: claim.LeaseEpoch,
	}); err != nil {
		t.Fatalf("fail %s: %v", jobID, err)
	}
}

// clearJobIdempotencyKeyAsWorker runs the clear the worker performs after a
// failure. It is the only thing that unblocks a retry of identical content,
// and it deliberately runs here through the real use case rather than by
// deleting from the fake store, so a test of "the retry is fresh" is
// testing the mechanism that actually ships.
func clearJobIdempotencyKeyAsWorker(t *testing.T, m *videoModule, jobID string) {
	t.Helper()
	cleared, err := videoapplication.NewClearJobIdempotencyKey(m.jobs, m.idempotency, m.idsFor).Execute(context.Background(), jobID)
	if err != nil {
		t.Fatalf("clear idempotency key for %s: %v", jobID, err)
	}
	if !cleared {
		t.Fatalf("clear idempotency key for %s reported nothing cleared", jobID)
	}
}

// jobStatus reads a job's current status through GET /api/video-jobs/:id —
// the endpoint the 202's status_url points at, so a duplicate's "the client
// learns the difference on its first poll" claim is checked the way a client
// would check it.
func jobStatus(t *testing.T, baseURL, token, jobID string) videoJobStatusResponse {
	t.Helper()
	resp := getWithAuthorization(t, baseURL+videoJobStatusPath(jobID), "Bearer "+token)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET job %s = %d, want %d", jobID, resp.StatusCode, http.StatusOK)
	}
	var status videoJobStatusResponse
	if err := json.NewDecoder(resp.Body).Decode(&status); err != nil {
		t.Fatalf("decode job status: %v", err)
	}
	return status
}

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// TestHandleVideoUpload_DuplicateWhileReservationInFlight_ReturnsExistingJob
// exercises the bounded wait-and-retry loop against a reservation that
// finalizes mid-wait: the duplicate must resolve to the original's job and
// answer with the ordinary acknowledgement, never a second job and never a
// second dispatch.
func TestHandleVideoUpload_DuplicateWhileReservationInFlight_ReturnsExistingJob(t *testing.T) {
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, store, repo := newIdempotencyTestVideoModule()
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	// Gate Finalize so the first request's reservation stays unfinalized
	// until this test explicitly releases it — otherwise the duplicate
	// below would always find an already-finalized key on its very first
	// Lookup, never actually exercising the bounded wait-and-retry loop
	// against an in-flight reservation that finalizes mid-wait.
	finalizeGate := make(chan struct{})
	store.finalizeGate = finalizeGate

	content := []byte("identical video content for concurrency test")
	videoPath := writeTestUploadContent(t, content)
	idemKey, err := videodomain.NewIdempotencyKey(userID.String(), sha256Hex(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	firstDone := make(chan struct{})
	var firstJobID string
	go func() {
		defer close(firstDone)
		firstJobID = uploadVideoAccepted(t, srv.URL, token, videoPath, "first.mp4").JobID
	}()

	// Wait until the first request has reserved (but, thanks to the gate,
	// not yet finalized) its idempotency key.
	deadline := time.Now().Add(5 * time.Second)
	for !store.hasReservation(idemKey) {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the first request to reserve its idempotency key")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Start the duplicate now: it must fail to reserve (the key is
	// already reserved) and enter the bounded wait-and-retry loop against
	// Lookup, which keeps returning found=false while Finalize stays
	// gated.
	duplicateDone := make(chan struct{})
	var dupAccepted uploadAcceptedResponse
	go func() {
		defer close(duplicateDone)
		dupAccepted = uploadVideoAccepted(t, srv.URL, token, videoPath, "duplicate.mp4")
	}()

	// Give the duplicate's poll loop time to actually run at least one
	// iteration against the still-unfinalized key before releasing
	// Finalize, so this genuinely covers a mid-wait transition rather
	// than an immediate hit on the loop's first check.
	time.Sleep(idempotencyLookupRetryInterval * 2)
	close(finalizeGate)

	<-duplicateDone
	<-firstDone

	if dupAccepted.JobID != firstJobID {
		t.Fatalf("duplicate job id = %q, want the original's %q", dupAccepted.JobID, firstJobID)
	}
	// The duplicate must have created nothing and dispatched nothing: one
	// job, one enqueue. enqueueCalls is the discriminating half — a second
	// job would also show up as a second dispatch, but a duplicate that
	// re-queued the *same* job would not show up in the count of rows.
	repo.mu.Lock()
	jobs, enqueues := len(repo.byID), repo.enqueueCalls
	repo.mu.Unlock()
	if jobs != 1 {
		t.Fatalf("jobs created = %d, want 1", jobs)
	}
	if enqueues != 1 {
		t.Fatalf("enqueue calls = %d, want 1 (the duplicate must not dispatch)", enqueues)
	}
}

// TestHandleVideoUpload_DuplicateAfterCompletion_ReturnsSameJobWithoutCreatingANewOne
// is the "no branch for the duplicate case" contract: a duplicate of a
// finished job gets the same acknowledgement shape as a fresh submission,
// naming the original, and the client learns it is done on its first poll.
func TestHandleVideoUpload_DuplicateAfterCompletion_ReturnsSameJobWithoutCreatingANewOne(t *testing.T) {
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, _, repo := newIdempotencyTestVideoModule()
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("identical video content for completed-duplicate test")
	videoPath := writeTestUploadContent(t, content)

	first := uploadVideoAccepted(t, srv.URL, token, videoPath, "first.mp4")
	driveJobToCompleted(t, module, first.JobID)

	duplicate := uploadVideoAccepted(t, srv.URL, token, videoPath, "duplicate.mp4")
	if duplicate.JobID != first.JobID {
		t.Fatalf("duplicate job id = %q, want the original's %q", duplicate.JobID, first.JobID)
	}
	// The acknowledgement reports the job's real status, and it is the
	// poll — not the upload response — that carries the outcome.
	if duplicate.Status != string(videodomain.JobStatusCompleted) {
		t.Fatalf("duplicate status = %q, want %q", duplicate.Status, videodomain.JobStatusCompleted)
	}
	if status := jobStatus(t, srv.URL, token, duplicate.JobID); status.Status != string(videodomain.JobStatusCompleted) {
		t.Fatalf("polled status = %q, want %q", status.Status, videodomain.JobStatusCompleted)
	}

	repo.mu.Lock()
	jobs, enqueues := len(repo.byID), repo.enqueueCalls
	repo.mu.Unlock()
	if jobs != 1 {
		t.Fatalf("jobs created = %d, want 1 (the duplicate must not create a second)", jobs)
	}
	if enqueues != 1 {
		t.Fatalf("enqueue calls = %d, want 1 (the duplicate must not dispatch)", enqueues)
	}
}

// TestHandleVideoUpload_RetryAfterWorkerClearedTheKey_CreatesNewJob is the
// retry-after-failure path with its new owner: the handler no longer learns
// that a job failed, so nothing it does unblocks the retry. What unblocks it
// is the worker's clear — and until that runs, an identical resubmission is
// still answered with a reference to the failed job.
func TestHandleVideoUpload_RetryAfterWorkerClearedTheKey_CreatesNewJob(t *testing.T) {
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, _, repo := newIdempotencyTestVideoModule()
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("identical video content for retry-after-failure test")
	videoPath := writeTestUploadContent(t, content)

	first := uploadVideoAccepted(t, srv.URL, token, videoPath, "fails.mp4")
	driveJobToFailed(t, module, first.JobID, "simulated extraction failure")

	// Before the clear: still a duplicate. This is the discriminating half
	// — without it, a retry that created a new job would pass below even if
	// the key had never been finalized in the first place.
	blocked := uploadVideoAccepted(t, srv.URL, token, videoPath, "too-early.mp4")
	if blocked.JobID != first.JobID {
		t.Fatalf("pre-clear retry job id = %q, want the failed job's %q", blocked.JobID, first.JobID)
	}

	clearJobIdempotencyKeyAsWorker(t, module, first.JobID)

	retry := uploadVideoAccepted(t, srv.URL, token, videoPath, "retry.mp4")
	if retry.JobID == first.JobID {
		t.Fatalf("retry job id = %q, want a new job — the cleared key must make it a fresh attempt", retry.JobID)
	}
	if retry.Status != string(videodomain.JobStatusQueued) {
		t.Fatalf("retry status = %q, want %q", retry.Status, videodomain.JobStatusQueued)
	}

	repo.mu.Lock()
	jobs, enqueues := len(repo.byID), repo.enqueueCalls
	repo.mu.Unlock()
	if jobs != 2 {
		t.Fatalf("jobs created = %d, want 2 (the original and the retry)", jobs)
	}
	if enqueues != 2 {
		t.Fatalf("enqueue calls = %d, want 2", enqueues)
	}
}

func TestHandleVideoUpload_DifferentUsersSameContent_BothSucceedIndependently(t *testing.T) {
	srv, tokens, _ := startIdempotencyTestServer(t)
	_, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, tokenB := issueTestToken(t, tokens, "7c9e6679-7425-40de-944b-e07fc1f90ae7")

	content := []byte("identical video content shared by two different users")
	videoPath := writeTestUploadContent(t, content)

	acceptedA := uploadVideoAccepted(t, srv.URL, tokenA, videoPath, "userA.mp4")
	acceptedB := uploadVideoAccepted(t, srv.URL, tokenB, videoPath, "userB.mp4")

	if acceptedA.JobID == acceptedB.JobID {
		t.Fatalf("both users were given job %q — identical content across users must not deduplicate", acceptedA.JobID)
	}
}

func TestHandleVideoUpload_ReservationNeverResolves_ReturnsConflict(t *testing.T) {
	srv, tokens, store := startIdempotencyTestServer(t)
	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("content whose reservation is pre-seeded and never resolved")
	videoPath := writeTestUploadContent(t, content)
	idemKey, err := videodomain.NewIdempotencyKey(userID.String(), sha256Hex(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Pre-seed a reservation directly, bypassing a real request, and
	// never finalize or clear it — simulating a handler that crashed
	// mid-flight.
	if _, reserved, err := store.Reserve(context.Background(), idemKey); err != nil || !reserved {
		t.Fatalf("failed to pre-seed reservation: reserved=%v err=%v", reserved, err)
	}

	resp, result := uploadVideo(t, srv.URL, token, videoPath, "conflict.mp4")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
	if result.Success {
		t.Fatal("expected Success=false for a 409 response")
	}
}

// TestHandleVideoUpload_DuplicateAfterFailure_ReturnsTheFailedJobBeforeClear
// covers the narrow window between a job reaching failed and the worker
// clearing its key. The duplicate is answered with a reference to the failed
// job — the same shape as every other duplicate — rather than with the
// failure itself, which the acknowledgement has no field for and which the
// client reads off its first poll instead.
func TestHandleVideoUpload_DuplicateAfterFailure_ReturnsTheFailedJobBeforeClear(t *testing.T) {
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, _, repo := newIdempotencyTestVideoModule()
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("identical video content for duplicate-after-failure test")
	videoPath := writeTestUploadContent(t, content)

	first := uploadVideoAccepted(t, srv.URL, token, videoPath, "first.mp4")
	driveJobToFailed(t, module, first.JobID, "simulated extraction failure")

	duplicate := uploadVideoAccepted(t, srv.URL, token, videoPath, "duplicate.mp4")
	if duplicate.JobID != first.JobID {
		t.Fatalf("duplicate job id = %q, want the failed job's %q", duplicate.JobID, first.JobID)
	}
	if duplicate.Status != string(videodomain.JobStatusFailed) {
		t.Fatalf("duplicate status = %q, want %q", duplicate.Status, videodomain.JobStatusFailed)
	}

	// The failure reason reaches the client through the poll, which is the
	// only place it lives now.
	status := jobStatus(t, srv.URL, token, duplicate.JobID)
	if !strings.Contains(status.ErrorReason, "simulated extraction failure") {
		t.Fatalf("polled error_reason = %q, want it to carry the original job's failure reason", status.ErrorReason)
	}

	repo.mu.Lock()
	jobs := len(repo.byID)
	repo.mu.Unlock()
	if jobs != 1 {
		t.Fatalf("jobs created = %d, want 1 (the duplicate must not create a second)", jobs)
	}
}

func TestHandleVideoUpload_CreateVideoJobFailure_ClearsReservationForImmediateRetry(t *testing.T) {
	repo := newCreateFailingRepository(1)
	srv, tokens, store := startIdempotencyTestServerWithRepo(t, repo)
	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("identical video content for create-failure retry test")
	videoPath := writeTestUploadContent(t, content)
	idemKey, err := videodomain.NewIdempotencyKey(userID.String(), sha256Hex(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp1, result1 := uploadVideo(t, srv.URL, token, videoPath, "first.mp4")
	defer resp1.Body.Close()
	if resp1.StatusCode != http.StatusInternalServerError {
		t.Fatalf("first upload status = %d, want %d (CreateVideoJob is forced to fail)", resp1.StatusCode, http.StatusInternalServerError)
	}
	if result1.Success {
		t.Fatalf("first upload should fail, got: %+v", result1)
	}

	// Check directly, not just via the retry's outcome below: a leftover
	// reservation here would still let the retry pass this test through a
	// different, unintended path — a broken Clear could leave the key
	// reserved (not finalized), sending the retry through the
	// bounded-retry-then-lookup loop, which would eventually time out and
	// return 409 rather than proving the reservation was actually cleared.
	if store.hasReservation(idemKey) {
		t.Fatal("CreateVideoJob failure must have cleared the reservation, but one is still present")
	}

	// An immediate retry with identical content must not be blocked by a
	// leftover reservation — CreateVideoJob's failure must have cleared
	// it. repo.failLeft is now 0, so this retry's own CreateVideoJob call
	// succeeds.
	retry := uploadVideoAccepted(t, srv.URL, token, videoPath, "retry.mp4")
	if retry.Status != string(videodomain.JobStatusQueued) {
		t.Fatalf("retry status = %q, want %q", retry.Status, videodomain.JobStatusQueued)
	}
}

// TestHandleVideoUpload_ReserveError_ProceedsWithoutIdempotencyProtection
// covers fail-open-upload-idempotency: a Reserve error (Redis down/erroring)
// must not block the upload — the request should succeed exactly as it
// would with no idempotency layer at all, instead of the old 500 "Failed to
// check upload idempotency".
func TestHandleVideoUpload_ReserveError_ProceedsWithoutIdempotencyProtection(t *testing.T) {
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, store, repo := newIdempotencyTestVideoModule()
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	store.reserveErr = errors.New("simulated redis outage")

	content := []byte("content uploaded while the idempotency store is erroring")
	videoPath := writeTestUploadContent(t, content)

	accepted := uploadVideoAccepted(t, srv.URL, token, videoPath, "reserve-error.mp4")
	if accepted.Status != string(videodomain.JobStatusQueued) {
		t.Fatalf("status = %q, want %q", accepted.Status, videodomain.JobStatusQueued)
	}
	// Proceeding "as if there were no idempotency layer" means the job was
	// really queued, not merely acknowledged.
	repo.mu.Lock()
	enqueues := repo.enqueueCalls
	repo.mu.Unlock()
	if enqueues != 1 {
		t.Fatalf("enqueue calls = %d, want 1", enqueues)
	}
}

// TestHandleVideoUpload_ReserveError_NeverCallsFinalizeOrClear proves the
// guard in handleVideoUpload actually skips Finalize/Clear when there was
// never a valid reservation, rather than calling them with an empty token
// (see fail-open-upload-idempotency's design.md Decision 1).
func TestHandleVideoUpload_ReserveError_NeverCallsFinalizeOrClear(t *testing.T) {
	srv, tokens, store := startIdempotencyTestServer(t)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	store.reserveErr = errors.New("simulated redis outage")

	content := []byte("content whose Finalize/Clear must never be invoked")
	videoPath := writeTestUploadContent(t, content)

	uploadVideoAccepted(t, srv.URL, token, videoPath, "no-finalize.mp4")
	if finalizeCalls, clearCalls := store.callCounts(); finalizeCalls != 0 || clearCalls != 0 {
		t.Fatalf("finalizeCalls=%d clearCalls=%d, want 0/0 (no reservation to finalize or clear)", finalizeCalls, clearCalls)
	}
}

// TestHandleVideoUpload_ReserveError_CreateVideoJobFailureStillSkipsClear
// confirms the guard on the CreateVideoJob-failure Clear call site: with no
// valid reservation (Reserve errored), Clear must never be called there
// either, and the response must reflect the CreateVideoJob failure itself
// (not the old Reserve-error 500).
func TestHandleVideoUpload_ReserveError_CreateVideoJobFailureStillSkipsClear(t *testing.T) {
	repo := newCreateFailingRepository(1)
	srv, tokens, store := startIdempotencyTestServerWithRepo(t, repo)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	store.reserveErr = errors.New("simulated redis outage")

	content := []byte("content whose CreateVideoJob failure must still skip Clear")
	videoPath := writeTestUploadContent(t, content)

	resp, result := uploadVideo(t, srv.URL, token, videoPath, "reserve-error-create-failure.mp4")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (CreateVideoJob is forced to fail)", resp.StatusCode, http.StatusInternalServerError)
	}
	if result.Success {
		t.Fatalf("CreateVideoJob was forced to fail, expected Success=false, got: %+v", result)
	}
	if strings.Contains(result.Message, "idempotency") {
		t.Fatalf("message = %q, should reflect the CreateVideoJob failure, not the old Reserve-error message", result.Message)
	}
	if finalizeCalls, clearCalls := store.callCounts(); finalizeCalls != 0 || clearCalls != 0 {
		t.Fatalf("finalizeCalls=%d clearCalls=%d, want 0/0 (no reservation to finalize or clear)", finalizeCalls, clearCalls)
	}
}

// TestHandleVideoUpload_ReserveError_EnqueueFailureStillSkipsClear confirms
// the guard also holds on the handler's remaining late failure path. It
// replaces the extraction- and result-storage-failure variants of this test:
// neither step runs in this process any more, but the invariant they
// protected is unchanged — a request that failed to Reserve holds no token,
// so no later failure path may Clear a key that belongs to some other
// request.
func TestHandleVideoUpload_ReserveError_EnqueueFailureStillSkipsClear(t *testing.T) {
	repo := newInMemoryVideoJobRepository()
	repo.enqueueErr = errors.New("outbox write failed")
	srv, tokens, store := startIdempotencyTestServerWithRepo(t, repo)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	store.reserveErr = errors.New("simulated redis outage")

	content := []byte("content whose enqueue failure must still skip Clear")
	videoPath := writeTestUploadContent(t, content)

	resp, result := uploadVideo(t, srv.URL, token, videoPath, "reserve-error-enqueue-failure.mp4")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d (the enqueue is forced to fail)", resp.StatusCode, http.StatusInternalServerError)
	}
	if result.Success {
		t.Fatalf("expected Success=false when the job could not be queued, got %+v", result)
	}
	// The discriminating assertion: pre-fix code returns 500 for the
	// Reserve error itself, before ever reaching CreateVideoJob — which
	// would make finalizeCalls/clearCalls == 0 trivially true for the wrong
	// reason. Reaching the enqueue proves the request really did proceed
	// past Reserve into the branch the guard covers.
	repo.mu.Lock()
	enqueues := repo.enqueueCalls
	repo.mu.Unlock()
	if enqueues != 1 {
		t.Fatalf("enqueue calls = %d, want 1 (the request must proceed past the Reserve error)", enqueues)
	}
	if finalizeCalls, clearCalls := store.callCounts(); finalizeCalls != 0 || clearCalls != 0 {
		t.Fatalf("finalizeCalls=%d clearCalls=%d, want 0/0 (no reservation to finalize or clear)", finalizeCalls, clearCalls)
	}
}

// TestHandleVideoUpload_GenuineConflict_StillReturns409 guards against a
// regression where the fail-open-upload-idempotency change could
// accidentally widen its "proceed anyway" behavior to the unrelated,
// already-correct reserved=false/err=nil conflict path.
func TestHandleVideoUpload_GenuineConflict_StillReturns409(t *testing.T) {
	srv, tokens, store := startIdempotencyTestServer(t)
	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("content whose reservation is pre-seeded and never resolved (regression guard)")
	videoPath := writeTestUploadContent(t, content)
	idemKey, err := videodomain.NewIdempotencyKey(userID.String(), sha256Hex(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, reserved, err := store.Reserve(context.Background(), idemKey); err != nil || !reserved {
		t.Fatalf("failed to pre-seed reservation: reserved=%v err=%v", reserved, err)
	}

	resp, result := uploadVideo(t, srv.URL, token, videoPath, "still-conflict.mp4")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d (a genuine conflict must be unaffected by the fail-open change)", resp.StatusCode, http.StatusConflict)
	}
	if result.Success {
		t.Fatal("expected Success=false for a 409 response")
	}
}

// fakeResultStorage is an in-memory videodomain.ResultStorage for tests that
// aren't about object storage itself (the idempotency suite), and for the
// one that needs a Put failure it can trigger on demand.
type fakeResultStorage struct {
	mu         sync.Mutex
	objects    map[string][]byte
	times      map[string]time.Time
	putErr     error
	statErr    error
	presignErr error
}

func newFakeResultStorage() *fakeResultStorage {
	return &fakeResultStorage{objects: make(map[string][]byte), times: make(map[string]time.Time)}
}

func (s *fakeResultStorage) Put(_ context.Context, key videodomain.StorageKey, localPath string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.putErr != nil {
		return s.putErr
	}
	data, err := os.ReadFile(localPath) // #nosec G304
	if err != nil {
		return err
	}
	s.objects[key.String()] = data
	s.times[key.String()] = time.Now()
	return nil
}

// PresignGet mimics the real adapter's offline signing: it never consults
// the stored objects, so an absent key yields a URL here exactly as it would
// against MinIO. presignErr drives the handler's presign-failure branch,
// which is otherwise unreachable.
func (s *fakeResultStorage) PresignGet(_ context.Context, key videodomain.StorageKey, ttl time.Duration, _ string) (string, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.presignErr != nil {
		return "", time.Time{}, s.presignErr
	}
	return "https://storage.test/" + key.String() + "?signature=fake", time.Now().Add(ttl), nil
}

func (s *fakeResultStorage) Stat(_ context.Context, key videodomain.StorageKey) (int64, time.Time, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.statErr != nil {
		return 0, time.Time{}, s.statErr
	}
	data, ok := s.objects[key.String()]
	if !ok {
		return 0, time.Time{}, videodomain.ErrResultNotFound
	}
	return int64(len(data)), s.times[key.String()], nil
}

// fakeSourceStorage is an in-memory domain.SourceStorage for handler tests
// that need deterministic control over timing or failure, where a real
// bucket would only add latency.
type fakeSourceStorage struct {
	mu      sync.Mutex
	objects map[string][]byte
	putErr  error
}

func newFakeSourceStorage() *fakeSourceStorage {
	return &fakeSourceStorage{objects: make(map[string][]byte)}
}

func (s *fakeSourceStorage) Put(_ context.Context, key videodomain.StorageKey, r io.Reader) error {
	if s.putErr != nil {
		return s.putErr
	}
	content, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.objects[key.String()] = content
	return nil
}

func (s *fakeSourceStorage) Get(_ context.Context, key videodomain.StorageKey, localPath string) error {
	s.mu.Lock()
	content, ok := s.objects[key.String()]
	s.mu.Unlock()
	if !ok {
		return videodomain.ErrSourceNotFound
	}
	if err := os.MkdirAll(filepath.Dir(localPath), 0750); err != nil {
		return err
	}
	return os.WriteFile(localPath, content, 0600)
}

func (s *fakeSourceStorage) Delete(_ context.Context, key videodomain.StorageKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.objects, key.String())
	return nil
}

func (s *fakeSourceStorage) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.objects)
}

// startSourceStorageTestServer runs the real router against a real bucket
// and hands back an inspector for it, so a test can assert what a request
// left behind under the uploads/ prefix.
func startSourceStorageTestServer(t *testing.T) (srv *httptest.Server, token, userID string, module *videoModule, repo *inMemoryVideoJobRepository, inspector bucketInspector) {
	t.Helper()
	identity, tokens := newTestIdentityModuleWithTokens(t)

	repo = newInMemoryVideoJobRepository()
	ids := videoidgen.New()
	sources, results, inspector := newTestStoragesWithInspector(t)
	module = newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, systemClock{}),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewEnqueueVideoJob(repo, ids),
		videoapplication.NewListUserResults(repo, results),
		newFakeIdempotencyStore(),
		repo,
		sources,
		results,
		ids,
	)

	srv = httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	user, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	return srv, token, user.String(), module, repo, inspector
}

// TestUpload_Success_StoresResultAndDeletesSourceObject is the core
// assertion of this change: the source lives in the bucket only for the
// duration of the request, and the result is what survives.
func TestUpload_Success_LeavesTheSourceObjectForTheWorker(t *testing.T) {
	srv, token, _, _, _, inspector := startSourceStorageTestServer(t)
	videoPath := generateTestVideo(t, 1)

	uploadVideoAccepted(t, srv.URL, token, videoPath, "source-lifecycle.mp4")

	// Ownership of the source object transfers at the enqueue commit: after
	// it, the object belongs to the consumer that will read it. Deleting it
	// here — which is exactly what the pre-cutover handler did — would pull
	// the input out from under a worker that has already been dispatched.
	if stored := inspector.keysWithPrefix(t, "uploads/"); len(stored) != 1 {
		t.Fatalf("source objects = %v, want exactly one — the worker has not read it yet", stored)
	}
	if stored := inspector.keysWithPrefix(t, "frames_"); len(stored) != 0 {
		t.Fatalf("result objects = %v, want none — the API extracts nothing", stored)
	}
}

// TestUpload_EnqueueFailure_DeletesStoredSourceObject is the other half of
// the ownership transfer: a job that never reached queued was never
// dispatched, so nobody is going to read its source and the handler still
// owns it. Asserted against the real bucket, because that is where the leak
// would accumulate.
func TestUpload_EnqueueFailure_DeletesStoredSourceObject(t *testing.T) {
	srv, token, _, _, repo, inspector := startSourceStorageTestServer(t)
	repo.enqueueErr = errors.New("outbox write failed")
	videoPath := generateTestVideo(t, 1)

	resp, result := uploadVideo(t, srv.URL, token, videoPath, "failed-source-cleanup.mp4")
	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
	if result.Success {
		t.Fatal("expected a failed result when the job could not be queued")
	}

	if leftover := inspector.keysWithPrefix(t, "uploads/"); len(leftover) != 0 {
		t.Fatalf("source objects left in the bucket after an enqueue failure: %v — nothing will ever read them", leftover)
	}
}

// newEnqueueTestVideoModule is newIdempotencyTestVideoModule but also hands
// back the SourceStorage fake, so a test can assert the handler's deferred
// cleanup ran on a branch that never reaches processing.
func newEnqueueTestVideoModule() (*videoModule, *fakeIdempotencyStore, *inMemoryVideoJobRepository, *fakeSourceStorage) {
	repo := newInMemoryVideoJobRepository()
	sources := newFakeSourceStorage()
	results := newFakeResultStorage()
	ids := videoidgen.New()
	store := newFakeIdempotencyStore()
	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, systemClock{}),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewEnqueueVideoJob(repo, ids),
		videoapplication.NewListUserResults(repo, results),
		store,
		repo,
		sources,
		results,
		ids,
	)
	return module, store, repo, sources
}

// TestHandleVideoUpload_QueuesTheJobThroughTheOutboxWritingPath is what the
// 202 has to actually mean. Enqueue is the only repository method that writes
// the video_job.queued outbox row the relay dispatches from, so a handler
// that used a plain Update instead would still answer 202 with a job in
// "queued" and pass every other assertion in this file — while dispatching
// nothing and leaving the job to sit there forever.
func TestHandleVideoUpload_QueuesTheJobThroughTheOutboxWritingPath(t *testing.T) {
	module, _, repo, _ := newEnqueueTestVideoModule()
	identity, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	defer srv.Close()
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	videoPath := writeTestUploadContent(t, []byte("enqueue ordering content"))
	accepted := uploadVideoAccepted(t, srv.URL, token, videoPath, "movie.mp4")

	if accepted.Status != string(videodomain.JobStatusQueued) {
		t.Fatalf("status = %q, want %q", accepted.Status, videodomain.JobStatusQueued)
	}
	repo.mu.Lock()
	calls := repo.enqueueCalls
	repo.mu.Unlock()
	if calls != 1 {
		t.Fatalf("repo.enqueueCalls = %d, want 1 — the job must be queued through the outbox-writing path", calls)
	}
}

// TestHandleVideoUpload_EnqueueFailure_DoesNotProcessAndReleasesEverything
// covers the failure branch the enqueue step introduces, in a handler that
// already coordinates a reservation, a stored object, and a job row. The
// success path cannot catch a regression here, and each of the three
// resources has its own way of leaking.
func TestHandleVideoUpload_EnqueueFailure_DoesNotProcessAndReleasesEverything(t *testing.T) {
	module, store, repo, sources := newEnqueueTestVideoModule()
	repo.enqueueErr = errors.New("outbox write failed")
	identity, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	defer srv.Close()
	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("enqueue failure content")
	videoPath := writeTestUploadContent(t, content)
	resp, result := uploadVideo(t, srv.URL, token, videoPath, "movie.mp4")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusInternalServerError)
	}
	if result.Success {
		t.Fatalf("expected a failed result, got: %+v", result)
	}
	// The handler's deferred cleanup owns the source object on every exit
	// path that did not queue the job — and this is one of them: nothing
	// was dispatched, so nothing will ever read these bytes.
	if sources.count() != 0 {
		t.Fatalf("source objects remaining = %d, want 0", sources.count())
	}
	// The reservation must not outlive the request, or an immediate retry of
	// the same content is blocked for the sentinel's full TTL.
	idemKey, err := videodomain.NewIdempotencyKey(userID.String(), sha256Hex(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if store.hasReservation(idemKey) {
		t.Fatal("the idempotency reservation survived an enqueue failure; a retry of the same content would be blocked")
	}
}

// TestHandleVideoUpload_QueuesTheJobBeforeFinalizingItsIdempotencyKey pins an
// ordering the compiler cannot, and which a reviewer will reasonably want to
// invert: upload-idempotency requires the key to be finalized only once both
// CreateVideoJob and EnqueueVideoJob have succeeded, because a finalized key
// advertises its job to every duplicate for the full 24-hour window — so
// finalizing a job that then failed to reach queued would deduplicate later
// uploads of the same bytes onto a job stuck in pending that nothing will
// ever process.
//
// The inverse ordering closes a different window (the worker's ClearByJob
// finds a bare reservation and correctly does nothing, after which the
// handler finalizes over it and pins the key to a failed job) at the cost of
// the one above. Both are narrow and both end in a 24-hour block; the spec
// picks this side, and this test is what stops a well-meaning local fix from
// quietly picking the other.
func TestHandleVideoUpload_QueuesTheJobBeforeFinalizingItsIdempotencyKey(t *testing.T) {
	module, store, repo, _ := newEnqueueTestVideoModule()
	identity, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	defer srv.Close()
	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("enqueue before finalize content")
	idemKey, err := videodomain.NewIdempotencyKey(userID.String(), sha256Hex(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var finalizedAtEnqueue bool
	repo.beforeEnqueue = func() { finalizedAtEnqueue = store.isFinalized(idemKey) }

	videoPath := writeTestUploadContent(t, content)
	uploadVideoAccepted(t, srv.URL, token, videoPath, "movie.mp4")

	if repo.enqueueCalls != 1 {
		t.Fatalf("enqueue calls = %d, want 1", repo.enqueueCalls)
	}
	if finalizedAtEnqueue {
		t.Fatal("the idempotency key was already finalized when the job was queued; an enqueue that then failed would advertise a job stuck in pending to every duplicate for the full window")
	}
	if !store.isFinalized(idemKey) {
		t.Fatal("expected the key to be finalized once the job was queued")
	}
}
