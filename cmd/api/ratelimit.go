package main

import (
	"context"
	"log"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// videoRateLimiter is the subset of *ratelimit.Limiter's behavior
// rateLimitMiddleware depends on, so tests can substitute an in-memory fake
// instead of requiring a live Redis instance — mirroring how videoModule
// depends on videodomain.IdempotencyStore rather than a concrete store.
type videoRateLimiter interface {
	Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error)
}

// rateLimitCheckTimeout bounds how long rateLimitMiddleware waits on
// limiter.Allow, independent of the shared Redis client's own
// connection/retry policy. Fail-open only protects availability if the
// failure surfaces quickly — without this bound, a hung (not merely
// refused) Redis connection could stall every authenticated request for the
// client's full default timeout before falling through to "allow".
const rateLimitCheckTimeout = 300 * time.Millisecond

// rateLimitMiddleware rejects a request with 429 once the authenticated
// caller has exceeded limiter's configured rate. It must run behind
// requireBearerAuth, which guarantees authenticatedUserID(c) is populated.
func rateLimitMiddleware(limiter videoRateLimiter) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := authenticatedUserID(c)
		if !ok {
			c.Next()
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), rateLimitCheckTimeout)
		defer cancel()

		allowed, retryAfter, err := limiter.Allow(ctx, "ratelimit:"+userID.String())
		if err != nil {
			// Fail open: an infrastructure hiccup in the rate limiter must not
			// take down otherwise-healthy request handling.
			log.Printf("rate limit check failed, allowing request: %v", err)
			c.Next()
			return
		}
		if !allowed {
			c.Header("Retry-After", strconv.Itoa(int(retryAfter.Seconds())))
			c.AbortWithStatusJSON(429, gin.H{"error": "rate limit exceeded, try again later"})
			return
		}

		c.Next()
	}
}
