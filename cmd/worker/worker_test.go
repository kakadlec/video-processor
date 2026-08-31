package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/minio/minio-go/v7"
	amqp "github.com/rabbitmq/amqp091-go"
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

// TestMain runs every test in this package from the repository root, which
// is where the binary itself resolves temp/ — the pipeline's scratch
// directory is a relative path, so without this the downloaded sources and
// extracted frames would land under cmd/worker/ instead, inside the tree.
//
// Unlike cmd/api's TestMain it gates on nothing. Every test here skips
// cleanly when the infrastructure it needs is absent, matching the adapter
// packages; task 10.1 is what confirms they reported PASS rather than SKIP.
func TestMain(m *testing.M) {
	if err := os.Chdir("../.."); err != nil {
		fmt.Fprintf(os.Stderr, "FATAL: failed to chdir to repo root: %v\n", err)
		os.Exit(1)
	}
	os.Exit(m.Run())
}

// testContentHash is a syntactically real SHA-256 (of the empty string), so
// the idempotency keys derived from it look like the ones production writes.
const testContentHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// workerTestDatabase is this package's own database, created beside the one
// VIDEO_POSTGRES_TEST_DSN names, for the same reason the relay's tests have
// theirs: `go test ./...` runs packages in parallel, and
// internal/video/infrastructure/postgres truncates video_jobs and
// video_job_outbox before each of its tests. Sharing would let the two
// packages wipe each other's rows mid-run.
const workerTestDatabase = "video_worker_test"

func testBrokerURL(t *testing.T) string {
	t.Helper()
	broker := os.Getenv("RABBITMQ_TEST_URL")
	if broker == "" {
		t.Skip("RABBITMQ_TEST_URL not set; skipping worker broker integration test")
	}
	return broker
}

func openTestConn(t *testing.T) *amqp.Connection {
	t.Helper()
	conn, err := platformrabbitmq.Open(platformrabbitmq.Config{URL: testBrokerURL(t)})
	if err != nil {
		t.Fatalf("open test broker: %v", err)
	}
	t.Cleanup(func() { _ = platformrabbitmq.Close(conn) })
	return conn
}

// testTopology names broker entities scoped to one test and deletes them
// afterwards. The production names are never declared here: this package
// deletes queues to force a reconnect, which is not something a test may do
// to a queue a running worker holds.
func testTopology(t *testing.T, conn *amqp.Connection) platformrabbitmq.Topology {
	t.Helper()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate topology suffix: %v", err)
	}
	prefix := "test." + strings.NewReplacer("/", "-", " ", "_").Replace(t.Name()) + "." + hex.EncodeToString(suffix)

	topo := platformrabbitmq.Topology{
		Exchange:       prefix + ".exchange",
		RoutingKey:     videomessaging.RoutingKeyJobQueued,
		WorkQueue:      prefix + ".work",
		DeadExchange:   prefix + ".dlx",
		DeadQueue:      prefix + ".dead",
		WorkMaxLength:  10,
		DeadMessageTTL: time.Minute,
		DeadMaxLength:  10,
	}
	t.Cleanup(func() {
		ch, err := conn.Channel()
		if err != nil {
			return
		}
		defer func() { _ = ch.Close() }()
		for _, q := range []string{topo.WorkQueue, topo.DeadQueue} {
			_, _ = ch.QueueDelete(q, false, false, false)
		}
		for _, x := range []string{topo.Exchange, topo.DeadExchange} {
			_ = ch.ExchangeDelete(x, false, false)
		}
	})
	return topo
}

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("VIDEO_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("VIDEO_POSTGRES_TEST_DSN not set; skipping worker integration test")
	}

	admin, err := videopostgres.Open(videopostgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	ctx := context.Background()
	// The name is a compile-time constant, not caller input, and CREATE
	// DATABASE takes no parameters. Already-exists is the normal case.
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+workerTestDatabase); err != nil && !strings.Contains(err.Error(), "already exists") {
		_ = admin.Close()
		t.Fatalf("create %s: %v", workerTestDatabase, err)
	}
	_ = admin.Close()

	db, err := videopostgres.Open(videopostgres.Config{DSN: withDatabase(t, dsn, workerTestDatabase)})
	if err != nil {
		t.Fatalf("open %s: %v", workerTestDatabase, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := videopostgres.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	// Not truncated: every test here seeds its own jobs under freshly
	// minted identifiers, and a truncate would race the goroutine a
	// drain-deadline test deliberately leaves parked in an extraction.
	return db
}

// withDatabase rewrites dsn's database name, keeping host, credentials, and
// parameters.
func withDatabase(t *testing.T, dsn, database string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse VIDEO_POSTGRES_TEST_DSN: %v", err)
	}
	parsed.Path = "/" + database
	return parsed.String()
}

