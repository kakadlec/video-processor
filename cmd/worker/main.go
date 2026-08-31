// Command worker consumes dispatched video jobs and runs the extraction
// pipeline for each one.
//
// It is the asynchronous half of the cutover: cmd/api stores the upload,
// queues the job, and answers 202, and this process does the work. It has no
// HTTP surface, no identity wiring, and no rate limiter, because it never
// acts on behalf of a caller — it acts on a job named by an internal
// dispatch. It must not be reachable from outside the deployment.
//
// It also runs no outbox relay. The relay is the API's, and a second one here
// would have two processes claiming from the same table for no gain.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/redis/go-redis/v9"

	platformrabbitmq "video-processor/internal/platform/rabbitmq"
	platformredis "video-processor/internal/platform/redis"
	videoapplication "video-processor/internal/video/application"
	videodomain "video-processor/internal/video/domain"
	videocache "video-processor/internal/video/infrastructure/cache"
	videoffmpeg "video-processor/internal/video/infrastructure/ffmpeg"
	videoidempotency "video-processor/internal/video/infrastructure/idempotency"
	videoidgen "video-processor/internal/video/infrastructure/idgen"
	videomessaging "video-processor/internal/video/infrastructure/messaging"
	videopostgres "video-processor/internal/video/infrastructure/postgres"
	videostorage "video-processor/internal/video/infrastructure/storage"
)

// drainTimeout bounds how long shutdown waits for the job in flight to reach
// a terminal state and be acknowledged.
//
// Generous, because abandoning a running job is strictly worse than waiting
// for it. Its row is already `processing`, and a redelivery cannot re-claim a
// `processing` row — ClaimForProcessing takes only `queued` — so a job
// dropped here is stranded until an operator or a later recovery mechanism
// intervenes. The deadline exists only so a wedged extraction cannot keep the
// process alive forever.
const drainTimeout = 5 * time.Minute

// Bounds on the retry of a terminal write, per the worker's own requirement
// that a completed extraction must not be reported as anything else. The
// artifact is already stored by the time this runs; all that is left is
// committing the row that points at it.
const (
	terminalWriteAttempts = 4
	terminalWriteBackoff  = 500 * time.Millisecond
)

// consumerTag names this process to the broker, for management listings.
const consumerTag = "video-worker"

func main() {
	ctx := context.Background()

	createDirs()

	deps, err := setupWorker(ctx)
	if err != nil {
		log.Fatal(err)
	}

	signalCtx, stopSignals := signal.NotifyContext(ctx, syscall.SIGINT, syscall.SIGTERM)
	defer stopSignals()

	run(signalCtx, deps, videomessaging.JobDispatchTopology(), drainTimeout)

	// Closed only after run has returned: the handler borrows both of these
	// for the whole of a job, and closing either underneath a running
	// extraction would fail the terminal write rather than the work.
	closeDB(deps.db)
	if err := deps.redis.Close(); err != nil {
		log.Printf("video: worker: close redis: %v", err)
	}
	// MinIO is absent for the same reason it is in cmd/api: that adapter
	// exposes no teardown.
}

// run consumes until ctx is cancelled, then waits up to drain for the job in
// flight to reach a terminal state and be acknowledged.
//
// Split out of main so the shutdown behaviour can be exercised: the topology
// and the drain deadline are parameters because a test needs its own queue
// names and cannot wait five minutes to observe an expiry.
func run(ctx context.Context, deps *workerDeps, topology platformrabbitmq.Topology, drain time.Duration) {
	// inFlight names the job currently being processed, so a drain that
	// times out can say which one it abandoned. Read from the shutdown path
	// while the handler goroutine writes it, hence atomic.
	var inFlight atomic.Pointer[string]

	consumer := videomessaging.NewConsumer(deps.rabbit, topology, consumerTag, func(handlerCtx context.Context, body []byte) videomessaging.Disposition {
		return deps.handle(handlerCtx, body, &inFlight)
	})

	consumerDone := make(chan struct{})
	go func() {
		defer close(consumerDone)
		if err := consumer.Run(ctx); err != nil {
			log.Printf("video: worker: consumer: %v", err)
		}
	}()

	<-ctx.Done()
	log.Print("video: worker: shutdown signal received")

	select {
	case <-consumerDone:
	case <-time.After(drain):
		if jobID := inFlight.Load(); jobID != nil {
			log.Printf("video: worker: drain deadline expired with job %s still running; it stays 'processing' and will not be redelivered", *jobID)
		} else {
			log.Print("video: worker: drain deadline expired")
		}
	}
}

// createDirs creates the scratch directory the pipeline downloads into and
// extracts through. cmd/api creates the same one; both processes need it,
// and neither can assume the other ran first.
func createDirs() {
	if err := os.MkdirAll("temp", 0750); err != nil {
		log.Printf("video: worker: create directory temp: %v", err)
	}
}

