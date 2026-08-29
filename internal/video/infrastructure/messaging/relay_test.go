package messaging

import (
	"bytes"
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"log"
	"net/url"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/platform/rabbitmq"
	"video-processor/internal/video/infrastructure/postgres"
)

// testEventType is this file's own outbox event_type, so seeded rows can
// never collide with the production one on a shared test database.
const testEventType = "test_video_job.queued"

// TestRoutingKeyMatchesTheOutboxEventType closes the one string that has to
// stay equal across two packages with nothing in the compiler enforcing it:
// the routing key messages are published under and the event_type the outbox
// stores. If they drift, Repository.Enqueue writes rows the relay's claim
// never matches, and dispatch stops silently.
func TestRoutingKeyMatchesTheOutboxEventType(t *testing.T) {
	if RoutingKeyJobQueued != postgres.VideoJobQueuedEventType {
		t.Fatalf("RoutingKeyJobQueued = %q, want it equal to postgres.VideoJobQueuedEventType = %q", RoutingKeyJobQueued, postgres.VideoJobQueuedEventType)
	}
}

func testBrokerURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL not set; skipping RabbitMQ integration test")
	}
	return url
}

func openTestConn(t *testing.T) *amqp.Connection {
	t.Helper()
	conn, err := rabbitmq.Open(rabbitmq.Config{URL: testBrokerURL(t)})
	if err != nil {
		t.Fatalf("open test broker: %v", err)
	}
	t.Cleanup(func() { _ = rabbitmq.Close(conn) })
	return conn
}

// relayTestDatabase is this package's own database, created on demand beside
// the one VIDEO_POSTGRES_TEST_DSN names.
//
// It is not sharing that database, and the isolation is not cosmetic: `go
// test ./...` runs packages in parallel, and internal/video/infrastructure/
// postgres truncates video_jobs and video_job_outbox before every one of its
// tests. Sharing would let each package wipe the other's rows mid-test, in a
// way that presents as an unrelated flake.
const relayTestDatabase = "video_relay_test"

func testDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("VIDEO_POSTGRES_TEST_DSN")
	if dsn == "" {
		t.Skip("VIDEO_POSTGRES_TEST_DSN not set; skipping outbox relay integration test")
	}

	admin, err := postgres.Open(postgres.Config{DSN: dsn})
	if err != nil {
		t.Fatalf("open test database: %v", err)
	}
	ctx := context.Background()
	// The name is a compile-time constant, not caller input, and CREATE
	// DATABASE takes no parameters. Already-exists is the normal case after
	// the first run.
	if _, err := admin.ExecContext(ctx, "CREATE DATABASE "+relayTestDatabase); err != nil && !strings.Contains(err.Error(), "already exists") {
		_ = admin.Close()
		t.Fatalf("create %s: %v", relayTestDatabase, err)
	}
	_ = admin.Close()

	db, err := postgres.Open(postgres.Config{DSN: withDatabase(t, dsn, relayTestDatabase)})
	if err != nil {
		t.Fatalf("open %s: %v", relayTestDatabase, err)
	}
	t.Cleanup(func() { _ = db.Close() })

	if err := postgres.Migrate(ctx, db); err != nil {
		t.Fatalf("migrate schema: %v", err)
	}
	if _, err := db.ExecContext(ctx, "TRUNCATE TABLE video_jobs, video_job_outbox"); err != nil {
		t.Fatalf("truncate tables: %v", err)
	}
	return db
}

// withDatabase rewrites dsn's database name, keeping its host, credentials,
// and parameters.
func withDatabase(t *testing.T, dsn, database string) string {
	t.Helper()
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse VIDEO_POSTGRES_TEST_DSN: %v", err)
	}
	parsed.Path = "/" + database
	return parsed.String()
}