func testRedis(t *testing.T) *redis.Client {
	t.Helper()
	addr := os.Getenv("REDIS_TEST_ADDR")
	if addr == "" {
		t.Skip("REDIS_TEST_ADDR not set; skipping worker integration test")
	}
	client := platformredis.Open(platformredis.Config{Addr: addr})
	t.Cleanup(func() { _ = client.Close() })
	return client
}

// testStorageConfig points at the MinIO the sibling storage adapter's tests
// use, with a bucket unique to this test: sources and results share one
// bucket in production, and a shared one here would accumulate objects in a
// named volume across suite runs.
func testStorageConfig(t *testing.T) videostorage.Config {
	t.Helper()
	endpoint := os.Getenv("VIDEO_MINIO_TEST_ENDPOINT")
	accessKey := os.Getenv("VIDEO_MINIO_TEST_ACCESS_KEY")
	secretKey := os.Getenv("VIDEO_MINIO_TEST_SECRET_KEY")
	if endpoint == "" || accessKey == "" || secretKey == "" {
		t.Skip("VIDEO_MINIO_TEST_ENDPOINT/ACCESS_KEY/SECRET_KEY not all set; skipping worker integration test")
	}

	name := strings.ToLower(t.Name())
	name = strings.NewReplacer("/", "-", "_", "-", " ", "-", ".", "-").Replace(name)
	bucket := fmt.Sprintf("worker-%s-%d", name, time.Now().UnixNano()%1e6)
	if len(bucket) > 63 {
		bucket = bucket[:63]
	}

	return videostorage.Config{
		Endpoint:       endpoint,
		AccessKey:      accessKey,
		SecretKey:      secretKey,
		Bucket:         strings.Trim(bucket, "-"),
		PublicEndpoint: endpoint,
	}
}

func requireFFmpeg(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("ffmpeg not found in PATH; skipping worker pipeline test")
	}
}

// gatedExtractor wraps the real ffmpeg extractor with a gate, so a test can
// hold a job open in the middle of its extraction — the only moment at which
// shutdown behaviour is observable at all.
//
// A test that does not need the gate leaves release nil and gets the real
// extractor's timing unchanged.
type gatedExtractor struct {
	inner   videodomain.FrameExtractor
	started chan string
	release chan struct{}
}

func newGatedExtractor(release chan struct{}) *gatedExtractor {
	return &gatedExtractor{
		inner:   videoffmpeg.New(),
		started: make(chan string, 8),
		release: release,
	}
}

func (g *gatedExtractor) ExtractFrames(ctx context.Context, jobID videodomain.VideoJobID, videoPath string) (string, int, []string, error) {
	select {
	case g.started <- jobID.String():
	default:
	}
	if g.release != nil {
		<-g.release
	}
	return g.inner.ExtractFrames(ctx, jobID, videoPath)
}

// workerTestEnv is one test's worth of real infrastructure, wired exactly as
// setupWorker wires the process: the same cache decorator, the same use
// cases, the same adapters — only the names are test-scoped.
type workerTestEnv struct {
	deps      *workerDeps
	db        *sql.DB
	ids       videoidgen.Adapter
	repo      *videopostgres.Repository
	keys      *videoidempotency.RedisStore
	sources   videodomain.SourceStorage
	minio     *minio.Client
	bucket    string
	extractor *gatedExtractor
}

// envOptions carries the two seams a test may need: a gate on the extractor,
// and a decorator around the repository. Both exist so a path that is
// otherwise unreachable — a job held mid-extraction, a terminal write that
// fails — can be produced without widening run() or handle().
type envOptions struct {
	release  chan struct{}
	decorate func(videodomain.VideoJobRepository) videodomain.VideoJobRepository
}

