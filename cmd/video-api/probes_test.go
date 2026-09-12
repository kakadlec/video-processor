package main

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

// errProbeDependencyDown stands in for a dependency that is not answering.
var errProbeDependencyDown = errors.New("the dependency is unreachable")

// serveProbeRequest serves one GET through the handler and returns the
// recorder. A recorder rather than an httptest.Server, deliberately: net/http
// stamps a Date on a served response, and two responses required to be
// indistinguishable would then differ for a reason that has nothing to do
// with what is being asserted.
func serveProbeRequest(t *testing.T, router http.Handler, path, bearer string) *httptest.ResponseRecorder {
	t.Helper()

	req := httptest.NewRequest(http.MethodGet, path, nil)
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)
	return rec
}

// countingProbe returns a check and the counter it increments, so a test can
// assert not merely that an endpoint answered but that it consulted nothing
// to do so.
func countingProbe(err error) (func(context.Context) error, *atomic.Int32) {
	var calls atomic.Int32
	return func(context.Context) error {
		calls.Add(1)
		return err
	}, &calls
}

func requireProbeResponse(t *testing.T, rec *httptest.ResponseRecorder, status int, body string) {
	t.Helper()

	if rec.Code != status {
		t.Fatalf("got status %d, want %d (body: %s)", rec.Code, status, rec.Body.String())
	}
	if got := rec.Body.String(); got != body {
		t.Fatalf("got body %q, want %q", got, body)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("got Cache-Control %q, want %q — an intermediary may answer a probe from a stored verdict", got, "no-store")
	}
}

// newProbeTestRouter builds this service's real router over a caller-supplied
// limiter and checker, so every probe assertion below is made against the
// middleware chain main actually mounts rather than a reconstruction of it.
func newProbeTestRouter(t *testing.T, limiter rateLimiter, readiness *readinessChecker) (*gin.Engine, testTokens) {
	t.Helper()

	auth, tokens := newTestAuthenticatorWithTokens(t)
	return setupRouter(auth, newTestVideoModule(t), limiter, readiness), tokens
}

// TestLivenessConsultsNothing drives the real router rather than the handler,
// because what is being asserted is the endpoint as it is actually reached.
// The checker behind it holds a failing dependency: a liveness endpoint that
// consulted it would answer 503, and one that consulted it and ignored the
// answer would still increment the counter.
func TestLivenessConsultsNothing(t *testing.T) {
	probe, calls := countingProbe(errProbeDependencyDown)
	router, _ := newProbeTestRouter(t, alwaysAllowRateLimiter{}, newChecker(readinessCheck{name: dependencyDatabase, probe: probe}))

	requireProbeResponse(t, serveProbeRequest(t, router, healthRoutePath, ""), http.StatusOK, probeBodyHealthy)

	if calls.Load() != 0 {
		t.Fatalf("liveness consulted a dependency %d times; it must consult none", calls.Load())
	}
}

func TestReadinessAnswersReadyWhileEveryDependencyAnswers(t *testing.T) {
	router, _ := newProbeTestRouter(t, alwaysAllowRateLimiter{}, newChecker(readinessCheck{name: dependencyDatabase, probe: readinessProbeOK}))

	requireProbeResponse(t, serveProbeRequest(t, router, readyRoutePath, ""), http.StatusOK, probeBodyReady)
}

// TestReadinessAnswersNotReadyAndNamesNothing covers the verdict and the
// non-disclosure rule together: naming the failing dependency in the body
// would publish this deployment's dependency inventory to an unauthenticated
// caller, and by repetition the times at which each part of it is degraded.
func TestReadinessAnswersNotReadyAndNamesNothing(t *testing.T) {
	router, _ := newProbeTestRouter(t, alwaysAllowRateLimiter{}, newChecker(readinessCheck{name: dependencyDatabase, probe: readinessProbeFailing}))

	rec := serveProbeRequest(t, router, readyRoutePath, "")
	requireProbeResponse(t, rec, http.StatusServiceUnavailable, probeBodyNotReady)

	if strings.Contains(rec.Body.String(), dependencyDatabase) {
		t.Fatalf("the readiness body names the failing dependency: %s", rec.Body.String())
	}
}

// TestTheReadinessEndpointReleasesTheWorkWhenTheCallerDisconnects pins the
// half of the bound that the checker's own test cannot reach. The checker
// bounds each dependency against whatever context it is handed, so a handler
// passing context.Background() keeps the timeout and silently drops
// cancellation — and the request is the only layer at which a caller can
// disconnect at all.
func TestTheReadinessEndpointReleasesTheWorkWhenTheCallerDisconnects(t *testing.T) {
	router, _ := newProbeTestRouter(t, alwaysAllowRateLimiter{}, newChecker(readinessCheck{name: dependencyDatabase, probe: readinessProbeHanging}))

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, readyRoutePath, nil).WithContext(ctx)

	done := make(chan struct{})
	go func() {
		defer close(done)
		router.ServeHTTP(httptest.NewRecorder(), req)
	}()

	cancel()
	select {
	case <-done:
	case <-time.After(readinessCheckTimeout / 2):
		t.Fatalf("the endpoint outlived its caller; the check is not derived from the request context")
	}
}

