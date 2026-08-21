package redis

import (
	"context"
	"fmt"

	"github.com/redis/go-redis/v9"
)

// Open constructs a Redis client from cfg. It does not establish a
// connection — callers are responsible for verifying connectivity (e.g. via
// Ping) before relying on the client. Constructing a client from an unparsed
// Addr string cannot fail, so this returns no error, unlike postgres.Open.
//
// ContextTimeoutEnabled is set to true so a context deadline/cancellation
// passed to a command actually bounds that command's socket I/O. go-redis
// v9 defaults this to false, in which case it silently substitutes
// context.Background() for the caller's context on every command — a
// context.WithTimeout wrapped around a call has no effect at all, and a
// hung (not merely refused) connection instead falls back to the client's
// own ReadTimeout (5s default). add-rate-limiting-middleware's fail-open
// bound depends on the caller's context actually being honored; callers
// that don't need a tight deadline are unaffected (context.Background()
// has none to enforce).
func Open(cfg Config) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: cfg.Addr, ContextTimeoutEnabled: true})
}

// Ping issues a round-trip health check against client.
func Ping(ctx context.Context, client *redis.Client) error {
	if err := client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("platform/redis: ping: %w", err)
	}
	return nil
}

// Close releases client's underlying connection pool.
func Close(client *redis.Client) error {
	return client.Close()
}
