package domain

import (
	"errors"
	"time"
)

// ErrInvalidToken is returned when a token is missing, malformed, expired, or fails signature verification.
var ErrInvalidToken = errors.New("identity: invalid token")

// TokenIssuer is the port for minting a signed access token for an authenticated user.
type TokenIssuer interface {
	Issue(userID UserID, expiresAt time.Time) (string, error)
}

// TokenVerifier is the port for verifying a bearer token and recovering the UserID it identifies.
type TokenVerifier interface {
	Verify(token string) (UserID, error)
}
