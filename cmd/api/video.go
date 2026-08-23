package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	platformredis "video-processor/internal/platform/redis"
	videoapplication "video-processor/internal/video/application"
	videodomain "video-processor/internal/video/domain"
	videocache "video-processor/internal/video/infrastructure/cache"
	videoffmpeg "video-processor/internal/video/infrastructure/ffmpeg"
	videoidempotency "video-processor/internal/video/infrastructure/idempotency"
	videoidgen "video-processor/internal/video/infrastructure/idgen"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
	videostorage "video-processor/internal/video/infrastructure/storage"
)

// Bounds for the wait-and-retry loop handleVideoUpload runs against
// idempotency.Lookup after a failed Reserve — see add-upload-idempotency-keys'
// design.md Decision 2a. Named constants so the adapter test suite's own
// sentinel-TTL assertion (which this bound must stay comfortably under) has
// something concrete to compare against.
const (
	idempotencyLookupRetryInterval = 100 * time.Millisecond
	idempotencyLookupRetryBound    = 2 * time.Second
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
	listUserResults *videoapplication.ListUserResults
	// completeJob and failJob are called directly by handleVideoUpload,
	// not just composed inside processVideoJob: ProcessVideoJob leaves a
	// successfully-extracted job in "processing" so the handler owns the
	// job's terminal state. See ProcessVideoJob's doc comment for why that
	// split no longer has a failure branch behind it.
	completeJob *videoapplication.CompleteJob
	failJob     *videoapplication.FailJob
	// idempotency backs POST /upload's content-hash duplicate detection —
	// see internal/video/domain.IdempotencyStore.
	idempotency videodomain.IdempotencyStore
	// jobs and results back GET /download/:filename and GET /api/status,
	// which resolve a storage key to its owning VideoJob and then stream or
	// stat the stored object.
	jobs    videodomain.VideoJobRepository
	results videodomain.ResultStorage
	idsFor  videodomain.VideoJobIDParser
}

func newVideoModule(createVideoJob *videoapplication.CreateVideoJob, getJobStatus *videoapplication.GetJobStatus, listUserJobs *videoapplication.ListUserJobs, processVideoJob *videoapplication.ProcessVideoJob, listUserResults *videoapplication.ListUserResults, completeJob *videoapplication.CompleteJob, failJob *videoapplication.FailJob, idempotency videodomain.IdempotencyStore, jobs videodomain.VideoJobRepository, results videodomain.ResultStorage, idsFor videodomain.VideoJobIDParser) *videoModule {
	return &videoModule{
		createVideoJob:  createVideoJob,
		getJobStatus:    getJobStatus,
		listUserJobs:    listUserJobs,
		processVideoJob: processVideoJob,
		listUserResults: listUserResults,
		completeJob:     completeJob,
		failJob:         failJob,
		idempotency:     idempotency,
		jobs:            jobs,
		results:         results,
		idsFor:          idsFor,
	}
}

