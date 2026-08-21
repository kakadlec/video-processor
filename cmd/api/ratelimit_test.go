package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/identity/domain"
)

// fakeVideoRateLimiter is a scriptable videoRateLimiter for exercising
// rateLimitMiddleware without a live Redis instance.
type fakeVideoRateLimiter struct {
	mu         sync.Mutex
	allow      bool
	retryAfter time.Duration
	err        error
	// blockUntilCtxDone, when true, simulates a hung (not merely refused)
	// Redis connection: Allow doesn't return until the caller's context is
	// canceled (i.e. rateLimitCheckTimeout fires), then returns the ctx's
	// own error — proving rateLimitMiddleware's timeout actually bounds the
	// wait rather than blocking indefinitely. Note this only proves the
	// middleware itself respects ctx.Done(); it does not prove the real
	// go-redis client does — see internal/platform/ratelimit's own
	// TestAllow_RespectsContextDeadline_RealClient for that.
	blockUntilCtxDone bool
	calls             []string
}

func (f *fakeVideoRateLimiter) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	f.mu.Lock()
	f.calls = append(f.calls, key)
	block := f.blockUntilCtxDone
	f.mu.Unlock()

	if block {
		<-ctx.Done()
		return false, 0, ctx.Err()
	}
	return f.allow, f.retryAfter, f.err
}

func (f *fakeVideoRateLimiter) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// newRateLimitTestRouter builds a minimal Gin router — just rateLimitMiddleware
// plus a probe handler that counts how many times it actually runs — so these
// tests exercise the middleware in isolation, independent of the real
// identity/video modules. authUserID, if non-zero, is injected directly as
// the value requireBearerAuth would have stored, so tests don't need a real
// bearer token; a zero UserID simulates an unauthenticated request (nothing
// injected), exercising the middleware's own authenticatedUserID(c) check.
func newRateLimitTestRouter(limiter videoRateLimiter, authUserID domain.UserID, injectAuth bool) (*httptest.Server, *int32) {
	var handlerCalls int32

	gin.SetMode(gin.TestMode)
	r := gin.New()
	if injectAuth {
		r.Use(func(c *gin.Context) {
			c.Set(authenticatedUserIDKey, authUserID)
			c.Next()
		})
	}
	r.Use(rateLimitMiddleware(limiter))
	r.GET("/probe", func(c *gin.Context) {
		atomic.AddInt32(&handlerCalls, 1)
		c.JSON(http.StatusOK, gin.H{"ok": true})
	})

	srv := httptest.NewServer(r)
	return srv, &handlerCalls
}

func testUserID(t *testing.T, uuid string) domain.UserID {
	t.Helper()
	userID, err := domain.NewUserID(uuid)
	if err != nil {
		t.Fatalf("failed to build test UserID: %v", err)
	}
	return userID
}

func TestRateLimitMiddleware_AllowedRequestReachesHandler(t *testing.T) {
	limiter := &fakeVideoRateLimiter{allow: true}
	srv, handlerCalls := newRateLimitTestRouter(limiter, testUserID(t, "3fa85f64-5717-4562-b3fc-2c963f66afa6"), true)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/probe")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusOK)
	}
	if atomic.LoadInt32(handlerCalls) != 1 {
		t.Fatalf("handler called %d times, want 1", atomic.LoadInt32(handlerCalls))
	}
	if limiter.callCount() != 1 {
		t.Fatalf("Allow called %d times, want 1", limiter.callCount())
	}
}

func TestRateLimitMiddleware_DeniedRequestReturns429AndNeverReachesHandler(t *testing.T) {
	limiter := &fakeVideoRateLimiter{allow: false, retryAfter: 42 * time.Second}
	srv, handlerCalls := newRateLimitTestRouter(limiter, testUserID(t, "3fa85f64-5717-4562-b3fc-2c963f66afa6"), true)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/probe")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
	if retryAfter := resp.Header.Get("Retry-After"); retryAfter != "42" {
		t.Fatalf("Retry-After = %q, want %q", retryAfter, "42")
	}

	var body videoErrorResponse
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body.Error != "rate limit exceeded, try again later" {
		t.Fatalf("body.Error = %q, want %q", body.Error, "rate limit exceeded, try again later")
	}

	if atomic.LoadInt32(handlerCalls) != 0 {
		t.Fatalf("handler called %d times, want 0 — a denied request must never reach the downstream handler", atomic.LoadInt32(handlerCalls))
	}
}

