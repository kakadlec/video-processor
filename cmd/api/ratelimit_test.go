package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"
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
	// wait rather than blocking indefinitely.
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

func TestRateLimitMiddleware_AllowedRequestReachesHandler(t *testing.T) {
	limiter := &fakeVideoRateLimiter{allow: true}
	identity, tokens := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, limiter))
	t.Cleanup(srv.Close)

	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		t.Fatalf("status = %d, want anything but 429 for an allowed request", resp.StatusCode)
	}
	if limiter.callCount() != 1 {
		t.Fatalf("Allow called %d times, want 1", limiter.callCount())
	}
}

func TestRateLimitMiddleware_DeniedRequestReturns429WithRetryAfter(t *testing.T) {
	limiter := &fakeVideoRateLimiter{allow: false, retryAfter: 42 * time.Second}
	identity, tokens := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, limiter))
	t.Cleanup(srv.Close)

	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", resp.StatusCode, http.StatusTooManyRequests)
	}
	retryAfter := resp.Header.Get("Retry-After")
	if retryAfter != "42" {
		t.Fatalf("Retry-After = %q, want %q", retryAfter, "42")
	}
}

func TestRateLimitMiddleware_LimiterErrorFailsOpen(t *testing.T) {
	limiter := &fakeVideoRateLimiter{err: errors.New("redis unreachable")}
	identity, tokens := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, limiter))
	t.Cleanup(srv.Close)

	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	resp := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+token)
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		t.Fatal("expected a limiter error to fail open, not to reject the request")
	}
}

func TestRateLimitMiddleware_UnresponsiveLimiterFailsOpenWithinBoundedTimeout(t *testing.T) {
	limiter := &fakeVideoRateLimiter{blockUntilCtxDone: true}
	identity, tokens := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, limiter))
	t.Cleanup(srv.Close)

	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	started := time.Now()
	resp := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+token)
	defer resp.Body.Close()
	elapsed := time.Since(started)

	if resp.StatusCode == http.StatusTooManyRequests {
		t.Fatal("expected an unresponsive limiter to fail open, not to reject the request")
	}
	if elapsed > 2*time.Second {
		t.Fatalf("request took %v to fail open, want it bounded by rateLimitCheckTimeout (%v)", elapsed, rateLimitCheckTimeout)
	}
}

func TestRateLimitMiddleware_UnauthenticatedRouteNeverInvokesLimiter(t *testing.T) {
	limiter := &fakeVideoRateLimiter{allow: true}
	identity, _ := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, limiter))
	t.Cleanup(srv.Close)

	resp := postJSONWithAuthorization(t, srv.URL+"/api/auth/login", "", map[string]string{"email": "nobody@example.com", "password": "wrong"})
	defer resp.Body.Close()

	if limiter.callCount() != 0 {
		t.Fatalf("Allow called %d times for an unauthenticated route, want 0", limiter.callCount())
	}
}

func TestRateLimitMiddleware_KeysDifferentUsersIndependently(t *testing.T) {
	limiter := &fakeVideoRateLimiter{allow: true}
	identity, tokens := newTestIdentityModuleWithTokens(t)
	video := newTestVideoModule(t)
	srv := httptest.NewServer(setupRouter(identity, video, limiter))
	t.Cleanup(srv.Close)

	_, tokenA := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")
	_, tokenB := issueTestToken(t, tokens, "4fa85f64-5717-4562-b3fc-2c963f66afa7")

	respA := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+tokenA)
	defer respA.Body.Close()
	respB := getWithAuthorization(t, srv.URL+"/api/status", "Bearer "+tokenB)
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
