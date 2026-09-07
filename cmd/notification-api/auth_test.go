package main

import (
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

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"

	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/jwtauth"
)

// The bearer middleware's own tests, carried here with the copy of the
// middleware itself. Route coverage does not substitute for them: a copy can
// regress on its own, which is the same reason ratelimit_test.go is carried
// rather than left behind. This service mounts requireBearerAuth, so this is
// where the assertions that exercise it belong.

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

// newTestAuthenticatorWithTokens also returns the token pair backing the
// authenticator, so tests can mint tokens under the key it verifies against.
// Nothing in this process can issue one: the fixture holds the private half
// that cmd/identity-api holds in production, which is both the only way left
// to obtain a token here and the more honest one — reaching Identity's route
// for it would test another service inside this one's suite, and is not
// available across a process boundary anyway.
func newTestAuthenticatorWithTokens(t *testing.T) (*authenticator, testTokens) {
	t.Helper()
	tokens := newTestTokens(t)
	return newAuthenticator(tokens.verifier), tokens
}

// newTestAuthenticatorWithTokens also returns the token pair backing the
// module, so tests can mint tokens under the key the module verifies against.
// This process no longer issues one over HTTP: a fixture that needs a token
// mints it here, with the test private key, which is both the only way left
// and the more honest one — obtaining it from /api/auth/login tested

func newProtectedTestServer(t *testing.T, module *authenticator) *httptest.Server {
	t.Helper()
	router := gin.New()
	router.GET("/protected", module.requireBearerAuth(), func(c *gin.Context) {
		userID, ok := authenticatedUserID(c)
		if !ok {
			c.JSON(http.StatusInternalServerError, authErrorResponse{Error: "missing authenticated user in context"})
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
	module, _ := newTestAuthenticatorWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	resp := getWithAuthorization(t, srv.URL+"/protected", "")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRequireBearerAuth_RejectsMalformedHeader(t *testing.T) {
	module, _ := newTestAuthenticatorWithTokens(t)
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
	module, _ := newTestAuthenticatorWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	resp := getWithAuthorization(t, srv.URL+"/protected", "Bearer ")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRequireBearerAuth_RejectsInvalidToken(t *testing.T) {
	module, _ := newTestAuthenticatorWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	resp := getWithAuthorization(t, srv.URL+"/protected", "Bearer not-a-real-jwt")
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRequireBearerAuth_RejectsExpiredToken(t *testing.T) {
	module, tokens := newTestAuthenticatorWithTokens(t)
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

// A token naming a key the verifier does not hold is rejected with the same
// shape every other invalid token produces, so a caller cannot learn which key
// ids exist by resubmitting.
func TestRequireBearerAuth_RejectsTokenNamingAnUnknownKeyID(t *testing.T) {
	module, tokens := newTestAuthenticatorWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	strangerIssuer, err := jwtauth.NewIssuer(tokens.keys.privatePEM, "a-key-id-the-verifier-does-not-hold")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	userID, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	token, err := strangerIssuer.Issue(userID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := getWithAuthorization(t, srv.URL+"/protected", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

// The public key is not confidential. Without the algorithm pinned by name, a
// verifier would accept it as an HMAC secret and authorize a token anyone
// holding the key set could sign.
func TestRequireBearerAuth_RejectsHS256SignedWithThePublicKey(t *testing.T) {
	module, tokens := newTestAuthenticatorWithTokens(t)
	srv := newProtectedTestServer(t, module)
	defer srv.Close()

	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.RegisteredClaims{
		Subject:   "3fa85f64-5717-4562-b3fc-2c963f66afa6",
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	forged.Header["kid"] = testTokenKeyID
	token, err := forged.SignedString([]byte(tokens.keys.publicPEM))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	resp := getWithAuthorization(t, srv.URL+"/protected", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusUnauthorized)
	}
}

func TestRequireBearerAuth_AcceptsValidTokenAndSetsUserID(t *testing.T) {
	module, tokens := newTestAuthenticatorWithTokens(t)
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

// issueTestToken mints a bearer token for a fixed, valid UserID under
// tokens' signing key, without going through registration/login.
func issueTestToken(t *testing.T, tokens testTokens, uuid string) (domain.UserID, string) {
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

// Every case this process can fail on here is the verifier's. The
// private-key, key-id and mismatched-pair cases belong to cmd/identity-api,
// with the issuer they are about; this binary has no private key and no way
// to construct one.
func TestSetupAuthenticator_PublicKeySetMissing_ReturnsError(t *testing.T) {
	t.Setenv(jwtauth.PublicKeysEnv, "")

	module, err := setupAuthenticator()
	if !errors.Is(err, jwtauth.ErrPublicKeysRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPublicKeysRequired)
	}
	if module != nil {
		t.Fatalf("expected a nil module on error, got %+v", module)
	}
}

func TestSetupAuthenticator_MalformedPublicKeySet_ReturnsError(t *testing.T) {
	t.Setenv(jwtauth.PublicKeysEnv, "not json")

	if _, err := setupAuthenticator(); err == nil {
		t.Fatal("expected an error for a malformed public key set")
	}
}

// A private key is not verification material, and a process handed the full
// pair as its key set is one line away from being able to mint. This binary
// has no such line, and it still refuses to start.
func TestSetupAuthenticator_PrivateKeyAsVerificationMaterial_ReturnsError(t *testing.T) {
	tokens := newTestTokens(t)
	encoded, err := json.Marshal(map[string]string{testTokenKeyID: tokens.keys.privatePEM})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Setenv(jwtauth.PublicKeysEnv, string(encoded))

	if _, err := setupAuthenticator(); !errors.Is(err, jwtauth.ErrPrivateKeyAsVerificationMaterial) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPrivateKeyAsVerificationMaterial)
	}
}

func TestSetupAuthenticator_LoadsAVerifier(t *testing.T) {
	tokens := newTestTokens(t)
	t.Setenv(jwtauth.PublicKeysEnv, testPublicKeySet(t, tokens))

	module, err := setupAuthenticator()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if _, err := module.tokens.Verify(token); err != nil {
		t.Fatalf("the loaded verifier rejected a token minted under its own key: %v", err)
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