// workerDeps is the composition root's product: everything one delivery
// needs, already wired.
type workerDeps struct {
	rabbit platformrabbitmq.Config
	db     *sql.DB
	redis  *redis.Client

	process  *videoapplication.ProcessVideoJob
	complete *videoapplication.CompleteJob
	clearKey *videoapplication.ClearJobIdempotencyKey
	sources  videodomain.SourceStorage
}

// setupWorker builds the Video Processing dependencies this process needs.
//
// Fail-fast on everything reachable: PostgreSQL and MinIO are confirmed here,
// because a worker that cannot read a source or write a result has nothing to
// contribute and should not sit in the queue consuming jobs it will only
// fail. The broker is the exception, exactly as in cmd/api: RABBITMQ_URL must
// be set, but the dial belongs to the consumer's own retry loop.
func setupWorker(ctx context.Context) (*workerDeps, error) {
	rabbitConfig, err := platformrabbitmq.LoadConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("video: %w", err)
	}

	pgConfig, err := videopostgres.LoadConfigFromEnv()
	if err != nil {
		return nil, fmt.Errorf("video: %w", err)
	}
	db, err := videopostgres.Open(pgConfig)
	if err != nil {
		return nil, err
	}
	// The worker migrates too. Both processes run the same idempotent
	// schema, and either may be the first to start.
	if err := videopostgres.Migrate(ctx, db); err != nil {
		closeDB(db)
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		closeDB(db)
		return nil, fmt.Errorf("video: connect to postgres: %w", err)
	}

	redisConfig, err := platformredis.LoadConfigFromEnv()
	if err != nil {
		closeDB(db)
		return nil, fmt.Errorf("video: %w", err)
	}
	redisClient := platformredis.Open(redisConfig)

	minioConfig, err := videostorage.LoadConfigFromEnv()
	if err != nil {
		closeDB(db)
		return nil, err
	}
	minioClient, err := videostorage.Open(minioConfig)
	if err != nil {
		closeDB(db)
		return nil, err
	}
	if err := videostorage.Ping(ctx, minioClient, minioConfig.Bucket); err != nil {
		closeDB(db)
		return nil, err
	}
	if err := videostorage.EnsureBucket(ctx, minioClient, minioConfig.Bucket); err != nil {
		closeDB(db)
		return nil, err
	}
	// The worker never presigns — issuing download grants is the API's job.
	// The presigning client is built anyway so ResultStorage is fully
	// constructed rather than carrying a nil that would panic the day
	// someone calls the other half of its interface.
	region, err := videostorage.BucketRegion(ctx, minioClient, minioConfig.Bucket)
	if err != nil {
		closeDB(db)
		return nil, err
	}
	presignClient, err := videostorage.OpenPresigner(minioConfig, region)
	if err != nil {
		closeDB(db)
		return nil, err
	}
	sourceStorage := videostorage.NewSourceStorage(minioClient, minioConfig.Bucket)
	resultStorage := videostorage.NewResultStorage(minioClient, presignClient, minioConfig.Bucket)

	ids := videoidgen.New()
	// The same cache decorator the API wires, so a status read served from
	// Redis follows the worker's transitions rather than lagging them.
	repo := videocache.NewCachedVideoJobRepository(videopostgres.NewRepository(db, ids), redisClient, ids)
	idempotencyStore := videoidempotency.NewRedisStore(redisClient)

	return &workerDeps{
		rabbit: rabbitConfig,
		db:     db,
		redis:  redisClient,
		process: videoapplication.NewProcessVideoJob(
			videoapplication.NewStartProcessing(repo, ids),
			videoapplication.NewFailJob(repo, ids),
			videoffmpeg.New(),
			sourceStorage,
			resultStorage,
			ids,
		),
		complete: videoapplication.NewCompleteJob(repo, ids),
		clearKey: videoapplication.NewClearJobIdempotencyKey(repo, idempotencyStore, ids),
		sources:  sourceStorage,
	}, nil
}

