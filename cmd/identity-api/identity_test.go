package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"video-processor/internal/identity/application"
	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/idgen"
	"video-processor/internal/identity/infrastructure/jwtauth"
	"video-processor/internal/identity/infrastructure/password"
	"video-processor/internal/identity/infrastructure/postgres"
)

// No TestMain. These tests drive this service's own router over an in-memory
// user repository and never open a database, so an IDENTITY_POSTGRES_TEST_DSN
// gate would guard nothing — the real adapter is covered by
// internal/identity/infrastructure/postgres's own suite. The ffmpeg and
// VIDEO_MINIO_* gates cmd/api's TestMain enforces are gone with the
// dependencies: this binary has neither.

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

// testTokens carries an issuer and the verifier over the same key pair, so
// tests can mint tokens (including deliberately expired or mis-signed ones)
// under the key the module verifies against. Issuing and verifying are
// separate types in production; this pairs them for a test's convenience.
type testTokens struct {
	issuer   *jwtauth.Issuer
	verifier *jwtauth.Verifier
	keys     testKeyPEMs
}

// Issue mints a token under the issuer half, so a fixture holding a testTokens
// reads the same as it did when one adapter did both.
func (tk testTokens) Issue(userID domain.UserID, expiresAt time.Time) (string, error) {
	return tk.issuer.Issue(userID, expiresAt)
}

type testKeyPEMs struct {
	privatePEM string
	publicPEM  string
}

const testTokenKeyID = "test-key"

var (
	testKeyOnce     sync.Once
	testKeyPEMsOnce testKeyPEMs
)

// newTestTokens returns an issuer and a verifier over one RSA key pair
// generated once per test binary — generation is expensive and the key's
// identity does not vary between tests.
func newTestTokens(t *testing.T) testTokens {
	t.Helper()

	testKeyOnce.Do(func() {
		key, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			panic(err)
		}
		privateDER, err := x509.MarshalPKCS8PrivateKey(key)
		if err != nil {
			panic(err)
		}
		publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
		if err != nil {
			panic(err)
		}
		testKeyPEMsOnce = testKeyPEMs{
			privatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
			publicPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
		}
	})

	issuer, err := jwtauth.NewIssuer(testKeyPEMsOnce.privatePEM, testTokenKeyID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	verifier, err := jwtauth.NewVerifier(map[string]string{testTokenKeyID: testKeyPEMsOnce.publicPEM})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return testTokens{issuer: issuer, verifier: verifier, keys: testKeyPEMsOnce}
}

// newTestIdentityModuleWithTokens also returns the token pair, so a test can
// verify what the login route minted. Unlike cmd/api's, this module holds no
// verifier — this service registers no authenticated route — so the pair is
// returned rather than wired in.
func newTestIdentityModuleWithTokens(t *testing.T) (*identityModule, testTokens) {
	t.Helper()

	repo := newInMemoryUserRepository()
	ids := idgen.New()
	passwords := password.New()
	tokens := newTestTokens(t)

	module := newIdentityModule(
		application.NewRegisterUser(repo, ids, passwords, systemClock{}),
		application.NewAuthenticateUser(repo, passwords, tokens.issuer, systemClock{}),
	)
	return module, tokens
}

func newTestIdentityModule(t *testing.T) *identityModule {
	t.Helper()
	module, _ := newTestIdentityModuleWithTokens(t)
	return module
}

// startTestServer serves this service's real router — the whole of it. There
// is no video module, no notification module and no limiter to stand in for,
// which is the shape difference that makes this suite worth having rather
// than relocating.
func startTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(setupRouter(newTestIdentityModule(t)))
	t.Cleanup(srv.Close)
	return srv
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
	srv := startTestServer(t)

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
	srv := startTestServer(t)

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "not-an-email", Password: "correct-horse"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleRegister_PasswordTooShort(t *testing.T) {
	srv := startTestServer(t)

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "user@example.com", Password: "short"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestHandleRegister_DuplicateEmail(t *testing.T) {
	srv := startTestServer(t)

	registerTestAccount(t, srv.URL, "user@example.com", "correct-horse")

	resp := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "USER@EXAMPLE.COM", Password: "another-password"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusConflict {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusConflict)
	}
}

