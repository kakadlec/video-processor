package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	videoapplication "video-processor/internal/video/application"
	videodomain "video-processor/internal/video/domain"
	videoffmpeg "video-processor/internal/video/infrastructure/ffmpeg"
	videoidgen "video-processor/internal/video/infrastructure/idgen"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
)

// videoPostgresDSNEnv is the environment variable holding the Video
// Processing PostgreSQL connection string, alongside identity's own
// IDENTITY_POSTGRES_DSN.
const videoPostgresDSNEnv = "VIDEO_POSTGRES_DSN"

const (
	defaultVideoJobListOffset = 0
	defaultVideoJobListLimit  = 20
)

// videoModule wires the Video Processing bounded context's job-lifecycle
// use cases to the HTTP layer.
type videoModule struct {
	createVideoJob  *videoapplication.CreateVideoJob
	getJobStatus    *videoapplication.GetJobStatus
	listUserJobs    *videoapplication.ListUserJobs
	processVideoJob *videoapplication.ProcessVideoJob
}

func newVideoModule(createVideoJob *videoapplication.CreateVideoJob, getJobStatus *videoapplication.GetJobStatus, listUserJobs *videoapplication.ListUserJobs, processVideoJob *videoapplication.ProcessVideoJob) *videoModule {
	return &videoModule{createVideoJob: createVideoJob, getJobStatus: getJobStatus, listUserJobs: listUserJobs, processVideoJob: processVideoJob}
}

// setupVideo builds the production Video Processing module from environment
// configuration, mirroring setupIdentity's fail-clearly-on-misconfiguration
// posture. VIDEO_POSTGRES_DSN is always required.
func setupVideo(ctx context.Context) (*videoModule, *sql.DB, error) {
	pgConfig, err := videopostgres.LoadConfigFromEnv()
	if err != nil {
		return nil, nil, fmt.Errorf("video: %w", err)
	}

	db, err := videopostgres.Open(pgConfig)
	if err != nil {
		return nil, nil, err
	}
	if err := videopostgres.Migrate(ctx, db); err != nil {
		closeDB(db)
		return nil, nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		closeDB(db)
		return nil, nil, fmt.Errorf("video: connect to postgres: %w", err)
	}

	ids := videoidgen.New()
	repo := videopostgres.NewRepository(db, ids)
	clock := systemClock{}
	extractor := videoffmpeg.New()

	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, clock),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewProcessVideoJob(
			videoapplication.NewEnqueueVideoJob(repo, ids),
			videoapplication.NewStartProcessing(repo, ids),
			videoapplication.NewCompleteJob(repo, ids),
			videoapplication.NewFailJob(repo, ids),
			extractor,
			ids,
		),
	)
	return module, db, nil
}

func (m *videoModule) registerRoutes(videoRoutes *gin.RouterGroup) {
	videoRoutes.POST("/upload", m.handleVideoUpload)

	jobs := videoRoutes.Group("/api/video-jobs")
	jobs.POST("", m.handleCreateVideoJob)
	jobs.GET("/:id", m.handleGetVideoJobStatus)
	jobs.GET("", m.handleListVideoJobs)
}

type createVideoJobRequest struct {
	OriginalFilename string `json:"original_filename"`
}

type videoJobResponse struct {
	JobID            string    `json:"job_id"`
	OriginalFilename string    `json:"original_filename"`
	Status           string    `json:"status"`
	CreatedAt        time.Time `json:"created_at"`
}

type videoJobStatusResponse struct {
	JobID       string `json:"job_id"`
	Status      string `json:"status"`
	FrameCount  int    `json:"frame_count"`
	ErrorReason string `json:"error_reason,omitempty"`
	StorageKey  string `json:"storage_key,omitempty"`
}

type videoJobListItemResponse struct {
	JobID            string `json:"job_id"`
	OriginalFilename string `json:"original_filename"`
	Status           string `json:"status"`
}

type videoJobListResponse struct {
	Jobs []videoJobListItemResponse `json:"jobs"`
}

type videoErrorResponse struct {
	Error string `json:"error"`
}

// ProcessingResult is POST /upload's response body — unchanged since before
// this handler ran its processing through the VideoJob application layer.
type ProcessingResult struct {
	Success    bool     `json:"success"`
	Message    string   `json:"message"`
	ZipPath    string   `json:"zip_path,omitempty"`
	FrameCount int      `json:"frame_count,omitempty"`
	Images     []string `json:"images,omitempty"`
}

