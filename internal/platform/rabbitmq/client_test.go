package rabbitmq_test

import (
	"strings"
	"testing"

	"video-processor/internal/platform/rabbitmq"
)

func TestOpen_SucceedsAgainstRunningBroker(t *testing.T) {
	conn := openTestConn(t)

	if conn == nil {
		t.Fatal("Open returned a nil connection with no error")
	}
	if conn.IsClosed() {
		t.Fatal("Open returned an already-closed connection")
	}
}

func TestOpen_FailsAgainstUnreachableBroker(t *testing.T) {
	// Distinctive userinfo appearing nowhere else, so the assertions below
	// cannot pass by accident. Both halves are checked: the spec forbids
	// exposing the credentials, not just the secret one.
	const (
		user = "unreachable-user-4f2a"
		pass = "unreachable-pass-9c71"
	)

	conn, err := rabbitmq.Open(rabbitmq.Config{URL: "amqp://" + user + ":" + pass + "@127.0.0.1:1/"})
	if err == nil {
		_ = rabbitmq.Close(conn)
		t.Fatal("expected an error dialing a port with no broker")
	}
	if conn != nil {
		t.Fatal("expected a nil connection alongside the error")
	}
	if strings.Contains(err.Error(), pass) {
		t.Fatalf("error leaks the URI password: %v", err)
	}
	if strings.Contains(err.Error(), user) {
		t.Fatalf("error leaks the URI username: %v", err)
	}
}

func TestPing_SucceedsAgainstLiveConnection(t *testing.T) {
	conn := openTestConn(t)

	if err := rabbitmq.Ping(conn); err != nil {
		t.Fatalf("Ping against a live connection: %v", err)
	}
}

// TestPing_FailsAgainstClosedConnection covers the case an IsClosed()-based
// implementation would also pass, and it is the strongest available here: a
// broker that has stopped answering *without* the connection being torn down
// — the case Ping's channel round trip exists to catch — cannot be forced
// from a test, because amqp091 exposes neither the socket nor a way to make
// the server hang. The round-trip requirement is stated normatively in the
// spec and enforced by review rather than by this assertion.
func TestPing_FailsAgainstClosedConnection(t *testing.T) {
	conn := openTestConn(t)

	if err := rabbitmq.Close(conn); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rabbitmq.Ping(conn); err == nil {
		t.Fatal("expected Ping to fail on a closed connection")
	}
}

func TestClose_ReleasesConnection(t *testing.T) {
	conn := openTestConn(t)

	if err := rabbitmq.Close(conn); err != nil {
		t.Fatalf("Close: %v", err)
	}
	// Proves release rather than just a nil return.
	if err := rabbitmq.Ping(conn); err == nil {
		t.Fatal("connection still usable after Close")
	}
}