// setupVideo builds the production Video Processing module from environment
// configuration, mirroring setupIdentity's fail-clearly-on-misconfiguration
// posture. VIDEO_POSTGRES_DSN and REDIS_ADDR are always required — the
// latter as of add-upload-idempotency-keys, whose idempotency mechanism is
// this videoModule's first real Redis consumer. The opened *redis.Client is
// also returned so callers (main's rate-limiter wiring) can reuse the same
// connection instead of opening a second one.
func setupVideo(ctx context.Context) (*videoModule, *sql.DB, *redis.Client, error) {
	pgConfig, err := videopostgres.LoadConfigFromEnv()
	if err != nil {
		return nil, nil, nil, fmt.Errorf("video: %w", err)
	}

	db, err := videopostgres.Open(pgConfig)
	if err != nil {
		return nil, nil, nil, err
	}
	if err := videopostgres.Migrate(ctx, db); err != nil {
		closeDB(db)
		return nil, nil, nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		closeDB(db)
		return nil, nil, nil, fmt.Errorf("video: connect to postgres: %w", err)
	}

	redisConfig, err := platformredis.LoadConfigFromEnv()
	if err != nil {
		closeDB(db)
		return nil, nil, nil, fmt.Errorf("video: %w", err)
	}
	// Open never itself connects (platformredis.Open's own contract) — a
	// Ping/command failure surfaces at request time instead of here, per
	// add-upload-idempotency-keys' design.md.
	redisClient := platformredis.Open(redisConfig)
	idempotencyStore := videoidempotency.NewRedisStore(redisClient)

	// Returned unwrapped, like videostorage.Open's error below: this
	// package's errors already carry the "video:" prefix.
	minioConfig, err := videostorage.LoadConfigFromEnv()
	if err != nil {
		closeDB(db)
		return nil, nil, nil, err
	}
	minioClient, err := videostorage.Open(minioConfig)
	if err != nil {
		closeDB(db)
		return nil, nil, nil, err
	}
	// Fail-closed, deliberately unlike the Redis wiring above: rate
	// limiting, idempotency, and the status cache all degrade to a slower
	// but correct system when Redis is down, whereas a bucket that cannot
	// be written leaves a completed job with nowhere to put its result.
	// There is nothing to degrade to, so reachability and the bucket are
	// confirmed here rather than discovered on the first upload.
	if err := videostorage.Ping(ctx, minioClient, minioConfig.Bucket); err != nil {
		closeDB(db)
		return nil, nil, nil, err
	}
	if err := videostorage.EnsureBucket(ctx, minioClient, minioConfig.Bucket); err != nil {
		closeDB(db)
		return nil, nil, nil, err
	}
	resultStorage := videostorage.NewResultStorage(minioClient, minioConfig.Bucket)

	ids := videoidgen.New()
	// Wrapping once here means every use case below — including
	// CreateVideoJob — shares the same cache-aside/write-through
	// decorator, per add-videojob-status-cache's design.md: Create is a
	// pure pass-through on the decorator, so there's no reason to keep a
	// separate uncached repo reference around just for it.
	repo := videocache.NewCachedVideoJobRepository(videopostgres.NewRepository(db, ids), redisClient, ids)
	clock := systemClock{}
	extractor := videoffmpeg.New()
	completeJob := videoapplication.NewCompleteJob(repo, ids)
	failJob := videoapplication.NewFailJob(repo, ids)

	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, clock),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewProcessVideoJob(
			videoapplication.NewEnqueueVideoJob(repo, ids),
			videoapplication.NewStartProcessing(repo, ids),
			failJob,
			extractor,
			resultStorage,
			ids,
		),
		videoapplication.NewListUserResults(repo, resultStorage),
		completeJob,
		failJob,
		idempotencyStore,
		repo,
		resultStorage,
		ids,
	)
	return module, db, redisClient, nil
}

func (m *videoModule) registerRoutes(videoRoutes *gin.RouterGroup) {
	videoRoutes.POST("/upload", m.handleVideoUpload)
	videoRoutes.GET("/download/:filename", m.handleDownload)
	videoRoutes.GET("/api/status", m.handleStatus)

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

// videoFilePart returns the "video" part of a multipart request without
// buffering its body, so a caller can validate the part's filename (e.g.
// via isValidVideoFile) before reading any of its content — unlike
// (*http.Request).FormFile, which calls ParseMultipartForm internally and
// therefore reads the entire request body before returning. Any part
// encountered before a match (or a non-file "video" field, i.e. one with
// no filename) is drained and closed so the underlying reader can advance;
// a request with no matching part reports the same not-found error
// FormFile itself would have returned.
func videoFilePart(req *http.Request) (*multipart.Part, string, error) {
	mr, err := req.MultipartReader()
	if err != nil {
		return nil, "", err
	}
	for {
		part, err := mr.NextPart()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil, "", http.ErrMissingFile
			}
			return nil, "", err
		}
		if part.FormName() == "video" && part.FileName() != "" {
			return part, part.FileName(), nil
		}
		// Best-effort drain so the underlying reader can advance to the
		// next part; a failure here just means NextPart's own next call
		// will surface the same underlying error.
		_, _ = io.Copy(io.Discard, part)
		_ = part.Close()
	}
}

