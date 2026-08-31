package domain

import (
	"context"
	"errors"
)

// ErrVideoJobNotFound is returned when no VideoJob matches the requested lookup.
var ErrVideoJobNotFound = errors.New("video: video job not found")

// ErrJobClaimLost reports that a job could not be claimed for processing
// because it was no longer queued — another consumer already took it, or it
// has since reached a terminal state.
//
// It is deliberately its own sentinel rather than a wrapping of
// ErrInvalidStatusTransition: a lost claim is a normal, expected outcome of
// at-least-once dispatch and tells the consumer to drop the message without
// touching the job, whereas an invalid transition is a defect. Callers
// distinguish them with errors.Is, which only works if the two are disjoint.
// It is likewise distinct from ErrVideoJobNotFound: the job exists.
var ErrJobClaimLost = errors.New("video: video job claim lost")

// VideoJobRepository is the persistence port for the VideoJob aggregate.
// FindByUserID orders results by CreatedAt descending, with VideoJobID as a
// tie-breaker for equal CreatedAt values. Callers pass already-validated
// offset (>= 0) and limit (1-100).
type VideoJobRepository interface {
	Create(ctx context.Context, job *VideoJob) error
	FindByID(ctx context.Context, id VideoJobID) (*VideoJob, error)
	FindByUserID(ctx context.Context, userID UserID, offset, limit int) ([]*VideoJob, error)
	// FindCompletedByUserID returns all of userID's completed jobs, in the
	// same order as FindByUserID. It takes no offset or limit on purpose:
	// its only caller is GET /api/status, which accepts no pagination
	// parameters and whose filesystem-backed predecessor listed every
	// artifact the caller owned, so any bound here would silently make a
	// user's older results unreachable. Filtering on status in the query
	// is what makes returning the whole set reasonable — the rows it
	// returns are exactly the rows that endpoint renders.
	FindCompletedByUserID(ctx context.Context, userID UserID) ([]*VideoJob, error)
	// Update persists an already-loaded VideoJob's current status,
	// frame count, error reason, and storage key. Unlike Create and
	// Enqueue, it does not write a transactional-outbox row.
	Update(ctx context.Context, job *VideoJob) error
	// Enqueue persists a job that has just transitioned to queued and, in
	// the same transaction, the outbox event describing that dispatch —
	// the row and the event it announces are never observably
	// inconsistent. That event is the only thing Enqueue writes which
	// Update does not; the status column update itself is identical.
	//
	// It exists as its own method rather than as a status-dependent branch
	// inside Update on purpose. Update is also CompleteJob's and FailJob's
	// path, so making it outbox-aware would turn event publication into a
	// side effect of a general-purpose write and would decide, in advance
	// and by accident, what the completion and failure events look like.
	Enqueue(ctx context.Context, job *VideoJob) error
	// ClaimForProcessing persists an already-loaded job's queued ->
	// processing transition conditionally: it takes effect only if the
	// stored row is still queued, and reports whether it did. That makes
	// the claim safe under at-least-once dispatch — two consumers handed
	// the same message race on one statement and exactly one wins.
	//
	// The name says "for processing" in full because it is not the outbox's
	// Claim: that one leases event rows to a relay, this one takes
	// ownership of a job.
	//
	// A false return is not an error. It means another consumer won, or the
	// job is no longer queued; the caller decides what that means. The
	// aggregate passed in has already been transitioned in memory, so a
	// loser must discard it rather than persist it by another route.
	//
	// It is deliberately not a recovery primitive: a job whose consumer
	// died mid-run stays processing, and nothing here reclaims it.
	ClaimForProcessing(ctx context.Context, job *VideoJob) (claimed bool, err error)
}
