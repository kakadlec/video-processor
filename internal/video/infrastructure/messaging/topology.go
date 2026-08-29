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
// The generation suffix is on the *exchange*, and the work queue's name
// merely follows it. That placement is the point: a direct exchange delivers
// each publish to every queue bound with the matching routing key, so
// versioning only the queue would let a replica that has not been redeployed
// yet reach a later generation's consumer — the double-processing window a
// separate queue was supposed to close. Versioning the exchange gives the
// generations separate delivery paths while leaving RoutingKeyJobQueued equal
// to the outbox event_type string, which is what keeps the database and the
// broker naming the event identically.
//
// The dead-letter exchange and queue carry no suffix on purpose: the sink is
// fanout, so every generation's dead-lettered messages land in one place to
// look at rather than one per generation.
const (
	ExchangeJobs        = "video.jobs.v1"
	RoutingKeyJobQueued = "video_job.queued"
	QueueJobs           = "video.jobs.queued.v1"
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
