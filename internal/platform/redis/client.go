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
func Open(cfg Config) *redis.Client {
	return redis.NewClient(&redis.Options{Addr: cfg.Addr})
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
