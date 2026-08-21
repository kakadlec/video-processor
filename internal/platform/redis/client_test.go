package redis_test

import (
	"context"
	"os"
	"testing"
	"time"

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

// TestOpen_ClientHonorsContextDeadlineDuringInFlightCommand proves Open sets
// ContextTimeoutEnabled (add-rate-limiting-middleware's fail-open latency
// bound depends on this): it must bound a command that is genuinely still
// in flight when the context expires, not merely reject a context that was
// already expired before the call started (go-redis rejects an
// already-canceled context up front regardless of this option, so that
// alone wouldn't distinguish the two configurations). BLPop against a key
// nobody pushes to blocks server-side for up to its given timeout without
// stalling Redis's other clients, giving a real in-flight command whose
// only way to return early is the context this test passes.
func TestOpen_ClientHonorsContextDeadlineDuringInFlightCommand(t *testing.T) {
	addr := testAddr(t)

	client := redis.Open(redis.Config{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	started := time.Now()
	_, err := client.BLPop(ctx, 5*time.Second, "platform-redis-test:context-timeout-honored:nonexistent").Result()
	elapsed := time.Since(started)

	if err == nil {
		t.Fatal("expected BLPop to fail once its context deadline passed mid-flight — success means the client is not bounded by the passed context (ContextTimeoutEnabled not honored)")
	}
	if elapsed > 1*time.Second {
		t.Fatalf("BLPop took %v to return after a 100ms context deadline, want it bounded near that — the client let it run past the deadline (falling back to BLPop's own 5s timeout)", elapsed)
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