// handleVideoUpload accepts a multipart video upload, creates a VideoJob
// for it, and drives that job through ProcessVideoJob synchronously before
// responding — the same external contract POST /upload always had, now
// backed by the VideoJob application layer instead of an inline ffmpeg call.
// artifactNotFoundMessage is the single body every rejected result read
// returns. The not-found and not-entitled responses are deliberately
// indistinguishable: a caller must not be able to tell "no such artifact"
// apart from "someone else's artifact" by status, body, or headers, or the
// endpoint becomes a probe for other users' results.
const artifactNotFoundMessage = "File not found"

func respondArtifactNotFound(c *gin.Context) {
	c.JSON(http.StatusNotFound, gin.H{"error": artifactNotFoundMessage})
}

// handleDownload streams a completed job's stored result to its owner.
//
// Entitlement is decided entirely from the VideoJob row: the requested key
// must parse to a job id, that job must exist, belong to the caller, be
// completed, and carry this exact key as its own StorageKey. That last
// check is what makes parsing the key safe — a forged key either names
// someone else's job (rejected by the owner check) or no job's recorded
// result (rejected here).
func (m *videoModule) handleDownload(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		// requireBearerAuth gates this route, so reaching here means the
		// router was misconfigured rather than that the caller is anonymous.
		log.Print("download: no authenticated user on a bearer-gated route")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal error"})
		return
	}

	key, err := videodomain.NewStorageKey(c.Param("filename"))
	if err != nil {
		respondArtifactNotFound(c)
		return
	}

	jobID, err := videodomain.VideoJobIDFromStorageKey(key, m.idsFor)
	if err != nil {
		respondArtifactNotFound(c)
		return
	}

	job, err := m.jobs.FindByID(c.Request.Context(), jobID)
	if err != nil {
		if !errors.Is(err, videodomain.ErrVideoJobNotFound) {
			log.Printf("download: look up job %s: %v", jobID.String(), err)
		}
		respondArtifactNotFound(c)
		return
	}

	if job.UserID().String() != userID.String() ||
		job.Status() != videodomain.JobStatusCompleted ||
		!job.StorageKey().Equal(key) {
		respondArtifactNotFound(c)
		return
	}

	reader, size, err := m.results.Open(c.Request.Context(), key)
	if err != nil {
		// Logged, never rendered: a storage error must not leak the
		// endpoint, bucket name, or client error text into a response body.
		log.Printf("download: open result %s: %v", key.String(), err)
		respondArtifactNotFound(c)
		return
	}
	defer reader.Close()

	c.Header("Content-Description", "File Transfer")
	c.Header("Content-Transfer-Encoding", "binary")
	c.Header("Content-Disposition", "attachment; filename="+key.String())

	c.DataFromReader(http.StatusOK, size, "application/zip", reader, nil)
}

// handleStatus lists the caller's completed results.
func (m *videoModule) handleStatus(c *gin.Context) {
	userID, ok := authenticatedUserID(c)
	if !ok {
		log.Print("status: no authenticated user on a bearer-gated route")
		c.JSON(http.StatusInternalServerError, gin.H{"error": "Internal error"})
		return
	}

	items, err := m.listUserResults.Execute(c.Request.Context(), videoapplication.ListUserResultsInput{
		UserID: userID.String(),
	})
	if err != nil {
		log.Printf("status: list results for user: %v", err)
		c.JSON(500, gin.H{"error": "Erro ao listar arquivos"})
		return
	}

	// Left nil rather than pre-allocated when empty: an empty listing has
	// always serialized as "files": null here, and the frontend reads it
	// that way.
	var results []map[string]interface{}
	for _, item := range items {
		results = append(results, map[string]interface{}{
			"filename":     item.StorageKey,
			"size":         item.Size,
			"created_at":   item.ModifiedAt.Format("2006-01-02 15:04:05"),
			"download_url": "/download/" + item.StorageKey,
		})
	}

	c.JSON(200, gin.H{
		"files": results,
		"total": len(results),
	})
}