func newWorkerTestEnv(t *testing.T, opts envOptions) *workerTestEnv {
	t.Helper()
	requireFFmpeg(t)
	db := testDB(t)
	redisClient := testRedis(t)

	cfg := testStorageConfig(t)
	client, err := videostorage.Open(cfg)
	if err != nil {
		t.Fatalf("open MinIO client: %v", err)
	}
	ctx := context.Background()
	if err := videostorage.EnsureBucket(ctx, client, cfg.Bucket); err != nil {
		t.Fatalf("ensure bucket %q: %v", cfg.Bucket, err)
	}
	t.Cleanup(func() { removeTestBucket(t, client, cfg.Bucket) })

	region, err := videostorage.BucketRegion(ctx, client, cfg.Bucket)
	if err != nil {
		t.Fatalf("bucket region %q: %v", cfg.Bucket, err)
	}
	presigner, err := videostorage.OpenPresigner(cfg, region)
	if err != nil {
		t.Fatalf("open presigning client: %v", err)
	}
	sources := videostorage.NewSourceStorage(client, cfg.Bucket)
	results := videostorage.NewResultStorage(client, presigner, cfg.Bucket)

	ids := videoidgen.New()
	plain := videopostgres.NewRepository(db, ids)
	var repo videodomain.VideoJobRepository = videocache.NewCachedVideoJobRepository(plain, redisClient, ids)
	if opts.decorate != nil {
		repo = opts.decorate(repo)
	}
	keys := videoidempotency.NewRedisStore(redisClient)
	extractor := newGatedExtractor(opts.release)

	deps := &workerDeps{
		// Empty when RABBITMQ_TEST_URL is unset, which is fine: the tests
		// that dial call testBrokerURL themselves and skip without it.
		rabbit: platformrabbitmq.Config{URL: os.Getenv("RABBITMQ_TEST_URL")},
		db:     db,
		redis:  redisClient,
		process: videoapplication.NewProcessVideoJob(
			videoapplication.NewStartProcessing(repo, ids),
			videoapplication.NewFailJob(repo, ids),
			extractor,
			sources,
			results,
			ids,
		),
		complete: videoapplication.NewCompleteJob(repo, ids),
		clearKey: videoapplication.NewClearJobIdempotencyKey(plain, keys, ids),
		sources:  sources,
	}

	if err := createDirs(); err != nil {
		t.Fatalf("createDirs: %v", err)
	}

	return &workerTestEnv{
		deps:      deps,
		db:        db,
		ids:       ids,
		repo:      plain,
		keys:      keys,
		sources:   sources,
		minio:     client,
		bucket:    cfg.Bucket,
		extractor: extractor,
	}
}

// removeTestBucket drains and deletes bucket. Failures are logged, never
// fatal: cleanup must not turn a passing test red, and it runs after a
// failed one too.
func removeTestBucket(t *testing.T, client *minio.Client, bucket string) {
	t.Helper()
	ctx := context.Background()
	for object := range client.ListObjects(ctx, bucket, minio.ListObjectsOptions{Recursive: true}) {
		if object.Err != nil {
			t.Logf("cleanup: list %s: %v", bucket, object.Err)
			continue
		}
		if err := client.RemoveObject(ctx, bucket, object.Key, minio.RemoveObjectOptions{}); err != nil {
			t.Logf("cleanup: remove %s/%s: %v", bucket, object.Key, err)
		}
	}
	if err := client.RemoveBucket(ctx, bucket); err != nil {
		t.Logf("cleanup: remove bucket %s: %v", bucket, err)
	}
}

// seedQueuedJob puts a job in exactly the state a dispatch describes: its
// source object stored, its row queued, and its outbox event written — all
// through the same repository POST /upload uses, so the payload the test
// publishes is the payload production would have published.
//
// It returns the job and the dispatch body read back out of the outbox.
func seedQueuedJob(t *testing.T, env *workerTestEnv, sourceVideo []byte) (*videodomain.VideoJob, []byte) {
	t.Helper()
	ctx := context.Background()

	userID, err := videodomain.NewUserID(uuid.NewString())
	if err != nil {
		t.Fatalf("build user id: %v", err)
	}
	filename, err := videodomain.NewOriginalFilename("input.mp4")
	if err != nil {
		t.Fatalf("build filename: %v", err)
	}
	sourceKey := videodomain.SourceStorageKey(uuid.NewString(), "input.mp4")
	if err := env.sources.Put(ctx, sourceKey, bytes.NewReader(sourceVideo)); err != nil {
		t.Fatalf("store source object: %v", err)
	}

	job, err := videodomain.NewVideoJob(env.ids, userID, filename, sourceKey, testContentHash, time.Now().UTC())
	if err != nil {
		t.Fatalf("build video job: %v", err)
	}
	if err := env.repo.Create(ctx, job); err != nil {
		t.Fatalf("create video job: %v", err)
	}
	if err := job.Enqueue(); err != nil {
		t.Fatalf("enqueue video job: %v", err)
	}
	if err := env.repo.Enqueue(ctx, job); err != nil {
		t.Fatalf("persist queued video job: %v", err)
	}

	return job, dispatchPayload(t, env.db, job.ID().String())
}

// dispatchPayload reads the outbox row Enqueue wrote, so tests publish the
// bytes the relay would have published rather than a hand-built lookalike.
func dispatchPayload(t *testing.T, db *sql.DB, jobID string) []byte {
	t.Helper()
	var payload []byte
	if err := db.QueryRowContext(context.Background(),
		`SELECT payload FROM video_job_outbox WHERE event_type = $1 AND payload->>'job_id' = $2`,
		videopostgres.VideoJobQueuedEventType, jobID,
	).Scan(&payload); err != nil {
		t.Fatalf("read dispatch payload for job %s: %v", jobID, err)
	}
	return payload
}

