package application

import "video-processor/internal/video/domain"

// terminalOutcome is the outcome a terminal use case is trying to record. It
// exists to compare what a call intended to write against what the stored job
// already carries, on all four fields at once: the recovery sweep's
// abandonment and a genuine extraction failure differ only in the reason, so
// a comparison on status alone would report one as the other's own commit.
type terminalOutcome struct {
	status      domain.JobStatus
	storageKey  string
	frameCount  int
	errorReason string
}

func (o terminalOutcome) matches(job *domain.VideoJob) bool {
	return job.Status() == o.status &&
		job.StorageKey().String() == o.storageKey &&
		job.FrameCount() == o.frameCount &&
		job.ErrorReason() == o.errorReason
}

// classifyRefusedTransition decides what a terminal transition the aggregate
// refused actually means for a caller holding epoch, given the job as the
// authoritative read found it.
//
// The aggregate can only say "not a legal edge from here". Three of those
// refusals are ordinary consequences of recovery rather than defects, and the
// worker's disposition table branches on the sentinels, not on
// ErrInvalidStatusTransition:
//
//   - the row moved past this caller's epoch — it was requeued mid-run and
//     re-claimed, so this caller is fenced;
//   - the row is terminal at this caller's own epoch with a different
//     outcome — another actor holding the same epoch (a leaseless worker and
//     the sweep abandoning it) finished first, which is also a fence;
//   - the row is terminal at this caller's own epoch with exactly this
//     outcome — this caller's own earlier commit, reported as success that
//     applied nothing, so a retry after a lost response still acks and cleans
//     up.
//
// Two refusals are deliberately not fences. A pending job is a genuine defect
// — nothing enqueued it — and keeps reporting the raw error, mirroring
// StartProcessing's own pending-versus-lost-claim discrimination. An
// equal-epoch queued row cannot be a real requeue, which always advances the
// epoch; it can only be a stale cache entry, which the authoritative read
// this use case performs prevents from reaching here at all. Fencing it would
// reject the rightful holder's own write.
//
// The PostgreSQL adapter's own classification of a refused statement is
// coarser — it sees a row, not an aggregate, and reports every refusal that
// is not the caller's recorded outcome as a fence. These two branches run
// first and are the ones with enough information, so the coarser reading is
// unreachable from here.
func classifyRefusedTransition(job *domain.VideoJob, epoch int64, want terminalOutcome, refusal error) (TransitionResult, error) {
	if job.Status() == domain.JobStatusPending {
		return TransitionResult{}, refusal
	}
	if job.LeaseEpoch() > epoch {
		return TransitionResult{}, domain.ErrJobFenced
	}
	if job.LeaseEpoch() == epoch && isTerminal(job.Status()) {
		if want.matches(job) {
			return TransitionResult{
				JobID:      job.ID().String(),
				Status:     string(job.Status()),
				LeaseEpoch: epoch,
				Applied:    false,
			}, nil
		}
		return TransitionResult{}, domain.ErrJobFenced
	}
	return TransitionResult{}, refusal
}

func isTerminal(status domain.JobStatus) bool {
	return status == domain.JobStatusCompleted || status == domain.JobStatusFailed
}
