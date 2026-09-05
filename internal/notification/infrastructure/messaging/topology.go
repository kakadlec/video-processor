// Package messaging holds the Notification context's own copy of the
// terminal-event wire contract — the topology names it declares and the
// message structures it decodes — together with the consumer that reads them.
//
// Every name and every field in this package already exists in
// internal/video/infrastructure/messaging. The duplication is deliberate and
// required: ddd-architecture forbids this context importing any package of
// Video Processing, as a property of the build rather than of the moment an
// event is handled, which is the same reason the domain declares its own
// UserID and its own copies of the two event-type strings.
//
// Copying is only safe while the copy is pinned, and it is: cmd/api is the
// one composition root that legitimately imports both contexts, and
// TestNotificationTerminalTopologyMatchesTheEmittedTopology and
// TestNotificationTerminalMessagesDecodeTheEmittedPayloads there fail if
// either side drifts. Without them the drift would be silent in both
// directions — a renamed exchange leaves this consumer bound to a queue
// nothing publishes to, and a renamed payload field decodes as a zero value.
package messaging

import (
	"time"

	"video-processor/internal/notification/domain"
	"video-processor/internal/platform/rabbitmq"
)

// The terminal-event topology, as Video Processing declares it.
//
// The dead-letter names carry no generation suffix and the rest do, which is
// the publisher's arrangement rather than a choice made here: the sink is
// fanout, so every generation's dead-lettered messages land in one place to
// look at.
//
// The bounds are copied too, and they are not decoration. RabbitMQ refuses to
// redeclare an existing queue whose arguments differ, so a consumer carrying
// a different x-max-length or dead-letter TTL would not consume from a
// slightly different queue — it would fail to declare at all, on every dial.
const (
	ExchangeTerminal    = "video.jobs.terminal.v1"
	QueueTerminalEvents = "video.jobs.terminal.events.v1"
	ExchangeDeadLetter  = "video.jobs.dlx"
	QueueDeadLetter     = "video.jobs.dead"

	terminalEventsMaxLength = 10000
	deadLetterTTL           = 24 * time.Hour
	deadLetterMaxLength     = 10000
)

// TerminalEventsTopology returns the descriptor this context declares on every
// dial. Its queue is bound under both terminal event types, so one consumer
// sees every outcome in the order the broker delivered them.
//
// The routing keys are the domain's event-type constants rather than fresh
// literals of this package's own. They are the same two strings, and writing
// them twice inside one context would put a drift beyond the reach of the
// pin: cmd/api compares the domain's copies with the ones Video Processing
// emits, so a literal here that disagreed with the domain would bind the
// queue under a key no stored preference is ever resolved against, and
// nothing would report it.
func TerminalEventsTopology() rabbitmq.Topology {
	return rabbitmq.Topology{
		Exchange:       ExchangeTerminal,
		RoutingKeys:    []string{domain.EventTypeVideoJobCompleted, domain.EventTypeVideoJobFailed},
		WorkQueue:      QueueTerminalEvents,
		DeadExchange:   ExchangeDeadLetter,
		DeadQueue:      QueueDeadLetter,
		WorkMaxLength:  terminalEventsMaxLength,
		DeadMessageTTL: deadLetterTTL,
		DeadMaxLength:  deadLetterMaxLength,
	}
}
