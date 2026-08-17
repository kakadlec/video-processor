package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	videoapplication "video-processor/internal/video/application"
	videodomain "video-processor/internal/video/domain"
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
	createVideoJob *videoapplication.CreateVideoJob
	getJobStatus   *videoapplication.GetJobStatus
	listUserJobs   *videoapplication.ListUserJobs
}

func newVideoModule(createVideoJob *videoapplication.CreateVideoJob, getJobStatus *videoapplication.GetJobStatus, listUserJobs *videoapplication.ListUserJobs) *videoModule {
	return &videoModule{createVideoJob: createVideoJob, getJobStatus: getJobStatus, listUserJobs: listUserJobs}
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

	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, clock),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
	)
	return module, db, nil
}

func (m *videoModule) registerRoutes(videoRoutes *gin.RouterGroup) {
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
