package redis_test

import (
	"errors"
	"testing"

	"video-processor/internal/platform/redis"
)

func TestLoadConfigFromEnv_RequiresAddr(t *testing.T) {
	t.Setenv("REDIS_ADDR", "")

	_, err := redis.LoadConfigFromEnv()
	if !errors.Is(err, redis.ErrAddrRequired) {
		t.Fatalf("error = %v, want %v", err, redis.ErrAddrRequired)
	}
}

func TestLoadConfigFromEnv_ReadsAddr(t *testing.T) {
	const addr = "localhost:6379"
	t.Setenv("REDIS_ADDR", addr)

	cfg, err := redis.LoadConfigFromEnv()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Addr != addr {
		t.Fatalf("Addr = %q, want %q", cfg.Addr, addr)
	}
}
