package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"

	"video-processor/internal/identity/infrastructure/jwtauth"
	videoapplication "video-processor/internal/video/application"
	videodomain "video-processor/internal/video/domain"
	videoffmpeg "video-processor/internal/video/infrastructure/ffmpeg"
	videoidgen "video-processor/internal/video/infrastructure/idgen"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
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
	entry, exists := s.entries[key.String()]
	if !exists || entry.token != token {
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
}

func newInMemoryVideoJobRepository() *inMemoryVideoJobRepository {
	return &inMemoryVideoJobRepository{byID: make(map[string]*videodomain.VideoJob)}
}

func cloneVideoJob(job *videodomain.VideoJob) *videodomain.VideoJob {
	clone, err := videodomain.RestoreVideoJob(job.ID(), job.UserID(), job.OriginalFilename(), job.StorageKey(), job.FrameCount(), job.ErrorReason(), job.Status(), job.CreatedAt())
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

func (r *inMemoryVideoJobRepository) Update(_ context.Context, job *videodomain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.byID[job.ID().String()]; !ok {
		return videodomain.ErrVideoJobNotFound
	}
	r.byID[job.ID().String()] = cloneVideoJob(job)
	return nil
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
	repo := newInMemoryVideoJobRepository()
	ids := videoidgen.New()
	extractor := videoffmpeg.New()
	completeJob := videoapplication.NewCompleteJob(repo, ids)
	failJob := videoapplication.NewFailJob(repo, ids)
	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, systemClock{}),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewProcessVideoJob(
			videoapplication.NewEnqueueVideoJob(repo, ids),
			videoapplication.NewStartProcessing(repo, ids),
			failJob,
			extractor,
			ids,
		),
		completeJob,
		failJob,
		newFakeIdempotencyStore(),
	)
	return module, repo
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

func TestExtractionFailureMessage_MapsSentinelErrorsToDistinctPtBRMessages(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want string
	}{
		{"no frames", videoffmpeg.ErrNoFramesExtracted, "Nenhum frame foi extraído do vídeo"},
		{"ffmpeg exec failed", videoffmpeg.ErrFfmpegExecFailed, "Erro no ffmpeg: boom"},
		{"zip creation failed", videoffmpeg.ErrZipCreationFailed, "Erro ao criar arquivo ZIP: boom"},
		{"unclassified", errors.New("something else"), "Erro no processamento: boom"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractionFailureMessage(tc.err, "boom")
			if got != tc.want {
				t.Fatalf("extractionFailureMessage() = %q, want %q", got, tc.want)
			}
		})
	}
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

	module, db, _, err := setupVideo(context.Background())
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

	_, _, _, err := setupVideo(context.Background())
	if err == nil {
		t.Fatal("expected an error when configured PostgreSQL is unreachable")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("expected a non-empty error message")
	}
}

// blockingFrameExtractor blocks ExtractFrames until release is closed, then
// returns a fixed result (or failWith, if set) — used to deterministically
// control how long a job stays "processing" without relying on real ffmpeg
// timing, for tests exercising the idempotency mechanism's concurrent-
// duplicate and retry-after-failure paths. invocations counts calls, so
// tests can prove a duplicate did (or didn't) actually reach ffmpeg, rather
// than only comparing response fields a buggy reprocessed run could also
// produce identically (the fixed storageKey/frameCount would match either
// way).
type blockingFrameExtractor struct {
	release     chan struct{}
	storageKey  string
	frameCount  int
	failWith    error
	invocations int32
}

func newImmediateFrameExtractor(storageKey string, frameCount int) *blockingFrameExtractor {
	e := &blockingFrameExtractor{release: make(chan struct{}), storageKey: storageKey, frameCount: frameCount}
	close(e.release)
	return e
}

func newFailingFrameExtractor(failWith error) *blockingFrameExtractor {
	e := &blockingFrameExtractor{release: make(chan struct{}), failWith: failWith}
	close(e.release)
	return e
}

func (f *blockingFrameExtractor) ExtractFrames(ctx context.Context, _ videodomain.VideoJobID, _ string) (videodomain.StorageKey, int, []string, error) {
	atomic.AddInt32(&f.invocations, 1)
	select {
	case <-f.release:
	case <-ctx.Done():
		return videodomain.StorageKey{}, 0, nil, ctx.Err()
	}
	if f.failWith != nil {
		return videodomain.StorageKey{}, 0, nil, f.failWith
	}
	key, err := videodomain.NewStorageKey(f.storageKey)
	if err != nil {
		panic(err)
	}
	return key, f.frameCount, []string{"frame_0001.png"}, nil
}