// handle is the whole message-disposition table, in one place.
//
// Ack means one thing only: this job reached a committed terminal state.
// Every other outcome rejects without requeueing, so the message lands on the
// dead-letter queue where it can be looked at. Requeueing is never right —
// the job's row is past `queued` in most of these cases, so a redelivery
// could only lose the claim and loop.
//
// No path acks a message it did not process.
func (d *workerDeps) handle(ctx context.Context, body []byte, inFlight *atomic.Pointer[string]) videomessaging.Disposition {
	msg, err := videomessaging.ParseJobQueuedMessage(body)
	if err != nil {
		log.Printf("video: worker: undecodable dispatch, dead-lettering: %v", err)
		return videomessaging.Reject
	}

	sourceKey, err := videodomain.NewStorageKey(msg.SourceKey)
	if err != nil {
		log.Printf("video: worker: dispatch for job %s names no source, dead-lettering: %v", msg.JobID, err)
		return videomessaging.Reject
	}

	inFlight.Store(&msg.JobID)
	defer inFlight.Store(nil)

	result, err := d.process.Execute(ctx, msg.JobID, sourceKey)
	switch {
	case errors.Is(err, videodomain.ErrJobClaimLost):
		// Another consumer owns this job, or it is already terminal. Nothing
		// is touched — emphatically including the source object, which that
		// other consumer is very likely reading right now.
		log.Printf("video: worker: job %s was already claimed, dropping the duplicate dispatch", msg.JobID)
		return videomessaging.Reject
	case errors.Is(err, videodomain.ErrVideoJobNotFound):
		log.Printf("video: worker: dispatch names unknown job %s, dead-lettering", msg.JobID)
		return videomessaging.Reject
	case err != nil:
		// The run broke before any terminal state was committed. The job
		// stays wherever it was and the source object stays put: leaking it
		// is recoverable through the bucket's lifecycle rule, deleting an
		// input that no terminal state accounts for is not.
		log.Printf("video: worker: job %s did not reach a terminal state, dead-lettering: %v", msg.JobID, err)
		return videomessaging.Reject
	}

	// ProcessVideoJob committed the failure itself. FailJob is never called
	// from here: a second failure write on a job this process no longer owns
	// is exactly the overwrite the claim exists to prevent.
	if !result.Success {
		log.Printf("video: worker: job %s failed: %s", msg.JobID, result.FailureReason)
		d.deleteSource(ctx, msg.JobID, sourceKey)
		d.clearIdempotencyKey(ctx, msg.JobID)
		return videomessaging.Ack
	}

	if err := d.completeWithRetry(ctx, result); err != nil {
		// The artifact is stored and the row still says `processing`. Both
		// are deliberately left alone: the source stays so the job can be
		// re-run by hand, and the message is dead-lettered rather than
		// acked, so the dispatch is still on record. The result key is
		// logged because it is the only pointer to an artifact no listing
		// will show.
		log.Printf("video: worker: job %s produced result %s but could not be marked completed, dead-lettering and keeping its source: %v", msg.JobID, result.StorageKey, err)
		return videomessaging.Reject
	}

	// Only now: the terminal state is committed, so the input has served its
	// purpose. Gating on the commit rather than on having won the claim is
	// the point — a deferred delete registered at claim time would fire on
	// every failure path above too.
	d.deleteSource(ctx, msg.JobID, sourceKey)
	log.Printf("video: worker: job %s completed with %d frames", msg.JobID, result.FrameCount)
	return videomessaging.Ack
}

// completeWithRetry commits the completion, retrying a bounded number of
// times with backoff on a context detached from the delivery's own.
//
// Detached because the alternative is absurd: the extraction succeeded and
// the zip is in the bucket, and giving up on the one write that makes it
// reachable — because a shutdown began — would turn finished work into an
// orphan.
func (d *workerDeps) completeWithRetry(ctx context.Context, result videoapplication.ProcessVideoJobResult) error {
	input := videoapplication.CompleteJobInput{
		JobID:      result.JobID,
		StorageKey: result.StorageKey,
		FrameCount: result.FrameCount,
	}

	var err error
	for attempt := 1; attempt <= terminalWriteAttempts; attempt++ {
		writeCtx, cancel := videoapplication.NewFinalizationContext()
		_, err = d.complete.Execute(writeCtx, input)
		cancel()
		if err == nil {
			return nil
		}
		log.Printf("video: worker: mark job %s completed (attempt %d/%d): %v", result.JobID, attempt, terminalWriteAttempts, err)
		if attempt < terminalWriteAttempts {
			time.Sleep(time.Duration(attempt) * terminalWriteBackoff)
		}
	}
	return err
}

// deleteSource removes the job's input object now that its outcome is
// committed. Best effort and never fatal, matching the handler's own
// contract: a failure leaves an object the bucket's lifecycle rule reclaims,
// and the key is logged so the residue is enumerable.
func (d *workerDeps) deleteSource(ctx context.Context, jobID string, sourceKey videodomain.StorageKey) {
	cleanupCtx, cancel := videoapplication.NewFinalizationContext()
	defer cancel()
	if err := d.sources.Delete(cleanupCtx, sourceKey); err != nil {
		log.Printf("video: worker: delete source %s for job %s: %v", sourceKey.String(), jobID, err)
	}
}

// clearIdempotencyKey releases a failed job's content-hash mapping, so the
// same user resubmitting the same bytes is processed again instead of being
// handed the failure forever. Only failures clear: a success is exactly what
// the mapping is for.
func (d *workerDeps) clearIdempotencyKey(ctx context.Context, jobID string) {
	clearCtx, cancel := videoapplication.NewFinalizationContext()
	defer cancel()
	if _, err := d.clearKey.Execute(clearCtx, jobID); err != nil {
		log.Printf("video: worker: clear idempotency key for job %s: %v", jobID, err)
	}
}

func closeDB(db *sql.DB) {
	if err := db.Close(); err != nil {
		log.Printf("video: worker: close postgres: %v", err)
	}
}
