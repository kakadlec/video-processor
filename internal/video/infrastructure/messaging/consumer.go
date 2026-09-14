package messaging

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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

// DefaultRequeuePause is the wait a Requeue disposition takes before the next
// delivery is accepted. It is a suggestion to the composition root rather
// than a value read from inside the Consumer, so a test can choose a shorter
// one; see NewConsumer.
//
// Five seconds, which is notification's consumer's value too. The symmetry is
// worth more than tuning either one: two consumers on one broker, one
// constant, one reason — and whatever the value, a dispatch resumes within
// one pause of the dependency coming back.
//
// It bounds each consumer's rate, not the message's, and the difference is
// the number to check a change to it against. Across N replicas the message
// is attempted up to N times per pause, so the average deployment-wide
// redelivery interval is roughly this value divided by N — under two seconds
// at the three workers docker-compose.yml runs. The attempts arrive as a
// burst rather than evenly spaced: a consumer in its pause still has a free
// prefetch slot, so the broker hands it the requeued message, which then
// waits in that consumer's buffer until the pause ends.
const DefaultRequeuePause = 5 * time.Second

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
	// again, so it would loop rather than recover. It covers a message
	// naming work this worker will never be able to perform, where Requeue
	// covers work it could not perform on this attempt because the
	// dependency it needed could not answer.
	Reject
	// Requeue returns the message to the queue and pauses before the next
	// one is taken.
	//
	// Reachable only where the handler has learned nothing about its
	// claim's outcome, having therefore run no extraction, acquired no
	// lease, read no source object and written no event. What licenses it
	// is that every state the claim could have reached is already owned: a
	// row still queued, which the same conditional claim decides on
	// redelivery, or a row left processing with no lease, which is what the
	// recovery sweeper exists to reach. After a claim reported won neither
	// holds, and Reject is the answer.
	Requeue
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
	config       rabbitmq.Config
	topology     rabbitmq.Topology
	handle       Handler
	tag          string
	requeuePause time.Duration
}

// NewConsumer wires a Consumer to the broker it dials, the topology it
// consumes from, the pause a Requeue takes, and the handler it delivers to.
// tag names this consumer to the broker; it appears in management listings
// and is otherwise inert.
//
// The topology and the pause are parameters rather than JobDispatchTopology()
// and DefaultRequeuePause read from inside. Both are composition-root
// decisions, and a consumer that reached for the production values itself
// could only ever be exercised against them — a Requeue's observable
// behaviour includes its wait, so a test that could not shorten it would be
// testing that Nack compiles.
func NewConsumer(config rabbitmq.Config, topology rabbitmq.Topology, tag string, requeuePause time.Duration, handle Handler) *Consumer {
	return &Consumer{
		config:       config,
		topology:     topology,
		handle:       handle,
		tag:          tag,
		requeuePause: requeuePause,
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
	lg := logger(componentJobConsumer)
	lg.Info("the job consumer started", slog.String("phase", phaseStarted))
	defer lg.Info("the job consumer stopped", slog.String("phase", phaseStopped))

	backoff := dialBackoffInitial
	for {
		if ctx.Err() != nil {
			return nil
		}

		conn, err := rabbitmq.Open(c.config)
		if err != nil {
			lg.Warn("connecting to the broker failed",
				slog.Duration("retry_in", backoff),
				slog.String("error", err.Error()))
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}
		lg.Info("the job consumer connected to the broker", slog.String("phase", phaseConnected))

		served, err := c.serve(ctx, conn)
		if err != nil {
			lg.Warn("the broker connection was lost",
				slog.String("phase", phaseConnectionLost),
				slog.String("error", err.Error()))
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
		lg.Warn("the broker connection was unusable",
			slog.Duration("retry_in", backoff))
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
					logger(componentJobConsumer).Error("requeueing a delivery on shutdown failed",
						slog.String("error", err.Error()))
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
//
// The Requeue pause is taken here, after the nack and before returning to the
// select. A nacked message goes back toward the head of the queue and is
// offered again at once, so this is the only position where the consumer
// holds nothing and has not yet asked for more work — before the nack it
// would withhold the message from every other replica for the whole pause,
// and outside dispatch it would be an idle wait every disposition pays.
//
// It observes ctx rather than the handler's detached context: by then the
// handler has returned and nothing is in hand to lose, so a shutdown should
// abandon the pause instead of spending the worker's drain on it.
func (c *Consumer) dispatch(ctx context.Context, delivery amqp.Delivery) {
	disposition := c.handle(context.WithoutCancel(ctx), delivery.Body)

	switch disposition {
	case Ack:
		if err := delivery.Ack(false); err != nil {
			logger(componentJobConsumer).Error("acknowledging the delivery failed",
				slog.String("error", err.Error()))
		}
	case Requeue:
		if err := delivery.Nack(false, true); err != nil {
			logger(componentJobConsumer).Error("requeueing the delivery failed",
				slog.String("error", err.Error()))
		}
		_ = sleepCtx(ctx, c.requeuePause)
	default:
		// requeue=false: the message goes to the dead-letter exchange, where it
		// can be looked at, rather than back onto the queue that would hand it
		// straight back.
		if err := delivery.Reject(false); err != nil {
			logger(componentJobConsumer).Error("rejecting the delivery failed",
				slog.String("error", err.Error()))
		}
	}
}
