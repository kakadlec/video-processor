package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// StartProcessing loads a VideoJob by ID and claims it for processing,
// transitioning it from queued to processing.
//
// The persistence step is a conditional claim, not a plain update: under
// at-least-once dispatch the same job can be delivered to two consumers, and
// the claim is what makes exactly one of them the owner. A consumer that
// loses returns domain.ErrJobClaimLost and must do nothing further to the
// job — not retry, not fail it, not write to it by any other route.
//
// It reads through an authoritative reader and writes through the caching
// one. Deciding who owns a job is not a decision a cache may answer, and this
// use case makes one: write-throughs are not ordered with respect to one
// another, so a claim's cache write delayed past a requeue's would leave
// processing cached against a queued row, the aggregate would refuse
// processing -> processing, and the worker would dead-letter a recovery that
// had already succeeded — permanently, since the sweep only scans processing.
type StartProcessing struct {
	reader domain.VideoJobRepository
	writer domain.VideoJobRepository
	idsFor domain.VideoJobIDParser
}

// NewStartProcessing wires the StartProcessing use case to its ports: an
// authoritative reader for the load, and the caching repository for the
// write.
func NewStartProcessing(reader, writer domain.VideoJobRepository, idsFor domain.VideoJobIDParser) *StartProcessing {
	return &StartProcessing{reader: reader, writer: writer, idsFor: idsFor}
}

// Execute runs the start-processing transition use case.
func (uc *StartProcessing) Execute(ctx context.Context, jobID string) (TransitionResult, error) {
	id, err := uc.idsFor.ParseVideoJobID(jobID)
	if err != nil {
		return TransitionResult{}, err
	}

	job, err := uc.reader.FindByID(ctx, id)
	if err != nil {
		return TransitionResult{}, err
	}

	// Two distinct paths reach a lost claim, and both must report it as the
	// sentinel rather than as whatever the layer below called it.
	//
	// The discrimination below survives the transition table gaining its
	// backwards edge, and deliberately so: recovery does not widen the claim
	// predicate, it returns an abandoned job to queued so this same edge can
	// be raced for again. Do not collapse these branches on the strength of
	// seeing processing -> queued in the table.
	//
	// This is the common one: by the time a duplicate delivery is read, the
	// winner has usually already moved the job past queued, so the aggregate
	// itself rejects the transition. The aggregate cannot say why — it
	// returns the same ErrInvalidStatusTransition for a pending job, which is
	// a genuine defect (nothing enqueued it) and not a lost claim — so the
	// status the load observed is what separates them. transitionTo leaves
	// the job unchanged on failure, so reading it here is safe.
	if err := job.StartProcessing(); err != nil {
		if job.Status() == domain.JobStatusPending {
			return TransitionResult{}, err
		}
		return TransitionResult{}, domain.ErrJobClaimLost
	}

	// And this is the rare one: the load saw queued, but the winner committed
	// in the window between that read and this write. The claim reports it.
	claimed, epoch, err := uc.writer.ClaimForProcessing(ctx, job)
	if err != nil {
		return TransitionResult{}, err
	}
	if !claimed {
		return TransitionResult{}, domain.ErrJobClaimLost
	}

	return TransitionResult{JobID: job.ID().String(), Status: string(job.Status()), LeaseEpoch: epoch, Applied: true}, nil
}
