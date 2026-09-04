package main

import (
	"context"
	"embed"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

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
	ctx := context.Background()

	identity, identityDB, err := setupIdentity(ctx)
	if err != nil {
		log.Fatal(err)
	}

	video, videoDB, redisClient, relay, err := setupVideo(ctx)
	if err != nil {
		log.Fatal(err)
	}

	notification, notificationDB, err := setupNotification(ctx)
	if err != nil {
		log.Fatal(err)
	}

	rateLimitConfig, err := platformratelimit.LoadConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	limiter := platformratelimit.NewLimiter(redisClient, rateLimitConfig)

	r := setupRouter(identity, video, notification, limiter)

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
			log.Printf("video: outbox relay: %v", err)
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

	fmt.Println("🎬 Servidor iniciado na porta 8080")
	fmt.Println("📂 Acesse: http://localhost:8080")

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
			log.Printf("http server: %v", err)
		}
	case <-signalCtx.Done():
		log.Print("shutdown signal received")
	}

	shutdownCtx, cancelShutdown := context.WithTimeout(ctx, shutdownTimeout)
	defer cancelShutdown()
	if err := server.Shutdown(shutdownCtx); err != nil {
		log.Printf("http server shutdown: %v", err)
	}

	// Ordering is load-bearing, not stylistic: the relay holds an open
	// database transaction while it runs, so closing the pool before joining
	// it would abort an in-flight claim instead of letting it resolve.
	// Joining first also means a claim that had already published is
	// committed rather than rolled back into a redelivery.
	stopRelay()
	relayDone.Wait()

	closeDB(videoDB)
	closeDB(identityDB)
	// Appended rather than inserted into the sequence above, which is
	// ordered against the relay. Nothing borrows this pool for a whole
	// operation — there is no notification relay and no goroutine holding
	// it — so it closes alongside the other two rather than among them.
	closeDB(notificationDB)
	if err := redisClient.Close(); err != nil {
		log.Printf("close redis: %v", err)
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

func setupRouter(identity *identityModule, video *videoModule, notification *notificationModule, limiter videoRateLimiter) *gin.Engine {
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
	videoRoutes.Use(identity.requireBearerAuth())
	videoRoutes.Use(rateLimitMiddleware(limiter))

	// No static mount remains. Source videos and result artifacts are both
	// objects in the bucket, reachable only through handlers that derive
	// entitlement from the VideoJob row — GET /download/:filename for
	// results, and nothing at all for sources, which no route exposes.
	video.registerRoutes(videoRoutes)

	// A separate group from videoRoutes, carrying the same middleware pair
	// in the same order. The pair is the invariant, not the group: these
	// routes serve no video-processing artifact, and sharing videoRoutes
	// would tie their authorization to a comment that describes something
	// else.
	notificationRoutes := r.Group("/")
	notificationRoutes.Use(identity.requireBearerAuth())
	notificationRoutes.Use(rateLimitMiddleware(limiter))
	notification.registerRoutes(notificationRoutes)

	identity.registerRoutes(r)

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
