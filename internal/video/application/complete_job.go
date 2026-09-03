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
	// LeaseEpoch is the fence epoch the caller won from StartProcessing and
	// has held for the whole run. It must be carried from there, never read
	// off the job this use case loads: by the time of the load the row may
	// already carry a successor's epoch, and the fence would then pass in
	// exactly the case it exists to reject.
	LeaseEpoch int64
}

// CompleteJob loads a VideoJob by ID, transitions it from processing to
// completed with its result, and persists it conditionally on the caller's
// lease epoch.
//
// It reads through an authoritative reader and writes through the caching
// one, for the same reason StartProcessing does: a stale cached record at the
// wrong status makes the aggregate refuse the transition before any statement
// runs, leaving the rightful holder unable to commit at all.
type CompleteJob struct {
	reader domain.VideoJobRepository
	writer domain.VideoJobRepository
	idsFor domain.VideoJobIDParser
}

// NewCompleteJob wires the CompleteJob use case to its ports: an
// authoritative reader for the load, and the caching repository for the
// write.
func NewCompleteJob(reader, writer domain.VideoJobRepository, idsFor domain.VideoJobIDParser) *CompleteJob {
	return &CompleteJob{reader: reader, writer: writer, idsFor: idsFor}
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

	job, err := uc.reader.FindByID(ctx, id)
	if err != nil {
		return TransitionResult{}, err
	}

	want := terminalOutcome{
		status:     domain.JobStatusCompleted,
		storageKey: storageKey.String(),
		frameCount: input.FrameCount,
	}
	if err := job.Complete(storageKey, input.FrameCount); err != nil {
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