// generateTestVideo produces a short synthetic video with ffmpeg's own
// testsrc source, so no binary fixture has to be committed.
func generateTestVideo(t *testing.T, durationSeconds int) []byte {
	t.Helper()
	path := t.TempDir() + "/input.mp4"
	cmd := exec.Command("ffmpeg",
		"-f", "lavfi",
		"-i", fmt.Sprintf("testsrc=duration=%d:size=320x240:rate=1", durationSeconds),
		"-y", path,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("generate test video: %v\n%s", err, output)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read generated video: %v", err)
	}
	return data
}

func statusOf(t *testing.T, env *workerTestEnv, job *videodomain.VideoJob) videodomain.JobStatus {
	t.Helper()
	stored, err := env.repo.FindByID(context.Background(), job.ID())
	if err != nil {
		t.Fatalf("reload job %s: %v", job.ID().String(), err)
	}
	return stored.Status()
}

func objectExists(t *testing.T, env *workerTestEnv, key string) bool {
	t.Helper()
	_, err := env.minio.StatObject(context.Background(), env.bucket, key, minio.StatObjectOptions{})
	if err == nil {
		return true
	}
	if minio.ToErrorResponse(err).Code == "NoSuchKey" {
		return false
	}
	t.Fatalf("stat %s/%s: %v", env.bucket, key, err)
	return false
}

// waitFor polls until condition holds, so a test can wait on work done by
// the consumer goroutine without sleeping a fixed amount.
func waitFor(t *testing.T, timeout time.Duration, what string, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for %s", timeout, what)
}

// syncBuffer collects log output written from the consumer goroutine while
// the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// captureLogs tees the standard logger into a buffer. The worker reports
// several outcomes only by logging them, so for those the log line is the
// observable behaviour rather than a convenience.
func captureLogs(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	previous := log.Writer()
	log.SetOutput(io.MultiWriter(previous, buf))
	t.Cleanup(func() { log.SetOutput(previous) })
	return buf
}

// declaredPublisher declares topo and opens a publisher on it, the way the
// relay does before its first cycle.
func declaredPublisher(t *testing.T, conn *amqp.Connection, topo platformrabbitmq.Topology) *videomessaging.Publisher {
	t.Helper()
	if err := platformrabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("declare topology: %v", err)
	}
	publisher, err := videomessaging.NewPublisher(conn, topo.Exchange, topo.RoutingKey)
	if err != nil {
		t.Fatalf("open publisher: %v", err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	return publisher
}

// publishDispatch publishes one dispatch and fails unless the broker both
// acknowledged it and routed it to a queue.
func publishDispatch(t *testing.T, publisher *videomessaging.Publisher, body []byte) {
	t.Helper()
	published, err := publisher.Publish(context.Background(), []videomessaging.Message{{ID: uuid.NewString(), Body: body}})
	if err != nil {
		t.Fatalf("publish dispatch: %v", err)
	}
	if len(published) != 1 {
		t.Fatal("dispatch was not routed to the work queue")
	}
}

func queueDepth(t *testing.T, conn *amqp.Connection, queue string) int {
	t.Helper()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	defer func() { _ = ch.Close() }()
	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		t.Fatalf("inspect queue %s: %v", queue, err)
	}
	return q.Messages
}

// startWorker runs the process's own run() in the background and hands back
// the cancellation that stands in for SIGTERM.
func startWorker(t *testing.T, env *workerTestEnv, topo platformrabbitmq.Topology, drain time.Duration) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		run(ctx, env.deps, topo, drain)
	}()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	return cancel, done
}

