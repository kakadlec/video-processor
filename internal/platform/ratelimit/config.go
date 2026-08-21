package ratelimit

import (
	"fmt"
	"os"
	"strconv"
)

const (
	maxRequestsEnv   = "RATE_LIMIT_MAX_REQUESTS"
	windowSecondsEnv = "RATE_LIMIT_WINDOW_SECONDS"

	defaultMaxRequests   = 60
	defaultWindowSeconds = 60
)

// Config holds the rate limiter's threshold and window.
type Config struct {
	MaxRequests   int
	WindowSeconds int
}

// LoadConfigFromEnv reads RATE_LIMIT_MAX_REQUESTS and RATE_LIMIT_WINDOW_SECONDS
// from the environment, falling back to safe defaults when either is unset or
// empty — unlike platformredis.LoadConfigFromEnv's REDIS_ADDR, absence here is
// not a startup failure. Both values, whether defaulted or explicit, must be
// strictly positive: a non-positive WindowSeconds would expire the Redis
// counter immediately (disabling the limit entirely), and a non-positive
// MaxRequests would reject every request.
func LoadConfigFromEnv() (Config, error) {
	maxRequests, err := positiveIntFromEnv(maxRequestsEnv, defaultMaxRequests)
	if err != nil {
		return Config{}, err
	}

	windowSeconds, err := positiveIntFromEnv(windowSecondsEnv, defaultWindowSeconds)
	if err != nil {
		return Config{}, err
	}

	return Config{MaxRequests: maxRequests, WindowSeconds: windowSeconds}, nil
}

func positiveIntFromEnv(name string, fallback int) (int, error) {
	raw := os.Getenv(name)
	if raw == "" {
		return fallback, nil
	}
	value, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("platform/ratelimit: %s: invalid integer %q: %w", name, raw, err)
	}
	if value <= 0 {
		return 0, fmt.Errorf("platform/ratelimit: %s: must be a strictly positive integer, got %d", name, value)
	}
	return value, nil
}
