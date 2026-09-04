// Package messaging defines the Video Processing context's job-dispatch and
// terminal-event topologies on the shared AMQP broker.
//
// The names live here rather than in internal/platform/rabbitmq because
// ddd-architecture confines that package to connection/lifecycle plumbing,
// never a specific bounded context's use case — and an exchange named
// video.jobs carrying a routing key named after a VideoJob event is exactly
// this context's use case. The generic descriptor and the function that
// declares it stay there; the names are ours.
package messaging

import (
	"time"

	"video-processor/internal/platform/rabbitmq"
)

// The declared job-dispatch topology.
//
// Every name here carries the generation, the routing key included, and that
// is a correction rather than a flourish. Versioning the exchange and queue
// alone does not isolate generations, because the two generations never meet
// at the broker in the first place — they meet in the database. Each replica
// runs its own outbox relay, and every relay claims from the one shared
// video_job_outbox table filtered on event_type and nothing else. During a
// rolling deploy an old replica's relay will therefore claim a new replica's
// row and publish it into the old exchange, where the old inline pipeline
// picks it up; and a new relay will claim an old row and hand it to the
// worker. Renaming the exchange changes neither. The generation has to be on
// the event_type string, which is the predicate both relays actually use, and
// this constant must equal it — an already-deployed relay cannot be taught a
// new predicate, but it can be handed a literal it will never match.
// postgres.videoJobQueuedEventType is that literal;
// TestRoutingKeyMatchesTheOutboxEventType is what pins the two together.
//
// The exchange and queue are versioned as well, so that a message from a
// generation that does slip through has nowhere to land, and so the two
// generations' backlogs are visibly separate in the broker.
//
// What the generation is for is the rolling-deploy window itself, not stale
// messages. A message left on the previous generation's queue is harmless on
// its own: ClaimForProcessing means a second consumer of the same job simply
// loses and drops it. The damage is specific — an old synchronous replica
// consumes a job, loses the claim to the new worker, and then runs its
// unconditional deferred delete of the source object out from under the
// worker's running extraction. Separate generations mean the old replica is
// never handed that job at all.
//
// The dead-letter exchange and queue carry no suffix on purpose: the sink is
// fanout, so every generation's dead-lettered messages land in one place to
// look at rather than one per generation.
const (
	ExchangeJobs        = "video.jobs.v2"
	RoutingKeyJobQueued = "video_job.queued.v2"
	QueueJobs           = "video.jobs.queued.v2"
	ExchangeDeadLetter  = "video.jobs.dlx"
	QueueDeadLetter     = "video.jobs.dead"

	jobsMaxLength       = 10000
	deadLetterTTL       = 24 * time.Hour
	deadLetterMaxLength = 10000
)

// The declared terminal-event topology: a job's outcome, announced once it is
// durable.
//
// It is a second topology rather than a second routing key on the dispatch
// exchange because the two streams have different consumers, different
// lifetimes, and different generations. Binding both to one exchange would
// mean a dispatch generation bump renaming the terminal stream too.
//
// The queue name says "terminal.events" rather than naming a state, because
// one queue carries both outcomes: a consumer interested in a job's end is
// interested in either. The generation suffix on the exchange, the queue, and
// the two routing keys moves together, and the routing keys must stay equal
// to postgres.VideoJobCompletedEventType and postgres.VideoJobFailedEventType
// for the reason the dispatch key must — TestRoutingKeyMatchesTheOutboxEventType
// pins all three pairs.
//
// The dead-letter sink is the dispatch topology's, unversioned and shared,
// for the same reason it is shared across generations: one place to look.
const (
	ExchangeTerminal        = "video.jobs.terminal.v1"
	RoutingKeyJobCompleted  = "video_job.completed.v1"
	RoutingKeyJobFailed     = "video_job.failed.v1"
	QueueTerminalEvents     = "video.jobs.terminal.events.v1"
	terminalEventsMaxLength = 10000
)

// JobDispatchTopology returns the dispatch descriptor this context declares.
func JobDispatchTopology() rabbitmq.Topology {
	return rabbitmq.Topology{
		Exchange:       ExchangeJobs,
		RoutingKeys:    []string{RoutingKeyJobQueued},
		WorkQueue:      QueueJobs,
		DeadExchange:   ExchangeDeadLetter,
		DeadQueue:      QueueDeadLetter,
		WorkMaxLength:  jobsMaxLength,
		DeadMessageTTL: deadLetterTTL,
		DeadMaxLength:  deadLetterMaxLength,
	}
}

// TerminalEventsTopology returns the terminal-event descriptor this context
// declares. Its queue is bound under both terminal routing keys, so one
// consumer sees every outcome in the order the broker delivered them.
//
// The queue is declared before any consumer exists, which is the point: the
// relay publishes mandatory and stamps published_at only for messages the
// broker both acknowledged and routed, so publishing into an exchange with no
// binding would return every message unroutable and re-attempt it on every
// poll forever. A declared queue holds the events until a consumer arrives.
//
// Like the job queue it carries a maximum length with reject-publish and no
// message TTL, but the TTL's absence has a different reason here. On the job
// queue an expired message would leave a job reporting "queued" with nothing
// able to advance it; here the job is already terminal and correct in the
// database — what expiry would discard is the only announcement that outcome
// ever gets.
func TerminalEventsTopology() rabbitmq.Topology {
	return rabbitmq.Topology{
		Exchange:       ExchangeTerminal,
		RoutingKeys:    []string{RoutingKeyJobCompleted, RoutingKeyJobFailed},
		WorkQueue:      QueueTerminalEvents,
		DeadExchange:   ExchangeDeadLetter,
		DeadQueue:      QueueDeadLetter,
		WorkMaxLength:  terminalEventsMaxLength,
		DeadMessageTTL: deadLetterTTL,
		DeadMaxLength:  deadLetterMaxLength,
	}
}
