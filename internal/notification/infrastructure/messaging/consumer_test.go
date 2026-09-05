package messaging

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/platform/rabbitmq"
)

// The pause a Requeue takes in these tests. Short enough that four tests do
// not add a minute to the suite, long enough that the gap it produces between
// two handler calls is not lost in scheduling noise.
const testRequeuePause = 300 * time.Millisecond

// The window every "the broker eventually reports this" assertion is given.
// Generous on purpose: what is being tested is which disposition was applied,
// never how fast a broker got around to reflecting it.
const brokerSettleTimeout = 10 * time.Second

// TestConsumer_Ack_RemovesTheMessage also covers what the handler is handed.
// The routing key is how a handler tells a completion from a failure without
// decoding the body twice, so a consumer that passed the wrong string — or an
// empty one — would send every event down the unrecognized-type path and
// dead-letter the lot.
func TestConsumer_Ack_RemovesTheMessage(t *testing.T) {
	conn := openTestConn(t)
	topo := declaredTestTopology(t, conn)

	var (
		mu         sync.Mutex
		seenKey    string
		seenBody   string
		handled    = make(chan struct{}, 1)
		routingKey = topo.RoutingKeys[0]
	)
	runConsumer(t, topo, testRequeuePause, func(_ context.Context, eventType string, body []byte) Disposition {
		mu.Lock()
		seenKey, seenBody = eventType, string(body)
		mu.Unlock()
		handled <- struct{}{}
		return Ack
	})

	publish(t, conn, topo.Exchange, routingKey, []byte(`{"job_id":"job-1"}`))
	waitFor(t, handled, "the handler was never called")

	mu.Lock()
	gotKey, gotBody := seenKey, seenBody
	mu.Unlock()
	if gotKey != routingKey {
		t.Errorf("handler saw event type %q, want %q", gotKey, routingKey)
	}
	if gotBody != `{"job_id":"job-1"}` {
		t.Errorf("handler saw body %q, want the published one", gotBody)
	}

	expectDepth(t, conn, topo.WorkQueue, 0, "an acknowledged message must leave the work queue")
	expectDepth(t, conn, topo.DeadQueue, 0, "an acknowledged message must not be dead-lettered")
}

func TestConsumer_Reject_DeadLettersWithoutRequeue(t *testing.T) {
	conn := openTestConn(t)
	topo := declaredTestTopology(t, conn)

	var calls int
	var mu sync.Mutex
	handled := make(chan struct{}, 4)
	runConsumer(t, topo, testRequeuePause, func(context.Context, string, []byte) Disposition {
		mu.Lock()
		calls++
		mu.Unlock()
		handled <- struct{}{}
		return Reject
	})

	publish(t, conn, topo.Exchange, topo.RoutingKeys[0], []byte(`not json`))
	waitFor(t, handled, "the handler was never called")

	expectDepth(t, conn, topo.WorkQueue, 0, "a rejected message must not return to the work queue")
	expectDepth(t, conn, topo.DeadQueue, 1, "a rejected message must reach the dead-letter queue")

	// The disposition's whole point is that it does not come back: a body
	// that cannot be decoded fails identically forever.
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("handler was called %d times, want 1 — a rejected message must not be redelivered", calls)
	}
}

// The requeue disposition has two observable halves, and asserting only the
// first would let a consumer that never paused pass: the message comes back,
// and it is not taken again until the pause has elapsed. Without the second
// half, a database that is still down is a hot loop.
func TestConsumer_Requeue_ReturnsTheMessageAfterAPause(t *testing.T) {
	conn := openTestConn(t)
	topo := declaredTestTopology(t, conn)

	var (
		mu      sync.Mutex
		at      []time.Time
		handled = make(chan struct{}, 4)
	)
	runConsumer(t, topo, testRequeuePause, func(context.Context, string, []byte) Disposition {
		mu.Lock()
		at = append(at, time.Now())
		attempt := len(at)
		mu.Unlock()
		handled <- struct{}{}
		if attempt == 1 {
			return Requeue
		}
		return Ack
	})

	publish(t, conn, topo.Exchange, topo.RoutingKeys[0], []byte(`{"job_id":"job-1"}`))
	waitFor(t, handled, "the handler was never called")
	waitFor(t, handled, "the requeued message was never redelivered")

	mu.Lock()
	first, second := at[0], at[1]
	mu.Unlock()
	// Compared against most of the pause rather than all of it: the two
	// timestamps are taken inside the handler, on either side of a wait the
	// consumer starts slightly later, so an exact bound would be flaky by
	// construction while a consumer that skipped the pause entirely would
	// still fail this.
	if gap := second.Sub(first); gap < testRequeuePause*3/4 {
		t.Errorf("redelivered after %s, want at least %s — the requeue pause was not taken", gap, testRequeuePause*3/4)
	}

	expectDepth(t, conn, topo.WorkQueue, 0, "the redelivery was acknowledged, so the queue must drain")
	expectDepth(t, conn, topo.DeadQueue, 0, "a requeued message must not be dead-lettered")
}

