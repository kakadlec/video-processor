package jwtauth_test

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/jwtauth"
)

var (
	_ domain.TokenIssuer   = (*jwtauth.Issuer)(nil)
	_ domain.TokenVerifier = (*jwtauth.Verifier)(nil)
)

// The two method sets are disjoint on purpose: a service holding a Verifier
// must not be able to mint. Nothing but this test stops a later change from
// adding Issue to Verifier for convenience.
func TestIssuerAndVerifierDoNotSatisfyEachOthersPort(t *testing.T) {
	if _, ok := any((*jwtauth.Verifier)(nil)).(domain.TokenIssuer); ok {
		t.Fatal("Verifier satisfies domain.TokenIssuer; a verifying service could then mint tokens")
	}
	if _, ok := any((*jwtauth.Issuer)(nil)).(domain.TokenVerifier); ok {
		t.Fatal("Issuer satisfies domain.TokenVerifier; issuing and verifying must stay separate capabilities")
	}
}

const (
	testKeyID      = "test-key-a"
	otherTestKeyID = "test-key-b"
)

// testKeys holds one generated RSA key pair in the PEM forms the constructors
// take. Generation is expensive, so each pair is generated once per test binary.
type testKeys struct {
	privatePEM string
	publicPEM  string
}

var (
	keyOnce   [2]sync.Once
	generated [2]testKeys
)

// testKeyPair returns one of two stable key pairs: index 0 is the pair almost
// every test uses, index 1 the second key a rotation needs.
func testKeyPair(t *testing.T, index int) testKeys {
	t.Helper()
	keyOnce[index].Do(func() {
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
		generated[index] = testKeys{
			privatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})),
			publicPEM:  string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicDER})),
		}
	})
	return generated[index]
}

// testIssuerAndVerifier builds an issuer and a verifier over the same key pair.
func testIssuerAndVerifier(t *testing.T) (*jwtauth.Issuer, *jwtauth.Verifier) {
	t.Helper()
	keys := testKeyPair(t, 0)

	issuer, err := jwtauth.NewIssuer(keys.privatePEM, testKeyID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	verifier, err := jwtauth.NewVerifier(map[string]string{testKeyID: keys.publicPEM})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return issuer, verifier
}

func testUserID(t *testing.T) domain.UserID {
	t.Helper()
	id, err := domain.NewUserID("3fa85f64-5717-4562-b3fc-2c963f66afa6")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return id
}

func TestNewIssuer_RequiresPrivateKey(t *testing.T) {
	_, err := jwtauth.NewIssuer("", testKeyID)
	if !errors.Is(err, jwtauth.ErrPrivateKeyRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPrivateKeyRequired)
	}
}

func TestNewIssuer_RequiresKeyID(t *testing.T) {
	_, err := jwtauth.NewIssuer(testKeyPair(t, 0).privatePEM, "")
	if !errors.Is(err, jwtauth.ErrKeyIDRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrKeyIDRequired)
	}
}

func TestNewIssuer_RejectsMalformedKey(t *testing.T) {
	if _, err := jwtauth.NewIssuer("not a pem document", testKeyID); err == nil {
		t.Fatal("error = nil, want a parse failure")
	}
}

func TestNewVerifier_RefusesEmptyKeySet(t *testing.T) {
	_, err := jwtauth.NewVerifier(nil)
	if !errors.Is(err, jwtauth.ErrPublicKeysRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPublicKeysRequired)
	}
}

// A service handed the full key pair as its verification material is one line
// away from minting tokens, so it must fail at startup rather than start.
func TestNewVerifier_RefusesPrivateKeyMaterial(t *testing.T) {
	_, err := jwtauth.NewVerifier(map[string]string{testKeyID: testKeyPair(t, 0).privatePEM})
	if !errors.Is(err, jwtauth.ErrPrivateKeyAsVerificationMaterial) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPrivateKeyAsVerificationMaterial)
	}
}

// A private key whose PEM type label claims it is public is still refused.
func TestNewVerifier_RefusesMislabelledPrivateKey(t *testing.T) {
	block, _ := pem.Decode([]byte(testKeyPair(t, 0).privatePEM))
	mislabelled := string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: block.Bytes}))

	_, err := jwtauth.NewVerifier(map[string]string{testKeyID: mislabelled})
	if !errors.Is(err, jwtauth.ErrPrivateKeyAsVerificationMaterial) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPrivateKeyAsVerificationMaterial)
	}
}

