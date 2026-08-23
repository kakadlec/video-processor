package domain

import (
	"context"
	"errors"
)

// ErrVideoJobNotFound is returned when no VideoJob matches the requested lookup.
var ErrVideoJobNotFound = errors.New("video: video job not found")

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
	// frame count, error reason, and storage key. Unlike Create, it does
	// not write a transactional-outbox row.
	Update(ctx context.Context, job *VideoJob) error
}
