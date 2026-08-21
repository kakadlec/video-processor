package ratelimit

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// allowScript atomically increments the request counter for a rate-limit key
// and, only on the first increment of the window, sets its expiry — so later
// requests within the same window don't keep pushing the TTL back out. It
// returns the post-increment count and the key's current remaining lifetime
// in one round trip, mirroring the atomicity idempotency.RedisStore's
// Finalize/Clear scripts already established elsewhere in this codebase.
//
// Remaining lifetime is read with PTTL (milliseconds), not TTL (whole
// seconds): TTL truncates, so it can report 0 while the key still has, say,
// 400ms left — which would make a denied response's Retry-After header read
// 0 (telling the client to retry immediately against a window that hasn't
// actually reset yet). Allow rounds the millisecond value up to the next
// whole second instead of truncating.
var allowScript = redis.NewScript(`
local count = redis.call("INCR", KEYS[1])
if count == 1 then
	redis.call("EXPIRE", KEYS[1], ARGV[1])
end
local ttlMs = redis.call("PTTL", KEYS[1])
return {count, ttlMs}
`)

// Limiter enforces a fixed-window request-rate limit per key, backed by a
// shared *redis.Client (internal/platform/redis).
type Limiter struct {
	client *redis.Client
	cfg    Config
}

// NewLimiter builds a Limiter from a shared Redis client and Config.
func NewLimiter(client *redis.Client, cfg Config) *Limiter {
	return &Limiter{client: client, cfg: cfg}
}

// Allow reports whether the request identified by key is within the
// configured rate limit for its current fixed window. When it is not,
// retryAfter gives the duration until the window resets and a fresh request
// is expected to succeed.
func (l *Limiter) Allow(ctx context.Context, key string) (allowed bool, retryAfter time.Duration, err error) {
	res, err := allowScript.Run(ctx, l.client, []string{key}, l.cfg.WindowSeconds).Result()
	if err != nil {
		return false, 0, fmt.Errorf("platform/ratelimit: allow: %w", err)
	}

	values, ok := res.([]interface{})
	if !ok || len(values) != 2 {
		return false, 0, fmt.Errorf("platform/ratelimit: allow: unexpected script result: %v", res)
	}

	count, err := toInt64(values[0])
	if err != nil {
		return false, 0, fmt.Errorf("platform/ratelimit: allow: count: %w", err)
	}
	ttlMs, err := toInt64(values[1])
	if err != nil {
		return false, 0, fmt.Errorf("platform/ratelimit: allow: ttl: %w", err)
	}

	if count <= int64(l.cfg.MaxRequests) {
		return true, 0, nil
	}
	if ttlMs < 0 {
		ttlMs = 0
	}
	// Round up to the next whole second so a sub-second remainder never
	// reports a Retry-After of 0 (see allowScript's doc comment above).
	retrySeconds := (ttlMs + 999) / 1000
	if retrySeconds < 1 {
		retrySeconds = 1
	}
	return false, time.Duration(retrySeconds) * time.Second, nil
}

func toInt64(v interface{}) (int64, error) {
	switch n := v.(type) {
	case int64:
		return n, nil
	case string:
		return strconv.ParseInt(n, 10, 64)
	default:
		return 0, fmt.Errorf("unexpected type %T", v)
	}
}