// TestAProbeIsAnsweredWithNoAuthorizationHeader pins that both probes sit
// outside the authenticated group, beside the three routes that serve the
// frontend. A probe carries no bearer token, and requiring one would make the
// Identity service a dependency of this service's health signal — the
// coupling distributing verification material by configuration was chosen to
// remove.
func TestAProbeIsAnsweredWithNoAuthorizationHeader(t *testing.T) {
	router, _ := newProbeTestRouter(t, alwaysAllowRateLimiter{}, newChecker())

	for _, path := range []string{healthRoutePath, readyRoutePath} {
		rec := serveProbeRequest(t, router, path, "")
		if rec.Code == http.StatusUnauthorized {
			t.Fatalf("%s answered 401 to an anonymous probe", path)
		}
		if rec.Code != http.StatusOK {
			t.Fatalf("%s answered %d to an anonymous probe, want 200", path, rec.Code)
		}
	}
}

// TestTheRateLimiterDoesNotCountAProbe is the assertion an idle-service test
// cannot make. A probe issued at a fixed interval from inside the limiter
// would eventually exhaust a budget and be answered 429, which a prober reads
// as an outage the limiter itself manufactured — a healthy service reporting
// unhealthy for a reason that has nothing to do with its dependencies.
//
// The budget is exhausted first, against a real route, so the limiter is
// demonstrably refusing before the probe is made: a test that only probed
// would pass against a limiter that was never consulted by anything.
func TestTheRateLimiterDoesNotCountAProbe(t *testing.T) {
	limiter := &fakeRateLimiter{allow: false, retryAfter: 30 * time.Second}
	router, tokens := newProbeTestRouter(t, limiter, newChecker())
	_, token := issueTestToken(t, tokens, "3fa85f64-5717-4562-b3fc-2c963f66afa6")

	const authenticatedRoute = "/api/status"
	exhausted := serveProbeRequest(t, router, authenticatedRoute, token)
	if exhausted.Code != http.StatusTooManyRequests {
		t.Fatalf("the budget is not exhausted: %s answered %d, want 429", authenticatedRoute, exhausted.Code)
	}
	refusals := limiter.callCount()

	for _, path := range []string{healthRoutePath, readyRoutePath} {
		rec := serveProbeRequest(t, router, path, token)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s answered %d with the caller's budget exhausted, want 200", path, rec.Code)
		}
	}

	if got := limiter.callCount(); got != refusals {
		t.Fatalf("the limiter was consulted %d times for two probes; it must be consulted none", got-refusals)
	}
}

// TestTwoDifferentReadinessFailuresAreIndistinguishable is the assertion that
// cannot be made by reading the handler, and it is the reason it is made over
// two failures rather than one: a body carrying the failing dependency's name
// passes every single-failure test in this file.
//
// This service is the only one in the deployment with two readiness
// dependencies, so it is the only place the comparison exists to be made. The
// failures are driven through the checker rather than by tearing down a real
// object store: the real bucket-absent case is already covered against MinIO
// in readiness_test.go, and what is under test here is the response, which
// cannot tell how the check came to fail.
func TestTwoDifferentReadinessFailuresAreIndistinguishable(t *testing.T) {
	databaseDown, _ := newProbeTestRouter(t, alwaysAllowRateLimiter{}, newChecker(
		readinessCheck{name: dependencyDatabase, probe: readinessProbeFailing},
		readinessCheck{name: dependencyObjectStorage, probe: readinessProbeOK},
	))
	objectStorageDown, _ := newProbeTestRouter(t, alwaysAllowRateLimiter{}, newChecker(
		readinessCheck{name: dependencyDatabase, probe: readinessProbeOK},
		readinessCheck{name: dependencyObjectStorage, probe: readinessProbeFailing},
	))

	first := serveProbeRequest(t, databaseDown, readyRoutePath, "")
	second := serveProbeRequest(t, objectStorageDown, readyRoutePath, "")

	requireProbeResponse(t, first, http.StatusServiceUnavailable, probeBodyNotReady)
	requireProbeResponse(t, second, http.StatusServiceUnavailable, probeBodyNotReady)

	if first.Code != second.Code {
		t.Fatalf("the two failures answered %d and %d", first.Code, second.Code)
	}
	if !bytes.Equal(first.Body.Bytes(), second.Body.Bytes()) {
		t.Fatalf("the two failures answered %q and %q", first.Body.String(), second.Body.String())
	}
	// The whole header map rather than a spot check: a dependency name added
	// under a header this test never thought to name would otherwise survive.
	if !reflect.DeepEqual(first.Header(), second.Header()) {
		t.Fatalf("the two failures answered different headers: %v and %v", first.Header(), second.Header())
	}

	for _, name := range []string{dependencyDatabase, dependencyObjectStorage} {
		if strings.Contains(first.Body.String(), name) || strings.Contains(second.Body.String(), name) {
			t.Fatalf("a readiness body names %q", name)
		}
	}
}