// TestWorker_ConsumesADispatchToCompletion is the end-to-end path this whole
// change exists for: a message on the queue, and nothing else, drives a job
// from queued to completed with its artifact stored and its input reclaimed.
func TestWorker_ConsumesADispatchToCompletion(t *testing.T) {
	conn := openTestConn(t)
	env := newWorkerTestEnv(t, envOptions{})
	topo := testTopology(t, conn)
	publisher := declaredPublisher(t, conn, topo)

	job, body := seedQueuedJob(t, env, generateTestVideo(t, 2))
	startWorker(t, env, topo, time.Second)
	publishDispatch(t, publisher, body)

	waitFor(t, 90*time.Second, "job "+job.ID().String()+" to complete", func() bool {
		return statusOf(t, env, job) == videodomain.JobStatusCompleted
	})

	stored, err := env.repo.FindByID(context.Background(), job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	resultKey := videodomain.ResultStorageKey(job.ID())
	if stored.StorageKey() != resultKey {
		t.Fatalf("stored key = %q, want %q", stored.StorageKey().String(), resultKey.String())
	}
	if stored.FrameCount() != 2 {
		t.Fatalf("frame count = %d, want 2", stored.FrameCount())
	}
	if !objectExists(t, env, resultKey.String()) {
		t.Fatalf("result %s is not in the bucket", resultKey.String())
	}
	// The source is deleted only after the terminal write commits, so it can
	// still be there for a moment after the status flips.
	waitFor(t, 30*time.Second, "the source object to be deleted", func() bool {
		return !objectExists(t, env, job.SourceKey().String())
	})
	waitFor(t, 30*time.Second, "the dispatch to be acknowledged", func() bool {
		return queueDepth(t, conn, topo.WorkQueue) == 0
	})
	if depth := queueDepth(t, conn, topo.DeadQueue); depth != 0 {
		t.Fatalf("dead-letter queue depth = %d, want 0 — a completed job must not be dead-lettered", depth)
	}
}

// TestWorker_DeadLettersAnUndecodableDispatch covers the other half of the
// consumer's plumbing: a rejection actually reaches the dead-letter queue.
// The disposition table below asserts the verdicts; this asserts that a
// Reject verdict has the effect the verdict is chosen for.
func TestWorker_DeadLettersAnUndecodableDispatch(t *testing.T) {
	conn := openTestConn(t)
	env := newWorkerTestEnv(t, envOptions{})
	topo := testTopology(t, conn)
	publisher := declaredPublisher(t, conn, topo)

	startWorker(t, env, topo, time.Second)
	publishDispatch(t, publisher, []byte("{ this is not a dispatch"))

	waitFor(t, 30*time.Second, "the dispatch to be dead-lettered", func() bool {
		return queueDepth(t, conn, topo.DeadQueue) == 1
	})
	if depth := queueDepth(t, conn, topo.WorkQueue); depth != 0 {
		t.Fatalf("work queue depth = %d, want 0 — a rejected message must not stay on the work queue", depth)
	}
}

// TestHandle_UnparseableDispatchIsDeadLettered and the three tests after it
// assert the disposition directly rather than inferring it from a queue
// depth. The verdict is what the worker actually decides; the broker only
// carries it out, and that carrying-out is covered once, above.
func TestHandle_UnparseableDispatchIsDeadLettered(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})

	var inFlight atomic.Pointer[string]
	if got := env.deps.handle(context.Background(), []byte("{ this is not a dispatch"), &inFlight); got != videomessaging.Reject {
		t.Fatalf("disposition = %v, want Reject", got)
	}
	if len(env.extractor.started) != 0 {
		t.Fatal("an unparseable dispatch must not reach the extractor")
	}
}

func TestHandle_DispatchNamingAnUnknownJobIsDeadLettered(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})

	body, err := json.Marshal(videomessaging.JobQueuedMessage{
		Type:        videopostgres.VideoJobQueuedEventType,
		JobID:       uuid.NewString(),
		UserID:      uuid.NewString(),
		SourceKey:   videodomain.SourceStorageKey(uuid.NewString(), "input.mp4").String(),
		ContentHash: testContentHash,
		OccurredAt:  time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("marshal dispatch: %v", err)
	}

	var inFlight atomic.Pointer[string]
	if got := env.deps.handle(context.Background(), body, &inFlight); got != videomessaging.Reject {
		t.Fatalf("disposition = %v, want Reject", got)
	}
	if len(env.extractor.started) != 0 {
		t.Fatal("a dispatch naming no job must not reach the extractor")
	}
}

// TestHandle_DuplicateDispatchIsDeadLetteredWithoutASecondExtraction is the
// at-least-once case the conditional claim exists for. The source object
// assertion is the one that matters: the winning consumer is very likely
// reading that object right now, so a duplicate that cleaned up after itself
// would break a run it has nothing to do with.
func TestHandle_DuplicateDispatchIsDeadLetteredWithoutASecondExtraction(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, body := seedQueuedJob(t, env, generateTestVideo(t, 1))
	ctx := context.Background()

	// Another consumer got there first.
	winner, err := env.repo.FindByID(ctx, job.ID())
	if err != nil {
		t.Fatalf("reload job: %v", err)
	}
	if err := winner.StartProcessing(); err != nil {
		t.Fatalf("claim job for the winner: %v", err)
	}
	claimed, err := env.repo.ClaimForProcessing(ctx, winner)
	if err != nil {
		t.Fatalf("persist the winner's claim: %v", err)
	}
	if !claimed {
		t.Fatal("the winner's claim was refused; the job was not queued")
	}

	var inFlight atomic.Pointer[string]
	if got := env.deps.handle(ctx, body, &inFlight); got != videomessaging.Reject {
		t.Fatalf("disposition = %v, want Reject", got)
	}
	if len(env.extractor.started) != 0 {
		t.Fatal("a duplicate dispatch must not run a second extraction")
	}
	if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q, want %q — the loser must not write to the job at all", status, videodomain.JobStatusProcessing)
	}
	if !objectExists(t, env, job.SourceKey().String()) {
		t.Fatal("the loser deleted the source object the winner is still reading")
	}
}

