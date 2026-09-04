package rabbitmq

import (
	"errors"
	"fmt"
	"time"

	amqp "github.com/rabbitmq/amqp091-go"
)

// Topology names the entities DeclareTopology declares and the bounds it
// applies to them.
//
// This package deliberately defines no default value for it and contains no
// name of its own: ddd-architecture confines internal/platform to
// connection/lifecycle plumbing, never a specific bounded context's use case.
// Callers own their names — see internal/video/infrastructure/messaging for
// the Video Processing context's.
//
// The overflow policies and the dead-letter queue's lack of its own
// dead-letter exchange are not fields. They are what make a declared topology
// bounded, and a caller able to vary them could declare an unbounded chain
// through this same function.
type Topology struct {
	Exchange string
	// RoutingKeys are the keys the work queue is bound under, one binding
	// each. A topology carrying a single stream passes one; a topology
	// whose queue must receive several event types passes several.
	//
	// An empty set is a caller defect and is refused rather than treated as
	// a fanout. The work exchange is direct, so a queue with no binding
	// receives nothing and every mandatory publish into that exchange comes
	// back unroutable — a silent failure this function can cheaply make
	// loud.
	RoutingKeys    []string
	WorkQueue      string
	DeadExchange   string
	DeadQueue      string
	WorkMaxLength  int
	DeadMessageTTL time.Duration
	DeadMaxLength  int
}

// Argument values applied to every topology this package declares. The work
// queue carries no x-message-ttl on purpose: expiring a live work message
// dead-letters it without touching the database row describing the same work,
// and the VideoJob state machine has no edge out of "queued" except to
// "processing" — so an expired message would leave a job reporting "queued"
// forever with nothing able to advance it. A maximum length with
// reject-publish bounds the queue without creating that inconsistency.
//
// reject-publish rather than reject-publish-dlx: a publisher that retries a
// refused message would deposit one dead-lettered copy per attempt, of work
// that was never lost. The nack the publisher already receives is the
// authoritative record.
const (
	overflowRejectPublish = "reject-publish"
	overflowDropHead      = "drop-head"

	argMessageTTL         = "x-message-ttl"
	argMaxLength          = "x-max-length"
	argOverflow           = "x-overflow"
	argDeadLetterExchange = "x-dead-letter-exchange"
)

// DeclareTopology declares topo's dead-letter sink, then its work exchange and
// queue, together with one binding per routing key. It is idempotent:
// declaring the same topology again succeeds.
//
// It opens its own channel and closes it before returning, so a failed
// declaration cannot leave a caller's long-lived publishing or consuming
// channel closed — AMQP provides no way to reopen a closed channel.
//
// Declaration order is load-bearing. The dead-letter exchange and queue come
// first because RabbitMQ does not validate at declare time that a queue's
// x-dead-letter-exchange names an existing exchange, and silently drops
// dead-lettered messages when it does not: declaring the sink first makes a
// partial failure leave a topology that is visibly incomplete rather than
// complete-looking and lossy.
func DeclareTopology(conn *amqp.Connection, topo Topology) error {
	if conn == nil {
		return errors.New("platform/rabbitmq: declare topology: nil connection")
	}
	if len(topo.RoutingKeys) == 0 {
		return errors.New("platform/rabbitmq: declare topology: no routing key")
	}

	ch, err := conn.Channel()
	if err != nil {
		return fmt.Errorf("platform/rabbitmq: declare topology: open channel: %w", err)
	}
	defer func() { _ = ch.Close() }()

	if err := ch.ExchangeDeclare(topo.DeadExchange, amqp.ExchangeFanout, true, false, false, false, nil); err != nil {
		return fmt.Errorf("platform/rabbitmq: declare dead-letter exchange %s: %w", topo.DeadExchange, err)
	}

	deadArgs := amqp.Table{
		argMessageTTL: int64(topo.DeadMessageTTL / time.Millisecond),
		argMaxLength:  int64(topo.DeadMaxLength),
		argOverflow:   overflowDropHead,
	}
	if _, err := ch.QueueDeclare(topo.DeadQueue, true, false, false, false, deadArgs); err != nil {
		return fmt.Errorf("platform/rabbitmq: declare dead-letter queue %s: %w", topo.DeadQueue, err)
	}
	// Fanout: the binding key is ignored, and both a current and a future
	// generation of the work topology land in this one sink.
	if err := ch.QueueBind(topo.DeadQueue, "", topo.DeadExchange, false, nil); err != nil {
		return fmt.Errorf("platform/rabbitmq: bind dead-letter queue %s: %w", topo.DeadQueue, err)
	}

	if err := ch.ExchangeDeclare(topo.Exchange, amqp.ExchangeDirect, true, false, false, false, nil); err != nil {
		return fmt.Errorf("platform/rabbitmq: declare exchange %s: %w", topo.Exchange, err)
	}

	workArgs := amqp.Table{
		argMaxLength:          int64(topo.WorkMaxLength),
		argOverflow:           overflowRejectPublish,
		argDeadLetterExchange: topo.DeadExchange,
	}
	if _, err := ch.QueueDeclare(topo.WorkQueue, true, false, false, false, workArgs); err != nil {
		return fmt.Errorf("platform/rabbitmq: declare work queue %s: %w", topo.WorkQueue, err)
	}
	for _, key := range topo.RoutingKeys {
		if err := ch.QueueBind(topo.WorkQueue, key, topo.Exchange, false, nil); err != nil {
			return fmt.Errorf("platform/rabbitmq: bind work queue %s under %s: %w", topo.WorkQueue, key, err)
		}
	}

	return nil
}