func (f *blockingFrameExtractor) Invocations() int {
	return int(atomic.LoadInt32(&f.invocations))
}

// newIdempotencyTestVideoModule wires a videoModule backed by an in-memory
// repository and idempotency store, with a caller-supplied extractor for
// deterministic control over processing timing — unlike
// newTestVideoModuleWithRepo, which always uses a real ffmpeg extractor.
func newIdempotencyTestVideoModule(extractor videodomain.FrameExtractor) (*videoModule, *fakeIdempotencyStore, *inMemoryVideoJobRepository) {
	repo := newInMemoryVideoJobRepository()
	ids := videoidgen.New()
	completeJob := videoapplication.NewCompleteJob(repo, ids)
	failJob := videoapplication.NewFailJob(repo, ids)
	store := newFakeIdempotencyStore()
	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, systemClock{}),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewProcessVideoJob(
			videoapplication.NewEnqueueVideoJob(repo, ids),
			videoapplication.NewStartProcessing(repo, ids),
			failJob,
			extractor,
			ids,
		),
		completeJob,
		failJob,
		store,
	)
	return module, store, repo
}

func startIdempotencyTestServer(t *testing.T, extractor videodomain.FrameExtractor) (*httptest.Server, jwtauth.Adapter, *fakeIdempotencyStore) {
	t.Helper()
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, store, _ := newIdempotencyTestVideoModule(extractor)
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)
	return srv, tokens, store
}

// newIdempotencyTestVideoModuleWithRepo is newIdempotencyTestVideoModule but
// with a caller-supplied repository — lets a test inject a repository whose
// Create fails on demand (createFailingRepository), which
// newIdempotencyTestVideoModule's always-succeeding in-memory repository
// can't do.
func newIdempotencyTestVideoModuleWithRepo(extractor videodomain.FrameExtractor, repo videodomain.VideoJobRepository) (*videoModule, *fakeIdempotencyStore) {
	ids := videoidgen.New()
	completeJob := videoapplication.NewCompleteJob(repo, ids)
	failJob := videoapplication.NewFailJob(repo, ids)
	store := newFakeIdempotencyStore()
	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, systemClock{}),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewProcessVideoJob(
			videoapplication.NewEnqueueVideoJob(repo, ids),
			videoapplication.NewStartProcessing(repo, ids),
			failJob,
			extractor,
			ids,
		),
		completeJob,
		failJob,
		store,
	)
	return module, store
}

