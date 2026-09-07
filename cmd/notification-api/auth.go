package main

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"video-processor/internal/identity/domain"
	"video-processor/internal/identity/infrastructure/jwtauth"
)

// authenticator is this service's whole share of the Identity bounded
// context: a token verifier and the middleware over it. It holds no issuer,
// and there is no code path in this binary that can construct one — nothing
// here can mint a token, whatever ends up in its environment.
//
// This is a copy of cmd/api's middleware rather than an import, because a
// package main cannot be imported. The duplication is transient by
// construction and ends as one copy per HTTP service, exactly as
// rateLimitMiddleware already is one thin gin wrapper per composition root
// over the shared internal/platform/ratelimit. The security-carrying half —
// algorithm pinning, kid lookup, indistinguishable rejection — stays single,
// in internal/identity/infrastructure/jwtauth.
type authenticator struct {
	tokens domain.TokenVerifier
}

func newAuthenticator(tokens domain.TokenVerifier) *authenticator {
	return &authenticator{tokens: tokens}
}

// setupAuthenticator builds the production authenticator from environment
// configuration. Only IDENTITY_JWT_PUBLIC_KEYS is read, and its absence is
// fatal: a service that starts and then rejects every request is worse than
// one that refuses to start.
func setupAuthenticator() (*authenticator, error) {
	verifier, err := jwtauth.LoadVerifierFromEnv()
	if err != nil {
		return nil, err
	}
	return newAuthenticator(verifier), nil
}

// authenticatedUserIDKey is the gin context key under which requireBearerAuth
// stores the authenticated UserID.
const authenticatedUserIDKey = "identity.authenticatedUserID"

const bearerPrefix = "Bearer "

// requireBearerAuth extracts an "Authorization: Bearer <token>" header,
// verifies it through the token port, and stores the resulting UserID in the
// request context. Missing, malformed, expired, or invalid tokens are
// rejected with 401 before the wrapped handler runs.
func (a *authenticator) requireBearerAuth() gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if !strings.HasPrefix(header, bearerPrefix) {
			c.AbortWithStatusJSON(http.StatusUnauthorized, authErrorResponse{Error: "missing or malformed authorization header"})
			return
		}

		token := strings.TrimPrefix(header, bearerPrefix)
		if token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, authErrorResponse{Error: "missing or malformed authorization header"})
			return
		}

		userID, err := a.tokens.Verify(token)
		if err != nil {
			c.AbortWithStatusJSON(http.StatusUnauthorized, authErrorResponse{Error: "invalid or expired token"})
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

type authErrorResponse struct {
	Error string `json:"error"`
}
