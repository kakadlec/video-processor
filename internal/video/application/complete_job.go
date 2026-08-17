package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// CompleteJobInput carries the caller-supplied completion fields.
type CompleteJobInput struct {
	JobID      string
	StorageKey string
	FrameCount int
}

// CompleteJob loads a VideoJob by ID, transitions it from processing to
// completed with its result, and persists it.
type CompleteJob struct {
	jobs   domain.VideoJobRepository
	idsFor domain.VideoJobIDParser
}

// NewCompleteJob wires the CompleteJob use case to its ports.
func NewCompleteJob(jobs domain.VideoJobRepository, idsFor domain.VideoJobIDParser) *CompleteJob {
	return &CompleteJob{jobs: jobs, idsFor: idsFor}
}

// Execute runs the completion transition use case.
func (uc *CompleteJob) Execute(ctx context.Context, input CompleteJobInput) (TransitionResult, error) {
	id, err := uc.idsFor.ParseVideoJobID(input.JobID)
	if err != nil {
		return TransitionResult{}, err
	}

	storageKey, err := domain.NewStorageKey(input.StorageKey)
	if err != nil {
		return TransitionResult{}, err
	}

	job, err := uc.jobs.FindByID(ctx, id)
	if err != nil {
		return TransitionResult{}, err
	}

	if err := job.Complete(storageKey, input.FrameCount); err != nil {
		return TransitionResult{}, err
	}

	if err := uc.jobs.Update(ctx, job); err != nil {
		return TransitionResult{}, err
	}

	return TransitionResult{JobID: job.ID().String(), Status: string(job.Status())}, nil
}
