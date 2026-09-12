package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"

	"video-processor/internal/platform/logging"
	platformratelimit "video-processor/internal/platform/ratelimit"
	platformredis "video-processor/internal/platform/redis"
)

// shutdownTimeout bounds how long in-flight requests get to finish once a
// signal arrives. Both routes are a single database round trip, so it is
// generous rather than tight for the same reason a tighter one would buy
// nothing.
const shutdownTimeout = 30 * time.Second

// readHeaderTimeout bounds how long a client may take to send its request
// headers.
const readHeaderTimeout = 10 * time.Second

func main() {
	// The severity is read, and the process logger installed, before any
	// other configuration: every remaining startup failure is then reportable
	// as a structured record.
	level, err := logging.ParseLevel(os.Getenv(logging.LevelEnvVar))
	if err != nil {
		logging.NewBootstrap(logging.ServiceNotificationAPI).Error("the configured log severity is unrecognized",
			slog.String("error", err.Error()))
		os.Exit(1)
	}
	slog.SetDefault(logging.New(logging.ServiceNotificationAPI, level))

	ctx := context.Background()

	auth, err := setupAuthenticator()
	if err != nil {
		logger(componentProcessStartup).Error("the token verifier could not be built",
			slog.String("error", err.Error()))
		os.Exit(1)
	}

	notification, notificationDB, err := setupNotification(ctx)
	if err != nil {
		logger(componentProcessStartup).Error("the notification module could not be built",
			slog.String("error", err.Error()))
		os.Exit(1)
	}

	limiter, redisClient, err := setupRateLimiter()
	if err != nil {
		// Closed explicitly before the exit, not deferred: os.Exit runs no
		// deferred call, exactly as log.Fatal did not.
		closeDB(notificationDB)
		logger(componentProcessStartup).Error("the rate limiter could not be built",
			slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Release mode, set here rather than in setupRouter: gin's mode is
	// process-global, and gin.New() plus every route registration writes an
	// unstructured [GIN-debug] line to gin.DefaultWriter while it is debug.
	// main never runs in a test binary, so the two test helpers that select
	// gin.TestMode are unaffected by this call.
	gin.SetMode(gin.ReleaseMode)

	r := setupRouter(auth, notification, limiter, newReadinessChecker(notificationDB))

	// Signal-aware rather than log.Fatal(r.Run(...)): that exits through
	// os.Exit, which runs no deferred call, so an in-flight preference write
	// would be cut off mid-transaction rather than allowed to finish.
	signalCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	server := newHTTPServer(r)

	logger(componentHTTPServer).Info("the notification API is listening", slog.String("addr", server.Addr))

	serverFailed := make(chan error, 1)
	go func() {
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverFailed <- err
		}
		close(serverFailed)
	}()

	select {
	case err := <-serverFailed:
		if err != nil {
			logger(componentHTTPServer).Error("the HTTP server stopped", slog.String("error", err.Error()))
		}
	case <-signalCtx.Done():
		logger(componentProcessShutdown).Info("a shutdown signal was received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(ctx, shutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		logger(componentHTTPServer).Error("shutting the HTTP server down failed", slog.String("error", err.Error()))
	}

	// Shutdown, then close both connections, and there is no ordering
	// constraint hiding in that: unlike cmd/video-api's sequence, nothing here
	// borrows either for a whole operation. There is no relay and no
	// background goroutine holding a transaction, so once Shutdown has
	// returned, every statement this process will ever run has finished.
	closeDB(notificationDB)
	if err := redisClient.Close(); err != nil {
		logger(componentProcessShutdown).Warn("closing the Redis client failed", slog.String("error", err.Error()))
	}
}

// setupRateLimiter builds the per-user limiter the preference routes are
// gated by, and returns the Redis client behind it so main can close it.
//
// REDIS_ADDR is required here because the routes are rate limited, not
// because this service caches anything or holds any lease — it does neither.
// The limiter is a genuine dependency of the middleware, and the counter it
// increments is deliberately the same one every other HTTP service
// increments: the budget is one budget per user across the system, so a
// service-local store would silently multiply every user's allowance.
func setupRateLimiter() (*platformratelimit.Limiter, *redis.Client, error) {
	redisConfig, err := platformredis.LoadConfigFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("notification: %w", err)
	}
	// Open never itself connects (platformredis.Open's own contract), and
	// that is the posture this service wants: a limiter that cannot reach
	// Redis fails open per request rather than refusing to start.
	redisClient := platformredis.Open(redisConfig)

	rateLimitConfig, err := platformratelimit.LoadConfigFromEnv()
	if err != nil {
		if closeErr := redisClient.Close(); closeErr != nil {
			logger(componentProcessStartup).Warn("closing the Redis client failed", slog.String("error", closeErr.Error()))
		}
		return nil, nil, err
	}
	return platformratelimit.NewLimiter(redisClient, rateLimitConfig), redisClient, nil
}

// newHTTPServer builds the server main serves through. The construction is
// extracted for the same reason setupRouter is: a test that configures its own
// server proves nothing about a root that forgot a field.
func newHTTPServer(handler http.Handler) *http.Server {
	return &http.Server{
		Addr:              ":8080",
		Handler:           handler,
		ReadHeaderTimeout: readHeaderTimeout,
		// net/http reports its own errors — a panic it served, a response
		// header it could not parse — through this logger. Left nil they go
		// to the standard log package, which slog.SetDefault bridges into the
		// handler at info: a failure recorded as routine, and discarded
		// outright by a process running at error severity.
		ErrorLog: slog.NewLogLogger(logger(componentHTTPServer).Handler(), slog.LevelError),
	}
}

func setupRouter(auth *authenticator, notification *notificationModule, limiter rateLimiter, readiness *readinessChecker) *gin.Engine {
	r := gin.New()

	// The access log and the recovery handler this service writes itself,
	// replacing what gin.Default() mounted. Both are global rather than
	// grouped: the access record has to cover the requests that matched no
	// route, and a panic can be raised from anywhere in the chain. The
	// access log runs outermost, as gin.Default()'s did, so a recovered
	// panic still yields an access record carrying the status it answered.
	r.Use(accessLogMiddleware(), recoveryMiddleware())

	// Byte-identical to the other services' CORS middleware, deliberately.
	// A browser reaches all of them through one origin, so advertising a
	// different policy per path would make the split observable to a client
	// for the first time. PUT is advertised for the preference write, which
	// is the route it was added for.
	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}

		c.Next()
	})

	// The probes are mounted on the engine, outside the group below and so
	// outside both of its middlewares. r.Use is not retroactive, so the
	// registration order here is what makes that true. A probe carries no
	// bearer token, which the limiter would have nothing to key on; worse, a
	// probe issued at a fixed interval from inside the limiter would
	// eventually exhaust a budget and be answered 429, which a prober reads
	// as an outage the limiter itself manufactured.
	newProbeEndpoints(readiness).registerRoutes(r)

	// Bearer authentication, then the limiter, in that order. The pair and
	// its order are the invariant — the limiter keys on the authenticated
	// subject, so it has nothing to key on ahead of the middleware that
	// establishes one. cmd/api stated this across two groups serving two
	// contexts before it was split away; this service carries it alone.
	notificationRoutes := r.Group("/")
	notificationRoutes.Use(auth.requireBearerAuth())
	notificationRoutes.Use(rateLimitMiddleware(limiter))
	notification.registerRoutes(notificationRoutes)

	return r
}

// No directory is created at startup: nothing here is written to disk on any
// path. Both routes read and write PostgreSQL and nothing else.