// The scenario the spec states as declaring on every dial: a broker whose
// terminal exchange and queue were deleted underneath a running consumer.
// Deleting the queue cancels the consume and closes the delivery channel, so
// the consumer redials — and if it declared only once at startup, it would
// come back to consume from a queue that no longer exists and loop there
// forever.
func TestConsumer_RedeclaresTheTopologyAfterItIsDeleted(t *testing.T) {
	conn := openTestConn(t)
	topo := declaredTestTopology(t, conn)

	handled := make(chan string, 4)
	runConsumer(t, topo, testRequeuePause, func(_ context.Context, _ string, body []byte) Disposition {
		handled <- string(body)
		return Ack
	})

	publish(t, conn, topo.Exchange, topo.RoutingKeys[0], []byte(`before`))
	if got := waitForValue(t, handled, "the handler was never called"); got != "before" {
		t.Fatalf("first delivery body = %q, want %q", got, "before")
	}

	deleteTopology(t, conn, topo)

	// Published repeatedly rather than once after a wait, because there is no
	// moment the test can wait for. rabbitmq.DeclareTopology creates the work
	// queue before it binds it, so an exchange and a queue that both exist
	// still routes nothing for a short window — and a non-mandatory publish
	// into that window is discarded silently. Retrying is what makes the
	// assertion "consumption resumed" rather than "consumption resumed
	// within one racy attempt".
	if got := publishUntilHandled(t, conn, topo.Exchange, topo.RoutingKeys[1], []byte(`after`), handled); got != "after" {
		t.Fatalf("second delivery body = %q, want %q", got, "after")
	}
}

// Shutdown does not abandon the delivery in hand: Run returns only once the
// handler that is already running has reached a disposition. The property
// holds because dispatch is called synchronously from the consume loop, which
// is exactly the kind of thing a refactor moving it onto a goroutine would
// break with every other test in this file still green — and it is what the
// composition root's bounded drain is built on.
func TestConsumer_ShutdownWaitsForTheDeliveryInHand(t *testing.T) {
	conn := openTestConn(t)
	topo := declaredTestTopology(t, conn)

	entered := make(chan struct{}, 1)
	release := make(chan struct{})
	cancel, done := startConsumer(t, topo, testRequeuePause, func(context.Context, string, []byte) Disposition {
		entered <- struct{}{}
		<-release
		return Ack
	})

	publish(t, conn, topo.Exchange, topo.RoutingKeys[0], []byte(`{"type":"held"}`))
	waitFor(t, entered, "the handler was never entered")

	cancel()
	select {
	case <-done:
		t.Fatal("Run returned while the handler was still running: the delivery in hand was abandoned")
	case <-time.After(100 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(brokerSettleTimeout):
		t.Fatal("Run did not return after the handler reached a disposition")
	}

	expectDepth(t, conn, topo.WorkQueue, 0, "the held delivery was acknowledged before shutdown completed")
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

// declaredTestTopology names entities scoped to one test, declares them, and
// deletes them afterwards. The production names are never declared here: a
// test-sized bound left behind under a production name resurfaces as a
// PRECONDITION_FAILED on someone else's branch.
//
// Both routing keys are kept, and they are the production event types rather
// than test-scoped strings — the exchange is test-scoped, so nothing can
// collide, and the redeclare test needs a second key that is genuinely bound.
func declaredTestTopology(t *testing.T, conn *amqp.Connection) rabbitmq.Topology {
	t.Helper()
	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate topology suffix: %v", err)
	}
	prefix := "test." + strings.NewReplacer("/", "-", " ", "_").Replace(t.Name()) + "." + hex.EncodeToString(suffix)

	production := TerminalEventsTopology()
	topo := rabbitmq.Topology{
		Exchange:       prefix + ".exchange",
		RoutingKeys:    production.RoutingKeys,
		WorkQueue:      prefix + ".work",
		DeadExchange:   prefix + ".dlx",
		DeadQueue:      prefix + ".dead",
		WorkMaxLength:  10,
		DeadMessageTTL: time.Minute,
		DeadMaxLength:  10,
	}
	t.Cleanup(func() { deleteTopology(t, conn, topo) })

	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("declare topology: %v", err)
	}
	return topo
}

