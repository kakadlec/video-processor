package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// FailJobInput carries the caller-supplied failure fields.
type FailJobInput struct {
	JobID  string
	Reason string
}

// FailJob loads a VideoJob by ID, transitions it from processing to failed
// with a reason, and persists it.
type FailJob struct {
	jobs   domain.VideoJobRepository
	idsFor domain.VideoJobIDParser
}

// NewFailJob wires the FailJob use case to its ports.
func NewFailJob(jobs domain.VideoJobRepository, idsFor domain.VideoJobIDParser) *FailJob {
	return &FailJob{jobs: jobs, idsFor: idsFor}
}

// Execute runs the failure transition use case.
func (uc *FailJob) Execute(ctx context.Context, input FailJobInput) (TransitionResult, error) {
	id, err := uc.idsFor.ParseVideoJobID(input.JobID)
	if err != nil {
		return TransitionResult{}, err
	}

	job, err := uc.jobs.FindByID(ctx, id)
	if err != nil {
		return TransitionResult{}, err
	}

	if err := job.Fail(input.Reason); err != nil {
		return TransitionResult{}, err
	}

	if err := uc.jobs.Update(ctx, job); err != nil {
		return TransitionResult{}, err
	}

	return TransitionResult{JobID: job.ID().String(), Status: string(job.Status())}, nil
}
