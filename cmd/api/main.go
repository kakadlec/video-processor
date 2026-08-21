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

	// The /uploads and /outputs static mounts serve the exact same artifacts
	// as handleDownload by filename, so they need the same per-owner check —
	// otherwise they'd be a direct bypass of handleDownload's ownership
	// enforcement below. rejectOwnerSidecarRequests runs unconditionally: the
	// sidecar files themselves must never be servable, including any left
	// over from before this route was protected.
	uploadsRoutes := videoRoutes.Group("/uploads")
	uploadsRoutes.Use(rejectOwnerSidecarRequests())
	uploadsRoutes.Use(requireArtifactOwnership("uploads"))
	uploadsRoutes.Static("/", "./uploads")

	outputsRoutes := videoRoutes.Group("/outputs")
	outputsRoutes.Use(rejectOwnerSidecarRequests())
	outputsRoutes.Use(requireArtifactOwnership("outputs"))
	outputsRoutes.Static("/", "./outputs")

	videoRoutes.GET("/download/:filename", handleDownload)
	videoRoutes.GET("/api/status", handleStatus)

	video.registerRoutes(videoRoutes)

	identity.registerRoutes(r)

	return r
}

// artifactOwnerSuffix names the sidecar file that records which
// authenticated user owns a video-processing artifact (an upload or an
// output zip), stored alongside it under the same directory. This is
// explicit, checked ownership metadata — not an inference from the
// artifact's filename or timestamp.
const artifactOwnerSuffix = ".owner"

// recordArtifactOwner persists that userID owns the artifact at
// filepath.Join(dir, artifactFilename). dir is confined to like videoPath
// and zipPath are confined elsewhere in this file.
func recordArtifactOwner(dir, artifactFilename, userID string) error {
	path := filepath.Clean(filepath.Join(dir, artifactFilename+artifactOwnerSuffix))
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return fmt.Errorf("invalid artifact filename: %s", artifactFilename)
	}
	return os.WriteFile(path, []byte(userID), 0600)
}

// artifactOwner returns the userID recorded by recordArtifactOwner for the
// artifact at filepath.Join(dir, artifactFilename), if any.
func artifactOwner(dir, artifactFilename string) (string, bool) {
	path := filepath.Clean(filepath.Join(dir, artifactFilename+artifactOwnerSuffix))
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return "", false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", false
	}
	return string(data), true
}

// removeArtifactOwner deletes the ownership sidecar for the artifact at
// filepath.Join(dir, artifactFilename), confined the same way as above.
func removeArtifactOwner(dir, artifactFilename string) error {
	path := filepath.Clean(filepath.Join(dir, artifactFilename+artifactOwnerSuffix))
	if !strings.HasPrefix(path, dir+string(os.PathSeparator)) {
		return fmt.Errorf("invalid artifact filename: %s", artifactFilename)
	}
	return os.Remove(path)
}

// rejectOwnerSidecarRequests blocks direct HTTP access to ownership sidecar
// files under a static file group. It's registered unconditionally,
// regardless of whether identity is configured, since a sidecar written
// during an earlier, identity-enabled run of the server must stay
// unreadable even after identity is turned off.
func rejectOwnerSidecarRequests() gin.HandlerFunc {
	return func(c *gin.Context) {
		filename := filepath.Base(c.Param("filepath"))
		if strings.HasSuffix(filename, artifactOwnerSuffix) {
			c.AbortWithStatusJSON(404, gin.H{"error": "File not found"})
			return
		}
		c.Next()
	}
}

// requireArtifactOwnership gates a static file group (uploads/ or outputs/)
// so an authenticated user can only fetch artifacts recorded as their own.
// It's only registered behind requireBearerAuth, so an authenticated UserID
// is always present in context by the time it runs.
func requireArtifactOwnership(dir string) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := authenticatedUserID(c)
		if !ok {
			c.AbortWithStatusJSON(404, gin.H{"error": "File not found"})
			return
		}

		filename := filepath.Base(c.Param("filepath"))
		owner, hasOwner := artifactOwner(dir, filename)
		if !hasOwner || owner != userID.String() {
			c.AbortWithStatusJSON(404, gin.H{"error": "File not found"})
			return
		}

		c.Next()
	}
}

func createDirs() {
	dirs := []string{"uploads", "outputs", "temp"}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0750); err != nil {
			log.Printf("Failed to create directory %s: %v", dir, err)
		}
	}
}

func handleDownload(c *gin.Context) {
	filename := c.Param("filename")
	filePath := filepath.Join("outputs", filename)

	// The not-found and not-owned responses below are deliberately
	// identical: a non-owner must not be able to tell "doesn't exist" apart
	// from "exists but isn't yours" by response content.
	if _, err := os.Stat(filePath); os.IsNotExist(err) {
		c.JSON(404, gin.H{"error": "File not found"})
		return
	}

	if userID, authenticated := authenticatedUserID(c); authenticated {
		owner, hasOwner := artifactOwner("outputs", filename)
		if !hasOwner || owner != userID.String() {
			c.JSON(404, gin.H{"error": "File not found"})
			return
		}
	}

	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Transfer-Encoding", "binary")
	c.Header("Content-Disposition", "attachment; filename="+filename)
	c.Header("Content-Type", "application/zip")

	c.File(filePath)
}

func handleStatus(c *gin.Context) {
	files, err := filepath.Glob(filepath.Join("outputs", "*.zip"))
	if err != nil {
		c.JSON(500, gin.H{"error": "Erro ao listar arquivos"})
		return
	}

	userID, authenticated := authenticatedUserID(c)

	var results []map[string]interface{}
	for _, file := range files {
		info, err := os.Stat(file)
		if err != nil {
			continue
		}

		if authenticated {
			owner, hasOwner := artifactOwner("outputs", filepath.Base(file))
			if !hasOwner || owner != userID.String() {
				continue
			}
		}

		results = append(results, map[string]interface{}{
			"filename":     filepath.Base(file),
			"size":         info.Size(),
			"created_at":   info.ModTime().Format("2006-01-02 15:04:05"),
			"download_url": "/download/" + filepath.Base(file),
		})
	}

	c.JSON(200, gin.H{
		"files": results,
		"total": len(results),
	})
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
