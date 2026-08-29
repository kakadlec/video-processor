package rabbitmq_test

import (
	"crypto/rand"
	"encoding/hex"
	"os"
	"strings"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/platform/rabbitmq"
)

// deadTTLForTests keeps the dead-letter TTL short enough that no test waits
// on it, while still being a real, assertable argument value.
const deadTTLForTests = time.Minute

// testURL skips the test unless RABBITMQ_TEST_URL is explicitly set, matching
// internal/platform/redis and internal/video/infrastructure/storage. The
// broker must be reached through a dedicated account: RabbitMQ confines the
// built-in "guest" user to loopback as the broker itself sees it, and every
// connection here arrives over a Docker network from another address.
func testURL(t *testing.T) string {
	t.Helper()

	url := os.Getenv("RABBITMQ_TEST_URL")
	if url == "" {
		t.Skip("RABBITMQ_TEST_URL not set; skipping RabbitMQ integration test")
	}
	return url
}

// openTestConn opens a connection for the test and closes it afterwards.
func openTestConn(t *testing.T) *amqp.Connection {
	t.Helper()

	conn, err := rabbitmq.Open(rabbitmq.Config{URL: testURL(t)})
	if err != nil {
		t.Fatalf("Open against the test broker: %v", err)
	}
	t.Cleanup(func() { _ = rabbitmq.Close(conn) })
	return conn
}

// testTopology builds a descriptor whose names are scoped to this test and
// deletes everything it named on cleanup, including when the test fails.
//
// Tests never declare messaging.JobDispatchTopology()'s names: a test-sized
// max-length left behind under a production name resurfaces as a
// PRECONDITION_FAILED on someone else's branch, and the overflow behavior
// needs a max-length of one message to be observable at all.
func testTopology(t *testing.T, conn *amqp.Connection) rabbitmq.Topology {
	t.Helper()

	suffix := make([]byte, 6)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatalf("generate topology suffix: %v", err)
	}
	prefix := "test." + sanitize(t.Name()) + "." + hex.EncodeToString(suffix)

	topo := rabbitmq.Topology{
		Exchange:       prefix + ".exchange",
		RoutingKey:     "test.work.queued",
		WorkQueue:      prefix + ".work",
		DeadExchange:   prefix + ".dlx",
		DeadQueue:      prefix + ".dead",
		WorkMaxLength:  10,
		DeadMessageTTL: deadTTLForTests,
		DeadMaxLength:  10,
	}

	t.Cleanup(func() {
		// A fresh channel: a failed declaration closes the one it happened
		// on, so the test's own channel may be unusable by now.
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

func sanitize(name string) string {
	return strings.NewReplacer("/", "-", " ", "_").Replace(name)
}
