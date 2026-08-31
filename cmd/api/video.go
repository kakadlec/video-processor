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
	"path/filepath"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"

	platformrabbitmq "video-processor/internal/platform/rabbitmq"
	platformredis "video-processor/internal/platform/redis"
	videoapplication "video-processor/internal/video/application"
	videodomain "video-processor/internal/video/domain"
	videocache "video-processor/internal/video/infrastructure/cache"
	videoidempotency "video-processor/internal/video/infrastructure/idempotency"
	videoidgen "video-processor/internal/video/infrastructure/idgen"
	videomessaging "video-processor/internal/video/infrastructure/messaging"
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
	createVideoJob *videoapplication.CreateVideoJob
	getJobStatus   *videoapplication.GetJobStatus
	listUserJobs   *videoapplication.ListUserJobs
	// enqueueVideoJob is the last thing POST /upload does to a job. The
	// pending -> queued transition writes the outbox row the relay
	// publishes from, and once it commits the job belongs to cmd/worker:
	// this process runs no extraction, completes no job, and fails none.
	// ProcessVideoJob, CompleteJob, and FailJob are deliberately absent
	// from this module for that reason — reintroducing any of them here
	// would put a second writer on a row a worker has claimed.
	enqueueVideoJob *videoapplication.EnqueueVideoJob
	listUserResults *videoapplication.ListUserResults
	// idempotency backs POST /upload's content-hash duplicate detection —
	// see internal/video/domain.IdempotencyStore.
	idempotency videodomain.IdempotencyStore
	// jobs backs GET /download/:filename's entitlement lookup. It is
	// deliberately the authoritative repository, not the cached decorator —
	// see setupVideo for why a stale cache entry must not be able to 404 a
	// download the listing is already showing.
	jobs videodomain.VideoJobRepository
	// sources backs POST /upload's inbound stream and its own cleanup. The
	// handler owns the source object's lifetime end to end: it is the only
	// place that reaches every exit path, including the ones ProcessVideoJob
	// never runs on.
	sources videodomain.SourceStorage
	results videodomain.ResultStorage
	idsFor  videodomain.VideoJobIDParser
}

func newVideoModule(createVideoJob *videoapplication.CreateVideoJob, getJobStatus *videoapplication.GetJobStatus, listUserJobs *videoapplication.ListUserJobs, enqueueVideoJob *videoapplication.EnqueueVideoJob, listUserResults *videoapplication.ListUserResults, idempotency videodomain.IdempotencyStore, jobs videodomain.VideoJobRepository, sources videodomain.SourceStorage, results videodomain.ResultStorage, idsFor videodomain.VideoJobIDParser) *videoModule {
	return &videoModule{
		createVideoJob:  createVideoJob,
		getJobStatus:    getJobStatus,
		listUserJobs:    listUserJobs,
		enqueueVideoJob: enqueueVideoJob,
		listUserResults: listUserResults,
		idempotency:     idempotency,
		jobs:            jobs,
		sources:         sources,
		results:         results,
		idsFor:          idsFor,
	}
}

