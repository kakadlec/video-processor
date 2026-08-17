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
}

// EnqueueVideoJob loads a VideoJob by ID, transitions it from pending to
// queued, and persists the result.
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

	if err := job.Enqueue(); err != nil {
		return TransitionResult{}, err
	}

	if err := uc.jobs.Update(ctx, job); err != nil {
		return TransitionResult{}, err
	}

	return TransitionResult{JobID: job.ID().String(), Status: string(job.Status())}, nil
}
