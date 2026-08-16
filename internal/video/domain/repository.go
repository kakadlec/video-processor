package domain

import (
	"context"
	"errors"
)

var ErrVideoJobNotFound = errors.New("video: video job not found")

type VideoJobRepository interface {
	Create(ctx context.Context, job *VideoJob) error
	FindByID(ctx context.Context, id VideoJobID) (*VideoJob, error)
	FindByUserID(ctx context.Context, userID UserID, offset, limit int) ([]*VideoJob, error)
}