func TestRateLimitMiddleware_LimiterErrorFailsOpenAndReachesHandler(t *testing.T) {
	limiter := &fakeVideoRateLimiter{err: errors.New("redis unreachable")}
	srv, handlerCalls := newRateLimitTestRouter(limiter, testUserID(t, "3fa85f64-5717-4562-b3fc-2c963f66afa6"), true)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/probe")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		t.Fatal("expected a limiter error to fail open, not to reject the request")
	}
	if atomic.LoadInt32(handlerCalls) != 1 {
		t.Fatalf("handler called %d times, want 1 — fail-open must still reach the downstream handler", atomic.LoadInt32(handlerCalls))
	}
}

func TestRateLimitMiddleware_UnresponsiveLimiterFailsOpenWithinBoundedTimeout(t *testing.T) {
	limiter := &fakeVideoRateLimiter{blockUntilCtxDone: true}
	srv, handlerCalls := newRateLimitTestRouter(limiter, testUserID(t, "3fa85f64-5717-4562-b3fc-2c963f66afa6"), true)
	t.Cleanup(srv.Close)

	started := time.Now()
	resp, err := http.Get(srv.URL + "/probe")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()
	elapsed := time.Since(started)

	if resp.StatusCode == http.StatusTooManyRequests {
		t.Fatal("expected an unresponsive limiter to fail open, not to reject the request")
	}
	if atomic.LoadInt32(handlerCalls) != 1 {
		t.Fatalf("handler called %d times, want 1 — bounded-timeout fail-open must still reach the downstream handler", atomic.LoadInt32(handlerCalls))
	}
	if elapsed > 2*time.Second {
		t.Fatalf("request took %v to fail open, want it bounded by rateLimitCheckTimeout (%v)", elapsed, rateLimitCheckTimeout)
	}
}

func TestRateLimitMiddleware_UnauthenticatedRequestNeverInvokesLimiter(t *testing.T) {
	limiter := &fakeVideoRateLimiter{allow: true}
	// injectAuth=false: nothing sets authenticatedUserIDKey, simulating a
	// request the identity layer never authenticated (or a route outside
	// videoRoutes, which never runs requireBearerAuth in the first place).
	srv, handlerCalls := newRateLimitTestRouter(limiter, domain.UserID{}, false)
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/probe")
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if limiter.callCount() != 0 {
		t.Fatalf("Allow called %d times for an unauthenticated request, want 0", limiter.callCount())
	}
	if atomic.LoadInt32(handlerCalls) != 1 {
		t.Fatalf("handler called %d times, want 1 — the middleware must pass unauthenticated requests through untouched", atomic.LoadInt32(handlerCalls))
	}
}

func TestRateLimitMiddleware_KeysDifferentUsersIndependently(t *testing.T) {
	limiter := &fakeVideoRateLimiter{allow: true}
	srvA, _ := newRateLimitTestRouter(limiter, testUserID(t, "3fa85f64-5717-4562-b3fc-2c963f66afa6"), true)
	t.Cleanup(srvA.Close)
	srvB, _ := newRateLimitTestRouter(limiter, testUserID(t, "4fa85f64-5717-4562-b3fc-2c963f66afa7"), true)
	t.Cleanup(srvB.Close)

	respA, err := http.Get(srvA.URL + "/probe")
	if err != nil {
		t.Fatalf("request A failed: %v", err)
	}
	defer respA.Body.Close()
	respB, err := http.Get(srvB.URL + "/probe")
	if err != nil {
		t.Fatalf("request B failed: %v", err)
	}
	defer respB.Body.Close()

	if limiter.callCount() != 2 {
		t.Fatalf("Allow called %d times, want 2", limiter.callCount())
	}
	limiter.mu.Lock()
	keyA, keyB := limiter.calls[0], limiter.calls[1]
	limiter.mu.Unlock()
	if keyA == keyB {
		t.Fatalf("expected distinct rate-limit keys for distinct users, got the same key %q for both", keyA)
	}
}
