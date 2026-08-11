package main

import (
	"archive/zip"
	"context"
	"embed"
	"fmt"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
)

//go:embed web
var webFS embed.FS

type VideoRequest struct {
	VideoPath string `json:"video_path"`
	OutputDir string `json:"output_dir"`
}

type ProcessingResult struct {
	Success    bool     `json:"success"`
	Message    string   `json:"message"`
	ZipPath    string   `json:"zip_path,omitempty"`
	FrameCount int      `json:"frame_count,omitempty"`
	Images     []string `json:"images,omitempty"`
}

func main() {
	createDirs()

	identity, _, err := setupIdentity(context.Background())
	if err != nil {
		log.Fatal(err)
	}

	r := setupRouterWithIdentity(identity)

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

func setupRouterWithIdentity(identity *identityModule) *gin.Engine {
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
	// artifacts. All of them require a valid bearer token.
	videoRoutes := r.Group("/")
	videoRoutes.Use(identity.requireBearerAuth())

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

	videoRoutes.POST("/upload", handleVideoUpload)
	videoRoutes.GET("/download/:filename", handleDownload)
	videoRoutes.GET("/api/status", handleStatus)

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

func handleVideoUpload(c *gin.Context) {
	file, header, err := c.Request.FormFile("video")
	if err != nil {
		c.JSON(400, ProcessingResult{
			Success: false,
			Message: "Erro ao receber arquivo: " + err.Error(),
		})
		return
	}
	defer file.Close()

	if !isValidVideoFile(header.Filename) {
		c.JSON(400, ProcessingResult{
			Success: false,
			Message: "Formato de arquivo não suportado. Use: mp4, avi, mov, mkv",
		})
		return
	}

	// requestID must be collision-resistant, not just distinct-looking: it
	// names the temp dir, the output zip, and (when authenticated) the
	// ownership sidecar for both. A colliding ID from two concurrent
	// requests would let one user's upload overwrite another's artifact and
	// its ownership record — a UUID keeps that probability negligible,
	// unlike the second-precision timestamp this used to be.
	requestID := uuid.NewString()
	safeFilename := filepath.Base(header.Filename)
	filename := fmt.Sprintf("%s_%s", requestID, safeFilename)
	videoPath := filepath.Clean(filepath.Join("uploads", filename))
	if !strings.HasPrefix(videoPath, "uploads"+string(os.PathSeparator)) {
		c.JSON(400, ProcessingResult{
			Success: false,
			Message: "Nome de arquivo inválido",
		})
		return
	}

	out, err := os.Create(videoPath)
	if err != nil {
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao salvar arquivo: " + err.Error(),
		})
		return
	}
	defer out.Close()

	_, err = io.Copy(out, file)
	if err != nil {
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao salvar arquivo: " + err.Error(),
		})
		return
	}

	userID, authenticated := authenticatedUserID(c)
	if authenticated {
		if err := recordArtifactOwner("uploads", filename, userID.String()); err != nil {
			log.Printf("Failed to record owner for upload %s: %v", filename, err)
		}
	}

	result := processVideo(videoPath, requestID)

	if result.Success {
		if err := os.Remove(videoPath); err != nil {
			log.Printf("Failed to remove original upload %s: %v", videoPath, err)
		}
		if authenticated {
			if err := removeArtifactOwner("uploads", filename); err != nil {
				log.Printf("Failed to remove owner record for upload %s: %v", filename, err)
			}
			// If ownership can't be recorded, the zip would otherwise be
			// reported as a success the owner can never actually retrieve
			// (every ownership-checked read path fails closed on a missing
			// sidecar). Treat it as a processing failure instead.
			if err := recordArtifactOwner("outputs", result.ZipPath, userID.String()); err != nil {
				log.Printf("Failed to record owner for output %s: %v", result.ZipPath, err)
				if removeErr := os.Remove(filepath.Join("outputs", result.ZipPath)); removeErr != nil {
					log.Printf("Failed to remove orphaned output %s: %v", result.ZipPath, removeErr)
				}
				c.JSON(500, ProcessingResult{
					Success: false,
					Message: "Failed to record artifact ownership",
				})
				return
			}
		}
	}

	c.JSON(200, result)
}

func processVideo(videoPath, requestID string) ProcessingResult {
	fmt.Printf("Iniciando processamento: %s\n", videoPath)

	tempDir := filepath.Join("temp", requestID)
	if err := os.MkdirAll(tempDir, 0750); err != nil {
		log.Printf("Failed to create temp directory %s: %v", tempDir, err)
	}
	defer os.RemoveAll(tempDir)

	framePattern := filepath.Join(tempDir, "frame_%04d.png")

	cmd := exec.Command("ffmpeg", // #nosec G204
		"-i", videoPath,
		"-vf", "fps=1",
		"-y",
		framePattern,
	)

	output, err := cmd.CombinedOutput()
	if err != nil {
		return ProcessingResult{
			Success: false,
			Message: fmt.Sprintf("Erro no ffmpeg: %s\nOutput: %s", err.Error(), string(output)),
		}
	}

	frames, err := filepath.Glob(filepath.Join(tempDir, "*.png"))
	if err != nil || len(frames) == 0 {
		return ProcessingResult{
			Success: false,
			Message: "Nenhum frame foi extraído do vídeo",
		}
	}

	fmt.Printf("📸 Extraídos %d frames\n", len(frames))

	zipFilename := fmt.Sprintf("frames_%s.zip", requestID)
	zipPath := filepath.Join("outputs", zipFilename)

	err = createZipFile(frames, zipPath)
	if err != nil {
		return ProcessingResult{
			Success: false,
			Message: "Erro ao criar arquivo ZIP: " + err.Error(),
		}
	}

	fmt.Printf("✅ ZIP criado: %s\n", zipPath)

	imageNames := make([]string, len(frames))
	for i, frame := range frames {
		imageNames[i] = filepath.Base(frame)
	}

	return ProcessingResult{
		Success:    true,
		Message:    fmt.Sprintf("Processamento concluído! %d frames extraídos.", len(frames)),
		ZipPath:    zipFilename,
		FrameCount: len(frames),
		Images:     imageNames,
	}
}

func createZipFile(files []string, zipPath string) error {
	zipPath = filepath.Clean(zipPath)
	if !strings.HasPrefix(zipPath, "outputs"+string(os.PathSeparator)) {
		return fmt.Errorf("invalid zip path: %s", zipPath)
	}

	zipFile, err := os.Create(zipPath)
	if err != nil {
		return err
	}
	defer zipFile.Close()

	zipWriter := zip.NewWriter(zipFile)
	defer zipWriter.Close()

	for _, file := range files {
		err := addFileToZip(zipWriter, file)
		if err != nil {
			return err
		}
	}

	return nil
}

func addFileToZip(zipWriter *zip.Writer, filename string) error {
	filename = filepath.Clean(filename)
	if !strings.HasPrefix(filename, "temp"+string(os.PathSeparator)) {
		return fmt.Errorf("invalid frame path: %s", filename)
	}

	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return err
	}

	header, err := zip.FileInfoHeader(info)
	if err != nil {
		return err
	}

	header.Name = filepath.Base(filename)
	header.Method = zip.Deflate

	writer, err := zipWriter.CreateHeader(header)
	if err != nil {
		return err
	}

	_, err = io.Copy(writer, file)
	return err
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
