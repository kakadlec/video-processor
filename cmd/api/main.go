package main

import (
	"context"
	"embed"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	platformratelimit "video-processor/internal/platform/ratelimit"
)

//go:embed web
var webFS embed.FS

func main() {
	createDirs()

	ctx := context.Background()

	identity, _, err := setupIdentity(ctx)
	if err != nil {
		log.Fatal(err)
	}

	video, _, redisClient, err := setupVideo(ctx)
	if err != nil {
		log.Fatal(err)
	}

	rateLimitConfig, err := platformratelimit.LoadConfigFromEnv()
	if err != nil {
		log.Fatal(err)
	}
	limiter := platformratelimit.NewLimiter(redisClient, rateLimitConfig)

	r := setupRouter(identity, video, limiter)

	fmt.Println("🎬 Servidor iniciado na porta 8080")
	fmt.Println("📂 Acesse: http://localhost:8080")

	log.Fatal(r.Run(":8080"))
}

func serveEmbeddedFile(c *gin.Context, path, contentType string) {
	data, err := webFS.ReadFile(path)
	if err != nil {
		c.Status(404)
		return
	}
	c.Data(200, contentType, data)
}

func setupRouter(identity *identityModule, video *videoModule, limiter videoRateLimiter) *gin.Engine {
	r := gin.Default()

	r.Use(func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "POST, GET, OPTIONS")
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
	// results, and nothing at all for sources, which never outlive the
	// request that stored them.
	video.registerRoutes(videoRoutes)

	identity.registerRoutes(r)

	return r
}

// createDirs creates the one directory the application still writes to.
// Both uploads/ and outputs/ are gone: source videos and result artifacts
// are objects in the bucket, and temp/ holds only per-request scratch —
// the downloaded source, the extracted frames, and the zip built from them.
func createDirs() {
	dirs := []string{"temp"}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			log.Printf("Failed to create directory %s: %v", dir, err)
		}
	}
}

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
