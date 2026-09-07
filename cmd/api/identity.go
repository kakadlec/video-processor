package main

import (
	"database/sql"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/jwtauth"
)

// systemClock is the production Clock for every module in this process. It
// lived here when Identity was the only context that needed one; the Video
// module uses it too, which is why it stays behind while the rest of
// Identity's account handling leaves.
type systemClock struct{}

func (systemClock) Now() time.Time { return time.Now() }

// identityModule is what is left of the Identity bounded context in this
// process: a token verifier and the middleware over it. Registering and
// logging in moved to cmd/identity-api, and with them the private key, the
// user repository and the pool behind it — this process authenticates
// callers, it does not administer accounts.
type identityModule struct {
	tokens domain.TokenVerifier
}

func newIdentityModule(tokens domain.TokenVerifier) *identityModule {
	return &identityModule{tokens: tokens}
}

// setupIdentity builds this process's Identity module from environment
// configuration. It loads a verifier and nothing else: there is no code path
// in this binary that can construct an issuer, so no private key can give it
// the ability to mint a token. That is the first place the issuer/verifier
// split earns its keep.
//
// The public key set is still required, and its absence is still fatal —
// a service that starts and then rejects every request is worse than one
// that refuses to start.
func setupIdentity() (*identityModule, error) {
	verifier, err := jwtauth.LoadVerifierFromEnv()
	if err != nil {
		return nil, err
	}
	return newIdentityModule(verifier), nil
}

// closeDB closes db, logging any failure — used on setup-failure paths where
// a different, more relevant error is already being returned to the caller.
func closeDB(db *sql.DB) {
	if err := db.Close(); err != nil {
		log.Printf("identity: close postgres: %v", err)
	}
}

// authenticatedUserIDKey is the gin context key under which requireBearerAuth
// stores the authenticated UserID.
const authenticatedUserIDKey = "identity.authenticatedUserID"

const bearerPrefix = "Bearer "

// requireBearerAuth extracts an "Authorization: Bearer <token>" header,
// verifies it through the token port, and stores the resulting UserID in the
// request context. Missing, malformed, expired, or invalid tokens are
// rejected with 401 before the wrapped handler runs.
func (m *identityModule) requireBearerAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, bearerPrefix) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, identityErrorResponse{Error: "missing or malformed authorization header"})
			return
		}

		token := strings.TrimPrefix(header, bearerPrefix)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, identityErrorResponse{Error: "missing or malformed authorization header"})
			return
		}

		userID, err := m.tokens.Verify(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, identityErrorResponse{Error: "invalid or expired token"})
			return
		}

		c.Set(authenticatedUserIDKey, userID)
		c.Next()
	}
}

// authenticatedUserID returns the UserID stored by requireBearerAuth, if any.
func authenticatedUserID(c *gin.Context) (domain.UserID, bool) {
	value, ok := c.Get(authenticatedUserIDKey)
	if !ok {
		return domain.UserID{}, false
	}
	userID, ok := value.(domain.UserID)
	return userID, ok
}

type identityErrorResponse struct {
	Error string `json:"error"`
}
