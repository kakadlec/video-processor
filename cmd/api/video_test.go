package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"

	"video-processor/internal/identity/infrastructure/jwtauth"
	videoapplication "video-processor/internal/video/application"
	videodomain "video-processor/internal/video/domain"
	videoidgen "video-processor/internal/video/infrastructure/idgen"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
)

// inMemoryVideoJobRepository is a fake videodomain.VideoJobRepository so
// these HTTP tests don't need a live PostgreSQL instance, mirroring
// inMemoryUserRepository in identity_test.go. Ordering matches
// videodomain.VideoJobRepository's documented contract: CreatedAt
// descending, VideoJobID ascending as a tie-breaker.
type inMemoryVideoJobRepository struct {
	mu   sync.Mutex
	byID map[string]*videodomain.VideoJob
}

func newInMemoryVideoJobRepository() *inMemoryVideoJobRepository {
	return &inMemoryVideoJobRepository{byID: make(map[string]*videodomain.VideoJob)}
}

func (r *inMemoryVideoJobRepository) Create(_ context.Context, job *videodomain.VideoJob) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.byID[job.ID().String()] = job
	return nil
}

func (r *inMemoryVideoJobRepository) FindByID(_ context.Context, id videodomain.VideoJobID) (*videodomain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	job, ok := r.byID[id.String()]
	if !ok {
		return nil, videodomain.ErrVideoJobNotFound
	}
	return job, nil
}

func (r *inMemoryVideoJobRepository) FindByUserID(_ context.Context, userID videodomain.UserID, offset, limit int) ([]*videodomain.VideoJob, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	var matches []*videodomain.VideoJob
	for _, job := range r.byID {
		if job.UserID().Equal(userID) {
			matches = append(matches, job)
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

// newTestVideoModule returns a videoModule backed by an in-memory
// repository, for callers that only need the router to have working video
// routes and don't inspect job state directly.
func newTestVideoModule(t *testing.T) *videoModule {
	t.Helper()
	module, _ := newTestVideoModuleWithRepo(t)
	return module
}

func newTestVideoModuleWithRepo(t *testing.T) (*videoModule, *inMemoryVideoJobRepository) {
	t.Helper()
	repo := newInMemoryVideoJobRepository()
	ids := videoidgen.New()
	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, systemClock{}),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
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
	srv := httptest.NewServer(setupRouter(identity, video))
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

	module, db, err := setupVideo(context.Background())
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

	_, _, err := setupVideo(context.Background())
	if err == nil {
		t.Fatal("expected an error when configured PostgreSQL is unreachable")
	}
	if strings.TrimSpace(err.Error()) == "" {
		t.Fatal("expected a non-empty error message")
	}
}
