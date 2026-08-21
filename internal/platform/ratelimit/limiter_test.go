package ratelimit_test

import (
	"context"
	"os"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"video-processor/internal/platform/ratelimit"
	platformredis "video-processor/internal/platform/redis"
)

// testAddr skips the test unless REDIS_TEST_ADDR is explicitly set, matching
// internal/platform/redis's own test pattern.
func testAddr(t *testing.T) string {
	t.Helper()

	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; skipping Redis integration test")
	}
	return addr
}

func newTestLimiter(t *testing.T, cfg ratelimit.Config) *ratelimit.Limiter {
	t.Helper()

	client := platformredis.Open(platformredis.Config{Addr: testAddr(t)})
	t.Cleanup(func() { _ = client.Close() })

	return ratelimit.NewLimiter(client, cfg)
}

func TestAllow_UnderLimitRequestsAllAllowed(t *testing.T) {
	limiter := newTestLimiter(t, ratelimit.Config{MaxRequests: 3, WindowSeconds: 10})
	key := uniqueKey(t)

	for i := 0; i < 3; i++ {
		allowed, _, err := limiter.Allow(context.Background(), key)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Fatalf("request %d: expected allowed, got denied", i+1)
		}
	}
}

func TestAllow_RequestCrossingLimitIsDenied(t *testing.T) {
	limiter := newTestLimiter(t, ratelimit.Config{MaxRequests: 2, WindowSeconds: 10})
	key := uniqueKey(t)

	for i := 0; i < 2; i++ {
		allowed, _, err := limiter.Allow(context.Background(), key)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !allowed {
			t.Fatalf("request %d: expected allowed, got denied", i+1)
		}
	}

	allowed, retryAfter, err := limiter.Allow(context.Background(), key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowed {
		t.Fatal("expected the request crossing the limit to be denied")
	}
	if retryAfter <= 0 || retryAfter > 10*time.Second {
		t.Fatalf("retryAfter = %v, want a positive duration bounded by the window", retryAfter)
	}
}

func TestAllow_DifferentKeysTrackedIndependently(t *testing.T) {
	limiter := newTestLimiter(t, ratelimit.Config{MaxRequests: 1, WindowSeconds: 10})
	keyA := uniqueKey(t)
	keyB := uniqueKey(t)

	allowedA, _, err := limiter.Allow(context.Background(), keyA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowedA {
		t.Fatal("expected first request for keyA to be allowed")
	}

	allowedADenied, _, err := limiter.Allow(context.Background(), keyA)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if allowedADenied {
		t.Fatal("expected second request for keyA to be denied")
	}

	allowedB, _, err := limiter.Allow(context.Background(), keyB)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowedB {
		t.Fatal("expected keyB's first request to be allowed, unaffected by keyA's state")
	}
}

func TestAllow_AllowedAgainAfterWindowElapses(t *testing.T) {
	limiter := newTestLimiter(t, ratelimit.Config{MaxRequests: 1, WindowSeconds: 1})
	key := uniqueKey(t)

	allowed, _, err := limiter.Allow(context.Background(), key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowed {
		t.Fatal("expected first request to be allowed")
	}

	denied, _, err := limiter.Allow(context.Background(), key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if denied {
		t.Fatal("expected second request within the window to be denied")
	}

	time.Sleep(1200 * time.Millisecond)

	allowedAfterWindow, _, err := limiter.Allow(context.Background(), key)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !allowedAfterWindow {
		t.Fatal("expected a request after the window elapsed to be allowed again")
	}
}

// uniqueKeyCounter disambiguates multiple uniqueKey calls within the same
// test (which otherwise share the same t.Name()).
var uniqueKeyCounter atomic.Int64

// uniqueKey gives each call its own Redis key namespace so concurrent test
// runs against a shared Redis instance, and multiple keys within one test,
// don't interfere with each other.
func uniqueKey(t *testing.T) string {
	t.Helper()
	return "ratelimit-test:" + t.Name() + ":" + strconv.FormatInt(uniqueKeyCounter.Add(1), 10)
}
