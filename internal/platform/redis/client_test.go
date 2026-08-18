package redis_test

import (
	"context"
	"os"
	"testing"

	"video-processor/internal/platform/redis"
)

// testAddr skips the test unless REDIS_TEST_ADDR is explicitly set, per
// design.md: the default unit-test path must not require a live external
// service. Set the env var and provision a real Redis instance to exercise
// this adapter end-to-end.
func testAddr(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; skipping Redis integration test")
	}
	return addr
}

func TestOpen_SucceedsWithoutConnecting(t *testing.T) {
	client := redis.Open(redis.Config{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })

	if client == nil {
		t.Fatal("Open returned a nil client")
	}
}

func TestPing_SucceedsAgainstRunningRedis(t *testing.T) {
	addr := testAddr(t)

	client := redis.Open(redis.Config{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	if err := redis.Ping(context.Background(), client); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPing_FailsAgainstUnreachableRedis(t *testing.T) {
	client := redis.Open(redis.Config{Addr: "127.0.0.1:1"})
	t.Cleanup(func() { _ = client.Close() })

	if err := redis.Ping(context.Background(), client); err == nil {
		t.Fatal("expected an error, got nil")
	}
}

func TestClose_ReleasesClient(t *testing.T) {
	addr := testAddr(t)

	client := redis.Open(redis.Config{Addr: addr})

	if err := redis.Close(client); err != nil {
		t.Fatalf("unexpected error closing client: %v", err)
	}

	if err := redis.Ping(context.Background(), client); err == nil {
		t.Fatal("expected an error pinging a closed client, got nil")
	}
}