func (m *videoModule) handleVideoUpload(c *gin.Context) {
	// Streamed via MultipartReader rather than c.Request.FormFile: FormFile
	// calls net/http's own ParseMultipartForm under the hood (this bypasses
	// Gin's c.FormFile/c.MultipartForm wrapper entirely, so Gin's
	// MaxMultipartMemory is never consulted here), which reads the entire
	// request body up front — spilling anything past net/http's own 32MiB
	// defaultMaxMemory to a temp file — before the filename is even
	// available. A large upload with an invalid extension would pay the
	// full transfer cost before being rejected. Reading part-by-part lets
	// the filename gate the body read instead.
	file, originalFilename, err := videoFilePart(c.Request)
	if err != nil {
		c.JSON(400, ProcessingResult{
			Success: false,
			Message: "Erro ao receber arquivo: " + err.Error(),
		})
		return
	}

	if !isValidVideoFile(originalFilename) {
		// Deliberately not closing file here: (*multipart.Part).Close
		// drains the rest of the part's body via io.Copy(io.Discard, p)
		// so the underlying multipart stream can advance to the next
		// part — exactly the full-body read this fix exists to avoid.
		// Leaving it unclosed is safe: a Part holds no OS-level resource
		// of its own, only a view into the request body reader.
		c.JSON(400, ProcessingResult{
			Success: false,
			Message: "Formato de arquivo não suportado. Use: mp4, avi, mov, mkv",
		})
		return
	}
	defer file.Close()

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
	safeFilename := filepath.Base(originalFilename)
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

	// Hashing through the same io.Copy pass that saves the file costs no
	// extra I/O — see add-upload-idempotency-keys' design.md Decision 1.
	hasher := sha256.New()
	if _, err := io.Copy(out, io.TeeReader(file, hasher)); err != nil {
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao salvar arquivo: " + err.Error(),
		})
		return
	}
	contentHash := hex.EncodeToString(hasher.Sum(nil))

	idemKey, err := videodomain.NewIdempotencyKey(userID.String(), contentHash)
	if err != nil {
		// Unreachable in practice: userID.String() is already validated
		// non-empty above, and contentHash is always a 64-char SHA-256
		// digest. Handled defensively rather than assumed.
		log.Printf("build idempotency key for upload %s: %v", filename, err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Internal error building idempotency key",
		})
		return
	}

	if err := recordArtifactOwner("uploads", filename, userID.String()); err != nil {
		log.Printf("Failed to record owner for upload %s: %v", filename, err)
	}

	// hasReservation tracks whether this request actually holds a valid
	// reservation token — false both when Reserve erred (Redis down: we
	// fail open and proceed without dedup protection, see
	// fail-open-upload-idempotency's design.md) and, trivially, whenever
	// we don't reach CreateVideoJob at all. Every later Finalize/Clear
	// call is guarded on this rather than on token's zero value, so a
	// Reserve error never triggers a doomed Redis call against an empty
	// token downstream.
	token, reserved, err := m.idempotency.Reserve(c.Request.Context(), idemKey)
	var hasReservation bool
	switch {
	case err != nil:
		log.Printf("idempotency reserve for upload %s: %v", filename, err)
	case !reserved:
		cleanupRedundantUpload(videoPath, filename)
		if jobID, found := m.waitForFinalizedIdempotencyKey(c.Request.Context(), idemKey); found {
			status, err := m.getJobStatus.Execute(c.Request.Context(), videoapplication.GetJobStatusInput{
				RequestingUserID: userID.String(),
				JobID:            jobID.String(),
			})
			if err != nil {
				log.Printf("get job status for duplicate upload %s: %v", filename, err)
				c.JSON(500, ProcessingResult{
					Success: false,
					Message: "Failed to retrieve existing job status",
				})
				return
			}
			c.JSON(200, duplicateProcessingResult(status))
			return
		}
		// The reservation never resolved within the bound — a genuine
		// edge case (the original request is abnormally slow or crashed
		// before finalizing/clearing), not the common duplicate path.
		c.JSON(http.StatusConflict, ProcessingResult{
			Success: false,
			Message: "Identical content is already being processed for this user; try again shortly.",
		})
		return
	default:
		hasReservation = true
	}

	created, err := m.createVideoJob.Execute(c.Request.Context(), videoapplication.CreateVideoJobInput{
		UserID:           userID.String(),
		OriginalFilename: safeFilename,
	})
	if err != nil {
		// A detached context: c.Request.Context() may already be canceled
		// (e.g. client disconnect, which could itself be why
		// CreateVideoJob failed), and this cleanup must still succeed so
		// an immediate retry isn't blocked for the sentinel's full TTL.
		if hasReservation {
			clearCtx, cancel := videoapplication.NewFinalizationContext()
			if cleared, clearErr := m.idempotency.Clear(clearCtx, idemKey, token); clearErr != nil || !cleared {
				log.Printf("clear idempotency reservation after CreateVideoJob error for upload %s: cleared=%v err=%v", filename, cleared, clearErr)
			}
			cancel()
		}
		log.Printf("create video job for upload %s: %v", filename, err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao registrar o processamento",
		})
		return
	}

	// A Finalize failure is non-fatal — the job just created is still
	// valid in PostgreSQL either way; see design.md Decision 7. Skipped
	// entirely when hasReservation is false: there is no reservation to
	// finalize.
	if hasReservation {
		if jobID, err := videodomain.NewVideoJobID(created.JobID); err != nil {
			log.Printf("invalid job id returned from CreateVideoJob for upload %s: %v", filename, err)
		} else if finalized, err := m.idempotency.Finalize(c.Request.Context(), idemKey, token, jobID); err != nil || !finalized {
			log.Printf("finalize idempotency key for job %s: finalized=%v err=%v", created.JobID, finalized, err)
		}
	}

	processed, err := m.processVideoJob.Execute(c.Request.Context(), created.JobID, videoPath)
	if err != nil {
		// The job's state here isn't confirmed failed (ProcessVideoJob's
		// own FailJob call may itself have errored) — leave the
		// idempotency key for its own TTL rather than clear it for an
		// uncertain job state.
		log.Printf("process video job %s: %v", created.JobID, err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao processar vídeo",
		})
		return
	}

	if !processed.Success {
		// ProcessVideoJob only returns Success:false once FailJob has
		// already succeeded internally (processing -> failed) — safe to
		// clear, so a retry with the same content isn't blocked. A
		// detached context, like above: the extraction error itself may
		// be a symptom of c.Request.Context() already being canceled
		// (ProcessVideoJob uses its own detached context for the same
		// reason when it calls FailJob internally), and this cleanup must
		// still succeed for the retry to actually work.
		if hasReservation {
			clearCtx, cancel := videoapplication.NewFinalizationContext()
			if cleared, clearErr := m.idempotency.Clear(clearCtx, idemKey, token); clearErr != nil || !cleared {
				log.Printf("clear idempotency key for failed job %s: cleared=%v err=%v", created.JobID, cleared, clearErr)
			}
			cancel()
		}
		c.JSON(200, ProcessingResult{
			Success: false,
			Message: extractionFailureMessage(processed.ExtractionError, processed.FailureReason),
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
	// The result is already durable by this point: ProcessVideoJob stores
	// the zip in the bucket as part of its own sequence, so a successful
	// return means there is nothing left for this handler to make reachable
	// before completing the job.
	//
	// A detached context: c.Request.Context() may already be canceled (e.g.
	// client disconnect), and this write must still succeed so the job
	// doesn't stay stuck in "processing".
	finalizeCtx, cancel := videoapplication.NewFinalizationContext()
	defer cancel()
	if _, err := m.completeJob.Execute(finalizeCtx, videoapplication.CompleteJobInput{
		JobID:      created.JobID,
		StorageKey: processed.StorageKey,
		FrameCount: processed.FrameCount,
	}); err != nil {
		log.Printf("complete video job %s: %v", created.JobID, err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao finalizar o processamento",
		})
		return
	}

	c.JSON(200, result)
}

// extractionFailureMessage maps ProcessVideoJob's classifiable
// ExtractionError to the same distinct pt-BR messages POST /upload always
// returned, instead of exposing the underlying (English, infrastructure)
// error text directly in the response body.
func extractionFailureMessage(extractionErr error, reason string) string {
	switch {
	case errors.Is(extractionErr, videoffmpeg.ErrNoFramesExtracted):
		return "Nenhum frame foi extraído do vídeo"
	case errors.Is(extractionErr, videoffmpeg.ErrFfmpegExecFailed):
		return "Erro no ffmpeg: " + reason
	case errors.Is(extractionErr, videoffmpeg.ErrZipCreationFailed):
		return "Erro ao criar arquivo ZIP: " + reason
	default:
		return "Erro no processamento: " + reason
	}
}

// cleanupRedundantUpload removes an upload that turned out to be a
// duplicate — this request's own saved file and owner sidecar, never the
// original request's artifacts (each request writes under its own
// uploadID-prefixed path).
func cleanupRedundantUpload(videoPath, filename string) {
	if err := os.Remove(videoPath); err != nil {
		log.Printf("Failed to remove redundant upload %s: %v", videoPath, err)
	}
	if err := removeArtifactOwner("uploads", filename); err != nil {
		log.Printf("Failed to remove owner record for redundant upload %s: %v", filename, err)
	}
}

// waitForFinalizedIdempotencyKey polls idempotency.Lookup for up to
// idempotencyLookupRetryBound after a failed Reserve, so a near-simultaneous
// duplicate resolves to the original request's job instead of an immediate
// 409 — see design.md Decision 2a.
func (m *videoModule) waitForFinalizedIdempotencyKey(ctx context.Context, key videodomain.IdempotencyKey) (videodomain.VideoJobID, bool) {
	deadline := time.Now().Add(idempotencyLookupRetryBound)
	for {
		jobID, found, err := m.idempotency.Lookup(ctx, key)
		if err != nil {
			log.Printf("idempotency lookup while waiting for reservation to resolve: %v", err)
		} else if found {
			return jobID, true
		}
		if time.Now().After(deadline) {
			return videodomain.VideoJobID{}, false
		}
		select {
		case <-ctx.Done():
			return videodomain.VideoJobID{}, false
		case <-time.After(idempotencyLookupRetryInterval):
		}
	}
}

// duplicateProcessingResult translates an existing job's status into
// ProcessingResult — the same response shape a non-duplicate POST /upload
// returns, never GetJobStatusResult's own field names (see design.md
// Decision 8). Per-frame Images names aren't persisted anywhere this status
// lookup can reach, so they're omitted; the frontend never reads that field
// regardless.
func duplicateProcessingResult(status videoapplication.GetJobStatusResult) ProcessingResult {
	switch status.Status {
	case string(videodomain.JobStatusCompleted):
		return ProcessingResult{
			Success:    true,
			Message:    fmt.Sprintf("Processing already completed for this content (%d frames extracted).", status.FrameCount),
			ZipPath:    status.StorageKey,
			FrameCount: status.FrameCount,
		}
	case string(videodomain.JobStatusFailed):
		return ProcessingResult{
			Success: false,
			Message: "Processing already failed for this content: " + status.ErrorReason,
		}
	default:
		return ProcessingResult{
			Success: false,
			Message: "This content is already being processed; try again shortly.",
		}
	}
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