// handleVideoUpload accepts a multipart video upload, creates a VideoJob
// for it, and drives that job through ProcessVideoJob synchronously before
// responding — the same external contract POST /upload always had, now
// backed by the VideoJob application layer instead of an inline ffmpeg call.
func (m *videoModule) handleVideoUpload(c *gin.Context) {
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

	userID, ok := authenticatedUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, ProcessingResult{
			Success: false,
			Message: "Cabeçalho de autorização ausente ou inválido",
		})
		return
	}

	// uploadID names only the saved upload file, independent of the
	// VideoJob's own ID (minted afterward, once the file is safely on
	// disk) — so a save failure below never leaves a dangling VideoJob row.
	uploadID := uuid.NewString()
	safeFilename := filepath.Base(header.Filename)
	filename := fmt.Sprintf("%s_%s", uploadID, safeFilename)
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

	if _, err := io.Copy(out, file); err != nil {
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao salvar arquivo: " + err.Error(),
		})
		return
	}

	if err := recordArtifactOwner("uploads", filename, userID.String()); err != nil {
		log.Printf("Failed to record owner for upload %s: %v", filename, err)
	}

	created, err := m.createVideoJob.Execute(c.Request.Context(), videoapplication.CreateVideoJobInput{
		UserID:           userID.String(),
		OriginalFilename: safeFilename,
	})
	if err != nil {
		log.Printf("create video job for upload %s: %v", filename, err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao registrar o processamento",
		})
		return
	}

	processed, err := m.processVideoJob.Execute(c.Request.Context(), created.JobID, videoPath)
	if err != nil {
		log.Printf("process video job %s: %v", created.JobID, err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao processar vídeo",
		})
		return
	}

	if !processed.Success {
		c.JSON(200, ProcessingResult{
			Success: false,
			Message: "Erro no processamento: " + processed.FailureReason,
		})
		return
	}

	result := ProcessingResult{
		Success:    true,
		Message:    fmt.Sprintf("Processamento concluído! %d frames extraídos.", processed.FrameCount),
		ZipPath:    processed.StorageKey,
		FrameCount: processed.FrameCount,
		Images:     processed.ImageNames,
	}

	if err := os.Remove(videoPath); err != nil {
		log.Printf("Failed to remove original upload %s: %v", videoPath, err)
	}
	if err := removeArtifactOwner("uploads", filename); err != nil {
		log.Printf("Failed to remove owner record for upload %s: %v", filename, err)
	}
	// If ownership can't be recorded, the zip would otherwise be reported
	// as a success the owner can never actually retrieve (every
	// ownership-checked read path fails closed on a missing sidecar).
	// Treat it as a processing failure instead.
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

	c.JSON(200, result)
}

func (m *videoModule) handleCreateVideoJob(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, videoErrorResponse{Error: "missing or malformed authorization header"})
		return
	}

	var req createVideoJobRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, videoErrorResponse{Error: "invalid request body"})
		return
	}

	result, err := m.createVideoJob.Execute(c.Request.Context(), videoapplication.CreateVideoJobInput{
		UserID:           userID.String(),
		OriginalFilename: req.OriginalFilename,
	})
	if err != nil {
		switch {
		case errors.Is(err, videodomain.ErrInvalidOriginalFilename):
			c.JSON(http.StatusBadRequest, videoErrorResponse{Error: "invalid original filename"})
		default:
			log.Printf("create video job: %v", err)
			c.JSON(http.StatusInternalServerError, videoErrorResponse{Error: "internal server error"})
		}
		return
	}

	c.JSON(http.StatusCreated, videoJobResponse{
		JobID:            result.JobID,
		OriginalFilename: result.OriginalFilename,
		Status:           result.Status,
		CreatedAt:        result.CreatedAt,
	})
}

func (m *videoModule) handleGetVideoJobStatus(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, videoErrorResponse{Error: "missing or malformed authorization header"})
		return
	}

	result, err := m.getJobStatus.Execute(c.Request.Context(), videoapplication.GetJobStatusInput{
		RequestingUserID: userID.String(),
		JobID:            c.Param("id"),
	})
	if err != nil {
		switch {
		case errors.Is(err, videodomain.ErrInvalidVideoJobID):
			c.JSON(http.StatusBadRequest, videoErrorResponse{Error: "invalid job id"})
		case errors.Is(err, videodomain.ErrVideoJobNotFound):
			c.JSON(http.StatusNotFound, videoErrorResponse{Error: "job not found"})
		default:
			log.Printf("get video job status: %v", err)
			c.JSON(http.StatusInternalServerError, videoErrorResponse{Error: "internal server error"})
		}
		return
	}

	c.JSON(http.StatusOK, videoJobStatusResponse{
		JobID:       result.JobID,
		Status:      result.Status,
		FrameCount:  result.FrameCount,
		ErrorReason: result.ErrorReason,
		StorageKey:  result.StorageKey,
	})
}

func (m *videoModule) handleListVideoJobs(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		c.JSON(http.StatusUnauthorized, videoErrorResponse{Error: "missing or malformed authorization header"})
		return
	}

	offset, err := parseVideoJobQueryInt(c, "offset", defaultVideoJobListOffset)
	if err != nil {
		c.JSON(http.StatusBadRequest, videoErrorResponse{Error: "invalid offset"})
		return
	}
	limit, err := parseVideoJobQueryInt(c, "limit", defaultVideoJobListLimit)
	if err != nil {
		c.JSON(http.StatusBadRequest, videoErrorResponse{Error: "invalid limit"})
		return
	}

	items, err := m.listUserJobs.Execute(c.Request.Context(), videoapplication.ListUserJobsInput{
		UserID: userID.String(),
		Offset: offset,
		Limit:  limit,
	})
	if err != nil {
		switch {
		case errors.Is(err, videoapplication.ErrLimitOutOfRange), errors.Is(err, videoapplication.ErrOffsetNegative):
			c.JSON(http.StatusBadRequest, videoErrorResponse{Error: err.Error()})
		default:
			log.Printf("list video jobs: %v", err)
			c.JSON(http.StatusInternalServerError, videoErrorResponse{Error: "internal server error"})
		}
		return
	}

	jobs := make([]videoJobListItemResponse, len(items))
	for i, item := range items {
		jobs[i] = videoJobListItemResponse{
			JobID:            item.JobID,
			OriginalFilename: item.OriginalFilename,
			Status:           item.Status,
		}
	}
	c.JSON(http.StatusOK, videoJobListResponse{Jobs: jobs})
}

// parseVideoJobQueryInt returns fallback when the named query parameter is
// absent, or the parsed integer when present — an explicitly supplied
// non-integer value is a caller error, not silently coerced to fallback.
func parseVideoJobQueryInt(c *gin.Context, name string, fallback int) (int, error) {
	raw := c.Query(name)
	if raw == "" {
		return fallback, nil
	}
	return strconv.Atoi(raw)
}
