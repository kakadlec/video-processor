package messaging

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/platform/rabbitmq"
)

// consumerPrefetch is the number of unacknowledged deliveries the broker may
// hand one consumer.
//
// One, and the reason differs from the worker's. There the constraint is
// duration: a prefetched delivery would sit behind an extraction of unbounded
// length. Here it is the claim: a delivery held unacknowledged behind the one
// being handled is invisible to every other notifier, and this consumer's
// recovery from a stalled peer is a requeue, which only helps a message the
// broker is free to hand elsewhere.
const consumerPrefetch = 1

const (
	dialBackoffInitial = 1 * time.Second
	dialBackoffMax     = 30 * time.Second
)

// DefaultRequeuePause is the wait a Requeue disposition takes before the next
// delivery is accepted. It is a suggestion to the composition root rather
// than a value read from inside the Consumer, so a test can choose a shorter
// one; see NewConsumer.
//
// Seconds, not milliseconds: the two situations that requeue are a database
// that is down and a claim another consumer still holds, and neither clears
// in the time a tight loop takes to come back around. Nor minutes: with a
// prefetch of one the requeued message returns to the head of the queue and
// is taken again by this same consumer, so the pause is also the granularity
// at which an abandoned claim's head-of-line block is retested.
const DefaultRequeuePause = 5 * time.Second

// Disposition is a handler's verdict on one delivery.
//
// Three values where internal/video/infrastructure/messaging has two, and the
// third is the point rather than an inconsistency. A requeued video job meets
// a row that has already moved past queued and can only lose the claim again,
// so redelivery loops instead of recovering. A requeued terminal event meets
// a handler that has attempted nothing, blocked by a condition — an
// unreachable database, a claim held by a peer — that resolves itself.
type Disposition int

// Numbered from one, so the zero value is no disposition at all rather than
// Ack — the same reason domain.ClaimGranted is. Ack is the destructive
// verdict here: the message leaves the queue with no dead-letter trace and
// the notification is never sent, which is exactly the silent failure the
// disposition table exists to prevent. A Disposition that arrived zero by
// accident — a table lookup that missed, a variable one branch forgot to
// assign — would take that path. It instead falls to dispatch's default and
// is dead-lettered, where it is visible.
//
// This is a deliberate difference from internal/video/infrastructure/
// messaging, whose Ack is zero. An acked video job is at least terminal in
// PostgreSQL; an acked terminal event leaves nothing anywhere.
const (
	// Ack removes the message from the queue. It asserts that the event
	// reached a settled outcome: delivered, deliberately not delivered, or
	// delivered and unrecordable. It does not merely assert that the handler
	// returned.
	Ack Disposition = iota + 1
	// Reject drops the message to the dead-letter exchange without
	// requeueing. It is for a message this consumer can never make sense of
	// — an undecodable body, an unrecognized event type — where redelivery
	// reproduces the same failure forever.
	Reject
	// Requeue returns the message to the queue and pauses before the next
	// one is taken.
	//
	// Reachable only before this handler has attempted anything. Once it
	// holds a claim, a redelivery meeting that row would be refused and
	// requeued indefinitely rather than resolving anything — so requeueing
	// after a failed recording would discard the outcome permanently, and
	// could send the webhook a second time first.
	Requeue
)

// Handler decides what becomes of one delivery. It is given the delivery's
// routing key, which the publisher sets equal to the event type it stored,
// and the raw body — enough to choose which message type to decode into
// without unmarshalling twice.
//
// It runs on a context detached from the consumer's own cancellation, so a
// termination signal neither aborts an outbound request mid-flight nor
// prevents its outcome from being recorded.
//
// It carries the entire decision table. This package deliberately knows
// nothing about preferences, claims, or destinations: it moves bytes and
// applies the verdict it is handed.
type Handler func(ctx context.Context, eventType string, body []byte) Disposition

// Consumer delivers terminal events from the terminal topology's queue to a
// Handler, one at a time, acknowledging each only as that Handler directs.
//
// Like the Video Processing consumer it owns its connection rather than
// receiving one: broker reachability is not a startup gate, and an AMQP
// connection can drop at any time regardless, so the dial/redial loop has to
// exist here either way.
type Consumer struct {
	config       rabbitmq.Config
	topology     rabbitmq.Topology
	tag          string
	requeuePause time.Duration
	handle       Handler
}

// NewConsumer wires a Consumer to the broker it dials, the topology it
// consumes from, the pause a Requeue takes, and the handler it delivers to.
// tag names this consumer to the broker; it appears in management listings
// and is otherwise inert.
//
// The topology and the pause are parameters rather than TerminalEventsTopology()
// and DefaultRequeuePause read from inside. Both are composition-root
// decisions, and a consumer that reached for the production values itself
// could only ever be exercised against them — a Requeue's observable
// behaviour includes its wait, so a test that could not shorten it would be
// testing that Nack compiles.
func NewConsumer(config rabbitmq.Config, topology rabbitmq.Topology, tag string, requeuePause time.Duration, handle Handler) *Consumer {
	return &Consumer{
		config:       config,
		topology:     topology,
		tag:          tag,
		requeuePause: requeuePause,
		handle:       handle,
	}
}