func TestNewVerifier_RejectsMalformedKey(t *testing.T) {
	if _, err := jwtauth.NewVerifier(map[string]string{testKeyID: "not a pem document"}); err == nil {
		t.Fatal("error = nil, want a parse failure")
	}
}

func TestIssueAndVerify_RoundTrip(t *testing.T) {
	issuer, verifier := testIssuerAndVerifier(t)
	userID := testUserID(t)

	token, err := issuer.Issue(userID, time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	got, err := verifier.Verify(token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got.Equal(userID) {
		t.Fatalf("Verify() = %v, want %v", got, userID)
	}
}

func TestIssue_StampsTheKeyID(t *testing.T) {
	issuer, _ := testIssuerAndVerifier(t)

	token, err := issuer.Issue(testUserID(t), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	parsed, _, err := jwt.NewParser().ParseUnverified(token, &jwt.RegisteredClaims{})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := parsed.Header["kid"]; got != testKeyID {
		t.Fatalf("kid = %v, want %q", got, testKeyID)
	}
	if got := parsed.Header["alg"]; got != "RS256" {
		t.Fatalf("alg = %v, want RS256", got)
	}
}

func TestVerify_ExpiredToken(t *testing.T) {
	issuer, verifier := testIssuerAndVerifier(t)

	token, err := issuer.Issue(testUserID(t), time.Now().Add(-time.Minute))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := verifier.Verify(token); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

func TestVerify_TokenSignedByAnotherKey(t *testing.T) {
	other := testKeyPair(t, 1)
	issuer, err := jwtauth.NewIssuer(other.privatePEM, testKeyID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, verifier := testIssuerAndVerifier(t)

	token, err := issuer.Issue(testUserID(t), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := verifier.Verify(token); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

// An unknown key id returns the same error every other invalid token returns,
// so a caller cannot probe which key ids the verifier holds.
func TestVerify_UnknownKeyID(t *testing.T) {
	keys := testKeyPair(t, 0)
	issuer, err := jwtauth.NewIssuer(keys.privatePEM, "a-key-id-the-verifier-does-not-hold")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, verifier := testIssuerAndVerifier(t)

	token, err := issuer.Issue(testUserID(t), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := verifier.Verify(token); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

func TestVerify_TokenWithoutKeyID(t *testing.T) {
	keys := testKeyPair(t, 0)
	block, _ := pem.Decode([]byte(keys.privatePEM))
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	claims := jwt.RegisteredClaims{
		Subject:   testUserID(t).String(),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	token, err := jwt.NewWithClaims(jwt.SigningMethodRS256, claims).SignedString(parsed)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, verifier := testIssuerAndVerifier(t)
	if _, err := verifier.Verify(token); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

// The public key is not confidential, so an unpinned verifier would accept it
// as an HMAC secret and validate a token anyone holding it could sign.
func TestVerify_RejectsHS256SignedWithThePublicKey(t *testing.T) {
	keys := testKeyPair(t, 0)
	_, verifier := testIssuerAndVerifier(t)

	claims := jwt.RegisteredClaims{
		Subject:   testUserID(t).String(),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	forged := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	forged.Header["kid"] = testKeyID
	token, err := forged.SignedString([]byte(keys.publicPEM))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if _, err := verifier.Verify(token); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

func TestVerify_RejectsAlgNone(t *testing.T) {
	claims := jwt.RegisteredClaims{
		Subject:   testUserID(t).String(),
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	}
	unsigned := jwt.NewWithClaims(jwt.SigningMethodNone, claims)
	unsigned.Header["kid"] = testKeyID
	token, err := unsigned.SignedString(jwt.UnsafeAllowNoneSignatureType)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	_, verifier := testIssuerAndVerifier(t)
	if _, err := verifier.Verify(token); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

func TestVerify_RejectsMalformedToken(t *testing.T) {
	_, verifier := testIssuerAndVerifier(t)
	if _, err := verifier.Verify("not.a.token"); !errors.Is(err, domain.ErrInvalidToken) {
		t.Fatalf("error = %v, want %v", err, domain.ErrInvalidToken)
	}
}

// The key set exists so a rotation needs no window in which tokens are
// rejected: the verifier holds both keys while the issuer switches.
func TestVerify_AcceptsTokensSignedUnderEitherHeldKey(t *testing.T) {
	outgoing := testKeyPair(t, 0)
	incoming := testKeyPair(t, 1)

	verifier, err := jwtauth.NewVerifier(map[string]string{
		testKeyID:      outgoing.publicPEM,
		otherTestKeyID: incoming.publicPEM,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, tc := range []struct {
		name       string
		privatePEM string
		keyID      string
	}{
		{name: "outgoing key", privatePEM: outgoing.privatePEM, keyID: testKeyID},
		{name: "incoming key", privatePEM: incoming.privatePEM, keyID: otherTestKeyID},
	} {
		t.Run(tc.name, func(t *testing.T) {
			issuer, err := jwtauth.NewIssuer(tc.privatePEM, tc.keyID)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			userID := testUserID(t)
			token, err := issuer.Issue(userID, time.Now().Add(time.Hour))
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			got, err := verifier.Verify(token)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !got.Equal(userID) {
				t.Fatalf("Verify() = %v, want %v", got, userID)
			}
		})
	}
}

func TestCheckPair_MatchingPair(t *testing.T) {
	issuer, verifier := testIssuerAndVerifier(t)
	if err := jwtauth.CheckPair(issuer, verifier); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestCheckPair_NoEntryUnderTheActiveKeyID(t *testing.T) {
	keys := testKeyPair(t, 0)
	issuer, err := jwtauth.NewIssuer(keys.privatePEM, "an-inactive-key-id")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	_, verifier := testIssuerAndVerifier(t)

	if err := jwtauth.CheckPair(issuer, verifier); !errors.Is(err, jwtauth.ErrKeyPairMismatch) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrKeyPairMismatch)
	}
}

func TestCheckPair_EntryIsAnotherKeysPublicHalf(t *testing.T) {
	issuer, err := jwtauth.NewIssuer(testKeyPair(t, 0).privatePEM, testKeyID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	verifier, err := jwtauth.NewVerifier(map[string]string{testKeyID: testKeyPair(t, 1).publicPEM})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if err := jwtauth.CheckPair(issuer, verifier); !errors.Is(err, jwtauth.ErrKeyPairMismatch) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrKeyPairMismatch)
	}
}

func TestLoadIssuerFromEnv(t *testing.T) {
	keys := testKeyPair(t, 0)
	t.Setenv(jwtauth.PrivateKeyEnv, keys.privatePEM)
	t.Setenv(jwtauth.KeyIDEnv, testKeyID)

	issuer, err := jwtauth.LoadIssuerFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := issuer.Issue(testUserID(t), time.Now().Add(time.Hour)); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadIssuerFromEnv_MissingPrivateKey(t *testing.T) {
	t.Setenv(jwtauth.PrivateKeyEnv, "")
	t.Setenv(jwtauth.KeyIDEnv, testKeyID)

	if _, err := jwtauth.LoadIssuerFromEnv(); !errors.Is(err, jwtauth.ErrPrivateKeyRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPrivateKeyRequired)
	}
}

func TestLoadVerifierFromEnv(t *testing.T) {
	keys := testKeyPair(t, 0)
	encoded, err := json.Marshal(map[string]string{testKeyID: keys.publicPEM})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Setenv(jwtauth.PublicKeysEnv, string(encoded))

	issuer, err := jwtauth.NewIssuer(keys.privatePEM, testKeyID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	verifier, err := jwtauth.LoadVerifierFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	token, err := issuer.Issue(testUserID(t), time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if _, err := verifier.Verify(token); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestLoadVerifierFromEnv_MissingKeySet(t *testing.T) {
	t.Setenv(jwtauth.PublicKeysEnv, "")

	if _, err := jwtauth.LoadVerifierFromEnv(); !errors.Is(err, jwtauth.ErrPublicKeysRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPublicKeysRequired)
	}
}

func TestLoadVerifierFromEnv_EmptyKeySet(t *testing.T) {
	t.Setenv(jwtauth.PublicKeysEnv, "{}")

	if _, err := jwtauth.LoadVerifierFromEnv(); !errors.Is(err, jwtauth.ErrPublicKeysRequired) {
		t.Fatalf("error = %v, want %v", err, jwtauth.ErrPublicKeysRequired)
	}
}

func TestLoadVerifierFromEnv_MalformedJSON(t *testing.T) {
	t.Setenv(jwtauth.PublicKeysEnv, "-----BEGIN PUBLIC KEY-----")

	_, err := jwtauth.LoadVerifierFromEnv()
	if err == nil {
		t.Fatal("error = nil, want a parse failure")
	}
	if !strings.Contains(err.Error(), jwtauth.PublicKeysEnv) {
		t.Fatalf("error = %v, want it to name %s", err, jwtauth.PublicKeysEnv)
	}
}
