package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/platform/logging"
)

// shutdownTimeout bounds how long in-flight requests get to finish once a
// signal arrives. This service's two routes are a bcrypt hash and a database
// round trip, so it is generous rather than tight for the same reason a
// tighter one would buy nothing.
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
		logging.NewBootstrap(logging.ServiceIdentityAPI).Error("the configured log severity is unrecognized",
			slog.String("error", err.Error()))
		os.Exit(1)
	}
	slog.SetDefault(logging.New(logging.ServiceIdentityAPI, level))

	ctx := context.Background()

	identity, identityDB, err := setupIdentity(ctx)
	if err != nil {
		logger(componentProcessStartup).Error("the identity module could not be built",
			slog.String("error", err.Error()))
		os.Exit(1)
	}

	// Release mode, set here rather than in setupRouter: gin's mode is
	// process-global, and gin.New() plus every route registration writes an
	// unstructured [GIN-debug] line to gin.DefaultWriter while it is debug.
	// main never runs in a test binary, so the two test helpers that select
	// gin.TestMode are unaffected by this call.
	gin.SetMode(gin.ReleaseMode)

	r := setupRouter(identity)

	// Signal-aware rather than log.Fatal(r.Run(...)): that exits through
	// os.Exit, which runs no deferred call, so an in-flight registration
	// would be cut off mid-transaction rather than allowed to finish.
	signalCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	server := &http.Server{
		Addr:              ":8080",
		Handler:           r,
		ReadHeaderTimeout: readHeaderTimeout,
	}

	logger(componentHTTPServer).Info("the identity API is listening", slog.String("addr", server.Addr))

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

	// Shutdown then close, and there is no ordering constraint hiding in
	// that: unlike cmd/video-api's sequence, nothing here borrows the pool for a
	// whole operation. There is no relay and no background goroutine holding
	// a transaction, so once Shutdown has returned, every statement this
	// process will ever run has finished.
	closeDB(identityDB)
}

func setupRouter(identity *identityModule) *gin.Engine {
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
	// for the first time. PUT is advertised here for a route this service
	// does not serve, and that is the point.
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

	// No bearer-auth group and no rate limiter, matching what the single API
	// did for these two routes: they are how a caller obtains a token, so
	// requiring one would be circular.
	identity.registerRoutes(r)

	return r
}
