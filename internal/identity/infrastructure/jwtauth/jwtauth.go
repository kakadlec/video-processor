// Package jwtauth implements domain.TokenIssuer and domain.TokenVerifier
// using signed, HMAC-SHA256 JWTs.
package jwtauth

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"

	"video-processor/internal/identity/domain"
)

// ErrSigningKeyRequired is returned when constructing an Adapter without a signing key.
// Startup must fail clearly rather than silently running with a default key.
var ErrSigningKeyRequired = errors.New("identity: jwt signing key is required")

// signingMethod is fixed and explicit: verification only ever accepts this
// algorithm, so a token claiming a different algorithm (including "none")
// is always rejected rather than trusted.
var signingMethod = jwt.SigningMethodHS256

// Adapter implements domain.TokenIssuer and domain.TokenVerifier using HMAC-signed JWTs.
type Adapter struct {
	signingKey []byte
}

// New builds a JWT adapter from a signing key sourced from configuration/environment.
func New(signingKey string) (Adapter, error) {
	if signingKey == "" {
		return Adapter{}, ErrSigningKeyRequired
	}
	return Adapter{signingKey: []byte(signingKey)}, nil
}

// Issue signs a token identifying userID, expiring at expiresAt.
func (a Adapter) Issue(userID domain.UserID, expiresAt time.Time) (string, error) {
	claims := jwt.RegisteredClaims{
		Subject:   userID.String(),
		IssuedAt:  jwt.NewNumericDate(time.Now()),
		ExpiresAt: jwt.NewNumericDate(expiresAt),
	}
	return jwt.NewWithClaims(signingMethod, claims).SignedString(a.signingKey)
}

// Verify checks tokenString's signature, algorithm, and expiry, returning
// the UserID it identifies. Missing, malformed, expired, or invalid tokens
// — including tokens signed with a different algorithm — return domain.ErrInvalidToken.
func (a Adapter) Verify(tokenString string) (domain.UserID, error) {
	claims := &jwt.RegisteredClaims{}
	parsed, err := jwt.ParseWithClaims(tokenString, claims, func(t *jwt.Token) (any, error) {
		return a.signingKey, nil
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
