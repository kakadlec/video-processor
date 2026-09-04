package rabbitmq_test

import (
	"context"
	"testing"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/platform/rabbitmq"
)

func TestDeclareTopology_IsIdempotent(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn)

	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("first DeclareTopology: %v", err)
	}
	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("second DeclareTopology: %v", err)
	}
}

func TestDeclareTopology_DeclaresEveryEntity(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn)

	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	ch := openChannel(t, conn)
	for _, x := range []struct {
		name string
		kind string
	}{
		{topo.DeadExchange, amqp.ExchangeFanout},
		{topo.Exchange, amqp.ExchangeDirect},
	} {
		if err := ch.ExchangeDeclarePassive(x.name, x.kind, true, false, false, false, nil); err != nil {
			t.Fatalf("exchange %s (%s) not declared as expected: %v", x.name, x.kind, err)
			return
		}
	}
	for _, q := range []string{topo.DeadQueue, topo.WorkQueue} {
		if _, err := ch.QueueDeclarePassive(q, true, false, false, false, nil); err != nil {
			t.Fatalf("queue %s not declared: %v", q, err)
		}
	}
}

// TestDeclareTopology_ConflictingRedeclarationIsRejected is what pins the
// declared argument set against something outside this package. Without it
// the descriptor would be asserted only against itself.
func TestDeclareTopology_ConflictingRedeclarationIsRejected(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn)

	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	conflicting := topo
	conflicting.WorkMaxLength = topo.WorkMaxLength + 1
	if err := rabbitmq.DeclareTopology(conn, conflicting); err == nil {
		t.Fatal("expected the broker to reject a redeclaration with a different max length")
	}
}

func TestDeclareTopology_FailsAgainstClosedConnection(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn)

	if err := rabbitmq.Close(conn); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := rabbitmq.DeclareTopology(conn, topo); err == nil {
		t.Fatal("expected DeclareTopology to fail on a closed connection")
	}
}

// TestWorkQueueOverflowIsRejected is the only test that proves reject-publish
// was applied rather than the default drop-head.
//
// Confirm mode is not optional here: basic.publish is asynchronous and
// unacknowledged by default, so PublishWithContext returns nil whether the
// broker accepted the message or refused it. A test reading that nil as
// acceptance would pass against a drop-head queue.
func TestWorkQueueOverflowIsRejected(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn)
	topo.WorkMaxLength = 1

	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	ch := openChannel(t, conn)
	if err := ch.Confirm(false); err != nil {
		t.Fatalf("enable publisher confirms: %v", err)
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 2))

	if ack := publish(t, ch, confirms, topo, "first"); !ack {
		t.Fatal("first publish into an empty queue was refused")
	}
	waitForDepth(t, conn, topo.WorkQueue, 1)

	if ack := publish(t, ch, confirms, topo, "second"); ack {
		t.Fatal("publish into a full queue was accepted: overflow is not reject-publish")
	}

	// The queued message is untouched — a drop-head queue would have
	// evicted it to make room.
	if depth := queueDepth(t, conn, topo.WorkQueue); depth != 1 {
		t.Fatalf("work queue depth = %d after a refused publish, want 1", depth)
	}
	// reject-publish, not reject-publish-dlx: the refused message is
	// discarded rather than deposited in the dead-letter queue, so a
	// retrying publisher cannot fill that queue with duplicates.
	if depth := queueDepth(t, conn, topo.DeadQueue); depth != 0 {
		t.Fatalf("dead-letter queue depth = %d, want 0: overflow is dead-lettering", depth)
	}
}

// TestWorkQueueHasNoMessageTTL guards a deliberate omission, which is exactly
// the kind of decision a later "surely this should expire" edit reverses
// without noticing. An expired job message is dead-lettered without any
// update to the video_jobs row, and the state machine has no edge out of
// "queued" except to "processing" — so the job would report "queued" forever.
//
// Absence is asserted the only way AMQP allows: redeclaring with the argument
// present must be rejected, which is only true if the live queue lacks it.
func TestWorkQueueHasNoMessageTTL(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn)

	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	ch := openChannel(t, conn)
	_, err := ch.QueueDeclare(topo.WorkQueue, true, false, false, false, amqp.Table{
		"x-max-length":           int64(topo.WorkMaxLength),
		"x-overflow":             "reject-publish",
		"x-dead-letter-exchange": topo.DeadExchange,
		"x-message-ttl":          int64(60000),
	})
	if err == nil {
		t.Fatal("redeclaring with an x-message-ttl succeeded: the work queue carries one")
	}
}

