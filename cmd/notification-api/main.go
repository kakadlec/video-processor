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

	r := setupRouter(auth, notification, limiter)

	// Signal-aware rather than log.Fatal(r.Run(...)): that exits through
	// os.Exit, which runs no deferred call, so an in-flight preference write
	// would be cut off mid-transaction rather than allowed to finish.
	signalCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	server := &http.Server{
		Addr:              ":8080",
		Handler:           r,
		ReadHeaderTimeout: readHeaderTimeout,
	}

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

func setupRouter(auth *authenticator, notification *notificationModule, limiter rateLimiter) *gin.Engine {
	r := gin.Default()

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