// setupVideo builds the production Video Processing module from environment
// configuration, mirroring setupIdentity's fail-clearly-on-misconfiguration
// posture. VIDEO_POSTGRES_DSN, REDIS_ADDR, and RABBITMQ_URL are always
// required. The opened *redis.Client is also returned so callers (main's
// rate-limiter wiring) can reuse the same connection instead of opening a
// second one, and so is the outbox relay, which main owns the lifetime of.
func setupVideo(ctx context.Context) (*videoModule, *sql.DB, *redis.Client, *videomessaging.Relay, error) {
	// Loaded first, and loaded only — this reads an environment variable and
	// touches no network. Every other subsystem below interleaves its config
	// load with opening and pinging the thing it configures, so leaving this
	// where the relay is actually built would make a missing RABBITMQ_URL
	// surface only after a database connection, a migration, and three
	// service round trips had already succeeded. A variable that is simply
	// absent should not cost that.
	//
	// Loaded, not dialed. An unset RABBITMQ_URL is misconfiguration and stops
	// startup like every other required variable, but broker reachability is
	// the relay's own concern: it is in no request path, it needs a redial
	// loop regardless because an AMQP connection can drop at any time, and
	// making the first dial fatal would couple this API's availability to the
	// broker's for a subsystem no request touches. See design.md decision 6 —
	// deliberately neither MinIO's fail-closed nor Redis's fail-open.
	rabbitConfig, err := platformrabbitmq.LoadConfigFromEnv()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("video: %w", err)
	}

	pgConfig, err := videopostgres.LoadConfigFromEnv()
	if err != nil {
		return nil, nil, nil, nil, fmt.Errorf("video: %w", err)
	}

	db, err := videopostgres.Open(pgConfig)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if err := videopostgres.Migrate(ctx, db); err != nil {
		closeDB(db)
		return nil, nil, nil, nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		closeDB(db)
		return nil, nil, nil, nil, fmt.Errorf("video: connect to postgres: %w", err)
	}

	redisConfig, err := platformredis.LoadConfigFromEnv()
	if err != nil {
		closeDB(db)
		return nil, nil, nil, nil, fmt.Errorf("video: %w", err)
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
		return nil, nil, nil, nil, err
	}
	minioClient, err := videostorage.Open(minioConfig)
	if err != nil {
		closeDB(db)
		return nil, nil, nil, nil, err
	}
	// Fail-closed, deliberately unlike the Redis wiring above: rate
	// limiting, idempotency, and the status cache all degrade to a slower
	// but correct system when Redis is down, whereas a bucket that cannot
	// be written leaves a completed job with nowhere to put its result.
	// There is nothing to degrade to, so reachability and the bucket are
	// confirmed here rather than discovered on the first upload.
	if err := videostorage.Ping(ctx, minioClient, minioConfig.Bucket); err != nil {
		closeDB(db)
		return nil, nil, nil, nil, err
	}
	if err := videostorage.EnsureBucket(ctx, minioClient, minioConfig.Bucket); err != nil {
		closeDB(db)
		return nil, nil, nil, nil, err
	}
	// The region is discovered here, on the reachable client, and handed to
	// the presigning client rather than configured: the server can simply
	// ask for a value an operator would otherwise have to keep correct, and
	// a wrong one stays invisible until the deployment moves off MinIO.
	// Fatal like Ping and EnsureBucket above — without it the presigning
	// client would try to discover the region itself, against a host it
	// generally cannot reach.
	region, err := videostorage.BucketRegion(ctx, minioClient, minioConfig.Bucket)
	if err != nil {
		closeDB(db)
		return nil, nil, nil, nil, err
	}
	presignClient, err := videostorage.OpenPresigner(minioConfig, region)
	if err != nil {
		closeDB(db)
		return nil, nil, nil, nil, err
	}
	resultStorage := videostorage.NewResultStorage(minioClient, presignClient, minioConfig.Bucket)
	sourceStorage := videostorage.NewSourceStorage(minioClient, minioConfig.Bucket)

	ids := videoidgen.New()
	// Every use case below shares the same cache-aside/write-through
	// decorator, per add-videojob-status-cache's design.md; Create is a pure
	// pass-through on it.
	//
	// authoritativeRepo is the undecorated PostgreSQL repository. It backs
	// GET /download/:filename's entitlement lookup so that endpoint and
	// GET /api/status read the same source: the listing queries PostgreSQL
	// directly (the cache is keyed per job ID and cannot serve a listing),
	// and if a completion's write-through SET and its fallback DEL both
	// fail — Redis down at exactly that moment — a stale "processing" entry
	// survives for the cache TTL. Reading through the cache here would then
	// 404 a download for a result the listing is already showing. A
	// PostgreSQL read is negligible next to streaming a zip, so nothing is
	// lost by skipping the cache on this path.
	authoritativeRepo := videopostgres.NewRepository(db, ids)
	repo := videocache.NewCachedVideoJobRepository(authoritativeRepo, redisClient, ids)
	relay := videomessaging.NewRelay(videopostgres.NewOutboxRepository(db), rabbitConfig)
	clock := systemClock{}
	// No extractor, no ProcessVideoJob, no CompleteJob, no FailJob. This
	// process hands work to cmd/worker and reads the outcome back; the
	// ffmpeg adapter is not wired here at all.
	module := newVideoModule(
		videoapplication.NewCreateVideoJob(repo, ids, clock),
		videoapplication.NewGetJobStatus(repo, ids),
		videoapplication.NewListUserJobs(repo),
		videoapplication.NewEnqueueVideoJob(repo, ids),
		videoapplication.NewListUserResults(repo, resultStorage),
		idempotencyStore,
		authoritativeRepo,
		sourceStorage,
		resultStorage,
		ids,
	)
	return module, db, redisClient, relay, nil
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
// uploadAcceptedResponse is what POST /upload returns once a job is queued,
// and equally what it returns for a duplicate submission naming the job that
// already exists. There is one shape and no outcome branch: the submission
// no longer knows an outcome to report, because nothing has been processed
// by the time it answers.
//
// StatusURL names GET /api/video-jobs/:id, which is the client's channel for
// everything that used to be in the synchronous response body.
type uploadAcceptedResponse struct {
	JobID     string `json:"job_id"`
	Status    string `json:"status"`
	StatusURL string `json:"status_url"`
}

