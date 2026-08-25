package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/identity/application"
	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/idgen"
	"video-processor/internal/identity/infrastructure/jwtauth"
	"video-processor/internal/identity/infrastructure/password"
	"video-processor/internal/identity/infrastructure/postgres"
)

// inMemoryUserRepository is a fake domain.UserRepository so these HTTP tests
// don't need a live PostgreSQL instance. The rest of the module (password
// hashing, JWT issuance, ID generation) uses the real infrastructure
// adapters, since none of them perform I/O.
type inMemoryUserRepository struct {
	mu      sync.Mutex
	byID    map[string]*domain.User
	byEmail map[string]*domain.User
}

func newInMemoryUserRepository() *inMemoryUserRepository {
	return &inMemoryUserRepository{
		byID:    make(map[string]*domain.User),
		byEmail: make(map[string]*domain.User),
	}
}

func (r *inMemoryUserRepository) Create(_ context.Context, user *domain.User) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	key := user.Email().NormalizedForLookup()
	if _, exists := r.byEmail[key]; exists {
		return domain.ErrUserAlreadyExists
	}
	r.byID[user.ID().String()] = user
	r.byEmail[key] = user
	return nil
}

func (r *inMemoryUserRepository) FindByID(_ context.Context, id domain.UserID) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, ok := r.byID[id.String()]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return user, nil
}

func (r *inMemoryUserRepository) FindByNormalizedEmail(_ context.Context, normalizedEmail string) (*domain.User, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	user, ok := r.byEmail[normalizedEmail]
	if !ok {
		return nil, domain.ErrUserNotFound
	}
	return user, nil
}

func newTestIdentityModule(t *testing.T) *identityModule {
	t.Helper()
	module, _ := newTestIdentityModuleWithTokens(t)
	return module
}

// newTestIdentityModuleWithTokens also returns the jwtauth adapter backing
// the module, so tests can mint tokens (including deliberately expired or
// mis-signed ones) under the same signing key the module verifies against.
func newTestIdentityModuleWithTokens(t *testing.T) (*identityModule, jwtauth.Adapter) {
	t.Helper()

	repo := newInMemoryUserRepository()
	ids := idgen.New()
	passwords := password.New()
	tokens, err := jwtauth.New("test-only-signing-key-do-not-use-in-production")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	module := newIdentityModule(
		application.NewRegisterUser(repo, ids, passwords, systemClock{}),
		application.NewAuthenticateUser(repo, passwords, tokens, systemClock{}),
		tokens,
	)
	return module, tokens
}

func postJSON(t *testing.T, url string, payload any) *http.Response {
	t.Helper()
	body, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("unexpected error marshaling request: %v", err)
	}
	resp, err := http.Post(url, "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return resp
}

func registerTestAccount(t *testing.T, baseURL, email, password string) {
	t.Helper()
	resp := postJSON(t, baseURL+"/api/auth/register", registerUserRequest{Email: email, Password: password})
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("registration status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}
}

func TestHandleRegister_Success(t *testing.T) {
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t), newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "User@Example.com", Password: "correct-horse"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusCreated)
	}

	var got registerUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("unexpected error decoding response: %v", err)
	}
	if got.Email != "User@example.com" {
		t.Fatalf("Email = %q, want %q", got.Email, "User@example.com")
	}
	if got.ID == "" {
		t.Fatal("expected a non-empty user id")
	}
}

func TestHandleRegister_InvalidEmail(t *testing.T) {
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t), newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "not-an-email", Password: "correct-horse"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleRegister_PasswordTooShort(t *testing.T) {
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t), newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "user@example.com", Password: "short"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleRegister_DuplicateEmail(t *testing.T) {
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t), newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	registerTestAccount(t, srv.URL, "user@example.com", "correct-horse")

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "USER@EXAMPLE.COM", Password: "another-password"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate registration status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
}

func TestHandleRegister_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t), newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/auth/register", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleLogin_Success(t *testing.T) {
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t), newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	registerTestAccount(t, srv.URL, "user@example.com", "correct-horse")

	resp := postJSON(t, srv.URL+"/api/auth/login", authenticateUserRequest{Email: "User@Example.com", Password: "correct-horse"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var got authenticateUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("unexpected error decoding response: %v", err)
	}
	if got.AccessToken == "" {
		t.Fatal("expected a non-empty access token")
	}
	if !got.ExpiresAt.After(time.Now()) {
		t.Fatalf("ExpiresAt = %v, want a time in the future", got.ExpiresAt)
	}
}

