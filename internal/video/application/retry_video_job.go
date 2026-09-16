package application

import (
	"context"
	"errors"

	"video-processor/internal/video/domain"
)

// RetryVideoJob loads a VideoJob by ID and returns it from processing to
// queued at the caller's held epoch, persisting the transition conditionally
// through VideoJobRepository.Requeue — the identical processing -> queued
// edge and epoch fence the recovery sweep uses for an abandoned lease
// (cmd/worker/sweeper.go), so a fresh video_job.queued.v2 outbox row is
// written in the same transaction as the transition, exactly as it is for a
// swept job.
//
// ProcessVideoJob is this use case's only caller, and only for a transient
// SourceStorage.Get or ResultStorage.Put failure it decided not to fail the
// job over. Unlike the sweep, which must confirm an abandoned lease before
// acting, this caller still holds the job: no confirmation is needed, only
// the same fence every terminal write already carries.
//
// It reads through an authoritative reader and writes through the caching
// one, for the same reason StartProcessing, CompleteJob, and FailJob do: a
// decision about who owns a job is not one a cache may answer.
type RetryVideoJob struct {
	reader domain.VideoJobRepository
	writer domain.VideoJobRepository
	idsFor domain.VideoJobIDParser
}

// NewRetryVideoJob wires the RetryVideoJob use case to its ports: an
// authoritative reader for the load, and the caching repository for the
// write.
func NewRetryVideoJob(reader, writer domain.VideoJobRepository, idsFor domain.VideoJobIDParser) *RetryVideoJob {
	return &RetryVideoJob{reader: reader, writer: writer, idsFor: idsFor}
}

// Execute runs the retry-requeue transition use case. epoch is the fence the
// caller holds — won from StartProcessing, never re-read off the loaded job,
// for the same reason FailJob's and CompleteJob's LeaseEpoch input is not: by
// the time of the load the row may already carry a successor's epoch, and
// the fence would then pass in exactly the case it exists to reject.
func (uc *RetryVideoJob) Execute(ctx context.Context, jobID string, epoch int64) (TransitionResult, error) {
	id, err := uc.idsFor.ParseVideoJobID(jobID)
	if err != nil {
		return TransitionResult{}, err
	}

	job, err := uc.reader.FindByID(ctx, id)
	if err != nil {
		return TransitionResult{}, err
	}

	if err := job.Requeue(); err != nil {
		return classifyRefusedRequeue(job, err)
	}

	requeued, err := uc.writer.Requeue(ctx, job, epoch)
	if err != nil {
		return TransitionResult{}, err
	}
	if !requeued {
		// The in-memory transition above succeeded, so the aggregate saw
		// this row as processing — the repository's own conditional
		// statement is what refused, and only the epoch it was asked to
		// match can have done that: a sweep or a successor already moved
		// the row past this caller's held epoch.
		return TransitionResult{}, domain.ErrJobFenced
	}

	return TransitionResult{
		JobID:      job.ID().String(),
		Status:     string(job.Status()),
		LeaseEpoch: epoch,
		Applied:    true,
	}, nil
}

// classifyRefusedRequeue decides what it means for the aggregate to refuse
// job.Requeue(), given the job as the authoritative read found it.
//
// Requeue's own precondition names processing explicitly, so most refusals
// this can reach are the row no longer being processing: it moved on while
// this caller was still working — completed or failed by whoever took over,
// or already returned to queued by a sweep or an earlier retry — and every
// one of those is a fence, not a defect. There is no idempotent-match case to
// carve out the way classifyRefusedTransition does for a terminal write: a
// real requeue always advances the epoch, so this caller's own earlier
// requeue can never be found still recorded as this caller's own.
//
// Two cases are not a fence. A pending job mirrors classifyRefusedTransition's
// own exception: nothing enqueued it, so nothing could have claimed it
// either — a genuine defect this call did not cause and must not mask. A
// refusal naming ErrSourceKeyRequiredToEnqueue is a defect for the same
// reason regardless of status: this caller only reaches Requeue after using
// this exact job's source key to read the object it just failed to store or
// fetch, so a freshly-loaded row with no source key would mean the row
// changed under a fenced write this use case cannot see, not that this
// caller's own retry ever legitimately produces it.
func classifyRefusedRequeue(job *domain.VideoJob, refusal error) (TransitionResult, error) {
	if job.Status() == domain.JobStatusPending || errors.Is(refusal, domain.ErrSourceKeyRequiredToEnqueue) {
		return TransitionResult{}, refusal
	}
	return TransitionResult{}, domain.ErrJobFenced
}