func TestDeadLetterQueueForwardsNowhere(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn)

	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	ch := openChannel(t, conn)
	_, err := ch.QueueDeclare(topo.DeadQueue, true, false, false, false, amqp.Table{
		"x-message-ttl":          int64(topo.DeadMessageTTL / time.Millisecond),
		"x-max-length":           int64(topo.DeadMaxLength),
		"x-overflow":             "drop-head",
		"x-dead-letter-exchange": topo.Exchange,
	})
	if err == nil {
		t.Fatal("redeclaring with an x-dead-letter-exchange succeeded: the dead-letter queue forwards somewhere")
	}
}

func openChannel(t *testing.T, conn *amqp.Connection) *amqp.Channel {
	t.Helper()

	ch, err := conn.Channel()
	if err != nil {
		t.Fatalf("open channel: %v", err)
	}
	t.Cleanup(func() { _ = ch.Close() })
	return ch
}

func publish(t *testing.T, ch *amqp.Channel, confirms <-chan amqp.Confirmation, topo rabbitmq.Topology, body string) bool {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	if err := ch.PublishWithContext(ctx, topo.Exchange, topo.RoutingKeys[0], false, false, amqp.Publishing{
		DeliveryMode: amqp.Persistent,
		Body:         []byte(body),
	}); err != nil {
		t.Fatalf("publish %q: %v", body, err)
	}

	select {
	case confirmation := <-confirms:
		return confirmation.Ack
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out awaiting the broker's confirmation for %q", body)
		return false
	}
}

func queueDepth(t *testing.T, conn *amqp.Connection, queue string) int {
	t.Helper()

	// Its own channel: a passive declare against a missing queue closes the
	// channel it ran on, and callers here may query more than once.
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

func waitForDepth(t *testing.T, conn *amqp.Connection, queue string, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		if depth := queueDepth(t, conn, queue); depth == want {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("queue %s never reached depth %d", queue, want)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// TestDeclareTopology_BindsEveryRoutingKeyToOneQueue is the descriptor's
// multi-binding contract. A topology whose queue must receive several event
// types gets one binding per key, and every one of them routes.
func TestDeclareTopology_BindsEveryRoutingKeyToOneQueue(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn)
	const secondKey = "test.work.second"
	topo.RoutingKeys = append(topo.RoutingKeys, secondKey)

	if err := rabbitmq.DeclareTopology(conn, topo); err != nil {
		t.Fatalf("DeclareTopology: %v", err)
	}

	ch := openChannel(t, conn)
	if err := ch.Confirm(false); err != nil {
		t.Fatalf("enable confirm mode: %v", err)
	}
	confirms := ch.NotifyPublish(make(chan amqp.Confirmation, 2))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	for _, key := range topo.RoutingKeys {
		if err := ch.PublishWithContext(ctx, topo.Exchange, key, true, false, amqp.Publishing{
			DeliveryMode: amqp.Persistent,
			Body:         []byte(key),
		}); err != nil {
			t.Fatalf("publish under %q: %v", key, err)
		}
		select {
		case confirmation := <-confirms:
			if !confirmation.Ack {
				t.Fatalf("publish under %q was nacked", key)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out awaiting the confirmation for %q", key)
		}
	}

	if depth := queueDepth(t, conn, topo.WorkQueue); depth != len(topo.RoutingKeys) {
		t.Fatalf("work queue depth = %d, want %d — one message per bound key", depth, len(topo.RoutingKeys))
	}
}

// TestDeclareTopology_RefusesADescriptorWithNoRoutingKey pins the rejection
// rather than the silence. The work exchange is direct, so a queue with no
// binding receives nothing and every mandatory publish comes back unroutable
// — a topology that declares "successfully" and then routes nothing at all.
//
// It also asserts nothing was declared, which is what makes the refusal
// useful: a partially declared topology would be indistinguishable from a
// complete one until the first publish.
func TestDeclareTopology_RefusesADescriptorWithNoRoutingKey(t *testing.T) {
	conn := openTestConn(t)
	topo := testTopology(t, conn)
	topo.RoutingKeys = nil

	if err := rabbitmq.DeclareTopology(conn, topo); err == nil {
		t.Fatal("DeclareTopology succeeded with no routing key")
	}

	// A passive declare of a queue that does not exist fails and closes the
	// channel it ran on, so each check gets its own.
	for _, queue := range []string{topo.WorkQueue, topo.DeadQueue} {
		ch, err := conn.Channel()
		if err != nil {
			t.Fatalf("open channel: %v", err)
		}
		_, err = ch.QueueDeclarePassive(queue, true, false, false, false, nil)
		_ = ch.Close()
		if err == nil {
			t.Errorf("queue %s exists; a refused declaration must declare nothing", queue)
		}
	}
}
