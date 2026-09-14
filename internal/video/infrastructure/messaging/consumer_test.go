package messaging

import (
	"context"
	"sync"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/platform/rabbitmq"
)

// testRequeuePause is the pause a Requeue takes in the test that measures it.
// Short enough not to add a visible amount to the suite, long enough that the
// gap it produces between two handler calls is not lost in scheduling noise.
const testRequeuePause = 300 * time.Millisecond

// abandonedRequeuePause is deliberately far longer than any test would wait
// for. The shutdown test asserts the consumer returns in a small fraction of
// it, so the gap between the two numbers is the whole discriminator: a pause
// taken on the handler's detached context would run this to completion.
const abandonedRequeuePause = 30 * time.Second

// The window every "the broker eventually reports this" assertion is given.
// Generous on purpose: what is being tested is which disposition was applied,
// never how fast a broker got around to reflecting it.
const brokerSettleTimeout = 10 * time.Second

// Ack is this package's zero Disposition, unlike notification's, and the
// switch dispatch applies must keep it that way: a handler returning Ack
// removes the message and dead-letters nothing.
func TestConsumer_Ack_RemovesTheMessage(t *testing.T) {
	conn := openTestConn(t)
	topo := declaredTestTopology(t, conn)

	handled := make(chan string, 4)
	runConsumer(t, topo, testRequeuePause, func(_ context.Context, body []byte) Disposition {
		handled <- string(body)
		return Ack
	})

	publish(t, conn, topo.Exchange, topo.RoutingKeys[0], []byte(`{"job_id":"job-1"}`))
	if got := waitForValue(t, handled, "the handler was never called"); got != `{"job_id":"job-1"}` {
		t.Errorf("handler saw body %q, want the published one", got)
	}

	expectDepth(t, conn, topo.WorkQueue, 0, "an acknowledged message must leave the work queue")
	expectDepth(t, conn, topo.DeadQueue, 0, "an acknowledged message must not be dead-lettered")
}

// The negative half of the requeue change, and the reason it is here rather
// than assumed: dispatch became a switch, and a Reject that started requeueing
// would leave every permanent sentinel looping instead of dead-lettering.
func TestConsumer_Reject_DeadLettersWithoutRequeue(t *testing.T) {
	conn := openTestConn(t)
	topo := declaredTestTopology(t, conn)

	var (
		mu      sync.Mutex
		calls   int
		handled = make(chan struct{}, 4)
	)
	runConsumer(t, topo, testRequeuePause, func(context.Context, []byte) Disposition {
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

	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Errorf("handler was called %d times, want 1 — a rejected message must not be redelivered", calls)
	}
}

// The requeue disposition has two observable halves, and asserting only the
// first would let a consumer that never paused pass: the message comes back,
// and it is not taken again until the pause has elapsed. Without the second
// half a database that is still down is a hot loop against three replicas.
func TestConsumer_Requeue_ReturnsTheMessageAfterAPause(t *testing.T) {
	conn := openTestConn(t)
	topo := declaredTestTopology(t, conn)

	var (
		mu      sync.Mutex
		at      []time.Time
		handled = make(chan struct{}, 4)
	)
	runConsumer(t, topo, testRequeuePause, func(context.Context, []byte) Disposition {
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
	// Measured inside the handler, on either side of a wait the consumer
	// starts slightly later — and never at the broker, whose redelivery may
	// well be sitting in the client's delivery channel while the consumer is
	// still paused. Compared against most of the pause rather than all of it,
	// because an exact bound would be flaky by construction while a consumer
	// that skipped the pause entirely still fails this.
	if gap := second.Sub(first); gap < testRequeuePause*3/4 {
		t.Errorf("redelivered after %s, want at least %s — the requeue pause was not taken", gap, testRequeuePause*3/4)
	}

	expectDepth(t, conn, topo.WorkQueue, 0, "the redelivery was acknowledged, so the queue must drain")
	expectDepth(t, conn, topo.DeadQueue, 0, "a requeued message must not be dead-lettered")
}

// The pause runs on the consumer's own cancellable context, not on the
// detached one the handler runs under. By the time it starts the handler has
// returned and nothing is in hand to lose, so shutdown must abandon it rather
// than spend the worker's bounded drain waiting it out.
//
// The assertion is a gap, not a deadline: the pause is thirty seconds and the
// consumer is given two to stop. Nothing about scheduling closes that gap, and
// a pause taken on the detached context cannot survive it.
func TestConsumer_Requeue_ShutdownAbandonsThePause(t *testing.T) {
	conn := openTestConn(t)
	topo := declaredTestTopology(t, conn)

	requeued := make(chan struct{}, 1)
	cancel, done := startConsumer(t, topo, abandonedRequeuePause, func(context.Context, []byte) Disposition {
		requeued <- struct{}{}
		return Requeue
	})

	publish(t, conn, topo.Exchange, topo.RoutingKeys[0], []byte(`{"job_id":"job-1"}`))
	waitFor(t, requeued, "the handler was never called")

	// No race to manage: the nack is not guarded by ctx, so cancelling before
	// it lands still requeues the message, and cancelling after it lands ends
	// the pause. Either ordering satisfies both assertions below.
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the consumer did not stop within 2s: the requeue pause was run out rather than abandoned")
	}

	expectDepth(t, conn, topo.WorkQueue, 1, "a requeued message must be back on the work queue")
	expectDepth(t, conn, topo.DeadQueue, 0, "a requeued message must not be dead-lettered")
}

// declaredTestTopology names test-scoped entities and declares them, so the
// first publish cannot name an exchange that does not exist yet. testTopology
// only names and cleans up — Relay's tests declare through declaredPublisher,
// and a consumer's do it here — and a non-mandatory publish to a missing
// exchange is reported asynchronously on the channel rather than to the
// caller, so skipping this loses the message silently.
func declaredTestTopology(t *testing.T, conn *amqp.Connection) rabbitmq.Topology {
	t.Helper()
	topo := testTopology(t, conn, 10)
	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("declare topology: %v", err)
	}
	return topo
}

// runConsumer starts a Consumer over topo and joins it at the end of the test,
// the way cmd/worker joins it at shutdown.
func runConsumer(t *testing.T, topo rabbitmq.Topology, pause time.Duration, handle Handler) {
	t.Helper()
	startConsumer(t, topo, pause, handle)
}

// startConsumer is runConsumer with the cancel function and the goroutine's
// completion channel handed back, for the test that has to cancel partway
// through and observe when Run returns. The cleanup still cancels and joins,
// so calling cancel early is safe and calling it not at all still stops the
// consumer.
func startConsumer(t *testing.T, topo rabbitmq.Topology, pause time.Duration, handle Handler) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	consumer := NewConsumer(rabbitmq.Config{URL: testBrokerURL(t)}, topo, "test-worker", pause, handle)
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
