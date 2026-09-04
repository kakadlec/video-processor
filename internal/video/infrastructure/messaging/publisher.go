package messaging

import (
	"context"
	"errors"
	"fmt"

	amqp "github.com/rabbitmq/amqp091-go"
)

// contentTypeJSON labels the payload the outbox stores, which is JSON.
const contentTypeJSON = "application/json"

// ErrBatchTooLarge is returned by Publish for a batch exceeding
// maxPublishBatch. See Publish for why the cap is a correctness bound rather
// than a tuning knob.

// maxPublishBatch bounds one Publish call, and with it the number of rows
// the relay claims per poll. It is also the returns channel's buffer: the
// confirmation wait must not be able to deadlock against a return the
// client cannot hand over, so the buffer has to cover a whole batch.
const maxPublishBatch = 100

var ErrBatchTooLarge = fmt.Errorf("video: publisher: batch exceeds the maximum of %d messages", maxPublishBatch)

// Message is one unit of work handed to a Publisher: an id used both to
// correlate the broker's answer and to stamp the row it came from, the
// routing key to publish it under, and the body to deliver.
//
// The key travels with the message rather than with the publisher because a
// relay may carry more than one event type on one exchange, and a key fixed
// at construction would then publish a message under the name of something
// it is not. Its source is the claimed row's own event_type.
type Message struct {
	ID         string
	RoutingKey string
	Body       []byte
}

// Publisher publishes messages to one exchange, each under its own routing
// key, on a channel running in confirm mode, and reports which of them the
// broker both acknowledged and routed to a queue.
//
// Both halves are needed and only the first is obvious. A publisher
// confirmation says the *exchange* accepted the publish, not that any queue
// received it: a non-mandatory publish to an exchange with no matching
// binding is acknowledged and then discarded. Every publish here is
// therefore mandatory, which turns that silent discard into a basic.return
// the broker sends before the acknowledgement, and a returned message is
// reported as not published.
type Publisher struct {
	ch       *amqp.Channel
	exchange string
	returns  chan amqp.Return
	closed   chan *amqp.Error
}

// NewPublisher opens a confirm-mode channel on conn for the given exchange.
//
// The exchange is an argument rather than read from a topology function
// inside: a publisher pointed at an exchange with no matching binding is the
// only way to exercise the unroutable path this type exists to handle, and
// the production topologies always have one.
func NewPublisher(conn *amqp.Connection, exchange string) (*Publisher, error) {
	if conn == nil {
		return nil, errors.New("video: publisher: nil connection")
	}

	ch, err := conn.Channel()
	if err != nil {
		return nil, fmt.Errorf("video: publisher: open channel: %w", err)
	}
	if err := ch.Confirm(false); err != nil {
		_ = ch.Close()
		return nil, fmt.Errorf("video: publisher: enable confirm mode: %w", err)
	}

	// Buffered to the largest batch a caller may publish, so the client's
	// dispatch goroutine never blocks handing us a return we have not read
	// yet — which would deadlock the confirmation wait that precedes the
	// drain.
	returns := ch.NotifyReturn(make(chan amqp.Return, maxPublishBatch))

	return &Publisher{
		ch:       ch,
		exchange: exchange,
		returns:  returns,
		closed:   ch.NotifyClose(make(chan *amqp.Error, 1)),
	}, nil
}

// Closed reports channel-level failures. A publish to a missing exchange
// closes the channel rather than the connection, and AMQP cannot reopen a
// closed channel, so a caller watching only the connection would keep
// polling through a publisher that can never succeed again.
func (p *Publisher) Closed() <-chan *amqp.Error {
	return p.closed
}

// Close releases the publishing channel.
func (p *Publisher) Close() error {
	return p.ch.Close()
}

// Publish delivers every message and returns the ids the broker both
// acknowledged and routed. An id missing from the result was refused
// (nacked, which is what a queue at its maximum length under reject-publish
// returns) or unroutable; neither is an error — the caller leaves those rows
// unstamped and tries again later.
//
// A returned error means the exchange is unusable: the batch's outcome is
// unknown and none of it may be recorded as delivered.
//
// The sequence — publish the whole batch, wait for every confirmation, then
// drain the returns — is what makes correlation sound. RabbitMQ sends
// basic.return before the basic.ack for the same unroutable publish, so by
// the time the last confirmation has arrived, every return for this batch is
// already buffered.
//
// That sequence is also why maxPublishBatch is enforced here rather than
// left to the caller. Returns are buffered, not read, until every
// confirmation has arrived; a batch larger than the buffer whose messages
// are unroutable would fill it and block the client's own dispatch
// goroutine, which is the goroutine that still owes us those confirmations.
// The wait would then never finish. Today's only caller claims at most this
// many rows, so the bound is unreachable through it — which is exactly why
// it is checked rather than assumed.
func (p *Publisher) Publish(ctx context.Context, messages []Message) ([]string, error) {
	if len(messages) == 0 {
		return nil, nil
	}
	if len(messages) > maxPublishBatch {
		return nil, ErrBatchTooLarge
	}

	confirmations := make([]*amqp.DeferredConfirmation, 0, len(messages))
	for _, msg := range messages {
		confirmation, err := p.ch.PublishWithDeferredConfirmWithContext(ctx, p.exchange, msg.RoutingKey, true, false, amqp.Publishing{
			// MessageId is the correlation handle. A basic.return carries
			// the message's properties but no delivery tag, so it cannot be
			// joined to its confirmation any other way.
			MessageId:   msg.ID,
			ContentType: contentTypeJSON,
			// Persistent (delivery mode 2) so a broker restart does not
			// discard queued work. Expiration is deliberately left unset:
			// RabbitMQ honours a per-message expiration independently of the
			// queue's arguments, so setting one would dead-letter a job
			// message even though the job queue carries no TTL on purpose —
			// and a dead-lettered dispatch leaves its VideoJob reporting
			// "queued" with nothing able to advance it.
			DeliveryMode: amqp.Persistent,
			Body:         msg.Body,
		})
		if err != nil {
			return nil, fmt.Errorf("video: publisher: publish %s: %w", msg.ID, err)
		}
		confirmations = append(confirmations, confirmation)
	}

	acked := make([]string, 0, len(messages))
	for i, confirmation := range confirmations {
		ok, err := confirmation.WaitContext(ctx)
		if err != nil {
			return nil, fmt.Errorf("video: publisher: await confirmation for %s: %w", messages[i].ID, err)
		}
		if ok {
			acked = append(acked, messages[i].ID)
		}
	}

	returned := p.drainReturns()
	published := make([]string, 0, len(acked))
	for _, id := range acked {
		if _, wasReturned := returned[id]; !wasReturned {
			published = append(published, id)
		}
	}
	return published, nil
}

// drainReturns collects the returns buffered so far without blocking. Every
// return belonging to the batch just confirmed has already arrived, because
// the broker sends it ahead of that message's acknowledgement.
func (p *Publisher) drainReturns() map[string]struct{} {
	returned := make(map[string]struct{})
	for {
		select {
		case ret, ok := <-p.returns:
			if !ok {
				return returned
			}
			returned[ret.MessageId] = struct{}{}
		default:
			return returned
		}
	}
}
