// Package jwtauth implements domain.TokenIssuer and domain.TokenVerifier
// using RS256-signed JWTs.
//
// Issuing and verifying are separate types over separate key material:
// Issuer holds a private key and only signs, Verifier holds a set of public
// keys by key id and only verifies. Their method sets are disjoint, so a
// service handed a Verifier cannot mint a token — that is a compile error
// rather than a convention.
package jwtauth

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"video-processor/internal/identity/domain"
)

// Environment variables holding the token key material. The split follows the
// capability split rather than the key pair: the first two belong to the one
// service that mints tokens, the third to every service that verifies them.
const (
	PrivateKeyEnv = "IDENTITY_JWT_PRIVATE_KEY"
	KeyIDEnv      = "IDENTITY_JWT_KEY_ID"
	PublicKeysEnv = "IDENTITY_JWT_PUBLIC_KEYS"
)

// Every configuration failure names the variable that carries the material, so
// a startup log says which of the three is wrong rather than that "a key" is.
var (
	// ErrPrivateKeyRequired is returned when constructing an Issuer without a private key.
	ErrPrivateKeyRequired = fmt.Errorf("identity: %s is required", PrivateKeyEnv)
	// ErrKeyIDRequired is returned when constructing an Issuer without an active key id.
	ErrKeyIDRequired = fmt.Errorf("identity: %s is required", KeyIDEnv)
	// ErrPublicKeysRequired is returned when constructing a Verifier over an empty key set.
	ErrPublicKeysRequired = fmt.Errorf("identity: %s must hold at least one entry mapping a key id to a PEM public key", PublicKeysEnv)
	// ErrPublicKeyIDRequired is returned when a Verifier key set holds an entry
	// under an empty key id. NewIssuer refuses to mint under one, so such an
	// entry can never be selected: the verifier would start and then reject
	// every token, which is the failure this refusal moves to startup.
	ErrPublicKeyIDRequired = fmt.Errorf("identity: %s holds an entry under an empty key id; no token can name it", PublicKeysEnv)
	// ErrPrivateKeyAsVerificationMaterial is returned when a Verifier is handed
	// a private key. A service configured with the full key pair as its
	// verification material is one line away from being able to mint tokens, so
	// it fails at startup instead.
	ErrPrivateKeyAsVerificationMaterial = fmt.Errorf("identity: %s carries private key material; a verifying service holds public keys only", PublicKeysEnv)
	// ErrKeyPairMismatch is returned when the verifier key set holds no entry
	// under the issuer's active key id, or holds one that is not the private
	// key's match.
	ErrKeyPairMismatch = fmt.Errorf("identity: %s holds no public key matching %s under the key id in %s", PublicKeysEnv, PrivateKeyEnv, KeyIDEnv)
)

// signingMethod is fixed and explicit: verification only ever accepts this
// algorithm. Pinning matters more with an asymmetric key than it did with a
// symmetric one — the public key is not confidential, so an unpinned verifier
// would accept it as an HMAC secret and validate a token anyone could sign.
var signingMethod = jwt.SigningMethodRS256

// Issuer mints RS256-signed access tokens. It implements domain.TokenIssuer
// and deliberately does not implement domain.TokenVerifier.
type Issuer struct {
	privateKey *rsa.PrivateKey
	keyID      string
}

// NewIssuer builds an Issuer from a PKCS#8 RSA private key in PEM form and the
// key id tokens are stamped with.
func NewIssuer(privateKeyPEM string, keyID string) (*Issuer, error) {
	if strings.TrimSpace(privateKeyPEM) == "" {
		return nil, ErrPrivateKeyRequired
	}
	if strings.TrimSpace(keyID) == "" {
		return nil, ErrKeyIDRequired
	}

	block, _ := pem.Decode([]byte(privateKeyPEM))
	if block == nil {
		return nil, fmt.Errorf("identity: %s is not valid PEM", PrivateKeyEnv)
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("identity: parse %s: %w", PrivateKeyEnv, err)
	}
	key, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return nil, fmt.Errorf("identity: %s holds a %T, want an RSA key", PrivateKeyEnv, parsed)
	}
	return &Issuer{privateKey: key, keyID: keyID}, nil
}

// Issue signs a token identifying userID, expiring at expiresAt, stamped with
// the issuer's key id so a verifier holding several keys knows which to use.
func (i *Issuer) Issue(userID domain.UserID, expiresAt time.Time) (string, error) {
	claims := jwt.RegisteredClaims{
		Subject:   userID.String(),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	}
	token := jwt.NewWithClaims(signingMethod, claims)
	token.Header["kid"] = i.keyID
	return token.SignedString(i.privateKey)
}

// Verifier verifies RS256-signed access tokens against a set of public keys
// held by key id. It implements domain.TokenVerifier and deliberately does not
// implement domain.TokenIssuer.
type Verifier struct {
	publicKeys map[string]*rsa.PublicKey
}