// testTopology names entities scoped to one test and deletes them afterwards.
// The production names are never declared here: a test-sized max-length left
// behind under a production name resurfaces as a PRECONDITION_FAILED on
// someone else's branch, and the reject-publish behavior needs a queue of one
// message to be observable at all.
func testTopology(t *testing.T, conn *amqp.Connection, maxLength int) rabbitmq.Topology {
	t.Helper()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate topology suffix: %v", err)
	}
	prefix := "test." + strings.NewReplacer("/", "-", " ", "_").Replace(t.Name()) + "." + hex.EncodeToString(suffix)

	topo := rabbitmq.Topology{
		Exchange:       prefix + ".exchange",
		RoutingKey:     testEventType,
		WorkQueue:      prefix + ".work",
		DeadExchange:   prefix + ".dlx",
		DeadQueue:      prefix + ".dead",
		WorkMaxLength:  maxLength,
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

// newTestRelay builds a Relay over test-scoped names. Constructed directly
// rather than through NewRelay, which is fixed to the production topology and
// event type on purpose — widening it for tests would let a caller point the
// relay somewhere it must never point in production.
func newTestRelay(t *testing.T, db *sql.DB, topo rabbitmq.Topology) *Relay {
	t.Helper()
	return &Relay{
		outbox:    postgres.NewOutboxRepository(db),
		config:    rabbitmq.Config{URL: testBrokerURL(t)},
		eventType: testEventType,
		topology:  topo,
	}
}

func seedOutboxRow(t *testing.T, db *sql.DB, body string) string {
	t.Helper()
	id := uuid.NewString()
	if _, err := db.ExecContext(context.Background(),
		`INSERT INTO video_job_outbox (id, event_type, payload, occurred_at) VALUES ($1, $2, $3, now())`,
		id, testEventType, []byte(body),
	); err != nil {
		t.Fatalf("seed outbox row: %v", err)
	}
	return id
}

func publishedAt(t *testing.T, db *sql.DB, id string) sql.NullTime {
	t.Helper()
	var stamped sql.NullTime
	if err := db.QueryRowContext(context.Background(),
		`SELECT published_at FROM video_job_outbox WHERE id = $1`, id,
	).Scan(&stamped); err != nil {
		t.Fatalf("read published_at: %v", err)
	}
	return stamped
}

func queueDepth(t *testing.T, conn *amqp.Connection, queue string) int {
	t.Helper()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	defer func() { _ = ch.Close() }()
	// A passive declare reports the current depth without altering the queue.
	q, err := ch.QueueDeclarePassive(queue, true, false, false, false, nil)
	if err != nil {
		t.Fatalf("inspect queue %s: %v", queue, err)
	}
	return q.Messages
}

// declaredPublisher declares topo and returns a publisher on it, the way
// Relay.serve does before its first cycle.
func declaredPublisher(t *testing.T, conn *amqp.Connection, topo rabbitmq.Topology) *Publisher {
	t.Helper()
	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("declare topology: %v", err)
	}
	publisher, err := NewPublisher(conn, topo.Exchange, topo.RoutingKey)
	if err != nil {
		t.Fatalf("open publisher: %v", err)
	}
	t.Cleanup(func() { _ = publisher.Close() })
	return publisher
}

func TestRelay_Cycle_PublishesStampsAndDoesNotRepublish(t *testing.T) {
	db := testDB(t)
	conn := openTestConn(t)
	topo := testTopology(t, conn, 10)
	relay := newTestRelay(t, db, topo)
	publisher := declaredPublisher(t, conn, topo)
	ctx := context.Background()

	id := seedOutboxRow(t, db, `{"type":"test_video_job.queued","job_id":"job-1"}`)

	if err := relay.cycle(ctx, publisher); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if depth := queueDepth(t, conn, topo.WorkQueue); depth != 1 {
		t.Fatalf("queue depth = %d, want 1", depth)
	}
	if !publishedAt(t, db, id).Valid {
		t.Fatal("published_at is NULL after a successful publish")
	}

	// A second cycle must find nothing: the stamp is what makes delivery
	// once-per-row under normal operation.
	if err := relay.cycle(ctx, publisher); err != nil {
		t.Fatalf("second cycle: %v", err)
	}
	if depth := queueDepth(t, conn, topo.WorkQueue); depth != 1 {
		t.Fatalf("queue depth after a second cycle = %d, want 1", depth)
	}
}