// TestHandle_UndecodableSourceFailsTheJobAndClearsItsIdempotencyKey covers
// the failure path's full obligation: the job is terminal, the input is
// reclaimed, and the content-hash mapping is released so the same user can
// resubmit the same bytes instead of being handed the failure forever.
func TestHandle_UndecodableSourceFailsTheJobAndClearsItsIdempotencyKey(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, body := seedQueuedJob(t, env, []byte("this is not a video"))
	ctx := context.Background()

	key, err := videodomain.NewIdempotencyKey(job.UserID().String(), testContentHash)
	if err != nil {
		t.Fatalf("build idempotency key: %v", err)
	}
	// Seeded through the real protocol rather than by writing the stored
	// string, so a drift in Finalize's format fails here instead of
	// silently no-opping the worker's clear in production.
	token, reserved, err := env.keys.Reserve(ctx, key)
	if err != nil || !reserved {
		t.Fatalf("reserve idempotency key: reserved=%v err=%v", reserved, err)
	}
	finalized, err := env.keys.Finalize(ctx, key, token, job.ID())
	if err != nil || !finalized {
		t.Fatalf("finalize idempotency key: finalized=%v err=%v", finalized, err)
	}

	var inFlight atomic.Pointer[string]
	if got := env.deps.handle(ctx, body, &inFlight); got != videomessaging.Ack {
		t.Fatalf("disposition = %v, want Ack — a committed failure is a resolved dispatch", got)
	}
	if status := statusOf(t, env, job); status != videodomain.JobStatusFailed {
		t.Fatalf("status = %q, want %q", status, videodomain.JobStatusFailed)
	}
	if objectExists(t, env, job.SourceKey().String()) {
		t.Fatal("the source object survived a committed failure")
	}
	if _, found, err := env.keys.Lookup(ctx, key); err != nil || found {
		t.Fatalf("idempotency key still resolves after a failure: found=%v err=%v", found, err)
	}
}

// TestHandle_ClearsTheKeyThroughAJobRecordThatPredatesContentHash is the
// rolling-deploy case for the clear. `content_hash` is new in this change and
// the cache record carries no version, so an API replica of the previous
// release that serves one status poll for a v2 job repopulates the cache in
// the old shape — every field it knew about, and no content_hash. omitempty
// decodes that to "", which is not an error anywhere: the key simply cannot
// be built, the clear logs and gives up, and the failed job's 24-hour mapping
// silently outlives it, blocking the retry this whole mechanism exists to
// allow.
//
// Reading the hash from PostgreSQL rather than through the decorator is what
// makes that impossible, and this test is the only thing that says so.
func TestHandle_ClearsTheKeyThroughAJobRecordThatPredatesContentHash(t *testing.T) {
	env := newWorkerTestEnv(t, envOptions{})
	job, body := seedQueuedJob(t, env, []byte("this is not a video"))
	ctx := context.Background()

	poisoned := fmt.Sprintf(
		`{"id":%q,"user_id":%q,"original_filename":%q,"source_key":%q,"frame_count":0,"status":%q,"created_at":%q}`,
		job.ID().String(), job.UserID().String(), job.OriginalFilename(), job.SourceKey().String(),
		string(videodomain.JobStatusQueued), job.CreatedAt().UTC().Format(time.RFC3339Nano),
	)
	if err := env.deps.redis.Set(ctx, "videojob:status:"+job.ID().String(), poisoned, time.Minute).Err(); err != nil {
		t.Fatalf("seed the previous release's cache record: %v", err)
	}

	key, err := videodomain.NewIdempotencyKey(job.UserID().String(), testContentHash)
	if err != nil {
		t.Fatalf("build idempotency key: %v", err)
	}
	token, reserved, err := env.keys.Reserve(ctx, key)
	if err != nil || !reserved {
		t.Fatalf("reserve idempotency key: reserved=%v err=%v", reserved, err)
	}
	finalized, err := env.keys.Finalize(ctx, key, token, job.ID())
	if err != nil || !finalized {
		t.Fatalf("finalize idempotency key: finalized=%v err=%v", finalized, err)
	}

	var inFlight atomic.Pointer[string]
	if got := env.deps.handle(ctx, body, &inFlight); got != videomessaging.Ack {
		t.Fatalf("disposition = %v, want Ack", got)
	}
	if status := statusOf(t, env, job); status != videodomain.JobStatusFailed {
		t.Fatalf("status = %q, want %q", status, videodomain.JobStatusFailed)
	}
	if _, found, err := env.keys.Lookup(ctx, key); err != nil || found {
		t.Fatalf("the key survived a failure whose cached job record carried no content hash: found=%v err=%v", found, err)
	}
}

