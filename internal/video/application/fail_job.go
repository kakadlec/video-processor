package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// FailJobInput carries the caller-supplied failure fields.
type FailJobInput struct {
	JobID  string
	Reason string
	// LeaseEpoch is the fence epoch the caller holds — won from
	// StartProcessing, or reported by the recovery scan for an abandonment.
	// It must be carried from there, never read off the job this use case
	// loads: by then the row may already carry a successor's epoch, and the
	// fence would pass in exactly the case it exists to reject.
	LeaseEpoch int64
}

// FailJob loads a VideoJob by ID, transitions it from processing to failed
// with a reason, and persists it conditionally on the caller's lease epoch.
//
// It reads through an authoritative reader and writes through the caching
// one, for the same reason CompleteJob does.
type FailJob struct {
	reader domain.VideoJobRepository
	writer domain.VideoJobRepository
	idsFor domain.VideoJobIDParser
}

// NewFailJob wires the FailJob use case to its ports: an authoritative reader
// for the load, and the caching repository for the write.
func NewFailJob(reader, writer domain.VideoJobRepository, idsFor domain.VideoJobIDParser) *FailJob {
	return &FailJob{reader: reader, writer: writer, idsFor: idsFor}
}

// Abandon fails the job at the fenced epoch with the fixed reason the
// recovery sweep records when a job has exhausted its requeues, or when it
// carries no source key to re-dispatch at all.
//
// It reuses this use case rather than adding a second path to failed, so
// every route into that state runs the same aggregate transition and the same
// fenced write.
func (uc *FailJob) Abandon(ctx context.Context, jobID string, epoch int64) (TransitionResult, error) {
	return uc.Execute(ctx, FailJobInput{JobID: jobID, Reason: abandonedFailureReason, LeaseEpoch: epoch})
}

// Execute runs the failure transition use case.
func (uc *FailJob) Execute(ctx context.Context, input FailJobInput) (TransitionResult, error) {
	id, err := uc.idsFor.ParseVideoJobID(input.JobID)
	if err != nil {
		return TransitionResult{}, err
	}

	job, err := uc.reader.FindByID(ctx, id)
	if err != nil {
		return TransitionResult{}, err
	}

	want := terminalOutcome{status: domain.JobStatusFailed, errorReason: input.Reason}
	if err := job.Fail(input.Reason); err != nil {
		return classifyRefusedTransition(job, input.LeaseEpoch, want, err)
	}

	applied, err := uc.writer.Update(ctx, job, input.LeaseEpoch)
	if err != nil {
		return TransitionResult{}, err
	}

	return TransitionResult{
		JobID:      job.ID().String(),
		Status:     string(job.Status()),
		LeaseEpoch: input.LeaseEpoch,
		Applied:    applied,
	}, nil
}
