package jwtauth_test

import (
	"errors"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/jwtauth"
)

var (
	_ domain.TokenIssuer   = jwtauth.Adapter{}
	_ domain.TokenVerifier = jwtauth.Adapter{}
)

const testSigningKey = "test-only-signing-key-do-not-use-in-production"

func testUserID(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func TestNew_RequiresSigningKey(t *testing.T) {
	_, err := jwtauth.New("")
	if !errors.Is(err, jwtauth.ErrSigningKeyRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrSigningKeyRequired)
	}
}

func TestAdapter_IssueAndVerify_RoundTrip(t *testing.T) {
	adapter, err := jwtauth.New(testSigningKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	userID := testUserID(t)
	token, err := adapter.Issue(userID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := adapter.Verify(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(userID) {
		t.Fatalf("Verify() = %v, want %v", got, userID)
	}
}

func TestAdapter_Verify_ExpiredToken(t *testing.T) {
	adapter, err := jwtauth.New(testSigningKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	token, err := adapter.Issue(testUserID(t), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = adapter.Verify(token)
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

func TestAdapter_Verify_WrongSigningKey(t *testing.T) {
	issuer, err := jwtauth.New(testSigningKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	verifier, err := jwtauth.New("a-completely-different-signing-key")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	token, err := issuer.Issue(testUserID(t), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = verifier.Verify(token)
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

func TestAdapter_Verify_RejectsAlgorithmSubstitution(t *testing.T) {
	adapter, err := jwtauth.New(testSigningKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Forge a token that claims the unsigned "none" algorithm, the classic
	// JWT algorithm-substitution attack. Verification must reject it outright
	// rather than trusting whatever algorithm the token itself claims.
	claims := jwt.RegisteredClaims{
		Subject:   testUserID(t).String(),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	forged, err := jwt.NewWithClaims(jwt.SigningMethodNone, claims).SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("unexpected error forging token: %v", err)
	}

	_, err = adapter.Verify(forged)
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

func TestAdapter_Verify_MalformedToken(t *testing.T) {
	adapter, err := jwtauth.New(testSigningKey)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, err = adapter.Verify("not-a-jwt")
	if !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}
