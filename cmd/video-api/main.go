package main

import (
	"context"
	"embed"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"video-processor/internal/platform/logging"
	platformratelimit "video-processor/internal/platform/ratelimit"
)

// shutdownTimeout bounds how long in-flight requests get to finish once a
// signal arrives. It stays generous even though no request runs ffmpeg any
// more: POST /upload still streams a whole video into object storage before
// it answers, and a shorter deadline would cut off a large upload that was
// nearly stored.
const shutdownTimeout = 30 * time.Second

// readHeaderTimeout bounds how long a client may take to send its request
// headers.
const readHeaderTimeout = 10 * time.Second

//go:embed web
var webFS embed.FS

func main() {
	// The severity is read, and the process logger installed, before any
	// other configuration: every remaining startup failure is then reportable
	// as a structured record.
	level, err := logging.ParseLevel(os.Getenv(logging.LevelEnvVar))
	if err != nil {
		logging.NewBootstrap(logging.ServiceVideoAPI).Error("the configured log severity is unrecognized",
			slog.String("error", err.Error()))
		os.Exit(1)
	}
	slog.SetDefault(logging.New(logging.ServiceVideoAPI, level))

	ctx := context.Background()

	auth, err := setupAuthenticator()
	if err != nil {
		logger(componentProcessStartup).Error("the token verifier could not be built",
			slog.String("error", err.Error()))
		os.Exit(1)
	}

	video, videoDB, redisClient, relay, err := setupVideo(ctx)
	if err != nil {
		logger(componentProcessStartup).Error("the video module could not be built",
			slog.String("error", err.Error()))
		os.Exit(1)
	}

	rateLimitConfig, err := platformratelimit.LoadConfigFromEnv()
	if err != nil {
		logger(componentProcessStartup).Error("the rate limiter could not be built",
			slog.String("error", err.Error()))
		os.Exit(1)
	}
	limiter := platformratelimit.NewLimiter(redisClient, rateLimitConfig)

	r := setupRouter(auth, video, limiter)

	// Signal-aware rather than log.Fatal(r.Run(...)): that exits through
	// os.Exit, which runs no deferred call and waits for nothing, so the
	// relay's stop requirement — resolve the in-flight claim, then release
	// the connection — cannot be met by cancelling a context alone.
	signalCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	relayCtx, stopRelay := context.WithCancel(ctx)
	var relayDone sync.WaitGroup
	relayDone.Add(1)
	go func() {
		defer relayDone.Done()
		if err := relay.Run(relayCtx); err != nil {
			logger(componentOutboxRelay).Error("the outbox relay returned an error",
				slog.String("error", err.Error()))
		}
	}()

	server := &http.Server{
		Addr:    ":8080",
		Handler: r,
		// Only the header read is bounded. A ReadTimeout or WriteTimeout
		// would cut off POST /upload, which streams a whole video into the
		// bucket before responding; headers arrive immediately regardless of
		// body size, so bounding that alone costs nothing and closes the
		// slow-header hold.
		ReadHeaderTimeout: readHeaderTimeout,
	}

	logger(componentHTTPServer).Info("the video API is listening", slog.String("addr", server.Addr))
	logger(componentHTTPServer).Info("the frontend is served at the root path", slog.String("path", "/"))

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

	// Ordering is load-bearing, not stylistic: the relay holds an open
	// database transaction while it runs, so closing the pool before joining
	// it would abort an in-flight claim instead of letting it resolve.
	// Joining first also means a claim that had already published is
	// committed rather than rolled back into a redelivery.
	stopRelay()
	relayDone.Wait()

	closeDB(videoDB)
	if err := redisClient.Close(); err != nil {
		logger(componentProcessShutdown).Warn("closing the Redis client failed", slog.String("error", err.Error()))
	}
	// MinIO is absent on purpose: that adapter exposes no teardown, because
	// *minio.Client has none — a wrapper could only report success while
	// releasing nothing.
}

func serveEmbeddedFile(c *gin.Context, path, contentType string) {
	data, err := webFS.ReadFile(path)
	if err != nil {
		c.Status(404)
		return
	}
	c.Data(200, contentType, data)
}

func setupRouter(auth *authenticator, video *videoModule, limiter rateLimiter) *gin.Engine {
	r := gin.Default()

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

	r.GET("/", func(c *gin.Context) {
		serveEmbeddedFile(c, "web/index.html", "text/html; charset=utf-8")
	})
	r.GET("/styles.css", func(c *gin.Context) {
		serveEmbeddedFile(c, "web/styles.css", "text/css; charset=utf-8")
	})
	r.GET("/app.js", func(c *gin.Context) {
		serveEmbeddedFile(c, "web/app.js", "application/javascript; charset=utf-8")
	})

	// videoRoutes holds every route that serves or accepts video-processing
	// artifacts. All of them require a valid bearer token and are subject to
	// per-user rate limiting.
	videoRoutes := r.Group("/")
	videoRoutes.Use(auth.requireBearerAuth())
	videoRoutes.Use(rateLimitMiddleware(limiter))

	// No static mount remains. Source videos and result artifacts are both
	// objects in the bucket, reachable only through handlers that derive
	// entitlement from the VideoJob row — GET /download/:filename for
	// results, and nothing at all for sources, which no route exposes.
	video.registerRoutes(videoRoutes)

	return r
}

// No directory is created at startup any more. uploads/ and outputs/ were
// already gone — source videos and results are objects in the bucket — and
// temp/ went with the extraction: this process downloads nothing, runs no
// ffmpeg, and writes no zip. cmd/worker creates the scratch directory it
// needs, where the work actually happens.

func isValidVideoFile(filename string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	validExts := []string{".mp4", ".avi", ".mov", ".mkv", ".wmv", ".flv", ".webm"}

	for _, validExt := range validExts {
		if ext == validExt {
			return true
		}
	}
	return false
}
