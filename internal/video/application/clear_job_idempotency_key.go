package application

import (
	"context"

	"video-processor/internal/video/domain"
)

// ClearJobIdempotencyKey removes the upload-idempotency mapping that points
// at a job, so a later submission of the same bytes by the same user is
// processed again instead of being answered with the failed job.
//
// It exists because the component that decides a job has failed is no longer
// the request that reserved the key. The worker holds a job identifier and
// nothing else: the reservation token was never persisted, and the request
// that owned it returned as soon as the job was queued. Rebuilding the key
// from the job's own UserID and ContentHash is what closes that gap, and it
// is why ContentHash is a persisted column rather than a request-scoped
// value.
//
// The key is derived through domain.NewIdempotencyKey — the same constructor
// the submitting handler uses. Deriving it any other way would silently
// diverge the moment either side changed.
//
// A job with no content hash has no key to clear and is reported as such,
// not as an error: a job created through POST /api/video-jobs never had one.
type ClearJobIdempotencyKey struct {
	jobs   domain.VideoJobRepository
	keys   domain.IdempotencyStore
	idsFor domain.VideoJobIDParser
}

// NewClearJobIdempotencyKey wires the use case to its ports.
func NewClearJobIdempotencyKey(jobs domain.VideoJobRepository, keys domain.IdempotencyStore, idsFor domain.VideoJobIDParser) *ClearJobIdempotencyKey {
	return &ClearJobIdempotencyKey{jobs: jobs, keys: keys, idsFor: idsFor}
}

// Execute clears jobID's idempotency key, reporting whether a mapping was
// actually removed. False with a nil error covers every ordinary case: the
// job carries no content hash, the key has already expired, or it names some
// other job.
func (uc *ClearJobIdempotencyKey) Execute(ctx context.Context, jobID string) (bool, error) {
	id, err := uc.idsFor.ParseVideoJobID(jobID)
	if err != nil {
		return false, err
	}

	job, err := uc.jobs.FindByID(ctx, id)
	if err != nil {
		return false, err
	}
	if job.ContentHash() == "" {
		return false, nil
	}

	key, err := domain.NewIdempotencyKey(job.UserID().String(), job.ContentHash())
	if err != nil {
		return false, err
	}
	return uc.keys.ClearByJob(ctx, key, job.ID())
}