// TestHandle_TerminalWriteFailureLeavesTheJobProcessing is the orphan case:
// the artifact is stored but the row that points at it cannot be written.
// Everything is deliberately left alone — the source stays so the job can be
// re-run by hand, and the dispatch is dead-lettered rather than acked, so it
// stays on record. The log line is the only pointer to an artifact no
// listing will show, which is why it is asserted rather than assumed.
func TestHandle_TerminalWriteFailureLeavesTheJobProcessing(t *testing.T) {
	writeErr := errors.New("simulated terminal write failure")
	env := newWorkerTestEnv(t, envOptions{
		decorate: func(inner videodomain.VideoJobRepository) videodomain.VideoJobRepository {
			return failCompletionRepository{VideoJobRepository: inner, err: writeErr}
		},
	})
	logs := captureLogs(t)
	job, body := seedQueuedJob(t, env, generateTestVideo(t, 1))

	var inFlight atomic.Pointer[string]
	if got := env.deps.handle(context.Background(), body, &inFlight); got != videomessaging.Reject {
		t.Fatalf("disposition = %v, want Reject — an uncommitted outcome must not be acknowledged", got)
	}
	if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q, want %q", status, videodomain.JobStatusProcessing)
	}
	if !objectExists(t, env, job.SourceKey().String()) {
		t.Fatal("the source object was deleted for a job that never reached a terminal state")
	}
	resultKey := videodomain.ResultStorageKey(job.ID())
	if !objectExists(t, env, resultKey.String()) {
		t.Fatalf("result %s is missing; the extraction is supposed to have succeeded", resultKey.String())
	}
	written := logs.String()
	if !strings.Contains(written, job.ID().String()) || !strings.Contains(written, resultKey.String()) {
		t.Fatalf("log does not name both the job and its result key; got:\n%s", written)
	}
}

// failCompletionRepository fails only the write that records a completion,
// leaving the conditional claim and every other path intact — the single
// seam that produces a stored artifact with no row pointing at it.
type failCompletionRepository struct {
	videodomain.VideoJobRepository
	err error
}

func (r failCompletionRepository) Update(ctx context.Context, job *videodomain.VideoJob) error {
	if job.Status() == videodomain.JobStatusCompleted {
		return r.err
	}
	return r.VideoJobRepository.Update(ctx, job)
}

// deleteQueue removes a queue out from under a running consumer, which is
// how the broker's disappearance is made observable without restarting it:
// the consumer is cancelled and its deliveries channel closes, exactly as it
// would on a lost connection.
func deleteQueue(t *testing.T, conn *amqp.Connection, queue string) {
	t.Helper()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	defer func() { _ = ch.Close() }()
	if _, err := ch.QueueDelete(queue, false, false, false); err != nil {
		t.Fatalf("delete queue %s: %v", queue, err)
	}
}

// publishWhenRoutable retries until the broker both acknowledges the publish
// and routes it to a queue. An unroutable publish is returned and discarded,
// so retrying costs nothing — and "routable again" is precisely the
// assertion that a consumer has redeclared the queue and its binding.
func publishWhenRoutable(t *testing.T, publisher *videomessaging.Publisher, body []byte, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		published, err := publisher.Publish(context.Background(), []videomessaging.Message{{ID: uuid.NewString(), Body: body}})
		if err != nil {
			t.Fatalf("publish dispatch: %v", err)
		}
		if len(published) == 1 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %s waiting for the dispatch to become routable", timeout)
}

