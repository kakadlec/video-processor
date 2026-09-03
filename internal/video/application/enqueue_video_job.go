package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// TransitionResult describes a VideoJob's ID and status after a single
// transition use case has run. It is shared by EnqueueVideoJob,
// StartProcessing, CompleteJob, and FailJob.
type TransitionResult struct {
	JobID  string
	Status string
	// LeaseEpoch is the fence epoch the transition ran under. StartProcessing
	// reports the epoch its claim returned, and the winner must carry that
	// value through to its terminal write rather than re-reading it: a later
	// read can only pick up a successor's.
	LeaseEpoch int64
	// Applied distinguishes "this call wrote the row" from "the row already
	// carried exactly this outcome". Only the terminal writes can report
	// false, and only for a caller finding its own earlier commit after a
	// lost response — which is a success, but not one that licenses the
	// one-shot cleanup of a job's source object and idempotency key.
	Applied bool
}

// EnqueueVideoJob loads a VideoJob by ID, transitions it from pending to
// queued, and persists the result through the repository's Enqueue method —
// which writes the video_job.queued outbox row in the same transaction, so
// the dispatch is recorded atomically with the status it announces.
type EnqueueVideoJob struct {
	jobs   domain.VideoJobRepository
	idsFor domain.VideoJobIDParser
}

// NewEnqueueVideoJob wires the EnqueueVideoJob use case to its ports.
func NewEnqueueVideoJob(jobs domain.VideoJobRepository, idsFor domain.VideoJobIDParser) *EnqueueVideoJob {
	return &EnqueueVideoJob{jobs: jobs, idsFor: idsFor}
}

// Execute runs the enqueue transition use case.
func (uc *EnqueueVideoJob) Execute(ctx context.Context, jobID string) (TransitionResult, error) {
	id, err := uc.idsFor.ParseVideoJobID(jobID)
	if err != nil {
		return TransitionResult{}, err
	}

	job, err := uc.jobs.FindByID(ctx, id)
	if err != nil {
		return TransitionResult{}, err
	}

	// The aggregate rejects a job with no source key here; this use case
	// propagates that rather than re-checking, so the rule has one home.
	if err := job.Enqueue(); err != nil {
		return TransitionResult{}, err
	}

	if err := uc.jobs.Enqueue(ctx, job); err != nil {
		return TransitionResult{}, err
	}

	return TransitionResult{JobID: job.ID().String(), Status: string(job.Status()), LeaseEpoch: job.LeaseEpoch(), Applied: true}, nil
}
