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

// ErrJobClaimOutcomeUnknown reports that a claim attempt ended without this
// call learning whether the claim was won, because the persistence layer
// could not answer it.
//
// It is the sibling of ErrJobClaimLost and the contrast is the whole point:
// a lost claim means another actor owns this job, while an unknown outcome
// means this call cannot say who owns it — including whether this worker
// does.
//
// What it asserts is only what holds on every branch it admits: the caller
// acquired no lease, read no source object, ran no extraction, wrote no
// event and attempted no terminal write. It asserts nothing whatever about
// the stored row — neither that the transition happened nor that it did not
// — and callers are written against that weaker guarantee. Resolving the
// ambiguity with a follow-up read is not one of their options: a repository
// that has just failed to answer is no likelier to answer, and an answer
// would report only what was true at that instant.
var ErrJobClaimOutcomeUnknown = errors.New("video: video job claim outcome unknown")

// ErrRepositoryUnavailable reports that the persistence layer could not
// answer: the connection could not be established, was lost while the
// statement was in flight, or the server itself replied that it cannot serve
// the request now.
//
// Two things it does not mean, both of which a caller will otherwise assume.
//
// It is not "the database answered and refused". An undefined column, an
// insufficient privilege, a constraint violation and a row the aggregate
// cannot be reconstructed from are all permanent on a running server and
// carry no marker, so a caller that retries on this sentinel cannot be made
// to retry forever by a half-applied migration or one unreadable row.
//
// It is not a promise that the statement had no effect. A connection lost in
// flight leaves the outcome unknown — the statement may well have committed
// — and what this sentinel asserts is only that *this caller could not learn
// the outcome*. Every caller is written against that weaker guarantee, and a
// caller reading it as "nothing happened" would be wrong on the branch that
// matters most.
var ErrRepositoryUnavailable = errors.New("video: repository unavailable")

// ErrJobFenced reports that a write was refused because the caller no longer
// holds the job: the stored row has moved past the lease epoch the caller was
// working under, or another actor holding the same epoch already committed a
// different outcome.
//
// It is disjoint from ErrJobClaimLost and from ErrVideoJobNotFound, and the
// distinction is the point. A lost claim means another consumer took the job
// *before* this one started, so nothing was done and nothing was thrown away.
// A fenced write means another actor took the job away *while* this one was
// working, so an extraction was completed and discarded. The job exists in
// both cases, unlike ErrVideoJobNotFound. Callers separate the three with
// errors.Is, which only works while they stay disjoint.
var ErrJobFenced = errors.New("video: video job fenced")

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
	// Update persists an already-loaded VideoJob's terminal outcome —
	// status, frame count, error reason, and storage key — and, in the same
	// transaction, the outbox event describing that outcome. The event is
	// written if and only if the conditional write affected a row, which is
	// part of this contract rather than an implementation detail: an
	// outcome and its announcement have one author, so a caller that is
	// fenced or finds its own outcome already recorded writes neither.
	//
	// A job whose status is neither completed nor failed is refused with an
	// error and nothing is written — there is no event type for it.
	//
	// epoch is the lease epoch the caller holds, won from
	// ClaimForProcessing or reported by the recovery scan, and the write
	// takes effect only while the stored row still carries it and is still
	// processing. A caller superseded by a requeue, or beaten to the
	// terminal state by another actor at the same epoch, gets ErrJobFenced
	// and no write.
	//
	// applied distinguishes "this call wrote the row" from "the row already
	// carried exactly this outcome". The second is not an error — a
	// retried write finding its own earlier commit succeeded — but only the
	// first licenses one-shot cleanup of the job's source object and
	// idempotency key.
	Update(ctx context.Context, job *VideoJob, epoch int64) (applied bool, err error)
	// Enqueue persists a job that has just transitioned to queued and, in
	// the same transaction, the outbox event describing that dispatch —
	// the row and the event it announces are never observably
	// inconsistent.
	//
	// It stays its own method now that Update also writes an event, because
	// what separates the two is the precondition, not the outbox: Enqueue
	// writes pending -> queued unconditionally and is called only by the
	// upload path, while Update writes a fenced terminal transition and is
	// called only by CompleteJob and FailJob. Neither could stand in for the
	// other.
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
	// The predicate still names queued alone even though recovery now
	// exists, and that is deliberate. Recovery does not widen this
	// statement; it returns an abandoned job to queued (see Requeue) so
	// that this same unconditional edge can be raced for again. Widening
	// the predicate to admit processing would make the claim depend on a
	// liveness signal that fails open, and two workers would run the same
	// live job.
	//
	// It returns the claimed row's lease epoch alongside the verdict. The
	// winner must carry that value through to its terminal write: it is the
	// fence, and re-reading it later can only pick up a successor's.
	ClaimForProcessing(ctx context.Context, job *VideoJob) (claimed bool, epoch int64, err error)
	// Requeue returns an abandoned job from processing to queued and, in the
	// same transaction, writes the outbox event re-dispatching it — the same
	// event type and payload Enqueue writes, because the consumer cannot
	// tell the two apart and must not have to.
	//
	// It is conditional on observedEpoch: the recovery scan decides a job is
	// abandoned at a particular epoch, and only that epoch's row may be
	// requeued. The statement advances the epoch by one, which is what
	// fences the previous holder's terminal write.
	//
	// A false return is not an error. It means another sweep won, or the job
	// left processing between the scan and the write.
	Requeue(ctx context.Context, job *VideoJob, observedEpoch int64) (requeued bool, err error)
	// FindProcessing returns up to limit jobs currently in processing, in a
	// deterministic order, starting strictly after the cursor. It is the
	// recovery scan's only read: not owner-scoped, and not reachable from
	// any route.
	//
	// The zero VideoJobID means "start of scan", which the first cycle and
	// every wrap pass. It is not optional and not a placeholder: it
	// serializes to the empty string while video_jobs.id is a PostgreSQL
	// uuid, so an implementation must omit the keyset predicate for it
	// rather than bind it, which would error before a row was examined.
	FindProcessing(ctx context.Context, after VideoJobID, limit int) ([]*VideoJob, error)
}
