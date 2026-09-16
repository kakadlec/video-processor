package main

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestMetricsHandler_ServesTheExpositionAndNothingElse pins the one route
// this surface carries: /metrics answers the exposition, and any other path
// answers the mux's own 404 rather than it -- there is no probe and no
// bearer group here, unlike the three HTTP services.
func TestMetricsHandler_ServesTheExpositionAndNothingElse(t *testing.T) {
	handler := newMetricsHandler()

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("GET /metrics = %d, want %d", recorder.Code, http.StatusOK)
	}
	if !strings.Contains(recorder.Body.String(), "go_goroutines") {
		t.Fatalf("the exposition carries no go_goroutines sample:\n%s", recorder.Body.String())
	}

	recorder = httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/", nil))
	if recorder.Code != http.StatusNotFound {
		t.Fatalf("GET / = %d, want %d — this surface carries one route", recorder.Code, http.StatusNotFound)
	}
}

// TestServeMetrics_ServesUntilCancelledThenShutsDown drives the lifecycle
// main wires in production, over an ephemeral listener rather than
// metricsAddr's fixed one so this test does not contend for a real port.
func TestServeMetrics_ServesUntilCancelledThenShutsDown(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	server := newMetricsServer()
	go func() {
		defer close(done)
		serveMetrics(ctx, server, listener)
	}()

	url := "http://" + listener.Addr().String() + "/metrics"
	waitForServing(t, url)

	resp, err := http.Get(url) //nolint:noctx // test helper, bounded below
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	// Drained fully before Close, not just closed: an unread body leaves the
	// client's connection unfit to reuse or confirm closed, which left
	// Shutdown below blocking for the full metricsShutdownTimeout waiting on
	// a connection this process never actually kept open.
	io.Copy(io.Discard, resp.Body) //nolint:errcheck // draining, not reading
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET %s = %d, want %d", url, resp.StatusCode, http.StatusOK)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(metricsShutdownTimeout + 5*time.Second):
		t.Fatal("serveMetrics did not return after cancellation")
	}

	if _, err := http.Get(url); err == nil { //nolint:noctx // test helper
		t.Fatal("the listener is still serving after shutdown")
	}
}

// waitForServing polls url until it answers or the bound elapses, so this
// test does not race the goroutine that starts serving.
func waitForServing(t *testing.T, url string) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := http.Get(url) //nolint:noctx // bounded by the deadline loop
		if err == nil {
			io.Copy(io.Discard, resp.Body) //nolint:errcheck // draining, not reading
			resp.Body.Close()
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("%s never started serving", url)
}
