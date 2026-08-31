// Package messaging defines the Video Processing context's job-dispatch
// topology on the shared AMQP broker.
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

// JobDispatchTopology returns the descriptor this context declares.
func JobDispatchTopology() rabbitmq.Topology {
	return rabbitmq.Topology{
		Exchange:       ExchangeJobs,
		RoutingKey:     RoutingKeyJobQueued,
		WorkQueue:      QueueJobs,
		DeadExchange:   ExchangeDeadLetter,
		DeadQueue:      QueueDeadLetter,
		WorkMaxLength:  jobsMaxLength,
		DeadMessageTTL: deadLetterTTL,
		DeadMaxLength:  deadLetterMaxLength,
	}
}
