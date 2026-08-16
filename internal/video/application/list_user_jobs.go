package application

import (
	"context"
	"errors"

	"video-processor/internal/video/domain"
)

// ErrLimitOutOfRange is returned when ListUserJobsInput.Limit is not between 1 and 100 inclusive.
var ErrLimitOutOfRange = errors.New("video: limit must be between 1 and 100")

// ErrOffsetNegative is returned when ListUserJobsInput.Offset is negative.
var ErrOffsetNegative = errors.New("video: offset must not be negative")

const (
	minLimit = 1
	maxLimit = 100
)

// ListUserJobsInput carries the caller-supplied listing and pagination fields.
type ListUserJobsInput struct {
	UserID string
	Offset int
	Limit  int
}

// ListUserJobsResultItem describes a single job in a listing page.
type ListUserJobsResultItem struct {
	JobID            string
	OriginalFilename string
	Status           string
}

// ListUserJobs returns a page of the caller's own VideoJobs, ordered newest
// first. Offset and limit are validated (not silently clamped) so the
// use case's behavior is predictable and testable at its boundaries.
type ListUserJobs struct {
	jobs domain.VideoJobRepository
}

// NewListUserJobs wires the ListUserJobs use case to its ports.
func NewListUserJobs(jobs domain.VideoJobRepository) *ListUserJobs {
	return &ListUserJobs{jobs: jobs}
}

// Execute runs the job listing use case.
func (uc *ListUserJobs) Execute(ctx context.Context, input ListUserJobsInput) ([]ListUserJobsResultItem, error) {
	if input.Limit < minLimit || input.Limit > maxLimit {
		return nil, ErrLimitOutOfRange
	}
	if input.Offset < 0 {
		return nil, ErrOffsetNegative
	}

	userID, err := domain.NewUserID(input.UserID)
	if err != nil {
		return nil, err
	}

	jobs, err := uc.jobs.FindByUserID(ctx, userID, input.Offset, input.Limit)
	if err != nil {
		return nil, err
	}

	results := make([]ListUserJobsResultItem, len(jobs))
	for i, job := range jobs {
		results[i] = ListUserJobsResultItem{
			JobID:            job.ID().String(),
			OriginalFilename: job.OriginalFilename().String(),
			Status:           string(job.Status()),
		}
	}
	return results, nil
}