// videoJobStatusPath builds the status URL an upload response hands back.
// One place, so the route and the pointer to it cannot drift apart.
func videoJobStatusPath(jobID string) string {
	return "/api/video-jobs/" + jobID
}

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

// downloadURLTTL bounds how long the storage service keeps admitting
// requests on an issued grant. A constant rather than configuration, like
// the status cache's TTL: the interval that actually has to be survived is
// the gap between this endpoint's JSON response and the browser's
// navigation, which is sub-second, because a URL is issued when the user
// clicks and not when the listing renders.
const downloadURLTTL = 5 * time.Minute

// downloadResponse is what GET /download/:filename returns now that it hands
// out a grant instead of the artifact. ExpiresAt is the instant the storage
// service stops admitting requests on URL, as read back off the grant itself.
type downloadResponse struct {
	URL       string `json:"url"`
	ExpiresAt string `json:"expires_at"`
}

// handleDownload authorizes a completed job's stored result for its owner
// and issues a time-bounded URL the caller redeems against object storage
// directly. It does not serve bytes; nothing in this process is on the
// transfer path any more.
//
// Entitlement is decided entirely from the VideoJob row: the requested key
// must parse to a job id, that job must exist, belong to the caller, be
// completed, and carry this exact key as its own StorageKey. That last
// check is what makes parsing the key safe — a forged key either names
// someone else's job (rejected by the owner check) or no job's recorded
// result (rejected here).
//
// Issuance is the complete authorization decision. The issued URL carries no
// identity, so nothing re-checks ownership when it is redeemed, and nothing
// can withdraw it before the storage service stops admitting it.
func (m *videoModule) handleDownload(c *gin.Context) {
	// Set before the first branch so it holds on every response this
	// handler can produce. On the 200 it keeps a private or user-agent
	// cache from retaining a working credential past the request that asked
	// for it; on the rejections it is what keeps them byte-identical to one
	// another down to their headers.
	c.Header("Cache-Control", "no-store")

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

	// Not a leftover from the streaming implementation. Signing is offline
	// and succeeds for a key holding no object, so without this a missing
	// object would surface as MinIO's own 404 — different origin, XML body —
	// instead of the rejection this endpoint promises to make
	// indistinguishable from every other. Both its not-found and its error
	// cases take the same path for that reason.
	if _, _, err := m.results.Stat(c.Request.Context(), key); err != nil {
		if !errors.Is(err, videodomain.ErrResultNotFound) {
			log.Printf("download: stat result %s: %v", key.String(), err)
		}
		respondArtifactNotFound(c)
		return
	}

	signedURL, expiresAt, err := m.results.PresignGet(c.Request.Context(), key, downloadURLTTL, key.String())
	if err != nil {
		// The key, never the URL: the URL is the credential this call just
		// minted, and the wrapped error names the endpoint and bucket.
		log.Printf("download: presign result %s: %v", key.String(), err)
		respondArtifactNotFound(c)
		return
	}

	c.JSON(http.StatusOK, downloadResponse{
		URL:       signedURL,
		ExpiresAt: expiresAt.UTC().Format(time.RFC3339),
	})
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

	// uploadID names only the stored source object, independent of the
	// VideoJob's own ID (minted afterward, once the object is safely in the
	// bucket) — so a storage failure below never leaves a dangling VideoJob
	// row.
	uploadID := uuid.NewString()
	safeFilename := filepath.Base(originalFilename)
	sourceKey := videodomain.SourceStorageKey(uploadID, safeFilename)

	// Hashing through the same single read pass that uploads the object
	// costs no extra I/O — see add-upload-idempotency-keys' design.md
	// Decision 1. Nothing is written to the local filesystem on the way in.
	hasher := sha256.New()
	if err := m.sources.Put(c.Request.Context(), sourceKey, io.TeeReader(file, hasher)); err != nil {
		// The adapter's error names the endpoint and bucket; log it, never
		// render it.
		log.Printf("store source %s: %v", sourceKey.String(), err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Failed to store the uploaded file",
		})
		return
	}
	// One deferred cleanup, registered the moment the object exists, rather
	// than a delete on each exit path below — so no early return that gets
	// added later can forget it. It covers the idempotency conflict, a
	// CreateVideoJob error, an EnqueueVideoJob error, and a panic.
	//
	// Guarded on the enqueue, and that guard is the whole cutover in one
	// line. Until the enqueue commits, these bytes belong to this request
	// and nothing else will ever look at them. After it commits they are the
	// worker's input, and deleting them here — on the way out of a request
	// that has already answered 202 — would destroy an extraction that has
	// very likely already started. The worker deletes them instead, once it
	// has committed a terminal state.
	//
	// Best effort, deliberately: one call, no retry. If storage is
	// unreachable at exactly this moment the object survives with nothing to
	// reclaim it, which is why the log line carries the key (it is what
	// makes the residual set enumerable) and why no spec here claims that no
	// source object survives. The operator-side backstop is an expiration
	// lifecycle rule on the uploads/ prefix, which is now the only guarantee
	// that a job enqueued but never dispatched has its source reclaimed.
	var enqueued bool
	defer func() {
		if enqueued {
			return
		}
		// A detached context: c.Request.Context() may already be canceled
		// (client disconnect), which can itself be why the request is
		// unwinding, and the cleanup must still run.
		cleanupCtx, cancel := videoapplication.NewFinalizationContext()
		defer cancel()
		if err := m.sources.Delete(cleanupCtx, sourceKey); err != nil {
			log.Printf("delete source %s: %v", sourceKey.String(), err)
		}
	}()
	contentHash := hex.EncodeToString(hasher.Sum(nil))

	idemKey, err := videodomain.NewIdempotencyKey(userID.String(), contentHash)
	if err != nil {
		// Unreachable in practice: userID.String() is already validated
		// non-empty above, and contentHash is always a 64-char SHA-256
		// digest. Handled defensively rather than assumed.
		log.Printf("build idempotency key for upload %s: %v", sourceKey.String(), err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Internal error building idempotency key",
		})
		return
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
		log.Printf("idempotency reserve for upload %s: %v", sourceKey.String(), err)
	case !reserved:
		if jobID, found := m.waitForFinalizedIdempotencyKey(c.Request.Context(), idemKey); found {
			status, err := m.getJobStatus.Execute(c.Request.Context(), videoapplication.GetJobStatusInput{
				RequestingUserID: userID.String(),
				JobID:            jobID.String(),
			})
			if err != nil {
				log.Printf("get job status for duplicate upload %s: %v", sourceKey.String(), err)
				c.JSON(500, ProcessingResult{
					Success: false,
					Message: "Failed to retrieve existing job status",
				})
				return
			}
			c.JSON(http.StatusAccepted, uploadAcceptedResponse{
				JobID:     status.JobID,
				Status:    status.Status,
				StatusURL: videoJobStatusPath(status.JobID),
			})
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
		SourceKey:        sourceKey.String(),
		// Persisted with the job so the worker can rebuild this request's
		// idempotency key from the job alone. Nothing else carries it: the
		// reservation token is not stored, and this request is gone by the
		// time a failure needs the key cleared.
		ContentHash: contentHash,
	})
	if err != nil {
		// A detached context: c.Request.Context() may already be canceled
		// (e.g. client disconnect, which could itself be why
		// CreateVideoJob failed), and this cleanup must still succeed so
		// an immediate retry isn't blocked for the sentinel's full TTL.
		if hasReservation {
			clearCtx, cancel := videoapplication.NewFinalizationContext()
			if cleared, clearErr := m.idempotency.Clear(clearCtx, idemKey, token); clearErr != nil || !cleared {
				log.Printf("clear idempotency reservation after CreateVideoJob error for upload %s: cleared=%v err=%v", sourceKey.String(), cleared, clearErr)
			}
			cancel()
		}
		log.Printf("create video job for upload %s: %v", sourceKey.String(), err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Erro ao registrar o processamento",
		})
		return
	}

	// The last thing this handler does to the job, and the point at which
	// ownership of the source object transfers. It commits the pending ->
	// queued transition together with the outbox row the relay publishes
	// from, so a dispatch exists for exactly the jobs that reached queued.
	// It runs before the idempotency key is finalized, so this failure
	// branch clears a plain reservation rather than a finalized entry.
	if _, err := m.enqueueVideoJob.Execute(c.Request.Context(), created.JobID); err != nil {
		// Handled like a CreateVideoJob failure: the job row exists but is
		// unqueued, and the source object's deferred cleanup above still
		// runs. A detached context for the same reason as there — the
		// request's may already be canceled, and leaving the reservation
		// behind would block an immediate retry for its full TTL.
		if hasReservation {
			clearCtx, cancel := videoapplication.NewFinalizationContext()
			if cleared, clearErr := m.idempotency.Clear(clearCtx, idemKey, token); clearErr != nil || !cleared {
				log.Printf("clear idempotency reservation after EnqueueVideoJob error for upload %s: cleared=%v err=%v", sourceKey.String(), cleared, clearErr)
			}
			cancel()
		}
		log.Printf("enqueue video job %s: %v", created.JobID, err)
		c.JSON(500, ProcessingResult{
			Success: false,
			Message: "Failed to queue the video for processing",
		})
		return
	}

	// Set the instant the enqueue commits, before anything below can fail:
	// past this point the source object is the worker's input, and the
	// deferred cleanup above must not touch it whatever else goes wrong.
	enqueued = true

	// Finalized after the enqueue, not before, per upload-idempotency's
	// "Reservation Is Finalized To The Real VideoJobID Only By Its Owning
	// Token": a finalized key advertises its job to every duplicate for
	// the full 24h window, so finalizing a job that then failed to reach
	// queued would deduplicate later uploads of the same bytes onto a job
	// stuck in pending that nothing will ever process.
	//
	// The order is not free, and the cost runs the other way: a worker
	// that fails this job clears the key by job ID, and until this line
	// runs the key is still a bare reservation, which ClearByJob leaves
	// alone by design. A clear landing in that window is a no-op this line
	// then overwrites, pinning the key to a failed job for its full
	// window. Both orderings trade one narrow 24h-block for another, which
	// is why the choice belongs to the spec and not to this function.
	//
	// A Finalize failure is non-fatal — the job just created is still
	// valid in PostgreSQL either way; see design.md Decision 7. Skipped
	// entirely when hasReservation is false: there is no reservation to
	// finalize.
	if hasReservation {
		if jobID, err := videodomain.NewVideoJobID(created.JobID); err != nil {
			log.Printf("invalid job id returned from CreateVideoJob for upload %s: %v", sourceKey.String(), err)
		} else if finalized, err := m.idempotency.Finalize(c.Request.Context(), idemKey, token, jobID); err != nil || !finalized {
			log.Printf("finalize idempotency key for job %s: finalized=%v err=%v", created.JobID, finalized, err)
		}
	}

	// 202, not 200: the work has been accepted, not done. No extraction has
	// run, no frame count exists, and no artifact is reachable yet — the
	// client learns all of that by polling StatusURL.
	c.JSON(http.StatusAccepted, uploadAcceptedResponse{
		JobID:     created.JobID,
		Status:    string(videodomain.JobStatusQueued),
		StatusURL: videoJobStatusPath(created.JobID),
	})
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