func TestHandleRegister_MalformedBody(t *testing.T) {
	srv := startTestServer(t)

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
	srv := startTestServer(t)

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

// The shape of what this service mints is now a contract with processes that
// are not in this test binary: every other service verifies these tokens with
// the public half alone, and can do so only if the algorithm and the key id
// are what it expects. Asserting "non-empty" would pass for a token none of
// them could check.
func TestHandleLogin_MintsAVerifiableRS256TokenNamingTheKeyID(t *testing.T) {
	module, tokens := newTestIdentityModuleWithTokens(t)
	srv := httptest.NewServer(setupRouter(module))
	defer srv.Close()

	registerTestAccount(t, srv.URL, "user@example.com", "correct-horse")
	registered := postJSON(t, srv.URL+"/api/auth/register", registerUserRequest{Email: "second@example.com", Password: "correct-horse"})
	defer registered.Body.Close()
	var created registerUserResponse
	if err := json.NewDecoder(registered.Body).Decode(&created); err != nil {
		t.Fatalf("unexpected error decoding response: %v", err)
	}

	resp := postJSON(t, srv.URL+"/api/auth/login", authenticateUserRequest{Email: "second@example.com", Password: "correct-horse"})
	defer resp.Body.Close()
	var got authenticateUserResponse
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		t.Fatalf("unexpected error decoding response: %v", err)
	}

	parsed, _, err := jwt.NewParser().ParseUnverified(got.AccessToken, &jwt.RegisteredClaims{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if alg := parsed.Header["alg"]; alg != "RS256" {
		t.Fatalf("alg = %v, want RS256", alg)
	}
	if kid := parsed.Header["kid"]; kid != testTokenKeyID {
		t.Fatalf("kid = %v, want %q", kid, testTokenKeyID)
	}

	// A verifier holding only the public half accepts it — which is the whole
	// arrangement every other service depends on.
	userID, err := tokens.verifier.Verify(got.AccessToken)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if userID.String() != created.ID {
		t.Fatalf("subject = %q, want the registered user id %q", userID.String(), created.ID)
	}
}

func TestHandleLogin_UnknownEmail(t *testing.T) {
	srv := startTestServer(t)

	resp := postJSON(t, srv.URL+"/api/auth/login", authenticateUserRequest{Email: "nobody@example.com", Password: "correct-horse"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHandleLogin_WrongPassword(t *testing.T) {
	srv := startTestServer(t)

	registerTestAccount(t, srv.URL, "user@example.com", "correct-horse")

	resp := postJSON(t, srv.URL+"/api/auth/login", authenticateUserRequest{Email: "user@example.com", Password: "wrong-password"})
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestHandleLogin_MalformedBody(t *testing.T) {
	srv := startTestServer(t)

	resp, err := http.Post(srv.URL+"/api/auth/login", "application/json", bytes.NewReader([]byte("not json")))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusBadRequest)
	}
}

func TestSetupIdentity_NeitherConfigured_ReturnsError(t *testing.T) {
	t.Setenv("IDENTITY_POSTGRES_DSN", "")
	t.Setenv(jwtauth.PrivateKeyEnv, "")
	t.Setenv(jwtauth.KeyIDEnv, "")
	t.Setenv(jwtauth.PublicKeysEnv, "")

	module, db, err := setupIdentity(context.Background())
	if err == nil {
		t.Fatal("expected an error when neither IDENTITY_POSTGRES_DSN nor the JWT key configuration is set")
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

func TestSetupIdentity_PrivateKeyMissing_ReturnsError(t *testing.T) {
	tokens := newTestTokens(t)
	t.Setenv("IDENTITY_POSTGRES_DSN", "postgres://user:pass@localhost:5432/identity")
	t.Setenv(jwtauth.PrivateKeyEnv, "")
	t.Setenv(jwtauth.KeyIDEnv, testTokenKeyID)
	t.Setenv(jwtauth.PublicKeysEnv, testPublicKeySet(t, tokens))

	_, _, err := setupIdentity(context.Background())
	if !errors.Is(err, jwtauth.ErrPrivateKeyRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPrivateKeyRequired)
	}
}

func TestSetupIdentity_KeyIDMissing_ReturnsError(t *testing.T) {
	tokens := newTestTokens(t)
	t.Setenv("IDENTITY_POSTGRES_DSN", "postgres://user:pass@localhost:5432/identity")
	t.Setenv(jwtauth.PrivateKeyEnv, tokens.keys.privatePEM)
	t.Setenv(jwtauth.KeyIDEnv, "")
	t.Setenv(jwtauth.PublicKeysEnv, testPublicKeySet(t, tokens))

	_, _, err := setupIdentity(context.Background())
	if !errors.Is(err, jwtauth.ErrKeyIDRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrKeyIDRequired)
	}
}

func TestSetupIdentity_PublicKeySetMissing_ReturnsError(t *testing.T) {
	tokens := newTestTokens(t)
	t.Setenv("IDENTITY_POSTGRES_DSN", "postgres://user:pass@localhost:5432/identity")
	t.Setenv(jwtauth.PrivateKeyEnv, tokens.keys.privatePEM)
	t.Setenv(jwtauth.KeyIDEnv, testTokenKeyID)
	t.Setenv(jwtauth.PublicKeysEnv, "")

	_, _, err := setupIdentity(context.Background())
	if !errors.Is(err, jwtauth.ErrPublicKeysRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPublicKeysRequired)
	}
}

// A mismatched pair is silent in the one place it can be caught and loud
// everywhere it cannot be attributed, so it has to fail Identity's startup.
func TestSetupIdentity_MismatchedKeyPair_ReturnsError(t *testing.T) {
	tokens := newTestTokens(t)
	t.Setenv("IDENTITY_POSTGRES_DSN", "postgres://user:pass@localhost:5432/identity")
	t.Setenv(jwtauth.PrivateKeyEnv, tokens.keys.privatePEM)
	t.Setenv(jwtauth.KeyIDEnv, "a-key-id-the-public-key-set-does-not-carry")
	t.Setenv(jwtauth.PublicKeysEnv, testPublicKeySet(t, tokens))

	_, _, err := setupIdentity(context.Background())
	if !errors.Is(err, jwtauth.ErrKeyPairMismatch) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrKeyPairMismatch)
	}
}

// testPublicKeySet renders tokens' public half in the JSON key-id-to-PEM form
// IDENTITY_JWT_PUBLIC_KEYS carries.
func testPublicKeySet(t *testing.T, tokens testTokens) string {
	t.Helper()
	encoded, err := json.Marshal(map[string]string{testTokenKeyID: tokens.keys.publicPEM})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return string(encoded)
}
