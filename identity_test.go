package main

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/identity/application"
	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/idgen"
	"video-processor/internal/identity/infrastructure/jwtauth"
	"video-processor/internal/identity/infrastructure/password"
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
	srv := httptest.NewServer(setupRouterWithIdentity(newTestIdentityModule(t)))
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
	srv := httptest.NewServer(setupRouterWithIdentity(newTestIdentityModule(t)))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "not-an-email", Password: "correct-horse"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleRegister_PasswordTooShort(t *testing.T) {
	srv := httptest.NewServer(setupRouterWithIdentity(newTestIdentityModule(t)))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "user@example.com", Password: "short"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleRegister_DuplicateEmail(t *testing.T) {
	srv := httptest.NewServer(setupRouterWithIdentity(newTestIdentityModule(t)))
	defer srv.Close()

	registerTestAccount(t, srv.URL, "user@example.com", "correct-horse")

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "USER@EXAMPLE.COM", Password: "another-password"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("duplicate registration status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
}

func TestHandleRegister_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(setupRouterWithIdentity(newTestIdentityModule(t)))
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
	srv := httptest.NewServer(setupRouterWithIdentity(newTestIdentityModule(t)))
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
	srv := httptest.NewServer(setupRouterWithIdentity(newTestIdentityModule(t)))
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/auth/login", authenticateUserRequest{Email: "nobody@example.com", Password: "correct-horse"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHandleLogin_WrongPassword(t *testing.T) {
	srv := httptest.NewServer(setupRouterWithIdentity(newTestIdentityModule(t)))
	defer srv.Close()

	registerTestAccount(t, srv.URL, "user@example.com", "correct-horse")

	resp := postJSON(t, srv.URL+"/api/auth/login", authenticateUserRequest{Email: "user@example.com", Password: "wrong-password"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHandleLogin_MalformedBody(t *testing.T) {
	srv := httptest.NewServer(setupRouterWithIdentity(newTestIdentityModule(t)))
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

func TestSetupRouter_IdentityRoutesNotRegisteredWithoutModule(t *testing.T) {
	srv := httptest.NewServer(setupRouter())
	defer srv.Close()

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "user@example.com", Password: "correct-horse"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("status = %d, want %d (identity routes must not be registered when setupRouter() is called without a module)", resp.StatusCode, http.StatusNotFound)
	}
}
