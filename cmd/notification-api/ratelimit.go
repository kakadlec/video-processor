package main

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
)

// rateLimiter is the subset of *ratelimit.Limiter's behavior
// rateLimitMiddleware depends on, so tests can substitute an in-memory fake
// instead of requiring a live Redis instance.
type rateLimiter interface {
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
//
// The key format is shared with every other service that mounts this
// middleware, deliberately: the budget is one budget per user across the
// whole system, and namespacing the counter per service would silently
// multiply every user's allowance by the number of services.
func rateLimitMiddleware(limiter rateLimiter) gin.HandlerFunc {
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
			logger(componentRateLimit).Warn("the rate limit check failed; the request is allowed", slog.String("error", err.Error()))
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