// TestRelay_Cycle_PublishesPersistentWithNoExpiration covers both publishing
// properties videojob-messaging requires.
//
// Persistence has a second half this cannot reach: that a queued message
// survives a broker restart. The suite runs inside a container with no Docker
// socket, so no test can restart the broker — that half is verified by the
// scripted check recorded in this change's PR (tasks.md 8.7a), and this
// in-process assertion is deliberately the weaker of the two.
func TestRelay_Cycle_PublishesPersistentWithNoExpiration(t *testing.T) {
	db := testDB(t)
	conn := openTestConn(t)
	topo := testTopology(t, conn, 10)
	relay := newTestRelay(t, db, topo)
	publisher := declaredPublisher(t, conn, topo)

	seedOutboxRow(t, db, `{"type":"test_video_job.queued","job_id":"job-1"}`)
	if err := relay.cycle(context.Background(), publisher); err != nil {
		t.Fatalf("cycle: %v", err)
	}

	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	defer func() { _ = ch.Close() }()
	msg, ok, err := ch.Get(topo.WorkQueue, true)
	if err != nil {
		t.Fatalf("get message: %v", err)
	}
	if !ok {
		t.Fatal("no message on the work queue")
	}
	if msg.DeliveryMode != amqp.Persistent {
		t.Fatalf("DeliveryMode = %d, want %d (persistent)", msg.DeliveryMode, amqp.Persistent)
	}
	// An expiration is honoured independently of the queue's arguments, so
	// setting one would dead-letter a job message even though the job queue
	// deliberately carries no TTL — leaving its VideoJob reporting "queued"
	// forever, with no edge out of that state.
	if msg.Expiration != "" {
		t.Fatalf("Expiration = %q, want it unset", msg.Expiration)
	}
}

// TestRelay_Cycle_NackedPublishLeavesTheRowUnstamped covers back-pressure: a
// job queue at its maximum length nacks under reject-publish, and that is the
// designed behavior, not an incident. The row must stay claimable and the
// cycle must not report an error, or a full queue would become a reconnect
// loop instead of a pause.
func TestRelay_Cycle_NackedPublishLeavesTheRowUnstamped(t *testing.T) {
	db := testDB(t)
	conn := openTestConn(t)
	topo := testTopology(t, conn, 1)
	relay := newTestRelay(t, db, topo)
	publisher := declaredPublisher(t, conn, topo)
	ctx := context.Background()

	// Fill the queue to its maximum first, so the relay's own publish is the
	// one refused.
	first := seedOutboxRow(t, db, `{"job_id":"job-1"}`)
	if err := relay.cycle(ctx, publisher); err != nil {
		t.Fatalf("cycle: %v", err)
	}
	if !publishedAt(t, db, first).Valid {
		t.Fatal("the first row should have been published")
	}

	second := seedOutboxRow(t, db, `{"job_id":"job-2"}`)
	if err := relay.cycle(ctx, publisher); err != nil {
		t.Fatalf("cycle returned an error for a nacked publish; back-pressure is not a failure: %v", err)
	}
	if stamped := publishedAt(t, db, second); stamped.Valid {
		t.Fatalf("published_at = %v, want NULL so the next poll retries it", stamped.Time)
	}
}

// TestPublisher_UnroutablePublishIsNotReportedAsPublished is the gap
// confirmations alone leave open: the broker acknowledges a publish the
// exchange accepted even when it reached no queue at all. Without mandatory
// publishing and return correlation, the relay would stamp a row for a
// dispatch that was silently discarded — permanently, and in the one
// component whose entire purpose is that this cannot happen.
func TestPublisher_UnroutablePublishIsNotReportedAsPublished(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn, 10)

	// The exchange alone, with no queue bound to it — which is also what a
	// production exchange looks like before anything declares its queue.
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	if err := ch.ExchangeDeclare(topo.Exchange, amqp.ExchangeDirect, true, false, false, false, nil); err != nil {
		t.Fatalf("declare exchange: %v", err)
	}
	_ = ch.Close()

	publisher, err := NewPublisher(conn, topo.Exchange, topo.RoutingKey)
	if err != nil {
		t.Fatalf("open publisher: %v", err)
	}
	defer func() { _ = publisher.Close() }()

	published, err := publisher.Publish(context.Background(), []Message{{ID: uuid.NewString(), Body: []byte(`{}`)}})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(published) != 0 {
		t.Fatalf("published = %v, want none — the message reached no queue", published)
	}

	// A routable publish on the same publisher must still be reported as
	// published. Returns are correlated per message and drained per batch,
	// so a return left buffered from the batch above would otherwise be
	// attributed to this one and suppress a stamp that was earned.
	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("declare topology: %v", err)
	}
	routable := Message{ID: uuid.NewString(), Body: []byte(`{}`)}
	published, err = publisher.Publish(context.Background(), []Message{routable})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(published) != 1 || published[0] != routable.ID {
		t.Fatalf("published = %v, want [%s]", published, routable.ID)
	}
}