func startIdempotencyTestServerWithRepo(t *testing.T, extractor videodomain.FrameExtractor, repo videodomain.VideoJobRepository) (*httptest.Server, jwtauth.Adapter, *fakeIdempotencyStore) {
	t.Helper()
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, store := newIdempotencyTestVideoModuleWithRepo(extractor, repo)
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

func sha256Hex(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

func TestHandleVideoUpload_DuplicateWhileProcessing_ReturnsExistingJobWithoutReprocessing(t *testing.T) {
	extractor := &blockingFrameExtractor{release: make(chan struct{}), storageKey: "frames_dup.zip", frameCount: 3}
	srv, tokens, store := startIdempotencyTestServer(t, extractor)
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
	go func() {
		defer close(firstDone)
		resp, result := uploadVideo(t, srv.URL, token, videoPath, "first.mp4")
		defer resp.Body.Close()
		if !result.Success {
			t.Errorf("first upload should succeed once released, got: %+v", result)
		}
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
	var dupResp *http.Response
	var dupResult ProcessingResult
	go func() {
		defer close(duplicateDone)
		dupResp, dupResult = uploadVideo(t, srv.URL, token, videoPath, "duplicate.mp4")
	}()

	// Give the duplicate's poll loop time to actually run at least one
	// iteration against the still-unfinalized key before releasing
	// Finalize, so this genuinely covers a mid-wait transition rather
	// than an immediate hit on the loop's first check.
	time.Sleep(idempotencyLookupRetryInterval * 2)
	close(finalizeGate)

	<-duplicateDone
	defer dupResp.Body.Close()
	if dupResult.Success {
		t.Fatal("duplicate arriving while the original is still processing should not report success")
	}
	if dupResult.ZipPath != "" {
		t.Fatalf("duplicate response should not carry a zip path while still processing, got %q", dupResult.ZipPath)
	}

	close(extractor.release)
	<-firstDone

	// Checked only now that the first request has fully completed
	// (extraction is guaranteed to have run): the duplicate must never
	// have reached the extractor itself.
	if got := extractor.Invocations(); got != 1 {
		t.Fatalf("extractor invocations = %d, want 1 (the duplicate must not reach ffmpeg)", got)
	}
}

func TestHandleVideoUpload_DuplicateAfterCompletion_ReturnsSameResultWithoutReprocessing(t *testing.T) {
	extractor := newImmediateFrameExtractor("frames_completed.zip", 5)
	srv, tokens, _ := startIdempotencyTestServer(t, extractor)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("identical video content for completed-duplicate test")
	videoPath := writeTestUploadContent(t, content)

	resp1, result1 := uploadVideo(t, srv.URL, token, videoPath, "first.mp4")
	defer resp1.Body.Close()
	if !result1.Success {
		t.Fatalf("first upload should succeed, got: %+v", result1)
	}

	resp2, result2 := uploadVideo(t, srv.URL, token, videoPath, "duplicate.mp4")
	defer resp2.Body.Close()
	if !result2.Success {
		t.Fatalf("duplicate of a completed upload should report success, got: %+v", result2)
	}
	if result2.ZipPath != result1.ZipPath {
		t.Fatalf("duplicate's zip path = %q, want %q (the original job's)", result2.ZipPath, result1.ZipPath)
	}
	if result2.FrameCount != result1.FrameCount {
		t.Fatalf("duplicate's frame count = %d, want %d", result2.FrameCount, result1.FrameCount)
	}
	// The fixed extractor always returns the same storageKey/frameCount
	// regardless of which job invokes it, so the response comparisons
	// above would still pass even if the duplicate incorrectly
	// reprocessed — this is the assertion that actually rules that out.
	if got := extractor.Invocations(); got != 1 {
		t.Fatalf("extractor invocations = %d, want 1 (the duplicate must not be reprocessed)", got)
	}
}

func TestHandleVideoUpload_RetryAfterFailure_CreatesNewJob(t *testing.T) {
	failingExtractor := newFailingFrameExtractor(errors.New("simulated extraction failure"))
	srv, tokens, _ := startIdempotencyTestServer(t, failingExtractor)
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	content := []byte("identical video content for retry-after-failure test")
	videoPath := writeTestUploadContent(t, content)

	resp1, result1 := uploadVideo(t, srv.URL, token, videoPath, "fails.mp4")
	defer resp1.Body.Close()
	if result1.Success {
		t.Fatalf("first upload should fail, got: %+v", result1)
	}

	resp2, result2 := uploadVideo(t, srv.URL, token, videoPath, "retry.mp4")
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusConflict {
		t.Fatal("a retry after failure should not be blocked with 409")
	}
	if result2.Success {
		// The retry hits the same failing extractor, so it fails too —
		// what matters is that it was treated as a fresh attempt
		// (reached ProcessVideoJob again), not blocked as a duplicate.
		t.Fatalf("retry should also reach the (still failing) extractor and fail the same way, got: %+v", result2)
	}
	// A duplicate blocked by 409 (or one that returned the first job's
	// stale failure without a new attempt) would also produce
	// Success=false here — this is what actually proves the retry
	// reached the extractor a second time, rather than being blocked.
	if got := failingExtractor.Invocations(); got != 2 {
		t.Fatalf("extractor invocations = %d, want 2 (the retry must create and process a fresh job)", got)
	}
}

func TestHandleVideoUpload_DifferentUsersSameContent_BothSucceedIndependently(t *testing.T) {
	extractor := newImmediateFrameExtractor("frames_multi_user.zip", 2)
	srv, tokens, _ := startIdempotencyTestServer(t, extractor)
	_, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, tokenB := issueTestToken(t, tokens, "7c9e6679-7425-40de-944b-e07fc1f90ae7")

	content := []byte("identical video content shared by two different users")
	videoPath := writeTestUploadContent(t, content)

	respA, resultA := uploadVideo(t, srv.URL, tokenA, videoPath, "userA.mp4")
	defer respA.Body.Close()
	if !resultA.Success {
		t.Fatalf("user A's upload should succeed, got: %+v", resultA)
	}

	respB, resultB := uploadVideo(t, srv.URL, tokenB, videoPath, "userB.mp4")
	defer respB.Body.Close()
	if !resultB.Success {
		t.Fatalf("user B's upload should succeed independently, got: %+v", resultB)
	}
}

func TestHandleVideoUpload_ReservationNeverResolves_ReturnsConflict(t *testing.T) {
	extractor := newImmediateFrameExtractor("frames_unused.zip", 1)
	srv, tokens, store := startIdempotencyTestServer(t, extractor)
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

func TestHandleVideoUpload_DuplicateAfterFailure_ReturnsFailedResultBeforeClear(t *testing.T) {
	failingExtractor := newFailingFrameExtractor(errors.New("simulated extraction failure"))
	identity, tokens := newTestIdentityModuleWithTokens(t)
	module, store, repo := newIdempotencyTestVideoModule(failingExtractor)
	srv := httptest.NewServer(setupRouter(identity, module, alwaysAllowRateLimiter{}))
	t.Cleanup(srv.Close)

	userID, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	// Gate Clear so the first request's failed job stays indexed by its
	// idempotency key until this test explicitly releases it — otherwise
	// the duplicate below would race the real Clear call and could land
	// on either side of it depending on scheduling, rather than
	// deterministically exercising the narrow pre-Clear window this test
	// targets (see design.md Decision 8 / the "Duplicate after the
	// original failed" scenario in openspec/specs/upload-idempotency).
	clearGate := make(chan struct{})
	store.clearGate = clearGate

	content := []byte("identical video content for duplicate-after-failure test")
	videoPath := writeTestUploadContent(t, content)
	idemKey, err := videodomain.NewIdempotencyKey(userID.String(), sha256Hex(content))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	firstDone := make(chan struct{})
	go func() {
		defer close(firstDone)
		resp, result := uploadVideo(t, srv.URL, token, videoPath, "first.mp4")
		defer resp.Body.Close()
		if result.Success {
			t.Errorf("first upload should fail, got: %+v", result)
		}
	}()

	// Finalize runs right after CreateVideoJob succeeds, before extraction
	// even starts, so wait for the key to resolve to a real job first...
	var jobID videodomain.VideoJobID
	deadline := time.Now().Add(5 * time.Second)
	for {
		if id, found, lookupErr := store.Lookup(context.Background(), idemKey); lookupErr == nil && found {
			jobID = id
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the first request to finalize its idempotency key")
		}
		time.Sleep(5 * time.Millisecond)
	}
	// ...then wait further until that job's own status has actually
	// reached "failed" (FailJob runs later, inside ProcessVideoJob).
	deadline = time.Now().Add(5 * time.Second)
	for {
		job, findErr := repo.FindByID(context.Background(), jobID)
		if findErr == nil && job.Status() == videodomain.JobStatusFailed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for the first request's job to reach failed status")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The job is failed and the idempotency key still resolves to it
	// (Clear is gated) — a duplicate arriving now must see that failure
	// translated into ProcessingResult, not create a new job.
	dupResp, dupResult := uploadVideo(t, srv.URL, token, videoPath, "duplicate.mp4")
	defer dupResp.Body.Close()
	if dupResult.Success {
		t.Fatalf("duplicate observing a failed job should not report success, got: %+v", dupResult)
	}
	if !strings.Contains(dupResult.Message, "already failed") {
		t.Fatalf("duplicate message = %q, want it to mention the job already failed", dupResult.Message)
	}
	if !strings.Contains(dupResult.Message, "simulated extraction failure") {
		t.Fatalf("duplicate message = %q, want it to incorporate the original job's failure reason", dupResult.Message)
	}

	close(clearGate)
	<-firstDone

	if got := failingExtractor.Invocations(); got != 1 {
		t.Fatalf("extractor invocations = %d, want 1 (the duplicate must not have been reprocessed)", got)
	}
}

func TestHandleVideoUpload_CreateVideoJobFailure_ClearsReservationForImmediateRetry(t *testing.T) {
	extractor := newImmediateFrameExtractor("frames_after_create_failure.zip", 2)
	repo := newCreateFailingRepository(1)
	srv, tokens, store := startIdempotencyTestServerWithRepo(t, extractor, repo)
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
	resp2, result2 := uploadVideo(t, srv.URL, token, videoPath, "retry.mp4")
	defer resp2.Body.Close()
	if resp2.StatusCode == http.StatusConflict {
		t.Fatal("a retry after a CreateVideoJob failure should not be blocked with 409")
	}
	if !result2.Success {
		t.Fatalf("retry after CreateVideoJob failure should succeed, got: %+v", result2)
	}
	if got := extractor.Invocations(); got != 1 {
		t.Fatalf("extractor invocations = %d, want 1 (only the successful retry should have reached ffmpeg)", got)
	}
}
