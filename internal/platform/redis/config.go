package redis

import (
	"errors"
	"os"
)

// ErrAddrRequired is returned when no Redis address is configured. Startup
// must fail clearly rather than silently running without a cache/lock backend.
var ErrAddrRequired = errors.New("platform/redis: REDIS_ADDR environment variable is required")

// Config holds Redis connection configuration.
type Config struct {
	Addr string
}

// LoadConfigFromEnv reads the Redis address from the environment.
func LoadConfigFromEnv() (Config, error) {
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		return Config{}, ErrAddrRequired
	}
	return Config{Addr: addr}, nil
}
