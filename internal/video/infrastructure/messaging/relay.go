package messaging

import (
	"context"
	"errors"
	"log"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"

	"video-processor/internal/platform/rabbitmq"
	"video-processor/internal/video/infrastructure/postgres"
)

// Relay pacing and batching. All compile-time constants rather than
// environment variables, matching the status cache's fixed TTL: none of them
// trades correctness for anything an operator would need to tune.
//
// maxPublishBatch (see publisher.go) bounds the claim, because the rows stay
// locked across the broker round trip below.
const (
	// pollInterval is chosen so a restarting replica costs seconds of
	// dispatch latency, not minutes — that latency lands in the user's wait
	// once the cutover makes processing asynchronous.
	pollInterval = 2 * time.Second
	// publishTimeout bounds the confirmation wait, and with it how long one
	// cycle can hold its row locks.
	publishTimeout = 15 * time.Second

	dialBackoffInitial = 1 * time.Second
	dialBackoffMax     = 30 * time.Second
)

// outboxClaimer is the slice of postgres.OutboxRepository this relay uses.
// Narrow by intent rather than for substitutability: the batch it hands back
// owns a real database transaction, so the tests below run against a real
// PostgreSQL, exactly like the adapter's own.
type outboxClaimer interface {
	Claim(ctx context.Context, eventTypes []string, limit int) (*postgres.OutboxBatch, error)
}

// Relay carries one closed set of outbox event types to the broker.
//
// Two of them run: the dispatch relay in cmd/api carries video_job.queued.v2,
// and the terminal relay in cmd/worker carries the two terminal events. Their
// sets are disjoint, which is what makes running both against the one
// video_job_outbox table free of contention — neither claims the other's rows
// and neither's backlog can starve the other's.
//
// It owns its connection rather than receiving one: the composition root
// must not treat broker reachability as a startup gate (see design.md
// decision 6), and an AMQP connection can drop at any time regardless, so
// the dial/redial loop has to exist here either way.
//
// Delivery is at least once. A row is stamped published only after the
// broker has both acknowledged and routed it, inside the same transaction
// that claimed it — so a crash between the acknowledgement and the commit
// republishes rather than losing the dispatch. The reverse ordering, which
// commits the claim before publishing, drops a dispatch permanently and
// silently on the same crash.
type Relay struct {
	outbox     outboxClaimer
	config     rabbitmq.Config
	eventTypes []string
	topology   rabbitmq.Topology
}

// NewRelay wires the job-dispatch relay to the outbox it drains and the
// broker it dials. Its set has one element, which is the shape every relay
// had before a second stream existed.
func NewRelay(outbox outboxClaimer, config rabbitmq.Config) *Relay {
	return &Relay{
		outbox:     outbox,
		config:     config,
		eventTypes: []string{postgres.VideoJobQueuedEventType},
		topology:   JobDispatchTopology(),
	}
}

// NewTerminalRelay wires the terminal-event relay. It carries both terminal
// event types on one connection and into one queue, because a consumer
// interested in how a job ended is interested in either outcome; splitting
// them would mean two relays and two connections for one logical stream.
func NewTerminalRelay(outbox outboxClaimer, config rabbitmq.Config) *Relay {
	return &Relay{
		outbox:     outbox,
		config:     config,
		eventTypes: []string{postgres.VideoJobCompletedEventType, postgres.VideoJobFailedEventType},
		topology:   TerminalEventsTopology(),
	}
}

// Run polls until ctx is cancelled, dialing the broker and redialing with
// backoff whenever the connection or the publishing channel is lost. It
// returns only after any in-flight claim has been resolved, so a caller can
// join it before closing the database handle it borrows.
//
// It always returns nil. Every failure it can meet is transient by
// construction — a broker that is down is a broker that will come back — and
// returning an error would only give the caller a reason to exit a process
// whose request path is unaffected.
func (r *Relay) Run(ctx context.Context) error {
	log.Print("video: outbox relay: started")
	defer log.Print("video: outbox relay: stopped")

	backoff := dialBackoffInitial
	for {
		if ctx.Err() != nil {
			return nil
		}

		conn, err := rabbitmq.Open(r.config)
		if err != nil {
			log.Printf("video: outbox relay: connect: %v; retrying in %s", err, backoff)
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			backoff = nextBackoff(backoff)
			continue
		}
		log.Print("video: outbox relay: connected")

		served, err := r.serve(ctx, conn)
		if err != nil {
			log.Printf("video: outbox relay: connection lost: %v", err)
		}
		_ = rabbitmq.Close(conn)
		if ctx.Err() != nil {
			return nil
		}

		// The backoff is reset by a connection that actually worked, not by
		// one that merely dialed. A failure after a successful dial — a
		// topology that conflicts with an existing declaration, a
		// permissions error, a channel that closes on every publish — would
		// otherwise redial in a tight loop, hammering the broker and
		// flooding the log with exactly the failure nobody is watching for.
		if served {
			backoff = dialBackoffInitial
			continue
		}
		log.Printf("video: outbox relay: connection was unusable; retrying in %s", backoff)
		if !sleepCtx(ctx, backoff) {
			return nil
		}
		backoff = nextBackoff(backoff)
	}
}

