package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// StartProcessing loads a VideoJob by ID, transitions it from queued to
// processing, and persists the result.
type StartProcessing struct {
	jobs   domain.VideoJobRepository
	idsFor domain.VideoJobIDParser
}

// NewStartProcessing wires the StartProcessing use case to its ports.
func NewStartProcessing(jobs domain.VideoJobRepository, idsFor domain.VideoJobIDParser) *StartProcessing {
	return &StartProcessing{jobs: jobs, idsFor: idsFor}
}

// Execute runs the start-processing transition use case.
func (uc *StartProcessing) Execute(ctx context.Context, jobID string) (TransitionResult, error) {
	id, err := uc.idsFor.ParseVideoJobID(jobID)
	if err != nil {
		return TransitionResult{}, err
	}

	job, err := uc.jobs.FindByID(ctx, id)
	if err != nil {
		return TransitionResult{}, err
	}

	if err := job.StartProcessing(); err != nil {
		return TransitionResult{}, err
	}

	if err := uc.jobs.Update(ctx, job); err != nil {
		return TransitionResult{}, err
	}

	return TransitionResult{JobID: job.ID().String(), Status: string(job.Status())}, nil
}