func TestHandleLogin_UnknownEmail(t *testing.T) {
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t), newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/auth/login", authenticateUserRequest{Email: "nobody@example.com", Password: "correct-horse"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHandleLogin_WrongPassword(t *testing.T) {
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t), newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	registerTestAccount(t, srv.URL, "user@example.com", "correct-horse")

	resp := postJSON(t, srv.URL+"/api/auth/login", authenticateUserRequest{Email: "user@example.com", Password: "wrong-password"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHandleLogin_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t), newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func newProtectedTestServer(t *testing.T, module *identityModule) *httptest.Server {
	t.Helper()
	router := gin.New()
	router.GET("/protected", module.requireBearerAuth(), func(c *gin.Context) {
		userID, ok := authenticatedUserID(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, identityErrorResponse{Error: "missing authenticated user in context"})
			return
		}
		c.JSON(http.StatusOK, gin.H{"user_id": userID.String()})
	})
	return httptest.NewServer(router)
}

func getWithAuthorization(t *testing.T, url, authorizationHeader string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if authorizationHeader != "" {
		req.Header.Set("Authorization", authorizationHeader)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return resp
}

func TestRequireBearerAuth_RejectsMissingHeader(t *testing.T) {
	module, _ := newTestIdentityModuleWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	resp := getWithAuthorization(t, srv.URL+"/protected", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRequireBearerAuth_RejectsMalformedHeader(t *testing.T) {
	module, _ := newTestIdentityModuleWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	for _, header := range []string{"not-a-bearer-token", "Basic dXNlcjpwYXNz", "Bearer"} {
		resp := getWithAuthorization(t, srv.URL+"/protected", header)
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("Authorization = %q: status = %d, want %d", header, resp.StatusCode, http.StatusUnauthorized)
		}
	}
}

func TestRequireBearerAuth_RejectsEmptyBearerToken(t *testing.T) {
	module, _ := newTestIdentityModuleWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	resp := getWithAuthorization(t, srv.URL+"/protected", "Bearer ")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRequireBearerAuth_RejectsInvalidToken(t *testing.T) {
	module, _ := newTestIdentityModuleWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	resp := getWithAuthorization(t, srv.URL+"/protected", "Bearer not-a-real-jwt")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRequireBearerAuth_RejectsExpiredToken(t *testing.T) {
	module, tokens := newTestIdentityModuleWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	userID, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expiredToken, err := tokens.Issue(userID, time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := getWithAuthorization(t, srv.URL+"/protected", "Bearer "+expiredToken)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRequireBearerAuth_AcceptsValidTokenAndSetsUserID(t *testing.T) {
	module, tokens := newTestIdentityModuleWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	userID, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	token, err := tokens.Issue(userID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := getWithAuthorization(t, srv.URL+"/protected", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	var got struct {
		UserID string `json:"user_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("unexpected error decoding response: %v", err)
	}
	if got.UserID != userID.String() {
		t.Fatalf("UserID = %q, want %q", got.UserID, userID.String())
	}
}

// uploadWithAuth performs a multipart /upload request, attaching an
// Authorization header only when token is non-empty, so it can exercise both
// the authenticated and unauthenticated paths through the same helper.
func uploadWithAuth(t *testing.T, baseURL, token, videoPath, filename string) *http.Response {
	t.Helper()

	body := &bytes.Buffer{}
	writer := multipart.NewWriter(body)
	part, err := writer.CreateFormFile("video", filename)
	if err != nil {
		t.Fatalf("failed to create form file: %v", err)
	}
	file, err := os.Open(videoPath)
	if err != nil {
		t.Fatalf("failed to open test video: %v", err)
	}
	defer file.Close()
	if _, err := io.Copy(part, file); err != nil {
		t.Fatalf("failed to copy video into form: %v", err)
	}
	if err := writer.Close(); err != nil {
		t.Fatalf("failed to close multipart writer: %v", err)
	}

	req, err := http.NewRequest(http.MethodPost, baseURL+"/upload", body)
	if err != nil {
		t.Fatalf("failed to build request: %v", err)
	}
	req.Header.Set("Content-Type", writer.FormDataContentType())
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("upload request failed: %v", err)
	}
	return resp
}

func TestVideoRoutes_PublicGetRoot(t *testing.T) {
	module, _ := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(module, newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
}

func TestVideoRoutes_RejectUnauthenticatedRequests(t *testing.T) {
	module, _ := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(module, newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	getCases := []string{
		"/api/status",
		"/download/whatever.zip",
	}
	for _, path := range getCases {
		resp := getWithAuthorization(t, srv.URL+path, "")
		resp.Body.Close()

		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("GET %s without token: status = %d, want %d", path, resp.StatusCode, http.StatusUnauthorized)
		}
	}

	fakeVideo := generateUndecodableVideo(t, "unauthenticated.mp4")
	resp := uploadWithAuth(t, srv.URL, "", fakeVideo, "test.mp4")
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("POST /upload without token: status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestVideoRoutes_FullFlowWithValidToken(t *testing.T) {
	module, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(module, newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	userID, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	token, err := tokens.Issue(userID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	videoPath := generateTestVideo(t, 1)
	uploadResp := uploadWithAuth(t, srv.URL, token, videoPath, "test-video.mp4")
	defer uploadResp.Body.Close()

	var result ProcessingResult
	if err := json.NewDecoder(uploadResp.Body).Decode(&result); err != nil {
		t.Fatalf("unexpected error decoding upload response: %v", err)
	}

	if uploadResp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated upload status = %d, want %d", uploadResp.StatusCode, http.StatusOK)
	}
	if !result.Success {
		t.Fatalf("expected success=true, got message: %s", result.Message)
	}

	statusResp := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+token)
	defer statusResp.Body.Close()
	if statusResp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated /api/status status = %d, want %d", statusResp.StatusCode, http.StatusOK)
	}

	downloadResp := getWithAuthorization(t, srv.URL+"/download/"+result.ZipPath, "Bearer "+token)
	defer downloadResp.Body.Close()
	if downloadResp.StatusCode != http.StatusOK {
		t.Fatalf("authenticated download status = %d, want %d", downloadResp.StatusCode, http.StatusOK)
	}
}

// issueTestToken mints a bearer token for a fixed, valid UserID under
// tokens' signing key, without going through registration/login.
func issueTestToken(t *testing.T, tokens jwtauth.Adapter, uuid string) (domain.UserID, string) {
	t.Helper()
	userID, err := domain.NewUserID(uuid)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	token, err := tokens.Issue(userID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return userID, token
}

// uploadAsUser uploads a freshly generated 1s test video authenticated as
// token, and returns the resulting result artifact's storage key. Nothing
// needs cleaning up: the artifact is an object in a dedicated test bucket,
// keyed by the job's own UUID, not a file in the developer's working tree.
func uploadAsUser(t *testing.T, baseURL, token string) string {
	t.Helper()

	videoPath := generateTestVideo(t, 1)
	resp := uploadWithAuth(t, baseURL, token, videoPath, "owned-video.mp4")
	defer resp.Body.Close()

	var result ProcessingResult
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("unexpected error decoding upload response: %v", err)
	}
	if resp.StatusCode != http.StatusOK || !result.Success {
		t.Fatalf("upload failed: status=%d success=%v message=%q", resp.StatusCode, result.Success, result.Message)
	}

	return result.ZipPath
}

func TestArtifactOwnership_DownloadRejectsNonOwner(t *testing.T) {
	module, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(module, newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	_, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, tokenB := issueTestToken(t, tokens, "550e8400-e29b-41d4-a716-446655440000")

	zipFilename := uploadAsUser(t, srv.URL, tokenA)

	ownResp := getWithAuthorization(t, srv.URL+"/download/"+zipFilename, "Bearer "+tokenA)
	defer ownResp.Body.Close()
	if ownResp.StatusCode != http.StatusOK {
		t.Fatalf("owner download status = %d, want %d", ownResp.StatusCode, http.StatusOK)
	}

	otherResp := getWithAuthorization(t, srv.URL+"/download/"+zipFilename, "Bearer "+tokenB)
	defer otherResp.Body.Close()
	if otherResp.StatusCode != http.StatusNotFound {
		t.Fatalf("non-owner download status = %d, want %d", otherResp.StatusCode, http.StatusNotFound)
	}
}

func TestArtifactOwnership_StatusScopedToOwner(t *testing.T) {
	module, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(module, newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	_, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, tokenB := issueTestToken(t, tokens, "550e8400-e29b-41d4-a716-446655440000")

	zipFilename := uploadAsUser(t, srv.URL, tokenA)

	statusA := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+tokenA)
	defer statusA.Body.Close()
	var resultA struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	if err := json.NewDecoder(statusA.Body).Decode(&resultA); err != nil {
		t.Fatalf("unexpected error decoding status response: %v", err)
	}
	if !containsFilename(resultA.Files, zipFilename) {
		t.Fatalf("expected owner's /api/status to include %q, got %+v", zipFilename, resultA.Files)
	}

	statusB := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+tokenB)
	defer statusB.Body.Close()
	var resultB struct {
		Files []struct {
			Filename string `json:"filename"`
		} `json:"files"`
	}
	if err := json.NewDecoder(statusB.Body).Decode(&resultB); err != nil {
		t.Fatalf("unexpected error decoding status response: %v", err)
	}
	if containsFilename(resultB.Files, zipFilename) {
		t.Fatalf("expected non-owner's /api/status to exclude %q, got %+v", zipFilename, resultB.Files)
	}
}

func containsFilename(files []struct {
	Filename string `json:"filename"`
}, filename string) bool {
	for _, f := range files {
		if f.Filename == filename {
			return true
		}
	}
	return false
}

// TestArtifactOwnership_StaticOutputsRouteIsGone replaces the ownership test
// that used to cover the /outputs static mount. That mount served the same
// bytes as GET /download/:filename under a parallel ownership check, and it
// is removed now that results live in object storage — so the property to
// guard is that no route answers there at all, for the owner or anyone else.
func TestArtifactOwnership_StaticOutputsRouteIsGone(t *testing.T) {
	module, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(module, newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	_, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	zipFilename := uploadAsUser(t, srv.URL, tokenA)

	resp := getWithAuthorization(t, srv.URL+"/outputs/"+zipFilename, "Bearer "+tokenA)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("GET /outputs/<key> status = %d, want %d — the static outputs mount must no longer exist", resp.StatusCode, http.StatusNotFound)
	}
}

// TestStaticUploadsRouteIsGone replaces the two ownership tests that covered
// the /uploads static mount and its sidecar files. Source videos are objects
// now, deleted before their own request finishes, so there is nothing to
// serve and no sidecar to leak — the property to guard is that no route
// answers there at all.
func TestStaticUploadsRouteIsGone(t *testing.T) {
	module, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(module, newTestVideoModule(t), alwaysAllowRateLimiter{}))
	defer srv.Close()

	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	for _, path := range []string{"/uploads/whatever.mp4", "/uploads/fake-artifact.mp4.owner"} {
		resp := getWithAuthorization(t, srv.URL+path, "Bearer "+token)
		resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Fatalf("GET %s status = %d, want %d — the static uploads mount must no longer exist", path, resp.StatusCode, http.StatusNotFound)
		}
	}
}

func TestSetupIdentity_NeitherConfigured_ReturnsError(t *testing.T) {
	t.Setenv("IDENTITY_POSTGRES_DSN", "")
	t.Setenv(identityJWTSigningKeyEnv, "")

	module, db, err := setupIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error when neither IDENTITY_POSTGRES_DSN nor the JWT signing key is set")
	}
	if !errors.Is(err, postgres.ErrDSNRequired) {
		t.Fatalf("expected error to wrap postgres.ErrDSNRequired, got: %v", err)
	}
	if module != nil {
		t.Fatalf("expected a nil module on error, got %+v", module)
	}
	if db != nil {
		t.Fatalf("expected a nil db on error, got %+v", db)
	}
}

func TestSetupIdentity_SigningKeyMissing_ReturnsError(t *testing.T) {
	t.Setenv("IDENTITY_POSTGRES_DSN", "postgres://user:pass@localhost:5432/identity")
	t.Setenv(identityJWTSigningKeyEnv, "")

	_, _, err := setupIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error when IDENTITY_POSTGRES_DSN is set but the JWT signing key is missing")
	}
	if !strings.Contains(err.Error(), identityJWTSigningKeyEnv) {
		t.Fatalf("expected error to mention %s, got: %v", identityJWTSigningKeyEnv, err)
	}
}

func TestSetupIdentity_DSNMissing_ReturnsError(t *testing.T) {
	t.Setenv("IDENTITY_POSTGRES_DSN", "")
	t.Setenv(identityJWTSigningKeyEnv, "a-signing-key")

	_, _, err := setupIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error when the JWT signing key is set but IDENTITY_POSTGRES_DSN is missing")
	}
	if !errors.Is(err, postgres.ErrDSNRequired) {
		t.Fatalf("expected error to wrap postgres.ErrDSNRequired, got: %v", err)
	}
}

func TestSetupIdentity_UnreachablePostgres_ReturnsError(t *testing.T) {
	// A loopback address on a port nothing listens on fails fast (connection
	// refused) rather than hanging, so this stays a fast unit-style test.
	t.Setenv("IDENTITY_POSTGRES_DSN", "postgres://user:pass@127.0.0.1:1/identity?sslmode=disable&connect_timeout=1")
	t.Setenv(identityJWTSigningKeyEnv, "a-signing-key")

	_, _, err := setupIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error when configured PostgreSQL is unreachable")
	}
}