// Run consumes until ctx is cancelled, dialing the broker and redialing with
// bounded backoff whenever the connection or the channel is lost.
//
// Cancellation stops it taking new work; it returns only once the delivery it
// was handling has reached a disposition, so a caller can join it before
// closing the database pool that delivery borrows. Bounding that join is the
// caller's job — this loop does not impose a drain of its own.
//
// It always returns nil: every failure it can meet is a broker that will come
// back.
func (c *Consumer) Run(ctx context.Context) error {
	log.Print("notification: terminal event consumer: started")
	defer log.Print("notification: terminal event consumer: stopped")

	backoff := dialBackoffInitial
	for {
		if ctx.Err() != nil {
			return nil
		}

		conn, err := rabbitmq.Open(c.config)
		if err != nil {
			log.Printf("notification: terminal event consumer: connect: %v; retrying in %s", err, backoff)
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}
		log.Print("notification: terminal event consumer: connected")

		served, err := c.serve(ctx, conn)
		if err != nil {
			log.Printf("notification: terminal event consumer: connection lost: %v", err)
		}
		_ = rabbitmq.Close(conn)
		if ctx.Err() != nil {
			return nil
		}

		// Reset by a connection that actually served, not by one that merely
		// dialed: a connection failing right after every dial would otherwise
		// redial in a tight loop.
		if served {
			backoff = dialBackoffInitial
			continue
		}
		log.Printf("notification: terminal event consumer: connection was unusable; retrying in %s", backoff)
		if !sleepCtx(ctx, backoff) {
			return nil
		}
		backoff = nextBackoff(backoff)
	}
}

// serve declares the topology, starts consuming, and dispatches deliveries
// until the connection or the channel fails, or ctx is cancelled.
//
// The bool reports whether this connection ever handled a delivery or reached
// an idle wait — whether it proved usable rather than merely dialable.
func (c *Consumer) serve(ctx context.Context, conn *amqp.Connection) (bool, error) {
	// Declared on every dial, exactly as the publishing relay does it and for
	// the same reason: against a fresh or recreated broker the queue would
	// otherwise not exist, and a consume on a missing queue closes the
	// channel. Declaring from both sides also means neither process's startup
	// depends on the other's order.
	if err := rabbitmq.DeclareTopology(conn, c.topology); err != nil {
		return false, err
	}

	ch, err := conn.Channel()
	if err != nil {
		return false, fmt.Errorf("notification: terminal event consumer: open channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	if err := ch.Qos(consumerPrefetch, 0, false); err != nil {
		return false, fmt.Errorf("notification: terminal event consumer: set qos: %w", err)
	}

	deliveries, err := ch.Consume(c.topology.WorkQueue, c.tag, false, false, false, false, nil)
	if err != nil {
		return false, fmt.Errorf("notification: terminal event consumer: consume %s: %w", c.topology.WorkQueue, err)
	}

	connClosed := conn.NotifyClose(make(chan *amqp.Error, 1))
	chClosed := ch.NotifyClose(make(chan *amqp.Error, 1))

	served := true
	for {
		select {
		case <-ctx.Done():
			return served, nil
		case amqpErr, ok := <-connClosed:
			if !ok {
				return served, errors.New("notification: terminal event consumer: broker connection closed")
			}
			return served, amqpErr
		case amqpErr, ok := <-chClosed:
			if !ok {
				return served, errors.New("notification: terminal event consumer: consuming channel closed")
			}
			return served, amqpErr
		case delivery, ok := <-deliveries:
			if !ok {
				return served, errors.New("notification: terminal event consumer: deliveries channel closed")
			}
			// A cancelled context and a buffered delivery race in the select
			// above, and Go picks between ready cases at random. Checking
			// here is what makes "no further event is started after the
			// signal" true rather than usually true. Requeueing is right on
			// this path because it is the same pre-attempt position the
			// Requeue disposition describes: nothing was read, nothing was
			// claimed, nothing was sent.
			if ctx.Err() != nil {
				if err := delivery.Nack(false, true); err != nil {
					log.Printf("notification: terminal event consumer: requeue on shutdown: %v", err)
				}
				return served, nil
			}
			c.dispatch(ctx, delivery)
		}
	}
}

// dispatch hands one delivery to the handler and applies its verdict.
//
// The handler runs on a context detached from ctx: a termination signal must
// not abort a webhook request in flight or, worse, the write that records its
// outcome.
//
// The Requeue pause is taken here, after the nack and before returning to the
// select, because that is what makes it a pause before the *next* delivery
// rather than an idle wait somewhere else. It observes ctx, so a shutdown
// during it returns immediately — nothing is in hand to lose.
func (c *Consumer) dispatch(ctx context.Context, delivery amqp.Delivery) {
	disposition := c.handle(context.WithoutCancel(ctx), delivery.RoutingKey, delivery.Body)

	switch disposition {
	case Ack:
		if err := delivery.Ack(false); err != nil {
			log.Printf("notification: terminal event consumer: ack: %v", err)
		}
	case Requeue:
		if err := delivery.Nack(false, true); err != nil {
			log.Printf("notification: terminal event consumer: requeue: %v", err)
		}
		_ = sleepCtx(ctx, c.requeuePause)
	default:
		// Reject, and every value that is not a disposition — see the
		// Disposition doc for why the unset one lands here rather than on
		// Ack. requeue=false: the message goes to the dead-letter exchange,
		// where it can be looked at, rather than back onto the queue that
		// would hand it straight back.
		if err := delivery.Reject(false); err != nil {
			log.Printf("notification: terminal event consumer: reject: %v", err)
		}
	}
}

// sleepCtx waits for d, reporting false if ctx ended first.
//
// This context's own copy of a helper Video Processing's messaging package
// also has. Sharing it would mean either an import ddd-architecture forbids
// or a home in internal/platform, which is confined to connection and
// lifecycle plumbing — a consumer loop's retry policy is neither.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	next := current * 2
	if next > dialBackoffMax {
		return dialBackoffMax
	}
	return next
}