// serve declares the topology, opens a publisher, and polls until the
// connection or the channel fails, or ctx is cancelled. Its error describes
// why the connection was given up, and is nil when ctx ended the loop.
//
// The bool reports whether this connection ever completed a polling cycle —
// that is, whether it proved usable rather than merely dialable. Run uses it
// to decide between redialing at once and backing off.
func (r *Relay) serve(ctx context.Context, conn *amqp.Connection) (bool, error) {
	// Declared on every dial, not once at startup. Against a fresh broker
	// the exchange does not exist and a publish to a missing exchange closes
	// the channel instead of failing routably; and a broker recreated while
	// the relay was disconnected gets its topology back on reconnect for the
	// same reason. Declaring is idempotent, so a topology something else also
	// declares — the worker's consumer, for the dispatch topology — costs
	// nothing here and removes any dependence on startup order.
	if err := rabbitmq.DeclareTopology(conn, r.topology); err != nil {
		return false, err
	}

	publisher, err := NewPublisher(conn, r.topology.Exchange)
	if err != nil {
		return false, err
	}
	defer func() { _ = publisher.Close() }()

	connClosed := conn.NotifyClose(make(chan *amqp.Error, 1))
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	var served bool
	// A cycle runs before the first tick, not after it: a reconnect
	// following an outage has a backlog waiting, and there is no reason to
	// add a poll interval to its age.
	for {
		if err := r.cycle(ctx, publisher); err != nil {
			return served, err
		}
		served = true

		select {
		case <-ctx.Done():
			return served, nil
		case amqpErr, ok := <-connClosed:
			// The client closes its notification channels rather than
			// sending on them when the close carries no error, so a receive
			// that reports !ok is a real loss, not a spurious wakeup. Both
			// arms say so explicitly instead of relying on this loop
			// returning either way.
			if !ok {
				return served, errors.New("video: outbox relay: broker connection closed")
			}
			return served, amqpErr
		case amqpErr, ok := <-publisher.Closed():
			if !ok {
				return served, errors.New("video: outbox relay: publishing channel closed")
			}
			return served, amqpErr
		case <-ticker.C:
		}
	}
}

// cycle claims a batch, publishes it, stamps the rows the broker accepted,
// and commits — all inside one transaction, so the stamps and the claim's
// locks are released together.
//
// Its error is reserved for broker failures, which the caller answers by
// reconnecting. A database failure is logged and swallowed: the rows stay
// unpublished and the next poll retries them, and dropping a working
// connection over it would help nothing.
func (r *Relay) cycle(ctx context.Context, publisher *Publisher) error {
	batch, err := r.outbox.Claim(ctx, r.eventTypes, maxPublishBatch)
	if err != nil {
		if ctx.Err() == nil {
			log.Printf("video: outbox relay: claim: %v", err)
		}
		return nil
	}
	// Unconditional, and a no-op after a successful commit: it is what
	// guarantees the row locks are released on every path, including a
	// cancelled context mid-cycle.
	defer batch.Rollback()

	messages := make([]Message, 0, len(batch.Messages()))
	for _, row := range batch.Messages() {
		// The routing key is the row's own event_type, not a per-relay
		// constant: a relay carrying more than one type would otherwise
		// publish every row under the name of whichever type it was
		// configured with.
		messages = append(messages, Message{ID: row.ID, RoutingKey: row.EventType, Body: row.Payload})
	}
	if len(messages) == 0 {
		return nil
	}

	publishCtx, cancel := context.WithTimeout(ctx, publishTimeout)
	defer cancel()
	published, err := publisher.Publish(publishCtx, messages)
	if err != nil {
		return err
	}

	// A refused message is not a failure. reject-publish nacks whatever
	// arrives at a full job queue, which is back-pressure working as
	// designed; the row stays unstamped and the next poll retries it. A
	// relay that errored here would turn back-pressure into a reconnect
	// loop, and one that stamped anyway would turn it into silent loss.
	if refused := refusedIDs(messages, published); len(refused) > 0 {
		log.Printf("video: outbox relay: broker did not accept %d of %d messages, left unpublished for the next poll: %v", len(refused), len(messages), refused)
	}

	if err := batch.MarkPublished(ctx, published); err != nil {
		if ctx.Err() == nil {
			log.Printf("video: outbox relay: mark published: %v", err)
		}
		return nil
	}
	if err := batch.Commit(); err != nil {
		if ctx.Err() == nil {
			log.Printf("video: outbox relay: commit: %v", err)
		}
	}
	return nil
}

// refusedIDs returns the ids in messages that published does not contain —
// the rows a nack or a return left unstamped. Logged by id because a stalled
// relay is otherwise invisible: uploads keep succeeding while nothing is
// dispatched.
func refusedIDs(messages []Message, published []string) []string {
	accepted := make(map[string]struct{}, len(published))
	for _, id := range published {
		accepted[id] = struct{}{}
	}
	var refused []string
	for _, msg := range messages {
		if _, ok := accepted[msg.ID]; !ok {
			refused = append(refused, msg.ID)
		}
	}
	return refused
}

// sleepCtx waits for d, reporting false if ctx ended first.
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