// NewVerifier builds a Verifier over a set of key id to PEM public key. The set
// is a set rather than a single key so a rotation can hold the outgoing and the
// incoming key at once; a verifier reads the key id off the token and is never
// told which one is active.
func NewVerifier(publicKeysByKeyID map[string]string) (*Verifier, error) {
	if len(publicKeysByKeyID) == 0 {
		return nil, ErrPublicKeysRequired
	}

	keys := make(map[string]*rsa.PublicKey, len(publicKeysByKeyID))
	for keyID, publicKeyPEM := range publicKeysByKeyID {
		if strings.TrimSpace(keyID) == "" {
			return nil, ErrPublicKeyIDRequired
		}
		key, err := parsePublicKey(publicKeyPEM)
		if err != nil {
			return nil, fmt.Errorf("identity: %s entry %q: %w", PublicKeysEnv, keyID, err)
		}
		keys[keyID] = key
	}
	return &Verifier{publicKeys: keys}, nil
}

// parsePublicKey decodes one PEM public key, refusing private-key material
// before attempting any public-key parse so the refusal is the error reported.
func parsePublicKey(publicKeyPEM string) (*rsa.PublicKey, error) {
	block, _ := pem.Decode([]byte(publicKeyPEM))
	if block == nil {
		return nil, errors.New("not valid PEM")
	}
	if strings.Contains(block.Type, "PRIVATE KEY") || isPrivateKey(block.Bytes) {
		return nil, ErrPrivateKeyAsVerificationMaterial
	}

	parsed, err := x509.ParsePKIXPublicKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse public key: %w", err)
	}
	key, ok := parsed.(*rsa.PublicKey)
	if !ok {
		return nil, fmt.Errorf("is %T, want an RSA key", parsed)
	}
	return key, nil
}

// isPrivateKey reports whether der holds a private key under any of the
// encodings x509 understands, catching material whose PEM type label lies.
func isPrivateKey(der []byte) bool {
	if _, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return true
	}
	if _, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return true
	}
	if _, err := x509.ParseECPrivateKey(der); err == nil {
		return true
	}
	return false
}

// Verify checks tokenString's signature, algorithm, key id, and expiry,
// returning the UserID it identifies. Missing, malformed, expired, or invalid
// tokens — including a token signed with a different algorithm and one naming
// a key id this verifier does not hold — all return domain.ErrInvalidToken, so
// a caller cannot probe which key ids exist.
func (v *Verifier) Verify(tokenString string) (domain.UserID, error) {
	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		keyID, ok := t.Header["kid"].(string)
		if !ok {
			return nil, domain.ErrInvalidToken
		}
		key, ok := v.publicKeys[keyID]
		if !ok {
			return nil, domain.ErrInvalidToken
		}
		return key, nil
	}, jwt.WithValidMethods([]string{signingMethod.Name}))
	if err != nil || !parsed.Valid {
		return domain.UserID{}, domain.ErrInvalidToken
	}

	id, err := domain.NewUserID(claims.Subject)
	if err != nil {
		return domain.UserID{}, domain.ErrInvalidToken
	}
	return id, nil
}

// LoadIssuerFromEnv builds the Issuer from PrivateKeyEnv and KeyIDEnv. Only the
// Identity service reads these.
func LoadIssuerFromEnv() (*Issuer, error) {
	return NewIssuer(os.Getenv(PrivateKeyEnv), os.Getenv(KeyIDEnv))
}

// LoadVerifierFromEnv builds the Verifier from PublicKeysEnv, a JSON object
// mapping key id to PEM public key — JSON because a PEM document carries
// newlines and delimiters that no delimiter-separated list survives.
func LoadVerifierFromEnv() (*Verifier, error) {
	raw := os.Getenv(PublicKeysEnv)
	if strings.TrimSpace(raw) == "" {
		return nil, ErrPublicKeysRequired
	}

	var publicKeysByKeyID map[string]string
	if err := json.Unmarshal([]byte(raw), &publicKeysByKeyID); err != nil {
		return nil, fmt.Errorf("identity: %s must be a JSON object mapping key id to PEM public key: %w", PublicKeysEnv, err)
	}
	return NewVerifier(publicKeysByKeyID)
}

// CheckPair reports whether verifier holds, under issuer's active key id, the
// public half of issuer's signing key.
//
// The Identity service registers no bearer-authenticated route and still reads
// the public key set for this one check, which is why it is not dead
// configuration: a mismatched pair is silent in the only place it could be
// caught and loud everywhere it cannot be attributed — Identity mints
// successfully, every other service rejects every token it mints, and the fault
// appears to belong to the services that are correct.
func CheckPair(issuer *Issuer, verifier *Verifier) error {
	publicKey, ok := verifier.publicKeys[issuer.keyID]
	if !ok {
		return fmt.Errorf("%w: %q", ErrKeyPairMismatch, issuer.keyID)
	}
	if !issuer.privateKey.PublicKey.Equal(publicKey) {
		return fmt.Errorf("%w: %q", ErrKeyPairMismatch, issuer.keyID)
	}
	return nil
}
