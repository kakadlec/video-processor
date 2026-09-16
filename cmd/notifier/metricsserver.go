package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"

	"video-processor/internal/platform/metrics"
)

// metricsAddr is the address the /metrics listener binds to inside this
// process's own container. It is not configurable, on the same reasoning as
// the three HTTP services' fixed :8080: every address on this image is an
// internal-network convention the compose file and Prometheus's scrape
// config both hardcode by container name, not a deployment choice, and this
// process is never reached from outside the compose network at all — see
// http_surface_test.go's amended doc comment for what makes this the one
// permitted exception to "the notifier constructs no HTTP server".
const metricsAddr = ":9102"

// metricsReadHeaderTimeout bounds how long a scraper may take to send its
// request headers, the same protection each HTTP service's own server
// carries.
const metricsReadHeaderTimeout = 10 * time.Second

// metricsShutdownTimeout bounds how long the metrics listener waits for an
// in-flight scrape to finish. Short, deliberately: unlike the delivery in
// flight that this process's own shutdown otherwise drains for (up to
// drainTimeout), a stalled scrape has nothing this process is responsible
// for finishing.
const metricsShutdownTimeout = 5 * time.Second

// newMetricsHandler mounts the exposition endpoint alone — no probes, no
// bearer group, no limiter, because this process authenticates no caller and
// consults no readiness dependency the way the three HTTP services do. A
// path other than /metrics answers the mux's own 404 rather than the
// exposition, so a stray probe here reads as "not found" instead of as a
// scrape.
func newMetricsHandler() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	return mux
}

// newMetricsServer builds the server main serves through, on the same
// pattern as each HTTP service's own newHTTPServer.
func newMetricsServer() *http.Server {
	return &http.Server{
		Addr:              metricsAddr,
		Handler:           newMetricsHandler(),
		ReadHeaderTimeout: metricsReadHeaderTimeout,
		ErrorLog:          slog.NewLogLogger(logger(componentMetricsServer).Handler(), slog.LevelError),
	}
}

// serveMetrics runs server over listener until ctx is cancelled, then shuts
// server down within metricsShutdownTimeout before returning. Its errors are
// logged, never fatal: this endpoint is diagnostic, and this process's job
// is to deliver notifications, not to serve them.
//
// listener is a parameter rather than built inside, so a test can bind an
// ephemeral port instead of contending for metricsAddr's fixed one — the
// production caller passes one bound to metricsAddr itself.
func serveMetrics(ctx context.Context, server *http.Server, listener net.Listener) {
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			logger(componentMetricsServer).Error("the metrics server stopped", slog.String("error", err.Error()))
		}
	}()

	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), metricsShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger(componentMetricsServer).Warn("shutting the metrics server down failed", slog.String("error", err.Error()))
	}
	<-done
}
