package messaging

import (
	"context"
	"encoding/json"
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
// One, and not for throughput. A job holds a claimed database row and an
// ffmpeg process for as long as it runs, so a second prefetched delivery
// would sit unacknowledged behind work of unbounded duration — invisible to
// every other consumer, because a prefetched message is not redelivered
// elsewhere. With one, an idle worker is the only worker holding nothing, and
// scaling out is adding processes rather than raising this number.
const consumerPrefetch = 1

// JobQueuedMessage is the consumer half of the video_job.queued wire
// contract, whose producer half is postgres.videoJobQueuedPayload.
//
// The two are separate types in separate packages with no compiler
// enforcement between them — infrastructure adapters do not import one
// another — so a field renamed on one side would silently decode as its zero
// value on this one. TestJobQueuedMessageDecodesTheOutboxPayload is what
// pins them.
type JobQueuedMessage struct {
	Type        string    `json:"type"`
	JobID       string    `json:"job_id"`
	UserID      string    `json:"user_id"`
	SourceKey   string    `json:"source_key"`
	ContentHash string    `json:"content_hash"`
	OccurredAt  time.Time `json:"occurred_at"`
}

// ParseJobQueuedMessage decodes a delivery body. A body that will not decode
// is a permanent defect, not a transient one: redelivering it would produce
// the same failure forever.
func ParseJobQueuedMessage(body []byte) (JobQueuedMessage, error) {
	var msg JobQueuedMessage
	if err := json.Unmarshal(body, &msg); err != nil {
		return JobQueuedMessage{}, fmt.Errorf("video: consumer: decode job message: %w", err)
	}
	return msg, nil
}

// Disposition is a handler's verdict on one delivery.
type Disposition int

const (
	// Ack removes the message from the queue. It asserts that the work the
	// message described reached a terminal, committed outcome — not merely
	// that the handler returned.
	Ack Disposition = iota
	// Reject drops the message to the dead-letter exchange without
	// requeueing. Requeueing is never the right answer here: a redelivery
	// of a job whose row is already past queued can only lose the claim
	// again, so it would loop rather than recover.
	Reject
)

// Handler decides what becomes of one delivery. It is given a context
// detached from the consumer's own cancellation, so a shutdown signal does
// not kill an extraction that is already running.
//
// It carries the entire decision table. This package deliberately knows
// nothing about jobs, claims, or storage: it moves bytes and applies the
// verdict it is handed.
type Handler func(ctx context.Context, body []byte) Disposition

// Consumer delivers job messages from the dispatch topology's work queue to
// a Handler, one at a time, acknowledging each only as that Handler directs.
//
// Like Relay, it owns its connection rather than receiving one: broker
// reachability is not a startup gate, and an AMQP connection can drop at any
// time regardless, so the dial/redial loop has to exist here either way.
type Consumer struct {
	config   rabbitmq.Config
	topology rabbitmq.Topology
	handle   Handler
	tag      string
}

// NewConsumer wires a Consumer to the broker it dials, the topology it
// consumes from, and the handler it delivers to. tag names this consumer to
// the broker; it appears in management listings and is otherwise inert.
//
// The topology is a parameter rather than JobDispatchTopology() read from
// inside, matching Relay: the names are a composition-root decision, and a
// consumer that reached for the production names itself could only ever be
// exercised against them.
func NewConsumer(config rabbitmq.Config, topology rabbitmq.Topology, tag string, handle Handler) *Consumer {
	return &Consumer{
		config:   config,
		topology: topology,
		handle:   handle,
		tag:      tag,
	}
}

// Run consumes until ctx is cancelled, dialing the broker and redialing with
// backoff whenever the connection or the channel is lost.
//
// Cancellation stops it taking new work; it returns only once the delivery it
// was handling has been resolved and acknowledged, so a caller can join it
// before closing the database and storage handles that delivery borrows.
//
// It always returns nil, for the same reason Relay.Run does: every failure it
// can meet is a broker that will come back.
func (c *Consumer) Run(ctx context.Context) error {
	log.Print("video: job consumer: started")
	defer log.Print("video: job consumer: stopped")

	backoff := dialBackoffInitial
	for {
		if ctx.Err() != nil {
			return nil
		}

		conn, err := rabbitmq.Open(c.config)
		if err != nil {
			log.Printf("video: job consumer: connect: %v; retrying in %s", err, backoff)
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}
		log.Print("video: job consumer: connected")

		served, err := c.serve(ctx, conn)
		if err != nil {
			log.Printf("video: job consumer: connection lost: %v", err)
		}
		_ = rabbitmq.Close(conn)
		if ctx.Err() != nil {
			return nil
		}

		// Reset by a connection that actually delivered, not by one that
		// merely dialed — the same distinction Relay.Run draws, and for the
		// same reason: a connection that fails right after every dial would
		// otherwise redial in a tight loop.
		if served {
			backoff = dialBackoffInitial
			continue
		}
		log.Printf("video: job consumer: connection was unusable; retrying in %s", backoff)
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
// an idle wait — that is, whether it proved usable rather than merely
// dialable.
func (c *Consumer) serve(ctx context.Context, conn *amqp.Connection) (bool, error) {
	// Declared on every dial, exactly as the relay does it and for the same
	// reason: against a fresh or recreated broker the queue would otherwise
	// not exist, and a consume on a missing queue closes the channel.
	// Declaring from both sides also means a worker started before any API
	// replica still has somewhere to listen.
	if err := rabbitmq.DeclareTopology(conn, c.topology); err != nil {
		return false, err
	}

	ch, err := conn.Channel()
	if err != nil {
		return false, fmt.Errorf("video: job consumer: open channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	if err := ch.Qos(consumerPrefetch, 0, false); err != nil {
		return false, fmt.Errorf("video: job consumer: set qos: %w", err)
	}

	deliveries, err := ch.Consume(c.topology.WorkQueue, c.tag, false, false, false, false, nil)
	if err != nil {
		return false, fmt.Errorf("video: job consumer: consume %s: %w", c.topology.WorkQueue, err)
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
				return served, errors.New("video: job consumer: broker connection closed")
			}
			return served, amqpErr
		case amqpErr, ok := <-chClosed:
			if !ok {
				return served, errors.New("video: job consumer: consuming channel closed")
			}
			return served, amqpErr
		case delivery, ok := <-deliveries:
			if !ok {
				return served, errors.New("video: job consumer: deliveries channel closed")
			}
			// A cancelled context and a buffered delivery race in the select
			// above, and Go picks between ready cases at random. Checking
			// here is what makes "no further job is started after the
			// signal" true rather than usually true. Requeueing is right on
			// this one path and only this one: nothing has been done to the
			// job, so another worker — or this one after a restart — can
			// take it intact.
			if ctx.Err() != nil {
				if err := delivery.Nack(false, true); err != nil {
					log.Printf("video: job consumer: requeue on shutdown: %v", err)
				}
				return served, nil
			}
			c.dispatch(ctx, delivery)
		}
	}
}

// dispatch hands one delivery to the handler and applies its verdict.
//
// The handler runs on a context detached from ctx: a shutdown signal must not
// kill an ffmpeg run or, worse, abort the database write that records its
// outcome. Bounding how long the process waits for that is the caller's job,
// not this loop's.
func (c *Consumer) dispatch(ctx context.Context, delivery amqp.Delivery) {
	disposition := c.handle(context.WithoutCancel(ctx), delivery.Body)

	if disposition == Ack {
		if err := delivery.Ack(false); err != nil {
			log.Printf("video: job consumer: ack: %v", err)
		}
		return
	}
	// requeue=false: the message goes to the dead-letter exchange, where it
	// can be looked at, rather than back onto the queue that would hand it
	// straight back.
	if err := delivery.Reject(false); err != nil {
		log.Printf("video: job consumer: reject: %v", err)
	}
}