func deleteTopology(t *testing.T, conn *amqp.Connection, topo rabbitmq.Topology) {
	t.Helper()
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
}

// runConsumer starts a Consumer over topo and joins it at the end of the
// test, the way cmd/notifier will join it at shutdown.
func runConsumer(t *testing.T, topo rabbitmq.Topology, pause time.Duration, handle Handler) {
	t.Helper()
	startConsumer(t, topo, pause, handle)
}

// startConsumer is runConsumer with the cancel function and the goroutine's
// completion channel handed back, for the one test that has to cancel partway
// through and observe when Run returns. The cleanup still cancels and joins,
// so calling cancel early is safe and calling it not at all still stops the
// consumer.
func startConsumer(t *testing.T, topo rabbitmq.Topology, pause time.Duration, handle Handler) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	consumer := NewConsumer(rabbitmq.Config{URL: testBrokerURL(t)}, topo, "test-notifier", pause, handle)
	go func() {
		defer close(done)
		_ = consumer.Run(ctx)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(brokerSettleTimeout):
			t.Error("the consumer did not return after its context was cancelled")
		}
	})
	return cancel, done
}

func publish(t *testing.T, conn *amqp.Connection, exchange, routingKey string, body []byte) {
	t.Helper()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open publishing channel: %v", err)
	}
	defer func() { _ = ch.Close() }()
	if err := ch.PublishWithContext(context.Background(), exchange, routingKey, false, false,
		amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: body}); err != nil {
		t.Fatalf("publish to %s under %s: %v", exchange, routingKey, err)
	}
}

func waitFor[T any](t *testing.T, ch <-chan T, message string) {
	t.Helper()
	waitForValue(t, ch, message)
}

func waitForValue[T any](t *testing.T, ch <-chan T, message string) T {
	t.Helper()
	select {
	case v := <-ch:
		return v
	case <-time.After(brokerSettleTimeout):
		t.Fatal(message)
		var zero T
		return zero
	}
}

// expectDepth polls rather than reading once: a disposition is applied on the
// consumer's goroutine and reflected in the broker's counters a moment later,
// so a single read races the acknowledgement it is meant to observe.
func expectDepth(t *testing.T, conn *amqp.Connection, queue string, want int, why string) {
	t.Helper()
	deadline := time.Now().Add(brokerSettleTimeout)
	var got int
	for {
		got = queueDepth(t, conn, queue)
		if got == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s depth = %d, want %d — %s", queue, got, want, why)
		}
		time.Sleep(25 * time.Millisecond)
	}
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

// publishUntilHandled publishes body until the handler reports it, returning
// what the handler saw. Every attempt is best effort: while the topology is
// being recreated a publish may name an exchange that does not exist yet, and
// the broker answers that on the channel rather than to the caller.
func publishUntilHandled[T any](t *testing.T, conn *amqp.Connection, exchange, routingKey string, body []byte, handled <-chan T) T {
	t.Helper()
	deadline := time.Now().Add(brokerSettleTimeout)
	for {
		tryPublish(t, conn, exchange, routingKey, body)
		select {
		case v := <-handled:
			return v
		case <-time.After(250 * time.Millisecond):
		}
		if time.Now().After(deadline) {
			t.Fatal("consumption did not resume after the topology was recreated")
			var zero T
			return zero
		}
	}
}

func tryPublish(t *testing.T, conn *amqp.Connection, exchange, routingKey string, body []byte) {
	t.Helper()
	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open publishing channel: %v", err)
	}
	defer func() { _ = ch.Close() }()
	_ = ch.PublishWithContext(context.Background(), exchange, routingKey, false, false,
		amqp.Publishing{ContentType: "application/json", DeliveryMode: amqp.Persistent, Body: body})
}
