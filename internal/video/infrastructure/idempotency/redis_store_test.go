package idempotency_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"

	platformredis "video-processor/internal/platform/redis"
	"video-processor/internal/video/domain"
	"video-processor/internal/video/infrastructure/idempotency"
)

// testStore skips the test unless REDIS_TEST_ADDR is explicitly set,
// mirroring internal/platform/redis's own test pattern: the default
// unit-test path must not require a live external service.
func testStore(t *testing.T) *idempotency.RedisStore {
	t.Helper()

	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; skipping Redis integration test")
	}

	client := platformredis.Open(platformredis.Config{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	return idempotency.NewRedisStore(client)
}

// testKey builds a unique IdempotencyKey per test so concurrent test runs
// against a shared Redis instance never collide.
func testKey(t *testing.T) domain.IdempotencyKey {
	t.Helper()
	key, err := domain.NewIdempotencyKey(t.Name(), uuid.NewString())
	if err != nil {
		t.Fatalf("unexpected error building test key: %v", err)
	}
	return key
}

func TestReserve_SecondReservationFailsWhileFirstIsInFlight(t *testing.T) {
	store := testStore(t)
	key := testKey(t)
	ctx := context.Background()

	_, reserved, err := store.Reserve(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !reserved {
		t.Fatal("first reservation should succeed")
	}

	_, reserved, err = store.Reserve(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reserved {
		t.Fatal("second reservation should fail while the first is still in flight")
	}
}

func TestLookup_MissForAbsentKey(t *testing.T) {
	store := testStore(t)
	key := testKey(t)

	_, found, err := store.Lookup(context.Background(), key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false for an absent key")
	}
}

func TestLookup_MissWhileReservationInFlight(t *testing.T) {
	store := testStore(t)
	key := testKey(t)
	ctx := context.Background()

	if _, reserved, err := store.Reserve(ctx, key); err != nil || !reserved {
		t.Fatalf("reserve failed: reserved=%v err=%v", reserved, err)
	}

	_, found, err := store.Lookup(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("an in-flight reservation should not be found as a real job")
	}
}

func TestReserveThenFinalize_LookupReturnsJobID(t *testing.T) {
	store := testStore(t)
	key := testKey(t)
	ctx := context.Background()

	token, reserved, err := store.Reserve(ctx, key)
	if err != nil || !reserved {
		t.Fatalf("reserve failed: reserved=%v err=%v", reserved, err)
	}

	jobID, err := domain.NewVideoJobID(uuid.NewString())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	finalized, err := store.Finalize(ctx, key, token, jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !finalized {
		t.Fatal("finalize with the correct token should succeed")
	}

	gotJobID, found, err := store.Lookup(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !found {
		t.Fatal("expected found=true after finalize")
	}
	if !gotJobID.Equal(jobID) {
		t.Fatalf("Lookup returned %q, want %q", gotJobID.String(), jobID.String())
	}
}

func TestFinalize_MismatchedTokenIsNoOp(t *testing.T) {
	store := testStore(t)
	key := testKey(t)
	ctx := context.Background()

	if _, reserved, err := store.Reserve(ctx, key); err != nil || !reserved {
		t.Fatalf("reserve failed: reserved=%v err=%v", reserved, err)
	}

	jobID, err := domain.NewVideoJobID(uuid.NewString())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	finalized, err := store.Finalize(ctx, key, "wrong-token", jobID)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if finalized {
		t.Fatal("finalize with a mismatched token should be a no-op")
	}

	// The original reservation is untouched, so it's still not found as
	// a real job.
	_, found, err := store.Lookup(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("original reservation should be unaffected by the failed finalize")
	}
}

func TestClearThenLookup_Miss(t *testing.T) {
	store := testStore(t)
	key := testKey(t)
	ctx := context.Background()

	token, reserved, err := store.Reserve(ctx, key)
	if err != nil || !reserved {
		t.Fatalf("reserve failed: reserved=%v err=%v", reserved, err)
	}

	cleared, err := store.Clear(ctx, key, token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cleared {
		t.Fatal("clear with the correct token should succeed")
	}

	_, found, err := store.Lookup(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false after clear")
	}

	// Clearing also frees the key for a fresh reservation.
	if _, reserved, err := store.Reserve(ctx, key); err != nil || !reserved {
		t.Fatalf("reserve after clear failed: reserved=%v err=%v", reserved, err)
	}
}

func TestClear_MismatchedTokenIsNoOp(t *testing.T) {
	store := testStore(t)
	key := testKey(t)
	ctx := context.Background()

	if _, reserved, err := store.Reserve(ctx, key); err != nil || !reserved {
		t.Fatalf("reserve failed: reserved=%v err=%v", reserved, err)
	}

	cleared, err := store.Clear(ctx, key, "wrong-token")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cleared {
		t.Fatal("clear with a mismatched token should be a no-op")
	}

	// A second reservation attempt should still fail — the original
	// reservation was never actually cleared.
	_, reserved, err := store.Reserve(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reserved {
		t.Fatal("reservation should still be held after a no-op clear")
	}
}

func TestClear_AfterFinalize_UsesTheSameOwningToken(t *testing.T) {
	store := testStore(t)
	key := testKey(t)
	ctx := context.Background()

	token, reserved, err := store.Reserve(ctx, key)
	if err != nil || !reserved {
		t.Fatalf("reserve failed: reserved=%v err=%v", reserved, err)
	}

	jobID, err := domain.NewVideoJobID(uuid.NewString())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if finalized, err := store.Finalize(ctx, key, token, jobID); err != nil || !finalized {
		t.Fatalf("finalize failed: finalized=%v err=%v", finalized, err)
	}

	// The same token that reserved the key must still be able to clear
	// it after it has been finalized — ownership persists across states.
	cleared, err := store.Clear(ctx, key, token)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cleared {
		t.Fatal("clear with the original owning token should succeed even after finalize")
	}

	_, found, err := store.Lookup(ctx, key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if found {
		t.Fatal("expected found=false after clearing a finalized key")
	}
}

func TestReserve_SentinelTTLIsSet(t *testing.T) {
	store := testStore(t)
	key := testKey(t)
	ctx := context.Background()

	if _, reserved, err := store.Reserve(ctx, key); err != nil || !reserved {
		t.Fatalf("reserve failed: reserved=%v err=%v", reserved, err)
	}

	ttl := rawTTL(t, key)
	if ttl <= 0 || ttl > 5*time.Minute {
		t.Fatalf("sentinel TTL = %v, want a positive value at or below 5 minutes", ttl)
	}
}

func TestFinalize_SetsTheFullIdempotencyWindowTTL(t *testing.T) {
	store := testStore(t)
	key := testKey(t)
	ctx := context.Background()

	token, reserved, err := store.Reserve(ctx, key)
	if err != nil || !reserved {
		t.Fatalf("reserve failed: reserved=%v err=%v", reserved, err)
	}

	jobID, err := domain.NewVideoJobID(uuid.NewString())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if finalized, err := store.Finalize(ctx, key, token, jobID); err != nil || !finalized {
		t.Fatalf("finalize failed: finalized=%v err=%v", finalized, err)
	}

	ttl := rawTTL(t, key)
	if ttl <= 23*time.Hour || ttl > 24*time.Hour {
		t.Fatalf("finalized TTL = %v, want close to 24h", ttl)
	}
}

// rawTTL issues a raw Redis TTL command against key, bypassing the
// RedisStore abstraction, to assert on the underlying key's expiry
// directly.
func rawTTL(t *testing.T, key domain.IdempotencyKey) time.Duration {
	t.Helper()
	client := platformredis.Open(platformredis.Config{Addr: os.Getenv("REDIS_TEST_ADDR")})
	t.Cleanup(func() { _ = client.Close() })
	ttl, err := client.TTL(context.Background(), key.String()).Result()
	if err != nil {
		t.Fatalf("unexpected error reading TTL: %v", err)
	}
	return ttl
}