// TestRun_ShutdownFinishesTheInFlightJobAndClaimsNoOther covers the pair of
// guarantees a restart depends on. Both are asserted on outcomes rather than
// on the branch that produces them: with a prefetch of one, whether the
// second dispatch was never delivered or was delivered and requeued is the
// broker's timing to decide, and either way it must still be on the queue
// with its job untouched.
func TestRun_ShutdownFinishesTheInFlightJobAndClaimsNoOther(t *testing.T) {
	conn := openTestConn(t)
	release := make(chan struct{})
	env := newWorkerTestEnv(t, envOptions{release: release})
	topo := testTopology(t, conn)
	publisher := declaredPublisher(t, conn, topo)

	first, firstBody := seedQueuedJob(t, env, generateTestVideo(t, 1))
	second, secondBody := seedQueuedJob(t, env, generateTestVideo(t, 1))

	cancel, done := startWorker(t, env, topo, time.Minute)
	publishDispatch(t, publisher, firstBody)
	publishDispatch(t, publisher, secondBody)

	select {
	case started := <-env.extractor.started:
		if started != first.ID().String() {
			t.Fatalf("extraction started for job %s, want the first dispatch %s", started, first.ID().String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("the first dispatch never reached the extractor")
	}

	cancel()
	close(release)

	select {
	case <-done:
	case <-time.After(90 * time.Second):
		t.Fatal("run did not return once the in-flight job finished")
	}

	if status := statusOf(t, env, first); status != videodomain.JobStatusCompleted {
		t.Fatalf("in-flight job status = %q, want %q — shutdown must finish it, not abandon it", status, videodomain.JobStatusCompleted)
	}
	if status := statusOf(t, env, second); status != videodomain.JobStatusQueued {
		t.Fatalf("second job status = %q, want %q — no further job may be claimed after the signal", status, videodomain.JobStatusQueued)
	}
	waitFor(t, 30*time.Second, "the second dispatch to be back on the work queue", func() bool {
		return queueDepth(t, conn, topo.WorkQueue) == 1
	})
	if depth := queueDepth(t, conn, topo.DeadQueue); depth != 0 {
		t.Fatalf("dead-letter queue depth = %d, want 0 — shutdown must not dead-letter anything", depth)
	}
}

// TestRun_DrainDeadlineExpiresNamingTheInFlightJob covers the deadline that
// exists only so a wedged extraction cannot keep the process alive forever.
// The job it abandons is stranded in `processing` — no redelivery can
// re-claim it — so the log line naming it is the only record an operator
// gets, and it is asserted for that reason.
func TestRun_DrainDeadlineExpiresNamingTheInFlightJob(t *testing.T) {
	conn := openTestConn(t)
	// Never closed. The extraction goroutine stays parked here for the rest
	// of the test binary, holding nothing but its own stack — which is the
	// only way to produce an extraction that outlives the deadline.
	release := make(chan struct{})
	env := newWorkerTestEnv(t, envOptions{release: release})
	topo := testTopology(t, conn)
	publisher := declaredPublisher(t, conn, topo)

	job, body := seedQueuedJob(t, env, generateTestVideo(t, 1))
	t.Cleanup(func() { _ = os.Remove("temp/" + job.ID().String() + "_source") })

	logs := captureLogs(t)
	cancel, done := startWorker(t, env, topo, 300*time.Millisecond)
	publishDispatch(t, publisher, body)

	select {
	case <-env.extractor.started:
	case <-time.After(60 * time.Second):
		t.Fatal("the dispatch never reached the extractor")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("run did not return after the drain deadline expired")
	}

	written := logs.String()
	if !strings.Contains(written, job.ID().String()) {
		t.Fatalf("drain-deadline log does not name the in-flight job; got:\n%s", written)
	}
	if !strings.Contains(written, "stays 'processing'") {
		t.Fatalf("drain-deadline log does not say the job is stranded; got:\n%s", written)
	}
	if status := statusOf(t, env, job); status != videodomain.JobStatusProcessing {
		t.Fatalf("status = %q, want %q", status, videodomain.JobStatusProcessing)
	}
}

// TestRun_ReconnectRedeclaresTheTopologyAndResumes is the reason both the
// relay and the consumer redeclare on every dial: against a broker that came
// back without their entities, a consumer that only reconnected would
// consume from a queue that no longer exists.
func TestRun_ReconnectRedeclaresTheTopologyAndResumes(t *testing.T) {
	conn := openTestConn(t)
	env := newWorkerTestEnv(t, envOptions{})
	topo := testTopology(t, conn)
	publisher := declaredPublisher(t, conn, topo)

	first, firstBody := seedQueuedJob(t, env, generateTestVideo(t, 1))
	startWorker(t, env, topo, time.Second)
	publishDispatch(t, publisher, firstBody)
	waitFor(t, 90*time.Second, "the first job to complete", func() bool {
		return statusOf(t, env, first) == videodomain.JobStatusCompleted
	})

	deleteQueue(t, conn, topo.WorkQueue)

	second, secondBody := seedQueuedJob(t, env, generateTestVideo(t, 1))
	// Routable again means the queue and its binding are both back, and only
	// the consumer's own redeclaration could have put them there.
	publishWhenRoutable(t, publisher, secondBody, 60*time.Second)
	waitFor(t, 90*time.Second, "the second job to complete after the reconnect", func() bool {
		return statusOf(t, env, second) == videodomain.JobStatusCompleted
	})
}
