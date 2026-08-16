package domain

import (
	"context"
	"errors"
)

// ErrVideoJobNotFound is returned when a requested job does not exist.
var ErrVideoJobNotFound = errors.New("video: video job not found")

// VideoJobRepository is the persistence port for VideoJob aggregates.
type VideoJobRepository interface {
	Create(ctx context.Context, job *VideoJob) error
	FindByID(ctx context.Context, id VideoJobID) (*VideoJob, error)
	FindByUserID(ctx context.Context, userID UserID, offset, limit int) ([]*VideoJob, error)
}