// TestRelay_Run_DeclaresTheTopologyOnAFreshBroker exercises the whole loop
// against a broker where the exchange does not exist. Nothing else in this
// system declares it, so without the relay's own declaration a publish would
// close the channel instead of failing routably, and the message would never
// arrive.
func TestRelay_Run_DeclaresTheTopologyOnAFreshBroker(t *testing.T) {
	db := testDB(t)
	conn := openTestConn(t)
	topo := testTopology(t, conn, 10)
	relay := newTestRelay(t, db, topo)

	id := seedOutboxRow(t, db, `{"job_id":"job-1"}`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()

	waitForStamp(t, db, id)
	if depth := queueDepth(t, conn, topo.WorkQueue); depth != 1 {
		t.Fatalf("queue depth = %d, want 1", depth)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Run returned %v, want nil", err)
	}
}

// TestRelay_Run_RedeclaresAfterLosingItsChannel is the reconnect half. The
// exchange and queue are deleted out from under a running relay, which is
// what a recreated broker looks like from the relay's side: the next publish
// closes its channel, and recovery is only possible because the topology is
// declared on every dial rather than once at startup.
func TestRelay_Run_RedeclaresAfterLosingItsChannel(t *testing.T) {
	db := testDB(t)
	conn := openTestConn(t)
	topo := testTopology(t, conn, 10)
	relay := newTestRelay(t, db, topo)

	first := seedOutboxRow(t, db, `{"job_id":"job-1"}`)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()
	defer func() {
		cancel()
		<-done
	}()

	waitForStamp(t, db, first)

	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	if _, err := ch.QueueDelete(topo.WorkQueue, false, false, false); err != nil {
		t.Fatalf("delete work queue: %v", err)
	}
	if err := ch.ExchangeDelete(topo.Exchange, false, false); err != nil {
		t.Fatalf("delete exchange: %v", err)
	}
	_ = ch.Close()

	second := seedOutboxRow(t, db, `{"job_id":"job-2"}`)
	waitForStamp(t, db, second)
	if depth := queueDepth(t, conn, topo.WorkQueue); depth != 1 {
		t.Fatalf("queue depth after redeclaration = %d, want 1", depth)
	}
}

// TestRelay_Run_StopsOnCancellationLeavingNoRowStampedUndelivered is the
// shutdown requirement. The invariant that matters is not that Run returns
// quickly but that it returns with its transaction resolved: a row stamped
// published without its message having been delivered is a dispatch lost for
// good, since nothing would ever claim it again.
func TestRelay_Run_StopsOnCancellationLeavingNoRowStampedUndelivered(t *testing.T) {
	db := testDB(t)
	conn := openTestConn(t)
	topo := testTopology(t, conn, 100)
	relay := newTestRelay(t, db, topo)

	const rows = 20
	var first string
	for i := 0; i < rows; i++ {
		id := seedOutboxRow(t, db, `{"job_id":"job"}`)
		if first == "" {
			first = id
		}
	}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()

	// Waited only until the relay is demonstrably cycling, then cancelled
	// without further synchronization, so the cancellation lands wherever
	// the loop happens to be — including inside a claim.
	waitForStamp(t, db, first)
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("Run returned %v, want nil", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}

	var stamped int
	if err := db.QueryRowContext(context.Background(),
		`SELECT count(*) FROM video_job_outbox WHERE event_type = $1 AND published_at IS NOT NULL`, testEventType,
	).Scan(&stamped); err != nil {
		t.Fatalf("count stamped rows: %v", err)
	}
	if stamped == 0 {
		t.Fatal("no row was stamped; the relay never got far enough for this test to mean anything")
	}
	if depth := queueDepth(t, conn, topo.WorkQueue); depth < stamped {
		t.Fatalf("%d rows are stamped published but the queue holds %d messages; a stamped row that was never delivered is lost for good", stamped, depth)
	}
}

// waitForStamp polls until id carries a published_at, or fails. The relay
// runs on its own ticker, so a test can only observe its effects.
func waitForStamp(t *testing.T, db *sql.DB, id string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if publishedAt(t, db, id).Valid {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatalf("row %s was never marked published", id)
}

// TestPublisher_RejectsAnOversizedBatch guards the deadlock the
// confirmation-first sequence would otherwise allow: returns are buffered
// and not read until every confirmation has arrived, so a batch larger than
// that buffer whose messages are unroutable would block the client's own
// dispatch goroutine — the one that still owes the confirmations being
// waited on. The current caller cannot reach this bound, which is why it is
// checked rather than assumed.
func TestPublisher_RejectsAnOversizedBatch(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn, 10)
	publisher := declaredPublisher(t, conn, topo)

	messages := make([]Message, maxPublishBatch+1)
	for i := range messages {
		messages[i] = Message{ID: uuid.NewString(), Body: []byte(`{}`)}
	}

	published, err := publisher.Publish(context.Background(), messages)
	if !errors.Is(err, ErrBatchTooLarge) {
		t.Fatalf("error = %v, want %v", err, ErrBatchTooLarge)
	}
	if published != nil {
		t.Fatalf("published = %v, want none", published)
	}
	if depth := queueDepth(t, conn, topo.WorkQueue); depth != 0 {
		t.Fatalf("queue depth = %d, want 0 — a rejected batch must publish nothing", depth)
	}
}

// TestRelay_Run_BacksOffWhenAConnectionIsUnusable covers the failure mode a
// dial-only backoff misses. A topology that conflicts with an existing
// declaration fails *after* a successful dial, so resetting the backoff on
// connect alone would redial in a tight loop — hammering the broker and
// flooding the log with the one failure nobody is watching for. The relay
// must instead treat a connection that never completed a cycle as a failed
// attempt.
func TestRelay_Run_BacksOffWhenAConnectionIsUnusable(t *testing.T) {
	db := testDB(t)
	conn := openTestConn(t)
	topo := testTopology(t, conn, 10)

	// Declared with a different max-length, so the relay's own declaration
	// is refused with PRECONDITION_FAILED on every attempt.
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	if _, err := ch.QueueDeclare(topo.WorkQueue, true, false, false, false, amqp.Table{"x-max-length": int64(topo.WorkMaxLength + 1)}); err != nil {
		t.Fatalf("declare conflicting queue: %v", err)
	}
	_ = ch.Close()

	// log output is captured rather than instrumented: the dial rate is only
	// observable through the relay's own lifecycle logging, which is also
	// the thing an operator would see flooding.
	var logs safeBuffer
	log.SetOutput(&logs)
	t.Cleanup(func() { log.SetOutput(os.Stderr) })

	relay := newTestRelay(t, db, topo)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- relay.Run(ctx) }()

	// Long enough for an unbacked-off loop to produce dozens of attempts and
	// for a backed-off one to produce at most a handful (1s + 2s).
	time.Sleep(3500 * time.Millisecond)
	cancel()
	<-done

	attempts := strings.Count(logs.String(), "video: outbox relay: connected")
	if attempts == 0 {
		t.Fatal("the relay never connected; the test never reached the path it is checking")
	}
	if attempts > 4 {
		t.Fatalf("the relay dialed %d times in 3.5s; a connection that fails after dialing must be backed off, not retried in a tight loop", attempts)
	}
}

// safeBuffer is a bytes.Buffer usable from the relay's goroutine and the
// test's at once — log writes are concurrent with the assertion's read.
type safeBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *safeBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *safeBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}
